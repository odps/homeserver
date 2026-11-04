package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-ical"
)

// Papeles: cada documento con fecha de caducidad es un evento de todo el día
// en el calendario «Vencimientos», con un aviso a las 9:00 de cada día de
// antelación (60, 30, 7…). Los avisos van a una hora fija (no relativos)
// porque los relativos de más de 8 días no se miran (nextAlarm, firing).
// Papeles manda solo el título y la fecha: nunca el número ni las notas.

const (
	deadlinesSlug   = "vencimientos"
	deadlinesPrefix = "papeles-"
	deadlinesMark   = "X-HOMESERVER-PAPELES"
	maxDeadlines    = 500
	alarmHour       = 9
)

// deadline es un vencimiento tal como lo manda Papeles.
type deadline struct {
	ID     int64  `json:"id"`
	Title  string `json:"titulo"`
	Date   string `json:"fecha"`  // AAAA-MM-DD
	Alarms []int  `json:"avisos"` // días de antelación
	day    time.Time
}

func (d *deadline) check() error {
	d.Title = strings.TrimSpace(d.Title)
	if d.ID <= 0 || d.Title == "" || utf8.RuneCountInString(d.Title) > 160 {
		return errors.New("vencimiento no válido")
	}
	t, err := time.ParseInLocation("2006-01-02", d.Date, madrid)
	if err != nil || t.Year() < 2000 || t.Year() > 2200 {
		return errors.New("fecha no válida")
	}
	d.day = t
	if len(d.Alarms) > 5 {
		return errors.New("demasiados avisos")
	}
	for _, a := range d.Alarms {
		if a < 0 || a > 365 {
			return errors.New("aviso no válido")
		}
	}
	slices.Sort(d.Alarms)
	d.Alarms = slices.Compact(d.Alarms)
	return nil
}

func (d deadline) mark() string {
	return fmt.Sprintf("1|%s|%s|%v", d.Title, d.Date, d.Alarms)
}

// deadlineEvent hace el evento: todo el día, con los avisos que aún no han
// pasado.
func deadlineEvent(d deadline, now time.Time) (*ical.Calendar, error) {
	title := d.Title
	notes := "Vencimiento de Papeles. Los datos y los avisos se cambian allí."
	allDay := true
	end := d.day.AddDate(0, 0, 1)
	cal, err := newObject(ical.CompEvent, eventInput{Title: &title, Notes: &notes, Start: &d.day, End: &end, AllDay: &allDay, Alarms: &[]int{}}, now)
	if err != nil {
		return nil, err
	}
	m, _ := master(cal)
	m.Props.SetText(ical.PropUID, fmt.Sprintf("%s%d@homeserver", deadlinesPrefix, d.ID))
	for _, days := range d.Alarms {
		at := d.day.AddDate(0, 0, -days).Add(alarmHour * time.Hour)
		if !at.After(now) {
			continue
		}
		a := ical.NewComponent(ical.CompAlarm)
		a.Props.SetText(ical.PropAction, "DISPLAY")
		a.Props.SetText(ical.PropDescription, title)
		p := ical.NewProp(ical.PropTrigger)
		p.SetDateTime(at.UTC())
		p.Params.Set(ical.ParamValue, string(ical.ValueDateTime))
		a.Props.Set(p)
		m.Children = append(m.Children, a)
	}
	return cal, nil
}

// deadlinesCalendar devuelve el calendario de vencimientos, y lo crea si no existe.
func (s *service) deadlinesCalendar(ctx context.Context, owner string) (collection, error) {
	c, err := s.store.collection(ctx, owner, "cal", deadlinesSlug)
	if !errors.Is(err, errNotFound) {
		return c, err
	}
	err = s.store.createCollection(ctx, collection{Owner: owner, Kind: "cal", Slug: deadlinesSlug, Name: "Vencimientos",
		Color: "#b5651d", Components: []string{ical.CompEvent}})
	if err != nil && !errors.Is(err, errConflict) {
		return c, err
	}
	return s.store.collection(ctx, owner, "cal", deadlinesSlug)
}

// syncDeadlines deja el calendario de vencimientos como dice Papeles.
func (s *service) syncDeadlines(ctx context.Context, owner string, list []deadline) (changed, removed int, err error) {
	if len(list) > maxDeadlines {
		return 0, 0, errors.New("demasiados vencimientos")
	}
	events := map[string]extEvent{}
	for _, d := range list {
		if err := d.check(); err != nil {
			return 0, 0, err
		}
		events[fmt.Sprintf("%s%d.ics", deadlinesPrefix, d.ID)] = extEvent{mark: d.mark(),
			build: func(now time.Time) (*ical.Calendar, error) { return deadlineEvent(d, now) }}
	}
	c, err := s.deadlinesCalendar(ctx, owner)
	if err != nil {
		return 0, 0, err
	}
	return s.syncExternal(ctx, c.ID, deadlinesPrefix, deadlinesMark, events)
}

// deadlinesPost es POST /interno/papeles/vencimientos, solo desde Papeles.
func (w *web) deadlinesPost(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		User string     `json:"usuario"`
		List []deadline `json:"vencimientos"`
	}
	if !w.internalJSON(rw, r, &in.User, &in) {
		return
	}
	changed, removed, err := w.svc.syncDeadlines(r.Context(), in.User, in.List)
	if err != nil {
		w.svc.log.Warn("vencimientos de papeles", "err", err)
		rw.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(rw).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(rw).Encode(map[string]any{"agenda": true, "cambiados": changed, "quitados": removed})
}
