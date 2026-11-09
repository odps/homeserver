package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// Contra la API de verdad de Red Eléctrica:
// CASA_LIVE=1 go test ./apps/casa -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("CASA_LIVE") == "" {
		t.Skip("sin CASA_LIVE")
	}
	r := newREE(defaultREE, false)
	today := dayOf(time.Now())
	days, err := r.fetch(context.Background(), today, today.Add(dayUnit))
	if err != nil || len(days[today]) < 23 {
		t.Fatalf("hoy: %d horas, %v", len(days[today]), err)
	}
	l := analyze(dayPrices{Day: today, Hours: days[today]}, time.Now())
	t.Logf("hoy: de %s a %s (media %s); 3 h más baratas: de %s; mañana: %d horas", kwh(l.Lo), kwh(l.Hi), kwh(l.Avg), l.Block3.label(), len(days[today.Add(dayUnit)]))
}
