package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Con una exportación de verdad, solo en local (no va al repositorio):
// ENTRENO_CHAT=ruta/al/chat.md go test ./apps/entreno -run ChatLocal -v
func TestChatLocal(t *testing.T) {
	path := os.Getenv("ENTRENO_CHAT")
	if path == "" {
		t.Skip("sin ENTRENO_CHAT")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sessions := chatSessions(parseChat(string(b), madrid), newMatcher(nil))
	unknown := map[string]int{}
	byKeyCount := map[string]int{}
	sets := 0
	for _, s := range sessions {
		for n, c := range unknownNames(s.Exercises) {
			unknown[n] += c
		}
		for _, e := range s.Exercises {
			byKeyCount[e.Key]++
			sets += len(e.Sets)
		}
	}
	t.Logf("%d sesiones, %d series, del %s al %s", len(sessions), sets, sessions[0].Day.Format("2006-01-02"), sessions[len(sessions)-1].Day.Format("2006-01-02"))
	t.Logf("sin reconocer: %v", unknown)
	var keys []string
	for k := range byKeyCount {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return byKeyCount[keys[i]] > byKeyCount[keys[j]] })
	t.Logf("ejercicios: %d distintos; los más hechos: %s", len(keys), strings.Join(keys[:min(8, len(keys))], ", "))
	for _, s := range sessions {
		if v := os.Getenv("ENTRENO_DIA"); v != "" && s.Day.Format("2006-01-02") != v {
			continue
		}
		var lines []string
		for _, e := range s.Exercises {
			lines = append(lines, "  "+e.Key+" ["+e.Name+"] "+summarize(e)+map[bool]string{true: " | " + e.Note}[e.Note != ""])
		}
		if os.Getenv("ENTRENO_DIA") != "" || os.Getenv("ENTRENO_TODO") != "" {
			t.Logf("%s %s notas=%q\n%s", s.Day.Format("2006-01-02"), s.Start.Format("15:04"), s.Notes, strings.Join(lines, "\n"))
		}
	}
}

// Rutinas y propuestas con el historial de verdad (solo en local):
// ENTRENO_CHAT=… TEST_DATABASE_URL=… go test ./apps/entreno -run RutinasLocal -v
func TestRutinasLocal(t *testing.T) {
	path := os.Getenv("ENTRENO_CHAT")
	if path == "" {
		t.Skip("sin ENTRENO_CHAT")
	}
	b, _ := os.ReadFile(path)
	svc := newTestService(t)
	ctx := t.Context()
	p, err := svc.previewImport(ctx, "yo", string(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.confirmImport(ctx, "yo", p.ID, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sugg, _ := svc.detectRoutines(ctx, "yo", now)
	days := []string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}
	for _, r := range sugg {
		var ds, items []string
		for _, d := range r.Days {
			ds = append(ds, days[d])
		}
		for _, it := range r.Items {
			items = append(items, fmt.Sprintf("%s %dx%d-%d rir%d %ds", it.Key, it.Sets, it.Min, it.Max, it.RIR, it.RestSec))
		}
		t.Logf("%s (%s): %s", r.Name, strings.Join(ds, ","), strings.Join(items, "; "))
		svc.store.saveRoutine(ctx, "yo", r)
	}
	r, _, _ := svc.todayRoutine(ctx, "yo", now)
	if want := os.Getenv("ENTRENO_RUTINA"); want != "" {
		list, _ := svc.store.routines(ctx, "yo")
		for _, x := range list {
			if strings.Contains(x.Name, want) {
				r = x
			}
		}
	}
	items, _ := svc.plan(ctx, "yo", r, now)
	t.Logf("hoy toca: %s", r.Name)
	for _, it := range items {
		t.Logf("  %s — última: %s\n     hoy: %s — %s%s", it.Name, it.LastText, it.Goal, it.Advice, map[bool]string{true: " [ESTANCADO]"}[it.Stalled])
	}
}
