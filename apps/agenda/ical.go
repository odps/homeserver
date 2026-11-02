package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // las zonas de los eventos del móvil, aunque el sistema no las tenga

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Eventos y tareas en iCalendar: lo que se guarda es el texto completo (así
// el móvil recupera exactamente lo que mandó); de él se sacan los campos para
// buscar, listar, repetir y avisar.

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

const prodID = "-//homeserver//agenda//ES"

// derived son las columnas que se calculan del iCalendar al guardarlo.
type derived struct {
	UID, Comp                      string
	Summary, Location, Description string
	Start, End                     *time.Time // End nil: se repite sin fin
	AllDay, Recurring, Done        bool
}

func encodeCal(cal *ical.Calendar) (string, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func decodeCal(data string) (*ical.Calendar, error) {
	return ical.NewDecoder(strings.NewReader(data)).Decode()
}

// master es el componente principal (el que no es una excepción de una serie).
func master(cal *ical.Calendar) (*ical.Component, error) {
	var first *ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent && c.Name != ical.CompToDo {
			continue
		}
		if first == nil {
			first = c
		}
		if c.Props.Get(ical.PropRecurrenceID) == nil {
			return c, nil
		}
	}
	if first == nil {
		return nil, errors.New("el calendario no tiene ningún evento ni tarea")
	}
	return first, nil
}

func text(c *ical.Component, name string) string {
	s, _ := c.Props.Text(name)
	return strings.TrimSpace(s)
}

// propTimes lee una fecha (o varias, separadas por comas, como en EXDATE).
// Las horas sin zona o con una zona desconocida se toman de Madrid.
func propTimes(p *ical.Prop) ([]time.Time, bool, error) {
	loc := madrid
	if tz := p.Params.Get(ical.PropTimezoneID); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	var out []time.Time
	allDay := false
	for _, v := range strings.Split(p.Value, ",") {
		v = strings.TrimSpace(v)
		var t time.Time
		var err error
		switch {
		case len(v) == 8:
			t, err = time.ParseInLocation("20060102", v, madrid)
			allDay = true
		case strings.HasSuffix(v, "Z"):
			t, err = time.ParseInLocation("20060102T150405Z", v, time.UTC)
		default:
			t, err = time.ParseInLocation("20060102T150405", v, loc)
		}
		if err != nil {
			return nil, false, fmt.Errorf("fecha %q no válida", v)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, false, errors.New("fecha vacía")
	}
	return out, allDay, nil
}

func propTime(c *ical.Component, name string) (time.Time, bool, bool) {
	p := c.Props.Get(name)
	if p == nil {
		return time.Time{}, false, false
	}
	ts, allDay, err := propTimes(p)
	if err != nil {
		return time.Time{}, false, false
	}
	return ts[0], allDay, true
}

// span devuelve el comienzo, la duración y si es de día completo. En las
// tareas, el «comienzo» es el vencimiento (o DTSTART si no vence).
func span(c *ical.Component) (time.Time, time.Duration, bool, bool) {
	if c.Name == ical.CompToDo {
		if due, allDay, ok := propTime(c, ical.PropDue); ok {
			return due, 0, allDay, true
		}
	}
	start, allDay, ok := propTime(c, ical.PropDateTimeStart)
	if !ok {
		return time.Time{}, 0, false, false
	}
	var dur time.Duration
	if end, _, ok := propTime(c, ical.PropDateTimeEnd); ok && c.Name == ical.CompEvent {
		dur = end.Sub(start)
	} else if p := c.Props.Get(ical.PropDuration); p != nil {
		dur, _ = p.Duration()
	} else if allDay && c.Name == ical.CompEvent {
		dur = 24 * time.Hour
	}
	if dur < 0 {
		dur = 0
	}
	return start, dur, allDay, true
}

func derive(cal *ical.Calendar) (derived, error) {
	m, err := master(cal)
	if err != nil {
		return derived{}, err
	}
	d := derived{
		UID: text(m, ical.PropUID), Comp: m.Name,
		Summary: text(m, ical.PropSummary), Location: text(m, ical.PropLocation), Description: text(m, ical.PropDescription),
	}
	if d.UID == "" {
		return d, errors.New("falta el UID")
	}
	if m.Name == ical.CompToDo {
		st := strings.ToUpper(text(m, ical.PropStatus))
		d.Done = st == "COMPLETED" || m.Props.Get(ical.PropCompleted) != nil
	}
	start, dur, allDay, ok := span(m)
	if !ok {
		return d, nil // tarea sin fecha
	}
	d.AllDay = allDay
	d.Start = &start
	end := start.Add(dur)
	d.End = &end
	if m.Props.Get(ical.PropRecurrenceRule) != nil {
		d.Recurring = true
		set, err := recurrence(m, start)
		if err != nil {
			return d, err
		}
		if r := set.GetRRule(); r != nil && r.OrigOptions.Until.IsZero() && r.OrigOptions.Count == 0 {
			d.End = nil
		} else {
			all := set.All()
			if len(all) > 0 {
				last := all[len(all)-1].Add(dur)
				d.End = &last
			}
		}
	}
	return d, nil
}

// recurrence monta la serie de un componente con RRULE (y sus EXDATE/RDATE).
func recurrence(c *ical.Component, start time.Time) (*rrule.Set, error) {
	p := c.Props.Get(ical.PropRecurrenceRule)
	opt, err := rrule.StrToROptionInLocation(p.Value, start.Location())
	if err != nil {
		return nil, fmt.Errorf("repetición no válida: %w", err)
	}
	opt.Dtstart = start
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, fmt.Errorf("repetición no válida: %w", err)
	}
	set := &rrule.Set{}
	set.RRule(rule)
	set.DTStart(start)
	for _, p := range c.Props[ical.PropExceptionDates] {
		if ts, _, err := propTimes(&p); err == nil {
			for _, t := range ts {
				set.ExDate(t)
			}
		}
	}
	for _, p := range c.Props[ical.PropRecurrenceDates] {
		if ts, _, err := propTimes(&p); err == nil {
			for _, t := range ts {
				set.RDate(t)
			}
		}
	}
	return set, nil
}

// occurrence es una vez concreta de un evento o tarea.
type occurrence struct {
	Start, End time.Time
	AllDay     bool
	Comp       *ical.Component
}

// occurrences devuelve las veces que caen (aunque sea en parte) entre from y
// to, ordenadas. Las excepciones de una serie (RECURRENCE-ID) sustituyen a la
// vez que cambian.
func occurrences(cal *ical.Calendar, from, to time.Time) []occurrence {
	m, err := master(cal)
	if err != nil {
		return nil
	}
	overrides := map[int64]*ical.Component{}
	for _, c := range cal.Children {
		if c == m || c.Name != m.Name {
			continue
		}
		if t, _, ok := propTime(c, ical.PropRecurrenceID); ok {
			overrides[t.Unix()] = c
		}
	}
	var out []occurrence
	add := func(c *ical.Component, start time.Time, dur time.Duration, allDay bool) {
		end := start.Add(dur)
		if end.Before(from) || !start.Before(to) || (dur > 0 && end.Equal(from)) {
			return
		}
		out = append(out, occurrence{Start: start, End: end, AllDay: allDay, Comp: c})
	}
	start, dur, allDay, ok := span(m)
	if !ok {
		return nil
	}
	if m.Props.Get(ical.PropRecurrenceRule) == nil {
		add(m, start, dur, allDay)
	} else if set, err := recurrence(m, start); err == nil {
		// La serie se recorre desde un poco antes: una vez larga que empezó
		// antes de from también cuenta.
		for _, t := range set.Between(from.Add(-dur-time.Second), to, true) {
			if _, ok := overrides[t.Unix()]; ok {
				continue
			}
			add(m, t, dur, allDay)
		}
	}
	for _, c := range overrides {
		if s, d, ad, ok := span(c); ok {
			if st := strings.ToUpper(text(c, ical.PropStatus)); st != "CANCELLED" {
				add(c, s, d, ad)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// alarm es un VALARM: a cierta distancia del comienzo (o del fin), o a una hora fija.
type alarm struct {
	Offset  time.Duration // negativo: antes
	FromEnd bool
	At      time.Time
}

func alarms(c *ical.Component) []alarm {
	var out []alarm
	for _, a := range c.Children {
		if a.Name != ical.CompAlarm {
			continue
		}
		p := a.Props.Get(ical.PropTrigger)
		if p == nil {
			continue
		}
		if p.ValueType() == ical.ValueDateTime || !strings.HasPrefix(strings.TrimLeft(p.Value, "+-"), "P") {
			if ts, _, err := propTimes(p); err == nil {
				out = append(out, alarm{At: ts[0]})
			}
			continue
		}
		d, err := p.Duration()
		if err != nil {
			continue
		}
		out = append(out, alarm{Offset: d, FromEnd: strings.EqualFold(p.Params.Get("RELATED"), "END")})
	}
	return out
}

// nextAlarm es el próximo aviso después de after (cero si no hay). Busca
// en el próximo año; las alarmas de más de 8 días de antelación no se miran.
func nextAlarm(cal *ical.Calendar, after time.Time) time.Time {
	var best time.Time
	m, err := master(cal)
	if err != nil {
		return best
	}
	if d, _ := derive(cal); d.Done {
		return best
	}
	consider := func(t time.Time) {
		if t.After(after) && (best.IsZero() || t.Before(best)) {
			best = t
		}
	}
	for _, a := range alarms(m) {
		if !a.At.IsZero() {
			consider(a.At)
		}
	}
	for _, o := range occurrences(cal, after.Add(-8*24*time.Hour), after.Add(366*24*time.Hour)) {
		if !best.IsZero() && o.Start.Add(-8*24*time.Hour).After(best) {
			break
		}
		for _, a := range occAlarms(m, o) {
			if !a.At.IsZero() {
				continue
			}
			base := o.Start
			if a.FromEnd {
				base = o.End
			}
			consider(base.Add(a.Offset))
		}
	}
	return best
}

// occAlarms son los avisos de una vez: los suyos o, si es una excepción de
// la serie sin avisos propios, los de la serie.
func occAlarms(m *ical.Component, o occurrence) []alarm {
	if l := alarms(o.Comp); len(l) > 0 || o.Comp == m {
		return l
	}
	return alarms(m)
}

// firing devuelve las veces cuyo aviso cae en (from, to].
func firing(cal *ical.Calendar, from, to time.Time) []occurrence {
	var out []occurrence
	m, err := master(cal)
	if err != nil {
		return nil
	}
	for _, a := range alarms(m) {
		if !a.At.IsZero() && a.At.After(from) && !a.At.After(to) {
			s, d, ad, _ := span(m)
			out = append(out, occurrence{Start: s, End: s.Add(d), AllDay: ad, Comp: m})
		}
	}
	for _, o := range occurrences(cal, from.Add(-8*24*time.Hour), to.Add(8*24*time.Hour)) {
		for _, a := range occAlarms(m, o) {
			if !a.At.IsZero() {
				continue
			}
			base := o.Start
			if a.FromEnd {
				base = o.End
			}
			if t := base.Add(a.Offset); t.After(from) && !t.After(to) {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

func newUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// madridTZ es la definición de la zona que acompaña a las horas que crea la
// app (los clientes CalDAV la esperan junto a cada TZID).
func madridTZ() *ical.Component {
	tz := ical.NewComponent(ical.CompTimezone)
	tz.Props.SetText(ical.PropTimezoneID, "Europe/Madrid")
	for _, r := range []struct{ name, from, to, tzname, start, rule string }{
		{ical.CompTimezoneDaylight, "+0100", "+0200", "CEST", "19700329T020000", "FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU"},
		{ical.CompTimezoneStandard, "+0200", "+0100", "CET", "19701025T030000", "FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU"},
	} {
		c := ical.NewComponent(r.name)
		c.Props.Set(&ical.Prop{Name: ical.PropTimezoneOffsetFrom, Value: r.from, Params: ical.Params{}})
		c.Props.Set(&ical.Prop{Name: ical.PropTimezoneOffsetTo, Value: r.to, Params: ical.Params{}})
		c.Props.SetText(ical.PropTimezoneName, r.tzname)
		c.Props.Set(&ical.Prop{Name: ical.PropDateTimeStart, Value: r.start, Params: ical.Params{}})
		c.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: r.rule, Params: ical.Params{}})
		tz.Children = append(tz.Children, c)
	}
	return tz
}

// eventInput son los datos de un evento o tarea que se crea o cambia desde la
// app o el asistente. En un cambio, los punteros nil dejan el valor como está.
type eventInput struct {
	Title    *string
	Start    *time.Time
	End      *time.Time
	AllDay   *bool
	Location *string
	Notes    *string
	Repeat   *string // RRULE (FREQ=WEEKLY;…) o "" para quitarla
	Alarms   *[]int  // minutos de antelación; vacío: sin aviso
	Due      *time.Time
	NoDue    bool // tarea: quitar el vencimiento
	Done     *bool
}

func setTime(c *ical.Component, name string, t time.Time, allDay bool) {
	p := ical.NewProp(name)
	if allDay {
		p.SetDate(t.In(madrid))
	} else {
		p.SetDateTime(t.In(madrid))
	}
	c.Props.Set(p)
}

func setAlarms(c *ical.Component, title string, minutes []int, fromEnd bool) {
	kept := c.Children[:0]
	for _, ch := range c.Children {
		if ch.Name != ical.CompAlarm {
			kept = append(kept, ch)
		}
	}
	c.Children = kept
	for _, m := range minutes {
		a := ical.NewComponent(ical.CompAlarm)
		a.Props.SetText(ical.PropAction, "DISPLAY")
		a.Props.SetText(ical.PropDescription, title)
		p := ical.NewProp(ical.PropTrigger)
		p.SetDuration(-time.Duration(m) * time.Minute)
		if fromEnd {
			p.Params.Set("RELATED", "END")
		}
		a.Props.Set(p)
		c.Children = append(c.Children, a)
	}
}

// newObject crea un calendario con un evento (VEVENT) o una tarea (VTODO).
func newObject(comp string, in eventInput, now time.Time) (*ical.Calendar, error) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, prodID)
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Children = append(cal.Children, madridTZ())
	c := ical.NewComponent(comp)
	c.Props.SetText(ical.PropUID, newUID())
	c.Props.SetDateTime(ical.PropCreated, now.UTC())
	cal.Children = append(cal.Children, c)
	if comp == ical.CompToDo {
		c.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	}
	if err := applyInput(cal, in, now); err != nil {
		return nil, err
	}
	return cal, nil
}

// applyInput cambia el componente principal con lo que traiga in.
func applyInput(cal *ical.Calendar, in eventInput, now time.Time) error {
	c, err := master(cal)
	if err != nil {
		return err
	}
	if in.Title != nil {
		if strings.TrimSpace(*in.Title) == "" {
			return errors.New("falta el título")
		}
		c.Props.SetText(ical.PropSummary, strings.TrimSpace(*in.Title))
	}
	for name, v := range map[string]*string{ical.PropLocation: in.Location, ical.PropDescription: in.Notes} {
		if v == nil {
			continue
		}
		if s := strings.TrimSpace(*v); s != "" {
			c.Props.SetText(name, s)
		} else {
			c.Props.Del(name)
		}
	}
	if c.Name == ical.CompEvent {
		start, dur, allDay, ok := span(c)
		if !ok && in.Start == nil {
			return errors.New("falta cuándo empieza")
		}
		if in.AllDay != nil {
			allDay = *in.AllDay
		}
		if in.Start != nil {
			start = *in.Start
		}
		end := start.Add(dur)
		if in.End != nil {
			end = *in.End
		} else if in.Start != nil && dur == 0 && !allDay {
			end = start.Add(time.Hour)
		}
		if allDay {
			start = dayStart(start)
			end = dayStart(end)
			if !end.After(start) {
				end = start.AddDate(0, 0, 1)
			}
		}
		if end.Before(start) {
			return errors.New("el evento acaba antes de empezar")
		}
		setTime(c, ical.PropDateTimeStart, start, allDay)
		c.Props.Del(ical.PropDuration)
		setTime(c, ical.PropDateTimeEnd, end, allDay)
	} else {
		if in.NoDue {
			c.Props.Del(ical.PropDue)
		} else if in.Due != nil {
			allDay := in.AllDay != nil && *in.AllDay
			setTime(c, ical.PropDue, *in.Due, allDay)
		}
		if in.Done != nil {
			if *in.Done {
				c.Props.SetText(ical.PropStatus, "COMPLETED")
				c.Props.SetDateTime(ical.PropCompleted, now.UTC())
				c.Props.Set(&ical.Prop{Name: ical.PropPercentComplete, Value: "100", Params: ical.Params{}})
			} else {
				c.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
				c.Props.Del(ical.PropCompleted)
				c.Props.Del(ical.PropPercentComplete)
			}
		}
	}
	if in.Repeat != nil {
		if r := strings.TrimPrefix(strings.TrimSpace(*in.Repeat), "RRULE:"); r != "" {
			if _, err := rrule.StrToROption(r); err != nil {
				return fmt.Errorf("repetición no válida: %w", err)
			}
			c.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: strings.ToUpper(r), Params: ical.Params{}})
		} else {
			c.Props.Del(ical.PropRecurrenceRule)
			c.Props.Del(ical.PropExceptionDates)
			c.Props.Del(ical.PropRecurrenceDates)
		}
	}
	if in.Alarms != nil {
		setAlarms(c, text(c, ical.PropSummary), *in.Alarms, c.Name == ical.CompToDo)
	}
	c.Props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
	c.Props.SetDateTime(ical.PropLastModified, now.UTC())
	seq := 0
	if p := c.Props.Get(ical.PropSequence); p != nil {
		seq, _ = p.Int()
	}
	c.Props.Set(&ical.Prop{Name: ical.PropSequence, Value: fmt.Sprint(seq + 1), Params: ical.Params{}})
	return nil
}

func dayStart(t time.Time) time.Time {
	t = t.In(madrid)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, madrid)
}

// alarmMinutes son las antelaciones (en minutos) de los avisos relativos.
func alarmMinutes(c *ical.Component) []int {
	var out []int
	for _, a := range alarms(c) {
		if a.At.IsZero() && a.Offset <= 0 {
			out = append(out, int(-a.Offset/time.Minute))
		}
	}
	return out
}

// describeRule pone en palabras las repeticiones más corrientes.
func describeRule(rule string) string {
	opt, err := rrule.StrToROption(rule)
	if err != nil || rule == "" {
		return ""
	}
	every := map[rrule.Frequency][2]string{
		rrule.DAILY: {"cada día", "días"}, rrule.WEEKLY: {"cada semana", "semanas"},
		rrule.MONTHLY: {"cada mes", "meses"}, rrule.YEARLY: {"cada año", "años"},
	}[opt.Freq]
	if every[0] == "" {
		return "se repite"
	}
	s := every[0]
	if opt.Interval > 1 {
		s = fmt.Sprintf("cada %d %s", opt.Interval, every[1])
	}
	if len(opt.Byweekday) > 0 && opt.Freq == rrule.WEEKLY {
		names := map[int]string{0: "lunes", 1: "martes", 2: "miércoles", 3: "jueves", 4: "viernes", 5: "sábado", 6: "domingo"}
		var days []string
		for _, d := range opt.Byweekday {
			days = append(days, names[d.Day()])
		}
		s += " (" + strings.Join(days, ", ") + ")"
	}
	if !opt.Until.IsZero() {
		s += ", hasta el " + opt.Until.In(madrid).Format("02/01/2006")
	} else if opt.Count > 0 {
		s += fmt.Sprintf(", %d veces", opt.Count)
	}
	return s
}
