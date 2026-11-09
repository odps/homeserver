package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// «¿Qué tengo hoy?» a media tarde: lo terminado no sale, lo que está en
// curso y lo que falta sí; el de la mañana sale entero.
func TestSummaryLeft(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	add := func(title string, h1, m1, h2, m2 int) {
		start, end := time.Date(2027, 3, 2, h1, m1, 0, 0, madrid), time.Date(2027, 3, 2, h2, m2, 0, 0, madrid)
		if _, err := svc.create(ctx, "ana", "", "VEVENT", eventInput{Title: ptr(title), Start: &start, End: &end, Alarms: &[]int{}}); err != nil {
			t.Fatal(err)
		}
	}
	add("Dentista", 9, 0, 9, 30)
	add("Reunión", 16, 0, 17, 0)
	add("Cena", 21, 0, 22, 30)
	at := time.Date(2027, 3, 2, 16, 20, 0, 0, madrid)
	text, err := svc.buildSummary(ctx, "ana", at, false, true)
	if err != nil || !strings.HasPrefix(text, "📋 Lo que te queda hoy, martes, 2 de marzo (son las 16:20)") ||
		!strings.Contains(text, "📅 Hoy\n• 16:00–17:00 · Reunión\n• 21:00–22:30 · Cena\n(1 evento ya ha pasado)") || strings.Contains(text, "Dentista") {
		t.Fatalf("lo que queda:\n%s\n%v", text, err)
	}
	if text, _ := svc.buildSummary(ctx, "ana", time.Date(2027, 3, 2, 23, 0, 0, 0, madrid), false, true); !strings.Contains(text, "📅 Hoy\nYa no te queda nada en la agenda.\n(3 eventos ya han pasado)") {
		t.Fatalf("de noche:\n%s", text)
	}
	if text, _ := svc.summary(ctx, "ana", at, false); !strings.HasPrefix(text, "☀️ Buenos días. ") || !strings.Contains(text, "Dentista") || strings.Contains(text, "ya ha pasado") {
		t.Fatalf("resumen de la mañana:\n%s", text)
	}
}
