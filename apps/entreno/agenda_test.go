package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRoutineHour(t *testing.T) {
	base := routine{Name: "Torso", Days: []int{1}, Items: []routineItem{{Key: "bench_press"}}}
	r := base
	r.Hour = "7:05"
	if err := r.check(); err != nil || r.Hour != "07:05" || r.Minutes != 75 {
		t.Fatalf("hora: %+v %v", r, err)
	}
	r = base
	r.Minutes = 60
	if err := r.check(); err != nil || r.Minutes != 0 {
		t.Fatalf("sin hora no hay duración: %+v %v", r, err)
	}
	for _, bad := range []routine{{Hour: "25:00"}, {Hour: "18:00", Minutes: 5}, {Hour: "18:00", Minutes: 400}} {
		r = base
		r.Hour, r.Minutes = bad.Hour, bad.Minutes
		if err := r.check(); err == nil || routineErr(err) == "" {
			t.Fatalf("acepta %+v (%v)", bad, err)
		}
	}
	got := agendaRoutines([]routine{
		{ID: 1, Name: "A", Days: []int{1, 4}, Hour: "18:00", Minutes: 60, Items: base.Items},
		{ID: 2, Name: "B", Items: base.Items},                                 // rotación: no va
		{ID: 3, Name: "C", Days: []int{3}, NoAgenda: true, Items: base.Items}, // quitada
		{ID: 4, Name: "D", Days: []int{6}, Items: base.Items},
	})
	if len(got) != 2 || got[0].ID != 1 || got[0].Start != "18:00" || got[1].ID != 4 || got[1].Start != "" {
		t.Fatalf("a la agenda: %+v", got)
	}
}

func TestAgendaSyncAndToday(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	var sent []map[string]any
	agenda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		sent = append(sent, in)
		if in["usuario"] == "sinagenda" {
			w.Write([]byte(`{"agenda":false}`))
			return
		}
		w.Write([]byte(`{"agenda":true,"cambiadas":1}`))
	}))
	defer agenda.Close()
	svc.agenda = &agendaClient{base: agenda.URL, http: agenda.Client()}

	// Lunes 2 de noviembre de 2026.
	monday := time.Date(2026, 11, 2, 7, 0, 0, 0, madrid)
	svc.now = func() time.Time { return monday }
	for i := range 3 {
		svc.record(ctx, "ana", "Bench press 4x6 60kg rir 2\nLat machine 3x10 50kg rir 2", monday.AddDate(0, 0, -7*(i+1)), "web")
	}
	id, err := svc.store.saveRoutine(ctx, "ana", routine{Name: "Torso", Days: []int{1, 4}, Hour: "18:00",
		Items: []routineItem{{Key: "bench_press", Sets: 4, Min: 6, Max: 8, RIR: 2}, {Key: "lat_pulldown", Sets: 3, Min: 10, Max: 12, RIR: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.syncAgenda(ctx, "ana")
	if err != nil || !res.Agenda || res.Changed != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	// Solo el nombre, los días y la hora: los ejercicios no salen.
	body, _ := json.Marshal(sent[0])
	if strings.Contains(string(body), "bench") || !strings.Contains(string(body), `"hora":"18:00"`) || !strings.Contains(string(body), `"minutos":75`) {
		t.Fatalf("lo mandado: %s", body)
	}

	// El resumen de la mañana.
	svc.store.saveGoals(ctx, "ana", goals{Kcal: 2400, Prot: 150})
	lines, err := svc.todayLines(ctx, "ana", monday)
	if err != nil || len(lines) != 4 || lines[0] != "Toca Torso a las 18:00:" || !strings.HasPrefix(lines[1], "• Press de banca") ||
		lines[3] != "Objetivo de hoy: 2.400 kcal y 150 g de proteína." {
		t.Fatalf("hoy:\n%s\n%v", strings.Join(lines, "\n"), err)
	}
	if _, err := svc.store.addMeals(ctx, "ana", monday.AddDate(0, 0, -1), "web", []mealEntry{{Meal: "comida", Name: "Arroz", Amount: 100, Unit: "g", macros: macros{Kcal: 1800, Prot: 120}}}); err != nil {
		t.Fatal(err)
	}
	lines, _ = svc.todayLines(ctx, "ana", monday)
	if last := lines[len(lines)-1]; last != "Ayer comiste 1.800 kcal y 120 g de proteína (objetivo: 2.400 kcal y 150 g de proteína)." {
		t.Fatalf("ayer: %s", last)
	}
	// El martes no toca nada (no se dice la rotación).
	if lines, _ := svc.todayLines(ctx, "ana", monday.AddDate(0, 0, 1)); len(lines) != 1 {
		t.Fatalf("martes: %v", lines)
	}
	if lines, _ := svc.todayLines(ctx, "luis", monday); len(lines) != 0 {
		t.Fatalf("luis ve lo de ana: %v", lines)
	}

	// Por HTTP: /interno/hoy solo desde dentro; el botón de Rutinas.
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := &webEnv{t: t, svc: svc, srv: srv}
	call := func(gateway bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/interno/hoy", strings.NewReader(`{"usuario":"ana"}`))
		r.RemoteAddr = "127.0.0.1:4000"
		if gateway {
			r.Header.Set("X-Forwarded-Prefix", "/entreno")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	if rec := call(true); rec.Code != http.StatusForbidden {
		t.Fatalf("por la pasarela: %d", rec.Code)
	}
	if rec := call(false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Toca Torso") {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}
	if code, loc, _ := e.post("/rutinas/agenda", "ana", url.Values{}); code != http.StatusSeeOther || !strings.Contains(loc, "ok=agenda") {
		t.Fatalf("botón: %d %s", code, loc)
	}
	if _, loc, _ := e.post("/rutinas/agenda", "sinagenda", url.Values{}); !strings.Contains(loc, "ok=agenda-no") {
		t.Fatalf("sin agenda: %s", loc)
	}
	// Guardar o borrar una rutina avisa a la Agenda.
	n := len(sent)
	if code, _, _ := e.post("/rutina/"+strconv.FormatInt(id, 10)+"/borrar", "ana", url.Values{}); code != http.StatusSeeOther {
		t.Fatalf("borrar: %d", code)
	}
	if len(sent) != n+1 || !strings.Contains(mustJSON(sent[n]), `"rutinas":[]`) {
		t.Fatalf("tras borrar: %v", sent[n:])
	}
	agenda.Close()
	if _, loc, _ := e.post("/rutinas/agenda", "ana", url.Values{}); !strings.Contains(loc, "ok=agenda-error") {
		t.Fatalf("agenda caída: %s", loc)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
