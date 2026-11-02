package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	sealpkg "homeserver/platform/seal"
)

// Rutinas: franjas que se repiten cada semana (trabajo, club, prácticas). Son
// eventos normales en un calendario aparte, «Rutinas», para que en el móvil
// se vean con su color y se puedan ocultar. El asistente las tiene en cuenta
// al buscar huecos, y el resumen de la mañana las pone en una sola línea.

const routinesSlug = "rutinas"

// routinesCalendar devuelve el calendario de rutinas, y lo crea si no existe.
func (s *service) routinesCalendar(ctx context.Context, owner string) (collection, error) {
	c, err := s.store.collection(ctx, owner, "cal", routinesSlug)
	if !errors.Is(err, errNotFound) {
		return c, err
	}
	err = s.store.createCollection(ctx, collection{Owner: owner, Kind: "cal", Slug: routinesSlug, Name: "Rutinas",
		Color: "#6b5b95", Components: []string{ical.CompEvent}})
	if err != nil && !errors.Is(err, errConflict) {
		return c, err
	}
	return s.store.collection(ctx, owner, "cal", routinesSlug)
}

var dayCodes = []struct{ code, name string }{
	{"MO", "lunes"}, {"TU", "martes"}, {"WE", "miercoles"}, {"TH", "jueves"}, {"FR", "viernes"}, {"SA", "sabado"}, {"SU", "domingo"},
}

// parseDays pasa días en palabras («lunes», «L-V», «laborables», «fin de
// semana», «todos») a los códigos de RRULE, en orden y sin repetir.
func parseDays(in []string) ([]string, error) {
	var out []string
	add := func(codes ...string) {
		for _, c := range codes {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	for _, raw := range in {
		w := strings.Join(sealpkg.Words(raw), " ")
		switch w {
		case "":
		case "laborables", "lunes a viernes", "l v", "entre semana", "de lunes a viernes":
			add("MO", "TU", "WE", "TH", "FR")
		case "fin de semana", "fines de semana", "s d":
			add("SA", "SU")
		case "todos", "todos los dias", "diario", "cada dia":
			add("MO", "TU", "WE", "TH", "FR", "SA", "SU")
		default:
			found := false
			for _, d := range dayCodes {
				if w == d.name || w == d.name[:3] || w == strings.ToLower(d.code) || w == d.name+"s" {
					add(d.code)
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("día «%s» no válido: usa lunes, martes… o laborables, fin de semana, todos", raw)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("faltan los días de la semana")
	}
	slices.SortFunc(out, func(a, b string) int {
		return slices.IndexFunc(dayCodes, func(d struct{ code, name string }) bool { return d.code == a }) -
			slices.IndexFunc(dayCodes, func(d struct{ code, name string }) bool { return d.code == b })
	})
	return out, nil
}

// routineInput describe una rutina nueva.
type routineInput struct {
	Title, Location, Notes string
	Days                   []string // en palabras (parseDays)
	Start, End             string   // HH:MM; si End no es posterior, acaba al día siguiente
	From                   time.Time
	Until                  *time.Time // último día incluido; nil: sin fin
	AlarmMin               int        // 0: sin aviso
}

func (s *service) createRoutine(ctx context.Context, owner string, in routineInput) (entry, error) {
	days, err := parseDays(in.Days)
	if err != nil {
		return entry{}, err
	}
	st, err1 := time.Parse("15:04", in.Start)
	en, err2 := time.Parse("15:04", in.End)
	if err1 != nil || err2 != nil {
		return entry{}, errors.New("las horas van en formato HH:MM")
	}
	from := dayStart(in.From)
	if in.From.IsZero() {
		from = dayStart(s.now())
	}
	// DTSTART tiene que ser una de las veces: el primer día de la semana que toque.
	for !slices.Contains(days, dayCodes[(int(from.Weekday())+6)%7].code) {
		from = from.AddDate(0, 0, 1)
	}
	start := from.Add(time.Duration(st.Hour())*time.Hour + time.Duration(st.Minute())*time.Minute)
	end := from.Add(time.Duration(en.Hour())*time.Hour + time.Duration(en.Minute())*time.Minute)
	if !end.After(start) {
		end = end.AddDate(0, 0, 1) // turno de noche
	}
	rule := "FREQ=WEEKLY;BYDAY=" + strings.Join(days, ",")
	if in.Until != nil {
		last := dayStart(*in.Until).AddDate(0, 0, 1).Add(-time.Second)
		if last.Before(start) {
			return entry{}, errors.New("la rutina acabaría antes de empezar")
		}
		rule += ";UNTIL=" + last.UTC().Format("20060102T150405Z")
	}
	if _, err := s.routinesCalendar(ctx, owner); err != nil {
		return entry{}, err
	}
	alarms := []int{}
	if in.AlarmMin > 0 {
		alarms = []int{in.AlarmMin}
	}
	return s.create(ctx, owner, routinesSlug, ical.CompEvent, eventInput{Title: &in.Title, Start: &start, End: &end,
		Location: &in.Location, Notes: &in.Notes, Repeat: &rule, Alarms: &alarms})
}

// routines son las rutinas (la serie, no cada vez).
func (s *service) routines(ctx context.Context, owner string) ([]entry, error) {
	c, err := s.store.collection(ctx, owner, "cal", routinesSlug)
	if errors.Is(err, errNotFound) {
		return []entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	objs, err := s.store.listObjects(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	p, _ := s.store.prefs(ctx, owner)
	out := []entry{}
	for _, o := range objs {
		if cal, err := decodeCal(o.Data); err == nil {
			out = append(out, s.entryFrom(o, cal, nil, p.Home))
		}
	}
	sortEntries(out)
	return out, nil
}

// skip quita una vez de un evento que se repite (o de una rutina): la del día
// day. El resto de la serie sigue igual.
func (s *service) skip(ctx context.Context, owner string, id int64, day time.Time) (entry, error) {
	for try := 0; try < 3; try++ {
		o, err := s.store.objectByID(ctx, owner, id)
		if err != nil {
			return entry{}, err
		}
		cal, err := decodeCal(o.Data)
		if err != nil {
			return entry{}, err
		}
		m, err := master(cal)
		if err != nil {
			return entry{}, err
		}
		if m.Props.Get(ical.PropRecurrenceRule) == nil {
			return entry{}, errors.New("no se repite: para quitarlo, bórralo")
		}
		occs := occurrences(cal, dayStart(day), dayStart(day).AddDate(0, 0, 1))
		i := slices.IndexFunc(occs, func(o occurrence) bool { return dayStart(o.Start).Equal(dayStart(day)) })
		if i < 0 {
			return entry{}, fmt.Errorf("el %s no toca", longDate(day))
		}
		occ := occs[i]
		p := ical.NewProp(ical.PropExceptionDates)
		if occ.AllDay {
			p.SetDate(occ.Start.In(madrid))
		} else {
			p.SetDateTime(occ.Start.In(madrid))
		}
		m.Props.Add(p)
		// Si esa vez estaba cambiada (RECURRENCE-ID), el cambio sobra.
		cal.Children = slices.DeleteFunc(cal.Children, func(c *ical.Component) bool {
			rid := c.Props.Get(ical.PropRecurrenceID)
			if c == m || rid == nil {
				return false
			}
			ts, _, err := propTimes(rid)
			return err == nil && len(ts) > 0 && ts[0].Equal(occ.Start)
		})
		now := s.now()
		if err := applyInput(cal, eventInput{}, now); err != nil {
			return entry{}, err
		}
		_, err = s.store.putObject(ctx, o.CollID, o.Name, cal, o.ETag, false, now)
		if errors.Is(err, errConflict) {
			continue
		}
		if err != nil {
			return entry{}, err
		}
		return s.entry(ctx, owner, id)
	}
	return entry{}, errConflict
}

// slot es un hueco libre.
type slot struct {
	Day     string `json:"dia"`
	Start   string `json:"desde"`
	End     string `json:"hasta"`
	Minutes int    `json:"minutos"`
}

// freeSlots busca huecos de al menos minDur entre las horas dayFrom y dayTo
// (HH:MM) de cada día de from a to (incluido). Ocupan los eventos con hora y
// las rutinas (si withRoutines); los de todo el día y las tareas, no.
func (s *service) freeSlots(ctx context.Context, owner string, from, to time.Time, dayFrom, dayTo string, minDur time.Duration, withRoutines bool) ([]slot, error) {
	a, err1 := time.Parse("15:04", dayFrom)
	b, err2 := time.Parse("15:04", dayTo)
	if err1 != nil || err2 != nil || !b.After(a) {
		return nil, errors.New("franja del día no válida: usa HH:MM-HH:MM")
	}
	from, to = dayStart(from), dayStart(to).AddDate(0, 0, 1)
	if to.Sub(from) > 31*24*time.Hour {
		return nil, errors.New("como mucho, un mes de una vez")
	}
	items, err := s.agenda(ctx, owner, from, to)
	if err != nil {
		return nil, err
	}
	type span struct{ start, end time.Time }
	var busy []span
	for _, e := range items {
		if e.Kind != "evento" || e.AllDay || e.Start == nil || e.End == nil || (e.Routine && !withRoutines) {
			continue
		}
		busy = append(busy, span{*e.Start, *e.End})
	}
	now := s.now()
	out := []slot{}
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		ws := d.Add(time.Duration(a.Hour())*time.Hour + time.Duration(a.Minute())*time.Minute)
		we := d.Add(time.Duration(b.Hour())*time.Hour + time.Duration(b.Minute())*time.Minute)
		if ws.Before(now) {
			ws = now.Truncate(15 * time.Minute).Add(15 * time.Minute).In(madrid)
		}
		cur := ws
		emit := func(end time.Time) {
			if end.Sub(cur) >= minDur {
				out = append(out, slot{Day: dayLabel(d), Start: cur.In(madrid).Format("15:04"), End: end.In(madrid).Format("15:04"),
					Minutes: int(end.Sub(cur).Minutes())})
			}
		}
		for _, sp := range busy { // ya vienen por orden de comienzo
			if !sp.end.After(cur) || !sp.start.Before(we) {
				continue
			}
			if sp.start.After(cur) {
				emit(sp.start)
			}
			if sp.end.After(cur) {
				cur = sp.end
			}
		}
		if cur.Before(we) {
			emit(we)
		}
	}
	return out, nil
}
