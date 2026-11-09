package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRoutinesSlotsAndSettings(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	day := func(s string) time.Time { d, _ := time.ParseInLocation("2006-01-02", s, madrid); return d }
	until := day("2027-06-30")
	// Empieza un sábado: la primera vez es el lunes siguiente.
	work, err := svc.createRoutine(ctx, "ana", routineInput{Title: "Trabajo", Days: []string{"laborables"}, Start: "09:00", End: "17:00",
		From: day("2027-02-27"), Until: &until, Location: "Oficina"})
	if err != nil || !work.Routine || work.Start.Format("2006-01-02 15:04") != "2027-03-01 09:00" {
		t.Fatalf("rutina: %+v %v", work, err)
	}
	if _, err := svc.createRoutine(ctx, "ana", routineInput{Title: "Club", Days: []string{"martes", "jueves"}, Start: "19:00", End: "21:00", From: day("2027-03-01")}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.createRoutine(ctx, "ana", routineInput{Title: "X", Days: []string{"lunex"}, Start: "09:00", End: "10:00"}); err == nil {
		t.Fatal("acepta un día que no existe")
	}
	if l, _ := svc.routines(ctx, "ana"); len(l) != 2 {
		t.Fatalf("rutinas: %+v", l)
	}
	start, end := time.Date(2027, 3, 2, 18, 0, 0, 0, madrid), time.Date(2027, 3, 2, 18, 30, 0, 0, madrid)
	if _, err := svc.create(ctx, "ana", "", "VEVENT", eventInput{Title: ptr("Médico"), Start: &start, End: &end, Alarms: &[]int{}}); err != nil {
		t.Fatal(err)
	}
	slots, err := svc.freeSlots(ctx, "ana", day("2027-03-02"), day("2027-03-02"), "08:00", "22:00", 30*time.Minute, true)
	var got []string
	for _, s := range slots {
		got = append(got, s.Start+"-"+s.End)
	}
	if err != nil || strings.Join(got, " ") != "08:00-09:00 17:00-18:00 18:30-19:00 21:00-22:00" {
		t.Fatalf("huecos: %v %v", got, err)
	}
	if slots, _ := svc.freeSlots(ctx, "ana", day("2027-03-02"), day("2027-03-02"), "08:00", "22:00", 30*time.Minute, false); len(slots) != 2 {
		t.Fatalf("huecos sin rutinas: %+v", slots)
	}

	// Un día suelto sin trabajo.
	if _, err := svc.skip(ctx, "ana", work.ID, day("2027-03-03")); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.agenda(ctx, "ana", day("2027-03-03"), day("2027-03-04")); len(l) != 0 {
		t.Fatalf("el miércoles saltado: %+v", l)
	}
	if _, err := svc.skip(ctx, "ana", work.ID, day("2027-03-06")); err == nil {
		t.Fatal("salta un sábado en que no toca")
	}
	text, _ := svc.summary(ctx, "ana", time.Date(2027, 3, 2, 7, 0, 0, 0, madrid), false)
	if !strings.Contains(text, "📅 Hoy\n• 09:00–17:00 · Trabajo 🔁\n• 18:00–18:30 · Médico\n• 19:00–21:00 · Club 🔁") {
		t.Fatalf("resumen:\n%s", text)
	}

	// Ajustes desde el asistente: solo cambia lo que se pasa.
	if err := svc.changeSettings(ctx, "ana", settingsIn{ResumenHora: ptr("25:00")}); err == nil {
		t.Fatal("acepta una hora imposible")
	}
	if err := svc.changeSettings(ctx, "ana", settingsIn{ResumenHora: ptr("08:15"), Importantes: &[]string{"Banco.es", "banco.es"}}); err != nil {
		t.Fatal(err)
	}
	v, err := svc.settingsView(ctx, "ana")
	if err != nil || v.ResumenHora != "08:15" || !v.ResumenManana || len(v.Importantes) != 1 || v.Importantes[0] != "banco.es" ||
		!strings.Contains(strings.Join(v.Calendarios, ","), "Rutinas") {
		t.Fatalf("ajustes: %+v %v", v, err)
	}
}
