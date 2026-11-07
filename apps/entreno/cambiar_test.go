package main

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMoveExercise(t *testing.T) {
	e := newWebEnv(t)
	svc := e.svc
	ctx := context.Background()
	day := time.Date(2026, 10, 5, 18, 0, 0, 0, madrid)
	svc.record(ctx, "ana", "Press banca 3x10 20kg rir 2\nLat machine 3x10 50kg", day, "web")
	svc.record(ctx, "ana", "Bench press 3x8 22kg", day.AddDate(0, 0, 3), "web")
	svc.record(ctx, "ana", "Press banca 3x10 22kg", day.AddDate(0, 0, 7), "web")
	svc.record(ctx, "luis", "Press banca 5x5 80kg", day, "web")
	if _, err := svc.store.saveRoutine(ctx, "ana", routine{Name: "Torso", Items: []routineItem{{Key: "bench_press"}, {Key: "lat_pulldown"}}}); err != nil {
		t.Fatal(err)
	}
	hist := func(user, key string) int {
		l, _ := svc.store.history(ctx, user, key, 0)
		return len(l)
	}
	if hist("ana", "bench_press") != 3 {
		t.Fatalf("antes: %d", hist("ana", "bench_press"))
	}

	// La página: las formas de escribirlo, y nada para otro ni sin historial.
	code, body := e.get("/ejercicio/bench_press/cambiar", "ana")
	if code != 200 || !strings.Contains(body, "«Press banca»") || !strings.Contains(body, "«Bench press»") || strings.Contains(body, `<option value="bench_press"`) {
		t.Fatalf("página: %d", code)
	}
	if code, _ := e.get("/ejercicio/dumbbell_bench_press/cambiar", "ana"); code != 404 {
		t.Fatalf("sin historial: %d", code)
	}

	// Solo lo escrito como «Bench press».
	code, loc, _ := e.post("/ejercicio/bench_press/cambiar", "ana", url.Values{"a": {"dumbbell_bench_press"}, "nombre": {"Bench press"}})
	if code != 303 || !strings.Contains(loc, "/ejercicio/dumbbell_bench_press?ok=cambiado") {
		t.Fatalf("parcial: %d %s", code, loc)
	}
	if hist("ana", "bench_press") != 2 || hist("ana", "dumbbell_bench_press") != 1 {
		t.Fatal("parcial: no cuadra")
	}
	if r, _ := svc.store.routine(ctx, "ana", 0); r.ID != 0 {
		t.Fatal("rutina 0")
	}
	if l, _ := svc.store.routines(ctx, "ana"); l[0].Items[0].Key != "bench_press" {
		t.Fatal("la parcial cambia la rutina")
	}

	// Todo: también la rutina, y lo nuevo con ese nombre va al bueno.
	res, err := svc.moveExercise(ctx, "ana", "bench_press", "dumbbell_bench_press", nil)
	if err != nil || res.Moved != 2 || res.Routines != 1 || strings.Join(res.Aliases, ",") != "Press banca" {
		t.Fatalf("todo: %+v %v", res, err)
	}
	if hist("ana", "dumbbell_bench_press") != 3 || hist("ana", "bench_press") != 0 || hist("luis", "bench_press") != 1 {
		t.Fatal("todo: no cuadra")
	}
	if l, _ := svc.store.routines(ctx, "ana"); l[0].Items[0].Key != "dumbbell_bench_press" {
		t.Fatalf("rutina: %+v", l[0].Items)
	}
	rec, err := svc.record(ctx, "ana", "Press banca 3x10 24kg", day.AddDate(0, 0, 10), "web")
	if err != nil || rec.Saved[0].Key != "dumbbell_bench_press" {
		t.Fatalf("después: %+v %v", rec, err)
	}
	if rec, _ := svc.record(ctx, "luis", "Press banca 5x5 82kg", day.AddDate(0, 0, 10), "web"); rec.Saved[0].Key != "bench_press" {
		t.Fatal("a luis también le cambia")
	}
	// El nombre oficial del viejo no se vuelve alias.
	if rec, _ := svc.record(ctx, "ana", "Press de banca con barra 1x5 60kg", day.AddDate(0, 0, 11), "web"); rec.Saved[0].Key != "bench_press" {
		t.Fatalf("nombre oficial: %+v", rec.Saved)
	}

	for _, bad := range [][2]string{{"bench_press", "bench_press"}, {"bench_press", "no_existe"}, {"no_existe", "bench_press"}} {
		if _, err := svc.moveExercise(ctx, "ana", bad[0], bad[1], nil); err == nil {
			t.Fatalf("acepta %v", bad)
		}
	}
	if code, _, body := e.post("/ejercicio/bench_press/cambiar", "ana", url.Values{"nombre": {"x"}}); code != 200 || !strings.Contains(body, "Elige a qué ejercicio") {
		t.Fatalf("sin destino: %d", code)
	}
	if code, _ := e.get("/ejercicio/bench_press/cambiar", ""); code != 401 {
		t.Fatalf("sin identidad: %d", code)
	}
}
