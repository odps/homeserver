package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fake simula la red: up indica si responde la puerta de enlace.
type fake struct {
	up      bool
	toggles int
}

func newTestWatcher(t *testing.T, f *fake) *watcher {
	w := &watcher{
		events:   filepath.Join(t.TempDir(), "events.jsonl"),
		failsMax: 4,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.reach = func(context.Context) bool { return f.up }
	w.toggle = func(context.Context) error { f.toggles++; return nil }
	return w
}

func TestTogglesAfterFailsWithBackoff(t *testing.T) {
	f := &fake{}
	w := newTestWatcher(t, f)
	ctx := context.Background()
	start := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) { w.check(ctx, start.Add(d)) }

	// 3 fallos: aún no actúa; al 4.º reinicia la wifi.
	for i := range 3 {
		at(time.Duration(i) * 30 * time.Second)
	}
	if f.toggles != 0 {
		t.Fatalf("reinicios tras 3 fallos = %d, quiero 0", f.toggles)
	}
	at(90 * time.Second)
	if f.toggles != 1 {
		t.Fatalf("reinicios tras 4 fallos = %d, quiero 1", f.toggles)
	}

	// Sigue sin red: espera 5 min antes del segundo y 10 min antes del tercero.
	at(90*time.Second + 4*time.Minute)
	if f.toggles != 1 {
		t.Fatal("reinició antes de los 5 min")
	}
	at(90*time.Second + 5*time.Minute)
	if f.toggles != 2 {
		t.Fatalf("reinicios = %d, quiero 2", f.toggles)
	}
	at(90*time.Second + 14*time.Minute)
	if f.toggles != 2 {
		t.Fatal("reinició antes de los 10 min")
	}
	at(90*time.Second + 15*time.Minute)
	if f.toggles != 3 {
		t.Fatalf("reinicios = %d, quiero 3", f.toggles)
	}

	// Vuelve la red: se reinicia el contador y queda constancia.
	f.up = true
	at(20 * time.Minute)
	if w.fails != 0 || w.backoff != 0 || !w.downAt.IsZero() {
		t.Fatalf("estado tras recuperar: fails=%d backoff=%v downAt=%v", w.fails, w.backoff, w.downAt)
	}
	got := events(t, w.events)
	if len(got) != 4 || got[3] != "recuperada" {
		t.Fatalf("eventos = %v, quiero 3 reinicios y recuperada", got)
	}
}

func TestBackoffCaps(t *testing.T) {
	w := newTestWatcher(t, &fake{})
	w.failsMax = 1
	start := time.Now()
	for i := range 10 {
		w.check(context.Background(), start.Add(time.Duration(i)*time.Hour))
	}
	if w.backoff != maxBackoff {
		t.Fatalf("backoff = %v, quiero %v", w.backoff, maxBackoff)
	}
}

func events(t *testing.T, path string) []string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e struct{ Event string }
		json.Unmarshal(sc.Bytes(), &e)
		out = append(out, e.Event)
	}
	return out
}
