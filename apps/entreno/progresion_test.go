package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func ir(n int) *int { return &n }

func pastOf(sets ...[]setLog) []done {
	var out []done
	day := time.Date(2026, 10, 27, 0, 0, 0, 0, madrid)
	for i, s := range sets {
		out = append(out, done{Day: day.AddDate(0, 0, -7*i), Entry: exerciseLog{Key: "bench_press", Sets: s}})
	}
	return out
}

func rep(n int, reps int, kg float64, rir *int) []setLog {
	var out []setLog
	for i := 0; i < n; i++ {
		out = append(out, setLog{Reps: reps, Kg: kg, RIR: rir})
	}
	return out
}

func TestProgress(t *testing.T) {
	bench, _ := lookup("bench_press")
	it := routineItem{Key: "bench_press", Sets: 4, Min: 6, Max: 8, RIR: 2}

	// Arriba del rango con RIR objetivo: sube carga.
	tg, adv, st := progress(it, bench, pastOf(rep(4, 8, 20, ir(2))))
	if st || tg.Kg != 22.5 || tg.Reps != 6 || !strings.Contains(adv, "sube a 22.5 kg") {
		t.Fatalf("subir: %+v %q %v", tg, adv, st)
	}
	// En el rango: misma carga, una más.
	if tg, _, _ := progress(it, bench, pastOf(rep(4, 6, 20, ir(2)))); tg.Kg != 20 || tg.Reps != 7 {
		t.Fatalf("una más: %+v", tg)
	}
	// Más duro de la cuenta (RIR 0): mantiene.
	if tg, adv, _ := progress(it, bench, pastOf(rep(4, 6, 20, ir(0)))); tg.Kg != 20 || tg.Reps != 6 || !strings.Contains(adv, "más duro") {
		t.Fatalf("mantener: %+v %q", tg, adv)
	}
	// Estancado: tres veces igual (como la banca del usuario, 4x6 20 kg a RIR 0).
	tg, adv, st = progress(it, bench, pastOf(rep(4, 6, 20, ir(0)), rep(4, 6, 20, ir(0)), rep(4, 6, 20, ir(0)), rep(4, 6, 20, ir(0))))
	if !st || tg.Kg != 17.5 || !strings.Contains(adv, "4 sesiones sin progresar") {
		t.Fatalf("estancado: %+v %q %v", tg, adv, st)
	}
	// Si la anterior era peor, sí progresa.
	if _, _, st := progress(it, bench, pastOf(rep(4, 6, 20, ir(0)), rep(4, 6, 20, ir(0)), rep(4, 6, 17.5, ir(2)))); st {
		t.Fatal("no está estancado: venía de menos")
	}
	// Por tiempo: +5 s.
	hold, _ := lookup("hollow_hold")
	ih := routineItem{Key: "hollow_hold", Sets: 3, Min: 30, Max: 45, RIR: 2}
	if tg, _, _ := progress(ih, hold, []done{{Entry: exerciseLog{Sets: []setLog{{Seconds: 30}, {Seconds: 30}}}}}); tg.Seconds != 35 {
		t.Fatalf("tiempo: %+v", tg)
	}
	// Primera vez.
	if tg, adv, _ := progress(it, bench, nil); tg.Reps != 6 || !strings.Contains(adv, "Primera vez") {
		t.Fatalf("primera vez: %+v %q", tg, adv)
	}
	// Las series de calentamiento (menos carga) no cuentan.
	warm := append([]setLog{{Reps: 10, Kg: 10}}, rep(4, 8, 20, ir(3))...)
	if tg, _, _ := progress(it, bench, pastOf(warm)); tg.Kg != 22.5 {
		t.Fatalf("con calentamiento: %+v", tg)
	}
}

func TestRoutinesDetectPlanAndLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	monday := time.Date(2026, 10, 5, 20, 0, 0, 0, madrid) // lunes
	for w := 0; w < 4; w++ {
		d := monday.AddDate(0, 0, 7*w)
		svc.record(ctx, "ana", "Back squat 4x5 70kg rir 2\nRomanian 3x6 60kg rir 2\nSeated calf 3x20 50kg rir 2", d, "web")
		svc.record(ctx, "ana", "Bench press 4x6 20kg rir 0\nLateral raise 3x8 10kg rir 2\nDead bug 3x10", d.AddDate(0, 0, 1), "web")
	}
	sugg, err := svc.detectRoutines(ctx, "ana", monday.AddDate(0, 0, 30))
	if err != nil || len(sugg) != 2 {
		t.Fatalf("detectar: %+v %v", sugg, err)
	}
	leg, push := sugg[0], sugg[1]
	if !strings.HasPrefix(leg.Name, "Pierna") || len(leg.Days) != 1 || leg.Days[0] != 1 || len(leg.Items) != 3 || leg.Items[0].Key != "back_squat" ||
		leg.Items[0].Sets != 4 || leg.Items[0].Min != 5 || leg.Items[0].Max != 7 || leg.Items[0].RIR != 2 {
		t.Fatalf("pierna: %+v", leg)
	}
	if push.Name != "Empuje" || push.Days[0] != 2 {
		t.Fatalf("empuje: %+v", push)
	}
	for _, r := range sugg {
		if _, err := svc.store.saveRoutine(ctx, "ana", r); err != nil {
			t.Fatal(err)
		}
	}
	// El martes toca empuje, con la banca estancada.
	tuesday := monday.AddDate(0, 0, 29)
	r, ok, err := svc.todayRoutine(ctx, "ana", tuesday)
	if err != nil || !ok || r.Name != "Empuje" {
		t.Fatalf("hoy: %+v %v %v", r, ok, err)
	}
	items, err := svc.plan(ctx, "ana", r, tuesday)
	if err != nil || len(items) != 3 || !items[0].Stalled || items[0].Sets != 3 || !strings.Contains(items[0].LastText, "4×6 · 20 kg · RIR 0") {
		t.Fatalf("plan: %+v %v", items, err)
	}
	// Serie a serie, sin duplicar al reenviar.
	st := setLog{Reps: 6, Kg: 17.5, RIR: ir(3), ID: "s1"}
	sid, err := svc.logSet(ctx, "ana", "bench_press", st, tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if sid2, _ := svc.logSet(ctx, "ana", "bench_press", st, tuesday); sid2 != sid {
		t.Fatal("otra sesión al reenviar")
	}
	svc.logSet(ctx, "ana", "bench_press", setLog{Reps: 6, Kg: 17.5, ID: "s2"}, tuesday)
	items, _ = svc.plan(ctx, "ana", r, tuesday)
	if len(items[0].DoneToday) != 2 {
		t.Fatalf("hechas hoy: %+v", items[0].DoneToday)
	}
	if _, err := svc.logSet(ctx, "ana", "no_existe", st, tuesday); err == nil {
		t.Fatal("acepta un ejercicio que no existe")
	}
	// Nadie ve las rutinas de otro; nada en claro.
	if l, _ := svc.store.routines(ctx, "luis"); len(l) != 0 {
		t.Fatal("otro ve las rutinas")
	}
	if err := svc.store.deleteRoutine(ctx, "luis", r.ID); err == nil {
		t.Fatal("otro borra la rutina")
	}
	noPlain(t, svc.store.db, "routines", "Empuje", "bench_press", "Pierna")
	if _, err := svc.store.saveRoutine(ctx, "ana", routine{Name: "Mala", Items: []routineItem{{Key: "u999"}}}); err == nil {
		t.Fatal("acepta un ejercicio propio que no existe")
	}
}
