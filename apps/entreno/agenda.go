package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"homeserver/platform/app"
)

// Agenda: las rutinas con días fijos van al calendario «Rutinas» de la
// Agenda (que las sincroniza con el móvil y las tiene en cuenta al buscar
// huecos), y el resumen de la mañana de la Agenda pide aquí lo de hoy. Las
// dos apps hablan por 127.0.0.1, sin pasar por la pasarela.

type agendaClient struct {
	base string
	http *http.Client
}

func newAgenda() *agendaClient {
	base := os.Getenv("AGENDA_URL")
	if base == "" {
		base = "http://127.0.0.1:8105"
	}
	return &agendaClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

// agendaRoutine es lo que se manda de cada rutina: el nombre, los días y la
// hora. Los ejercicios no salen de Entreno.
type agendaRoutine struct {
	ID      int64  `json:"id"`
	Name    string `json:"nombre"`
	Days    []int  `json:"dias"`
	Start   string `json:"hora,omitempty"`
	Minutes int    `json:"minutos,omitempty"`
}

// agendaResult es lo que contesta la Agenda.
type agendaResult struct {
	Agenda  bool   `json:"agenda"` // false: la persona no usa la Agenda
	Changed int    `json:"cambiadas"`
	Removed int    `json:"quitadas"`
	Error   string `json:"error"`
}

var errNoAgenda = errors.New("la agenda no responde")

func (a *agendaClient) push(ctx context.Context, owner string, list []agendaRoutine) (agendaResult, error) {
	var res agendaResult
	if list == nil {
		list = []agendaRoutine{}
	}
	body, _ := json.Marshal(map[string]any{"usuario": owner, "rutinas": list})
	req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/interno/entreno/rutinas", strings.NewReader(string(body)))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return res, errNoAgenda
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&res); err != nil && resp.StatusCode == http.StatusOK {
		return res, err
	}
	if resp.StatusCode != http.StatusOK {
		if res.Error == "" {
			res.Error = resp.Status
		}
		return res, fmt.Errorf("agenda: %s", res.Error)
	}
	return res, nil
}

// agendaRoutines son las rutinas que van al calendario.
func agendaRoutines(list []routine) []agendaRoutine {
	out := []agendaRoutine{}
	for _, r := range list {
		if len(r.Days) == 0 || r.NoAgenda {
			continue
		}
		out = append(out, agendaRoutine{ID: r.ID, Name: r.Name, Days: r.Days, Start: r.Hour, Minutes: r.Minutes})
	}
	return out
}

// syncAgenda manda a la Agenda las rutinas de la persona tal como están.
func (s *service) syncAgenda(ctx context.Context, owner string) (agendaResult, error) {
	if s.agenda == nil {
		return agendaResult{}, errNoAgenda
	}
	list, err := s.store.routines(ctx, owner)
	if err != nil {
		return agendaResult{}, err
	}
	return s.agenda.push(ctx, owner, agendaRoutines(list))
}

// syncAgendaQuiet es syncAgenda tras guardar o borrar una rutina: si la
// Agenda falla, queda en el log y se arregla con el botón de Rutinas.
func (s *service) syncAgendaQuiet(ctx context.Context, owner string) {
	if s.agenda == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.syncAgenda(ctx, owner); err != nil {
		s.log.Warn("rutinas a la agenda", "err", err)
	}
}

// ---- Resumen de la mañana ----

const maxTodayItems = 8

// todayLines son las líneas de Entreno para el resumen de la mañana: la
// rutina de hoy (solo si tiene hoy como día fijo) con lo propuesto, y la
// comida de ayer frente a los objetivos. Sin nada que decir, ninguna.
func (s *service) todayLines(ctx context.Context, owner string, now time.Time) ([]string, error) {
	var lines []string
	list, err := s.store.routines(ctx, owner)
	if err != nil {
		return nil, err
	}
	wd := int(now.In(madrid).Weekday())
	for _, r := range list {
		if !containsInt(r.Days, wd) {
			continue
		}
		items, err := s.plan(ctx, owner, r, now)
		if err != nil {
			return nil, err
		}
		head := "Toca " + r.Name
		if r.Hour != "" {
			head += " a las " + r.Hour
		}
		lines = append(lines, head+":")
		for i, it := range items {
			if i == maxTodayItems {
				lines = append(lines, fmt.Sprintf("• y %d más", len(items)-i))
				break
			}
			line := "• " + it.Name + ": " + it.Goal
			if it.Stalled {
				line += " (estancado: semana de descarga)"
			}
			lines = append(lines, line)
		}
		break
	}

	g, err := s.store.goals(ctx, owner)
	if err != nil {
		return nil, err
	}
	yesterday := dayOf(now).AddDate(0, 0, -1)
	meals, err := s.store.meals(ctx, owner, yesterday, yesterday)
	if err != nil {
		return nil, err
	}
	goal := func() string {
		var parts []string
		if g.Kcal > 0 {
			parts = append(parts, thousands(g.Kcal)+" kcal")
		}
		if g.Prot > 0 {
			parts = append(parts, trimNum(g.Prot)+" g de proteína")
		}
		return strings.Join(parts, " y ")
	}
	switch {
	case len(meals) > 0:
		t := dayOfFood(yesterday, meals).Total
		line := fmt.Sprintf("Ayer comiste %s kcal y %s g de proteína", thousands(t.Kcal), trimNum(t.Prot))
		if g.Kcal > 0 || g.Prot > 0 {
			line += " (objetivo: " + goal() + ")"
		}
		lines = append(lines, line+".")
	case g.Kcal > 0 || g.Prot > 0:
		lines = append(lines, "Objetivo de hoy: "+goal()+".")
	}
	return lines, nil
}

// internalToday es POST /interno/hoy, solo para la Agenda.
func (w *web) internalToday(rw http.ResponseWriter, r *http.Request) {
	if !app.Internal(r) {
		http.Error(rw, "solo para las apps del servidor", http.StatusForbidden)
		return
	}
	var in struct {
		User string `json:"usuario"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil || in.User == "" {
		http.Error(rw, "petición no válida", http.StatusBadRequest)
		return
	}
	lines, err := w.svc.todayLines(r.Context(), in.User, w.now())
	if err != nil {
		w.svc.log.Warn("resumen de hoy", "err", err)
		http.Error(rw, "error", http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"lineas": lines})
}

// agendaPost es el botón «Pasar a la Agenda» de Rutinas.
func (w *web) agendaPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	res, err := w.svc.syncAgenda(r.Context(), who.User)
	switch {
	case err != nil:
		w.svc.log.Warn("rutinas a la agenda", "err", err)
		back(rw, r, "/rutinas", "agenda-error")
	case !res.Agenda:
		back(rw, r, "/rutinas", "agenda-no")
	default:
		back(rw, r, "/rutinas", "agenda")
	}
}
