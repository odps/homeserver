package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"

	"homeserver/platform/geo"
	sealpkg "homeserver/platform/seal"
)

// service junta la agenda, los contactos y el correo para la web, el
// asistente y los avisos.
type service struct {
	store      store
	mail       *mailer
	log        *slog.Logger
	summarizer func(ctx context.Context, prompt string) (string, error) // nil: sin resumen de la IA
	geo        *geo.Client                                              // nil: sin el tiempo en el resumen
	now        func() time.Time
	summaries  sync.Map // usuario → día cuyo resumen se está preparando
}

// entry es una vez de un evento, o una tarea, lista para mostrar.
type entry struct {
	ID       int64      `json:"id"`
	Kind     string     `json:"tipo"` // evento o tarea
	Title    string     `json:"titulo"`
	Start    *time.Time `json:"inicio,omitempty"`
	End      *time.Time `json:"fin,omitempty"`
	AllDay   bool       `json:"todo_el_dia,omitempty"`
	When     string     `json:"cuando,omitempty"`
	Location string     `json:"lugar,omitempty"`
	Maps     string     `json:"mapa,omitempty"`
	Route    string     `json:"ruta,omitempty"`
	Notes    string     `json:"notas,omitempty"`
	Repeat   string     `json:"repeticion,omitempty"`
	RRule    string     `json:"-"`
	Alarms   []int      `json:"avisos_min,omitempty"`
	Calendar string     `json:"calendario"`
	Routine  bool       `json:"rutina,omitempty"` // del calendario de rutinas
	Done     bool       `json:"hecha,omitempty"`
	Overdue  bool       `json:"vencida,omitempty"`
}

var shortDays = []string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}
var weekdays = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
var months = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}

func dayLabel(t time.Time) string {
	t = t.In(madrid)
	return fmt.Sprintf("%s %d %s", shortDays[t.Weekday()], t.Day(), months[t.Month()-1][:3])
}

func longDate(t time.Time) string {
	t = t.In(madrid)
	return fmt.Sprintf("%s, %d de %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
}

// whenText pone la fecha en palabras: «jue 5 nov, 17:00–18:00».
func whenText(start, end time.Time, allDay bool, task bool) string {
	start, end = start.In(madrid), end.In(madrid)
	if task {
		if allDay {
			return "vence el " + dayLabel(start)
		}
		return "vence el " + dayLabel(start) + " a las " + start.Format("15:04")
	}
	if allDay {
		last := end.AddDate(0, 0, -1)
		if !last.After(start) {
			return dayLabel(start) + ", todo el día"
		}
		return dayLabel(start) + " – " + dayLabel(last)
	}
	if dayStart(start).Equal(dayStart(end)) || end.Equal(start) {
		if end.Equal(start) {
			return dayLabel(start) + ", " + start.Format("15:04")
		}
		return dayLabel(start) + ", " + start.Format("15:04") + "–" + end.Format("15:04")
	}
	return dayLabel(start) + " " + start.Format("15:04") + " – " + dayLabel(end) + " " + end.Format("15:04")
}

func (s *service) entryFrom(o object, cal *ical.Calendar, occ *occurrence, home string) entry {
	m, _ := master(cal)
	c := m
	if occ != nil {
		c = occ.Comp
	}
	e := entry{ID: o.ID, Kind: "evento", Title: text(c, ical.PropSummary), Location: text(c, ical.PropLocation),
		Notes: text(c, ical.PropDescription), Calendar: o.Calendar, Alarms: alarmMinutes(c), Routine: o.Calendar == routinesSlug}
	if m.Name == ical.CompToDo {
		e.Kind = "tarea"
		d, _ := derive(cal)
		e.Done = d.Done
	}
	if p := m.Props.Get(ical.PropRecurrenceRule); p != nil {
		e.RRule = p.Value
		e.Repeat = describeRule(p.Value)
	}
	if e.Title == "" {
		e.Title = "(sin título)"
	}
	var start, end time.Time
	var allDay, ok bool
	if occ != nil {
		start, end, allDay, ok = occ.Start, occ.End, occ.AllDay, true
	} else {
		var dur time.Duration
		start, dur, allDay, ok = span(c)
		end = start.Add(dur)
	}
	if ok {
		start, end = start.In(madrid), end.In(madrid)
		e.Start, e.AllDay = &start, allDay
		if e.Kind == "evento" {
			e.End = &end
		}
		e.When = whenText(start, end, allDay, e.Kind == "tarea")
		if e.Kind == "tarea" && !e.Done {
			due := start
			if allDay {
				due = start.AddDate(0, 0, 1)
			}
			e.Overdue = due.Before(s.now())
		}
	}
	if e.Location != "" {
		e.Maps = mapsLink(e.Location)
		e.Route = routeLink(home, e.Location)
	}
	return e
}

// agenda son los eventos (cada vez de los que se repiten) y las tareas con
// vencimiento entre from y to, por orden.
func (s *service) agenda(ctx context.Context, owner string, from, to time.Time) ([]entry, error) {
	objs, err := s.store.objectsBetween(ctx, owner, from, to)
	if err != nil {
		return nil, err
	}
	p, _ := s.store.prefs(ctx, owner)
	var out []entry
	for _, o := range objs {
		cal, err := decodeCal(o.Data)
		if err != nil {
			continue
		}
		for _, occ := range occurrences(cal, from, to) {
			e := s.entryFrom(o, cal, &occ, p.Home)
			if e.Kind == "tarea" && e.Done {
				continue
			}
			out = append(out, e)
		}
	}
	sortEntries(out)
	return out, nil
}

func sortEntries(l []entry) {
	key := func(e entry) time.Time {
		if e.Start == nil {
			return time.Time{}
		}
		return *e.Start
	}
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && key(l[j]).Before(key(l[j-1])); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func (s *service) tasks(ctx context.Context, owner string) ([]entry, error) {
	objs, err := s.store.openTasks(ctx, owner)
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
	return out, nil
}

func (s *service) entry(ctx context.Context, owner string, id int64) (entry, error) {
	o, err := s.store.objectByID(ctx, owner, id)
	if err != nil {
		return entry{}, err
	}
	cal, err := decodeCal(o.Data)
	if err != nil {
		return entry{}, err
	}
	p, _ := s.store.prefs(ctx, owner)
	return s.entryFrom(o, cal, nil, p.Home), nil
}

func (s *service) searchEntries(ctx context.Context, owner, q string) ([]entry, error) {
	if len(sealpkg.Words(q)) == 0 {
		return nil, errors.New("faltan las palabras a buscar")
	}
	objs, err := s.store.searchObjects(ctx, owner, q, 30)
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
	return out, nil
}

// create crea un evento o una tarea en un calendario (por defecto, el primero).
func (s *service) create(ctx context.Context, owner, calendar, comp string, in eventInput) (entry, error) {
	c, err := s.pickCalendar(ctx, owner, calendar)
	if err != nil {
		return entry{}, err
	}
	if in.Alarms == nil && comp == ical.CompEvent {
		p, _ := s.store.prefs(ctx, owner)
		if p.DefaultMin > 0 && !(in.AllDay != nil && *in.AllDay) {
			in.Alarms = &[]int{p.DefaultMin}
		}
	}
	now := s.now()
	cal, err := newObject(comp, in, now)
	if err != nil {
		return entry{}, err
	}
	d, _ := derive(cal)
	o, err := s.store.putObject(ctx, c.ID, d.UID+".ics", cal, "", true, now)
	if err != nil {
		return entry{}, err
	}
	return s.entry(ctx, owner, o.ID)
}

func (s *service) pickCalendar(ctx context.Context, owner, slug string) (collection, error) {
	list, err := s.store.collections(ctx, owner, "cal")
	if err != nil {
		return collection{}, err
	}
	for _, c := range list {
		if slug == "" || strings.EqualFold(c.Slug, slug) || strings.EqualFold(c.Name, slug) {
			return c, nil
		}
	}
	return collection{}, fmt.Errorf("no hay ningún calendario «%s»", slug)
}

// update cambia un evento o tarea; se reintenta si el móvil lo cambia a la vez.
func (s *service) update(ctx context.Context, owner string, id int64, in eventInput) (entry, error) {
	for try := 0; try < 3; try++ {
		o, err := s.store.objectByID(ctx, owner, id)
		if err != nil {
			return entry{}, err
		}
		cal, err := decodeCal(o.Data)
		if err != nil {
			return entry{}, err
		}
		now := s.now()
		if err := applyInput(cal, in, now); err != nil {
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

func (s *service) remove(ctx context.Context, owner string, id int64) (entry, error) {
	e, err := s.entry(ctx, owner, id)
	if err != nil {
		return e, err
	}
	o, err := s.store.objectByID(ctx, owner, id)
	if err != nil {
		return e, err
	}
	return e, s.store.deleteObject(ctx, o.CollID, o.Name, "")
}

// ---- Contactos ----

func (s *service) contacts(ctx context.Context, owner, q string, limit int) ([]contact, error) {
	rows, err := s.store.searchCards(ctx, owner, q, limit)
	if err != nil {
		return nil, err
	}
	out := []contact{}
	for _, r := range rows {
		if c, err := decodeCard(r.Data); err == nil {
			out = append(out, viewContact(r.ID, c))
		}
	}
	return out, nil
}

func (s *service) contact(ctx context.Context, owner string, id int64) (contact, error) {
	r, err := s.store.cardByID(ctx, owner, id)
	if err != nil {
		return contact{}, err
	}
	c, err := decodeCard(r.Data)
	if err != nil {
		return contact{}, err
	}
	return viewContact(r.ID, c), nil
}

// saveContact crea (id 0) o cambia un contacto.
func (s *service) saveContact(ctx context.Context, owner string, id int64, in contactInput) (contact, error) {
	for try := 0; try < 3; try++ {
		var card vcard.Card
		var collID int64
		var name, etag string
		if id == 0 {
			books, err := s.store.collections(ctx, owner, "card")
			if err != nil || len(books) == 0 {
				return contact{}, errors.New("no hay libreta de contactos")
			}
			card, collID = newCard(), books[0].ID
			name = card.Value(vcard.FieldUID) + ".vcf"
		} else {
			r, err := s.store.cardByID(ctx, owner, id)
			if err != nil {
				return contact{}, err
			}
			if card, err = decodeCard(r.Data); err != nil {
				return contact{}, err
			}
			collID, name, etag = r.CollID, r.Name, r.ETag
		}
		now := s.now()
		if err := applyContact(card, in, now); err != nil {
			return contact{}, err
		}
		r, err := s.store.putCard(ctx, collID, name, card, etag, id == 0, now)
		if errors.Is(err, errConflict) && id != 0 {
			continue
		}
		if err != nil {
			return contact{}, err
		}
		return viewContact(r.ID, card), nil
	}
	return contact{}, errConflict
}

func (s *service) removeContact(ctx context.Context, owner string, id int64) (contact, error) {
	c, err := s.contact(ctx, owner, id)
	if err != nil {
		return c, err
	}
	r, err := s.store.cardByID(ctx, owner, id)
	if err != nil {
		return c, err
	}
	return c, s.store.deleteCard(ctx, r.CollID, r.Name, "")
}

// ---- Avisos ----

// fireAlarms manda (a la cola de Telegram) los avisos que han llegado a su
// hora y calcula el siguiente de cada evento. Los que se pasaron hace más de
// media hora (la app estuvo parada) se saltan.
func (s *service) fireAlarms(ctx context.Context) {
	now := s.now()
	due, err := s.store.dueAlarms(ctx, now)
	if err != nil {
		s.log.Warn("avisos", "err", err)
		return
	}
	for _, a := range due {
		cal, err := decodeCal(a.Data)
		if err != nil {
			s.store.setNextAlarm(ctx, a.ID, a.ETag, time.Time{})
			continue
		}
		p, _ := s.store.prefs(ctx, a.Owner)
		if p.RemindersOn && now.Sub(a.Next) < 30*time.Minute {
			for _, occ := range firing(cal, a.Next.Add(-time.Second), now) {
				e := s.entryFrom(a.object, cal, &occ, p.Home)
				if err := s.store.addNotice(ctx, a.Owner, "recordatorio", reminderText(e, now), now); err != nil {
					s.log.Warn("aviso", "err", err)
				}
			}
		}
		s.store.setNextAlarm(ctx, a.ID, a.ETag, nextAlarm(cal, now))
	}
}

func reminderText(e entry, now time.Time) string {
	var b strings.Builder
	b.WriteString("Recordatorio: " + e.Title)
	if e.Start != nil {
		start := e.Start.In(madrid)
		day := ""
		switch dayStart(start).Sub(dayStart(now)) / (24 * time.Hour) {
		case 0:
			day = "hoy"
		case 1:
			day = "mañana"
		default:
			day = "el " + dayLabel(start)
		}
		switch {
		case e.Kind == "tarea" && e.AllDay:
			b.WriteString("\nVence " + day + ".")
		case e.Kind == "tarea":
			b.WriteString("\nVence " + day + " a las " + start.Format("15:04") + ".")
		case e.AllDay:
			b.WriteString("\n" + strings.ToUpper(day[:1]) + day[1:] + ", todo el día.")
		default:
			b.WriteString("\n" + strings.ToUpper(day[:1]) + day[1:] + " a las " + start.Format("15:04") + ".")
		}
	}
	if e.Location != "" {
		b.WriteString("\nLugar: " + e.Location + "\nCómo llegar: " + e.Route)
	}
	if e.Notes != "" {
		b.WriteString("\n" + truncate(e.Notes, 300))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

// summary es el resumen del día: agenda, tareas, cumpleaños y correo.
func (s *service) summary(ctx context.Context, owner string, now time.Time, withAI bool) (string, error) {
	local := now.In(madrid)
	today := dayStart(local)
	var b strings.Builder
	// section escribe un bloque con su título; sin líneas, no sale.
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n\n" + title + "\n" + strings.Join(lines, "\n"))
	}
	day := longDate(local)
	b.WriteString("☀️ Buenos días. " + strings.ToUpper(day[:1]) + day[1:])

	if place, d, ok := s.weather(ctx, owner); ok {
		section(skyIcon(d.Sky)+" TIEMPO", []string{place + ": " + d.Line()})
	}

	// Hoy: eventos y rutinas en una sola línea de tiempo (ya vienen por hora).
	events, err := s.agenda(ctx, owner, today, today.AddDate(0, 0, 1))
	if err != nil {
		return "", err
	}
	var todayLines []string
	for _, e := range events {
		if e.Kind != "evento" {
			continue
		}
		line := "• "
		switch {
		case e.AllDay:
			line += "Todo el día · "
		case e.Start != nil && e.End != nil:
			line += e.Start.Format("15:04") + "–" + e.End.Format("15:04") + " · "
		}
		line += e.Title
		if e.Routine {
			line += " 🔁"
		}
		if e.Location != "" && !e.Routine {
			line += " · " + e.Location
		}
		todayLines = append(todayLines, line)
	}
	if len(todayLines) == 0 {
		todayLines = []string{"Nada en la agenda."}
	}
	section("📅 HOY", todayLines)

	tasks, err := s.tasks(ctx, owner)
	if err != nil {
		return "", err
	}
	var due []string
	for _, t := range tasks {
		switch {
		case t.Overdue:
			due = append(due, "⚠️ "+t.Title+" ("+strings.Replace(t.When, "vence", "venció", 1)+")")
		case t.Start != nil && t.Start.Before(today.AddDate(0, 0, 1)):
			line := "• " + t.Title
			if !t.AllDay {
				line += ", a las " + t.Start.Format("15:04")
			}
			due = append(due, line)
		}
	}
	section("✅ TAREAS", due)

	var ships []string
	for _, l := range s.shipmentLines(ctx, owner, now) {
		ships = append(ships, "• "+strings.TrimPrefix(l, "📦 "))
	}
	section("📦 ENVÍOS", ships)

	var bdays []string
	if rows, err := s.store.birthdays(ctx, owner, local.Format("01-02")); err == nil {
		for _, r := range rows {
			if c, err := decodeCard(r.Data); err == nil {
				v := viewContact(r.ID, c)
				line := "• " + v.Name
				if y := v.Birthday; len(y) == 10 {
					var year int
					fmt.Sscanf(y[:4], "%d", &year)
					if year > 1900 {
						line += fmt.Sprintf(" (%d años)", local.Year()-year)
					}
				}
				bdays = append(bdays, line)
			}
		}
	}
	section("🎂 CUMPLEAÑOS", bdays)

	// Correo: solo lo personal y lo importante se lista y pasa por la IA; de
	// lo demás (notificaciones y comerciales), el número.
	unread := mailQuery{UnreadOnly: true, Since: now.Add(-24 * time.Hour), Limit: 50, Categories: []string{catPersonal, catImportant}}
	mails, err := s.store.searchMessages(ctx, owner, unread)
	counts, _ := s.store.countMessages(ctx, owner, unread)
	if err == nil && (len(mails) > 0 || counts[catNotice]+counts[catBulk] > 0) {
		var lines []string
		for i, m := range mails {
			if i == 8 {
				lines = append(lines, fmt.Sprintf("• y %d más", len(mails)-8))
				break
			}
			line := "• " + senderName(m) + ": " + orNone(m.Subject)
			if m.Category == catImportant {
				line = "❗ " + senderName(m) + ": " + orNone(m.Subject) + " (" + m.Reason + ")"
			}
			lines = append(lines, line)
		}
		if n, c := counts[catNotice], counts[catBulk]; n+c > 0 {
			lines = append(lines, fmt.Sprintf("Además, %s y %s, que no pasan por la IA.", plural(n, "notificación", "notificaciones"), plural(c, "comercial", "comerciales")))
		}
		section(fmt.Sprintf("✉️ CORREO SIN LEER (24 h): %d", len(mails)+counts[catNotice]+counts[catBulk]), lines)
		if withAI && s.summarizer != nil && len(mails) > 0 {
			sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			text, err := s.summarizer(sctx, mailPrompt(mails))
			cancel()
			if err != nil {
				s.log.Warn("resumen del correo", "usuario", owner, "err", err)
			} else if text = strings.TrimSpace(text); text != "" {
				section("💡 LO IMPORTANTE DEL CORREO", []string{text})
			}
		}
	}

	// Mañana, en corto: hora y título de cada evento (como mucho 5).
	tomorrow, _ := s.agenda(ctx, owner, today.AddDate(0, 0, 1), today.AddDate(0, 0, 2))
	var next []string
	for _, e := range tomorrow {
		if e.Kind != "evento" {
			continue
		}
		if len(next) == 5 {
			next = append(next, "• …")
			break
		}
		line := "• Todo el día · " + e.Title
		if !e.AllDay && e.Start != nil {
			line = "• " + e.Start.Format("15:04") + " · " + e.Title
		}
		if e.Routine {
			line += " 🔁"
		}
		next = append(next, line)
	}
	section("🔜 MAÑANA", next)
	return strings.TrimSpace(b.String()), nil
}

// skyIcon es el icono del cielo que da Open-Meteo (geo.Sky).
func skyIcon(sky string) string {
	switch sky {
	case "despejado":
		return "☀️"
	case "poco nuboso":
		return "🌤"
	case "nublado":
		return "☁️"
	case "niebla":
		return "🌫"
	case "llovizna", "lluvia":
		return "🌧"
	case "nieve":
		return "❄️"
	case "tormenta":
		return "⛈"
	}
	return "🌡"
}

// setMailRules guarda los remitentes importantes y a ignorar y reclasifica.
func (s *service) setMailRules(ctx context.Context, owner string, important, ignore []string) error {
	for _, l := range [][]string{important, ignore} {
		for _, e := range l {
			if strings.ContainsAny(e, " <>\"") || !strings.Contains(e, ".") {
				return fmt.Errorf("«%s» no es una dirección ni un dominio (p. ej. ana@empresa.es o empresa.es)", e)
			}
		}
	}
	p, err := s.store.prefs(ctx, owner)
	if err != nil {
		return err
	}
	p.MailImportant, p.MailIgnore = important, ignore
	if err := s.store.savePrefs(ctx, owner, p); err != nil {
		return err
	}
	return s.store.reclassify(ctx, owner)
}

// markSender pone una dirección en una de las listas ("importante",
// "ignorar") o la quita de las dos ("").
func (s *service) markSender(ctx context.Context, owner, addr, as string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return errors.New("el correo no tiene remitente")
	}
	p, err := s.store.prefs(ctx, owner)
	if err != nil {
		return err
	}
	drop := func(l []string) []string { return slices.DeleteFunc(l, func(e string) bool { return e == addr }) }
	imp, ign := drop(p.MailImportant), drop(p.MailIgnore)
	switch as {
	case "importante":
		imp = append(imp, addr)
	case "ignorar":
		ign = append(ign, addr)
	case "":
	default:
		return fmt.Errorf("«%s»: usa importante, ignorar o nada", as)
	}
	return s.setMailRules(ctx, owner, imp, ign)
}

// weather es el tiempo de hoy en la ciudad de la persona (ok = false si no
// se sabe la ciudad o el servicio no responde).
func (s *service) weather(ctx context.Context, owner string) (place string, day geo.Day, ok bool) {
	if s.geo == nil {
		return "", day, false
	}
	p, _ := s.store.prefs(ctx, owner)
	city := p.weatherCity()
	if city == "" {
		return "", day, false
	}
	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	lat, lon, label, err := s.geo.City(wctx, city)
	if err != nil {
		s.log.Warn("tiempo", "err", err)
		return "", day, false
	}
	f, err := s.geo.Weather(wctx, lat, lon, 1)
	if err != nil || len(f.Days) == 0 {
		s.log.Warn("tiempo", "err", err)
		return "", day, false
	}
	return label, f.Days[0], true
}

func senderName(m message) string {
	if m.FromName != "" {
		return m.FromName
	}
	return m.FromAddr
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(sin asunto)"
	}
	return s
}

func mailPrompt(mails []message) string {
	var b strings.Builder
	b.WriteString("Estos son los correos sin leer de las últimas 24 horas de una persona. Escribe en español de España, " +
		"en texto plano sin Markdown, entre 3 y 6 líneas que empiecen por «• » con lo importante: qué requiere respuesta o " +
		"una acción, y cualquier cita, fecha o plazo que convenga apuntar en la agenda. Los de tipo Importante vienen de " +
		"servicios (seguridad, condiciones, pagos, citas, trámites): di en una línea qué cambia o qué hay que hacer. " +
		"No inventes nada.\n\n")
	for i, m := range mails {
		// El modelo gratuito admite pocos tokens por minuto: unos 16 000
		// caracteres de correo como mucho.
		if i == 12 || b.Len() > 16000 {
			fmt.Fprintf(&b, "(Y %d correos más sin incluir.)\n", len(mails)-i)
			break
		}
		kind := categoryName(m.Category)
		if m.Reason != "" {
			kind += " (" + m.Reason + ")"
		}
		fmt.Fprintf(&b, "--- Correo %d\nDe: %s <%s>\nAsunto: %s\nFecha: %s\nTipo: %s\n\n%s\n\n", i+1, m.FromName, m.FromAddr,
			m.Subject, m.Date.In(madrid).Format("02/01 15:04"), kind, truncate(m.Body, 2000))
	}
	return b.String()
}

// dueSummary prepara (en segundo plano) el resumen de la mañana de una
// cuenta cuando llega su hora; queda en la cola de avisos.
func (s *service) dueSummary(ctx context.Context, owner string) {
	now := s.now()
	local := now.In(madrid)
	day, hm := local.Format("2006-01-02"), local.Format("15:04")
	p, err := s.store.prefs(ctx, owner)
	if err != nil || !p.SummaryOn || hm < p.SummaryAt || hm >= "12:00" {
		return
	}
	var last string
	s.store.db.QueryRow(ctx, `SELECT summary_day FROM users WHERE username = $1`, owner).Scan(&last)
	if last == day {
		return
	}
	if v, busy := s.summaries.LoadOrStore(owner, day); busy && v == day {
		return
	}
	go func() {
		defer s.summaries.Delete(owner)
		ctx := context.Background()
		text, err := s.summary(ctx, owner, now, p.SummaryAI)
		if err != nil {
			s.log.Warn("resumen de la mañana", "usuario", owner, "err", err)
			return
		}
		s.store.db.Exec(ctx, `UPDATE users SET summary_day = $2 WHERE username = $1`, owner, day)
		s.store.addNotice(ctx, owner, "resumen", "Buenos días. "+text, now)
	}()
}

// run atiende los avisos cada minuto y lee el correo cada 5.
func (s *service) run(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
		tick := time.NewTicker(5 * time.Minute)
		defer tick.Stop()
		for {
			s.syncAll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		s.fireAlarms(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *service) syncAll(ctx context.Context) {
	list, err := s.store.accounts(ctx, "")
	if err != nil {
		s.log.Warn("correo", "err", err)
		return
	}
	for _, a := range list {
		if n, err := s.mail.syncAccount(ctx, a); err != nil {
			s.log.Warn("correo", "cuenta", a.Address, "err", err)
		} else if n > 0 {
			s.log.Info("correo nuevo", "cuenta", a.Address, "correos", n)
		}
	}
	if err := s.store.pruneShipments(ctx, s.now()); err != nil {
		s.log.Warn("envíos", "err", err)
	}
}

// remaining es la agenda entre from y to para el asistente: si el intervalo
// incluye este momento, empieza ahora (lo terminado no sale; lo que está en
// curso, sí) y se añaden delante las tareas vencidas sin hacer. withPast lo
// da todo desde from.
func (s *service) remaining(ctx context.Context, owner string, from, to time.Time, withPast bool) ([]entry, error) {
	now := s.now()
	fromNow := !withPast && from.Before(now) && to.After(now)
	if fromNow {
		from = now
	}
	l, err := s.agenda(ctx, owner, from, to)
	if err != nil || !fromNow {
		return l, err
	}
	tasks, err := s.tasks(ctx, owner)
	if err != nil {
		return l, err
	}
	var overdue []entry
	for _, t := range tasks {
		if t.Overdue {
			overdue = append(overdue, t)
		}
	}
	return append(overdue, l...), nil
}
