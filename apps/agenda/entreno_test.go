package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGymRoutines(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	svc.store.ensureUser(ctx, "luis")
	monday := time.Date(2027, 3, 1, 7, 0, 0, 0, madrid)
	svc.now = func() time.Time { return monday }
	day := func(s string) time.Time { d, _ := time.ParseInLocation("2006-01-02", s, madrid); return d }

	list := []gymRoutine{
		{ID: 7, Name: "Torso", Days: []int{1, 4}, Start: "18:00", Minutes: 75},
		{ID: 8, Name: "Pierna", Days: []int{3}},
	}
	if n, r, err := svc.syncGym(ctx, "ana", list); err != nil || n != 2 || r != 0 {
		t.Fatalf("primera vez: %d %d %v", n, r, err)
	}
	l, _ := svc.agenda(ctx, "ana", day("2027-03-01"), day("2027-03-08"))
	var got []string
	for _, e := range l {
		when := "día"
		if !e.AllDay {
			when = e.Start.Format("Mon 15:04") + "-" + e.End.Format("15:04")
		} else {
			when = e.Start.Format("Mon") + " día"
		}
		got = append(got, when+" "+e.Title)
		if !e.Routine {
			t.Fatalf("no está en rutinas: %+v", e)
		}
	}
	want := "Mon 18:00-19:15 Gimnasio: Torso|Wed día Gimnasio: Pierna|Thu 18:00-19:15 Gimnasio: Torso"
	if strings.Join(got, "|") != want {
		t.Fatalf("semana:\n%s\nquería:\n%s", strings.Join(got, "|"), want)
	}
	// Ocupa al buscar huecos (la de todo el día, no).
	slots, _ := svc.freeSlots(ctx, "ana", day("2027-03-01"), day("2027-03-01"), "17:00", "20:00", 30*time.Minute, true)
	if len(slots) != 2 || slots[0].End != "18:00" || slots[1].Start != "19:15" {
		t.Fatalf("huecos: %+v", slots)
	}

	// Saltar el jueves y volver a mandar lo mismo: no se toca nada.
	thu, _ := svc.agenda(ctx, "ana", day("2027-03-04"), day("2027-03-05"))
	if _, err := svc.skip(ctx, "ana", thu[0].ID, day("2027-03-04")); err != nil {
		t.Fatal(err)
	}
	if n, r, err := svc.syncGym(ctx, "ana", list); err != nil || n != 0 || r != 0 {
		t.Fatalf("sin cambios: %d %d %v", n, r, err)
	}
	// Cambia la hora: se rehace, y el jueves sigue saltado.
	list[0].Start = "19:00"
	if n, _, err := svc.syncGym(ctx, "ana", list); err != nil || n != 1 {
		t.Fatalf("cambio: %d %v", n, err)
	}
	if l, _ := svc.agenda(ctx, "ana", day("2027-03-04"), day("2027-03-05")); len(l) != 0 {
		t.Fatalf("el jueves saltado ha vuelto: %+v", l)
	}
	if l, _ := svc.agenda(ctx, "ana", day("2027-03-08"), day("2027-03-09")); len(l) != 1 || l[0].Start.Format("15:04") != "19:00" {
		t.Fatalf("el lunes siguiente: %+v", l)
	}
	// Una rutina que ya no está (o sin días) se borra; lo demás de rutinas no.
	if _, err := svc.createRoutine(ctx, "ana", routineInput{Title: "Trabajo", Days: []string{"laborables"}, Start: "09:00", End: "17:00"}); err != nil {
		t.Fatal(err)
	}
	if n, r, err := svc.syncGym(ctx, "ana", list[1:]); err != nil || n != 0 || r != 1 {
		t.Fatalf("quitar: %d %d %v", n, r, err)
	}
	if rs, _ := svc.routines(ctx, "ana"); len(rs) != 2 {
		t.Fatalf("rutinas tras quitar: %+v", rs)
	}
	// Cada uno lo suyo.
	if rs, _ := svc.routines(ctx, "luis"); len(rs) != 0 {
		t.Fatalf("luis ve rutinas de ana: %+v", rs)
	}
	for _, bad := range []gymRoutine{{ID: 1, Name: "X"}, {ID: 1, Name: "X", Days: []int{9}}, {ID: 1, Name: "X", Days: []int{1}, Start: "25:00", Minutes: 60},
		{ID: 1, Name: "X", Days: []int{1}, Start: "10:00", Minutes: 5}, {Name: "X", Days: []int{1}}} {
		if _, _, err := svc.syncGym(ctx, "ana", []gymRoutine{bad}); err == nil {
			t.Fatalf("acepta %+v", bad)
		}
	}

	// Por HTTP: solo desde dentro, y sin crear cuenta a quien no usa la agenda.
	w := &web{svc: svc}
	post := func(body string, gateway bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/interno/entreno/rutinas", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			r.Header.Set("X-Forwarded-Prefix", "/agenda")
		}
		rec := httptest.NewRecorder()
		w.gymSyncPost(rec, r)
		return rec
	}
	if rec := post(`{"usuario":"ana","rutinas":[]}`, true); rec.Code != http.StatusForbidden {
		t.Fatalf("por la pasarela: %d", rec.Code)
	}
	rec := post(`{"usuario":"nadie","rutinas":[{"id":3,"nombre":"X","dias":[1]}]}`, false)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out["agenda"] != false {
		t.Fatalf("sin agenda: %d %s", rec.Code, rec.Body)
	}
	if users, _ := svc.store.users(ctx); strings.Contains(strings.Join(users, ","), "nadie") {
		t.Fatal("ha creado la cuenta")
	}
	if rec := post(`{"usuario":"ana","rutinas":[]}`, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"quitadas":1`) {
		t.Fatalf("vaciar: %d %s", rec.Code, rec.Body)
	}

	// El resumen de la mañana trae lo de Entreno (si responde).
	svc.gym = func(ctx context.Context, owner string) ([]string, error) {
		return []string{"Toca Torso: 5 ejercicios."}, nil
	}
	text, _ := svc.summary(ctx, "ana", monday, false)
	if !strings.Contains(text, "🏋️ Entreno\n• Toca Torso: 5 ejercicios.") {
		t.Fatalf("resumen:\n%s", text)
	}
}

func TestGymTodayClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/interno/hoy" || in["usuario"] != "ana" {
			http.Error(w, "no", 400)
			return
		}
		w.Write([]byte(`{"lineas":["una"," ","dos"]}`))
	}))
	defer srv.Close()
	lines, err := gymToday(srv.URL)(context.Background(), "ana")
	if err != nil || strings.Join(lines, "|") != "una|dos" {
		t.Fatalf("%v %v", lines, err)
	}
	if _, err := gymToday(srv.URL)(context.Background(), "luis"); err == nil {
		t.Fatal("no da error")
	}
}
