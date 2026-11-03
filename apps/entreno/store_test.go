package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

func newTestService(t *testing.T) *service {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	return &service{store: store{pool, box}, log: slog.New(slog.DiscardHandler), imports: newImports()}
}

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	for _, s := range texts {
		if strings.Contains(dump, s) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}

func TestRecordHistoryAndPrivacy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 3, 20, 0, 0, 0, madrid)
	res, err := svc.record(ctx, "ana", "Lat machine 3x10 50kg rir 2\nFace pull 3x15 15kg\nRir 2\nMolestia hombro derecho", day, "web")
	if err != nil || len(res.Saved) != 2 || len(res.Unknown) != 0 || res.SessionID == 0 {
		t.Fatalf("apuntar: %+v %v", res, err)
	}
	// Lo que se dice después el mismo día va a la misma sesión.
	res2, err := svc.record(ctx, "ana", "Pressa 3x12 70kg\nMaquinón raro 3x10 20kg", day.Add(30*time.Minute), "asistente")
	if err != nil || res2.SessionID != res.SessionID || len(res2.Saved) != 1 || len(res2.Unknown) != 1 || res2.Unknown[0] != "Maquinón raro" {
		t.Fatalf("segunda vez: %+v %v", res2, err)
	}
	s, err := svc.store.session(ctx, "ana", res.SessionID)
	if err != nil || len(s.Exercises) != 3 || s.Exercises[2].Key != "leg_press" || strings.Join(s.Notes, "|") != "Molestia hombro derecho" {
		t.Fatalf("sesión: %+v %v", s, err)
	}
	if got := summarize(s.Exercises[1]); got != "3×15 · 15 kg · RIR 2" {
		t.Fatalf("face pull: %s", got)
	}
	// Un alias y un ejercicio propio.
	key, err := svc.store.addCustom(ctx, "ana", "Maquinón raro", "reps")
	if err != nil {
		t.Fatal(err)
	}
	svc.store.setAlias(ctx, "ana", "Maquinón raro", key)
	if res, err := svc.record(ctx, "ana", "Maquinón raro 3x10 25kg", day.AddDate(0, 0, 7), "web"); err != nil || len(res.Saved) != 1 || res.Saved[0].Key != key {
		t.Fatalf("propio: %+v %v", res, err)
	}
	svc.record(ctx, "ana", "Lat machine 3x10 55kg rir 1", day.AddDate(0, 0, 7), "web")
	h, err := svc.store.history(ctx, "ana", "lat_pulldown", 0)
	if err != nil || len(h) != 2 || h[0].Entry.Sets[0].Kg != 55 {
		t.Fatalf("historial: %+v %v", h, err)
	}
	if _, v := best(h[0].Entry); v != 75.2 { // 55 × (1 + 11/30)
		t.Fatalf("1RM estimado: %v", v)
	}
	stats, _ := svc.store.exerciseStats(ctx, "ana")
	if len(stats) != 4 || stats[0].Times < 1 {
		t.Fatalf("estadísticas: %+v", stats)
	}
	// Nada en claro, y nadie ve lo de otro.
	noPlain(t, svc.store.db, "sessions", "Molestia", "hombro")
	noPlain(t, svc.store.db, "entries", "Lat machine", "lat_pulldown", "Face", "Pressa", "kg")
	noPlain(t, svc.store.db, "aliases", "Maquin", "leg_press")
	noPlain(t, svc.store.db, "custom_exercises", "Maquin")
	if _, err := svc.store.session(ctx, "luis", res.SessionID); err == nil {
		t.Fatal("otro ve la sesión")
	}
	if err := svc.store.deleteSession(ctx, "luis", res.SessionID); err == nil {
		t.Fatal("otro borra la sesión")
	}
	if h, _ := svc.store.history(ctx, "luis", "lat_pulldown", 0); len(h) != 0 {
		t.Fatal("otro ve el historial")
	}
	if l, _ := svc.store.sessions(ctx, "luis", time.Time{}, day.AddDate(1, 0, 0), 0); len(l) != 0 {
		t.Fatal("otro ve las sesiones")
	}
}

func TestImportFlow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	chat := sampleChat + "\n## 8 May 2026\n\n[19:00] **You:** Maquinón raro 3x10 20kg\nBench press 3x8 16kg rir 2\n"
	p, err := svc.previewImport(ctx, "ana", chat)
	if err != nil || len(p.Sessions) != 4 || p.Unknown["Maquinón raro"] != 1 {
		t.Fatalf("vista previa: %+v %v", p, err)
	}
	if _, ok := svc.imports.get("luis", p.ID); ok {
		t.Fatal("otro ve la vista previa")
	}
	saved, skipped, err := svc.confirmImport(ctx, "ana", p.ID, map[string]string{"Maquinón raro": "nuevo"})
	if err != nil || saved != 4 || skipped != 0 {
		t.Fatalf("importar: %d %d %v", saved, skipped, err)
	}
	// Otra vez: los días ya importados se saltan.
	p, _ = svc.previewImport(ctx, "ana", chat)
	if p.Already != 4 || len(p.Unknown) != 0 { // el nombre ya es un ejercicio propio
		t.Fatalf("segunda vista previa: %+v", p)
	}
	if saved, skipped, _ := svc.confirmImport(ctx, "ana", p.ID, nil); saved != 0 || skipped != 4 {
		t.Fatalf("segunda importación: %d %d", saved, skipped)
	}
	l, _ := svc.store.sessions(ctx, "ana", time.Time{}, time.Now(), 0)
	if len(l) != 4 || l[0].Source != "whatsapp" || l[0].Day.Format("2006-01-02") != "2026-05-08" {
		t.Fatalf("sesiones: %+v", l)
	}
	if _, err := svc.previewImport(ctx, "ana", "hola"); err == nil {
		t.Fatal("acepta un fichero sin entrenos")
	}
}
