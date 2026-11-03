package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A media mañana, lo que ya terminó no sale; lo que está en curso y lo que
// queda, sí; y las tareas vencidas, delante.
func TestRemainingFromNow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	at := func(h, m int) *time.Time { v := time.Date(2026, 11, 3, h, m, 0, 0, madrid); return &v }
	svc.now = func() time.Time { return *at(11, 0) }
	for _, ev := range []struct {
		title      string
		start, end *time.Time
	}{{"Desayuno", at(8, 0), at(9, 0)}, {"Reunión", at(10, 30), at(11, 30)}, {"Dentista", at(17, 0), at(18, 0)}} {
		if _, err := svc.create(ctx, "ana", "", "VEVENT", eventInput{Title: ptr(ev.title), Start: ev.start, End: ev.end, Alarms: &[]int{}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.create(ctx, "ana", "", "VTODO", eventInput{Title: ptr("Llamar al banco"), Due: at(9, 30), Alarms: &[]int{}}); err != nil {
		t.Fatal(err)
	}
	day := dayStart(*at(0, 0))
	titles := func(l []entry) string {
		var out []string
		for _, e := range l {
			out = append(out, e.Title)
		}
		return strings.Join(out, ",")
	}
	l, err := svc.remaining(ctx, "ana", day, day.AddDate(0, 0, 1), false)
	if err != nil || titles(l) != "Llamar al banco,Reunión,Dentista" || !l[0].Overdue {
		t.Fatalf("lo que queda: %s %v", titles(l), err)
	}
	if l, _ := svc.remaining(ctx, "ana", day, day.AddDate(0, 0, 1), true); titles(l) != "Desayuno,Llamar al banco,Reunión,Dentista" {
		t.Fatalf("con lo pasado: %s", titles(l))
	}
	// Un día que no incluye ahora (mañana), entero.
	if l, _ := svc.remaining(ctx, "ana", day.AddDate(0, 0, 1), day.AddDate(0, 0, 2), false); len(l) != 0 {
		t.Fatalf("mañana: %s", titles(l))
	}
}
