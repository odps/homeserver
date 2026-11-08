package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-ical"
)

// Entreno: las rutinas de gimnasio con días fijos salen en el calendario de
// rutinas (y así en el móvil, en el resumen de la mañana y al buscar
// huecos). Entreno manda la lista completa de una persona cada vez que cambia
// algo, y aquí se crea, cambia o borra un evento por rutina
// («entreno-<id>.ics»). Lo que no ha cambiado no se toca, para no perder los
// días saltados ni lo que se haya retocado en el móvil.
//
// Al revés, el resumen de la mañana pide a Entreno unas líneas con lo de hoy
// (la rutina y lo propuesto, los objetivos de comida y cómo fue ayer).

const (
	gymPrefix = "entreno-"
	gymMark   = "X-HOMESERVER-ENTRENO" // huella de lo que mandó Entreno
	maxGym    = 30
)

// gymRoutine es una rutina tal como la manda Entreno.
type gymRoutine struct {
	ID      int64  `json:"id"`
	Name    string `json:"nombre"`
	Days    []int  `json:"dias"`              // 0 domingo … 6 sábado
	Start   string `json:"hora,omitempty"`    // HH:MM; vacío: todo el día
	Minutes int    `json:"minutos,omitempty"` // duración, con hora
}

var weekCodes = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func (g *gymRoutine) check() error {
	g.Name = strings.TrimSpace(g.Name)
	if g.ID <= 0 || g.Name == "" || utf8.RuneCountInString(g.Name) > 60 || len(g.Days) == 0 || len(g.Days) > 7 {
		return errors.New("rutina no válida")
	}
	for _, d := range g.Days {
		if d < 0 || d > 6 {
			return errors.New("día no válido")
		}
	}
	slices.Sort(g.Days)
	g.Days = slices.Compact(g.Days)
	if g.Start == "" {
		g.Minutes = 0
		return nil
	}
	if _, err := time.Parse("15:04", g.Start); err != nil || len(g.Start) != 5 {
		return errors.New("hora no válida")
	}
	if g.Minutes < 15 || g.Minutes > 300 {
		return errors.New("duración no válida")
	}
	return nil
}

// mark resume lo que define el evento: si no cambia, el evento se deja.
func (g gymRoutine) mark() string {
	return fmt.Sprintf("1|%s|%v|%s|%d", g.Name, g.Days, g.Start, g.Minutes)
}

// gymEvent hace el evento semanal de una rutina, desde el primer día que
// toque a partir de from.
func gymEvent(g gymRoutine, from time.Time, now time.Time) (*ical.Calendar, error) {
	day := dayStart(from)
	for !slices.Contains(g.Days, int(day.Weekday())) {
		day = day.AddDate(0, 0, 1)
	}
	var codes []string
	for _, d := range g.Days {
		codes = append(codes, weekCodes[d])
	}
	// La semana empieza en lunes, como en parseDays.
	slices.SortFunc(codes, func(a, b string) int {
		return (slices.Index(weekCodes, a)+6)%7 - (slices.Index(weekCodes, b)+6)%7
	})
	title := "Gimnasio: " + g.Name
	notes := "Rutina de Entreno. Los días y la hora se cambian allí."
	rule := "FREQ=WEEKLY;BYDAY=" + strings.Join(codes, ",")
	in := eventInput{Title: &title, Notes: &notes, Repeat: &rule, Alarms: &[]int{}}
	if g.Start == "" {
		allDay := true
		end := day.AddDate(0, 0, 1)
		in.Start, in.End, in.AllDay = &day, &end, &allDay
	} else {
		t, _ := time.Parse("15:04", g.Start)
		start := day.Add(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute)
		end := start.Add(time.Duration(g.Minutes) * time.Minute)
		in.Start, in.End = &start, &end
	}
	cal, err := newObject(ical.CompEvent, in, now)
	if err != nil {
		return nil, err
	}
	m, _ := master(cal)
	m.Props.SetText(ical.PropUID, fmt.Sprintf("%s%d@homeserver", gymPrefix, g.ID))
	return cal, nil
}

// keepSkips pasa los días saltados del evento viejo al nuevo, a la hora
// nueva, si ese día de la semana sigue tocando.
func keepSkips(old *ical.Component, cal *ical.Calendar, g gymRoutine) {
	m, _ := master(cal)
	var t time.Time
	if g.Start != "" {
		t, _ = time.Parse("15:04", g.Start)
	}
	for _, ex := range old.Props.Values(ical.PropExceptionDates) {
		ts, _, err := propTimes(&ex)
		if err != nil {
			continue
		}
		for _, x := range ts {
			day := dayStart(x.In(madrid))
			if !slices.Contains(g.Days, int(day.Weekday())) {
				continue
			}
			p := ical.NewProp(ical.PropExceptionDates)
			if g.Start == "" {
				p.SetDate(day)
			} else {
				p.SetDateTime(day.Add(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute))
			}
			m.Props.Add(p)
		}
	}
}

// syncGym deja el calendario de rutinas como dice Entreno. Devuelve cuántos
// eventos ha creado o cambiado y cuántos ha borrado.
func (s *service) syncGym(ctx context.Context, owner string, list []gymRoutine) (changed, removed int, err error) {
	if len(list) > maxGym {
		return 0, 0, errors.New("demasiadas rutinas")
	}
	want := map[string]gymRoutine{}
	for _, g := range list {
		if err := g.check(); err != nil {
			return 0, 0, err
		}
		want[fmt.Sprintf("%s%d.ics", gymPrefix, g.ID)] = g
	}
	c, err := s.routinesCalendar(ctx, owner)
	if err != nil {
		return 0, 0, err
	}
	events := map[string]extEvent{}
	for name, g := range want {
		events[name] = extEvent{mark: g.mark(),
			build: func(now time.Time) (*ical.Calendar, error) { return gymEvent(g, now, now) },
			carry: func(old *ical.Component, cal *ical.Calendar) { keepSkips(old, cal, g) }}
	}
	return s.syncExternal(ctx, c.ID, gymPrefix, gymMark, events)
}

// gymSyncPost es POST /interno/entreno/rutinas, solo desde Entreno.
func (w *web) gymSyncPost(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		User     string       `json:"usuario"`
		Routines []gymRoutine `json:"rutinas"`
	}
	if !w.internalJSON(rw, r, &in.User, &in) {
		return
	}
	changed, removed, err := w.svc.syncGym(r.Context(), in.User, in.Routines)
	if err != nil {
		w.svc.log.Warn("rutinas de entreno", "err", err)
		rw.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(rw).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(rw).Encode(map[string]any{"agenda": true, "cambiadas": changed, "quitadas": removed})
}

// gymToday pide a Entreno las líneas de hoy para el resumen de la mañana.
// Si no responde, el resumen sale sin ellas.
func gymToday(base string) func(ctx context.Context, owner string) ([]string, error) {
	return appToday("entreno", base)
}

// appToday pide a otra app del servidor (Entreno, Cartera) sus líneas para
// el resumen de la mañana, por POST /interno/hoy (el usuario va en el
// cuerpo, no en la URL).
func appToday(name, base string) func(ctx context.Context, owner string) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	return func(ctx context.Context, owner string) ([]string, error) {
		body, _ := json.Marshal(map[string]string{"usuario": owner})
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/interno/hoy", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s no responde: %w", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", name, resp.Status)
		}
		var out struct {
			Lines []string `json:"lineas"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&out); err != nil {
			return nil, err
		}
		if len(out.Lines) > 12 {
			out.Lines = out.Lines[:12]
		}
		for i, l := range out.Lines {
			out.Lines[i] = truncate(strings.TrimSpace(l), 200)
		}
		return slices.DeleteFunc(out.Lines, func(l string) bool { return l == "" }), nil
	}
}
