package main

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Datos de ejemplo genéricos (el repositorio es público): conceptos, tiendas
// y suscripciones inventadas o de marcas conocidas, nunca de nadie.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// El lunes 9 de noviembre de 2026 a mediodía.
func testNow() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, madrid) }

func day(s string) time.Time {
	d, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// newTestService da un servicio con la base de datos de pruebas.
func newTestService(t *testing.T) (*service, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := &clock{t: testNow()}
	return &service{store: store{pool, box}, log: slog.New(slog.DiscardHandler), now: c.now}, c
}

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	if dump == "" {
		t.Errorf("%s está vacía: la prueba no comprueba nada", table)
	}
	for _, s := range texts {
		if strings.Contains(strings.ToLower(dump), strings.ToLower(s)) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}
