package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Datos de ejemplo genéricos (el repositorio es público): hábitos corrientes
// y notas inventadas.

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

// El jueves 12 de noviembre de 2026 a mediodía (hora de Madrid).
func testNow() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, madrid) }

// at es una hora de hoy en Madrid.
func at(hh, mm int) time.Time {
	n := testNow()
	return time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, madrid)
}

func day(s string) time.Time {
	d, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

// ago es el día de hace n días (respecto a testNow).
func ago(n int) time.Time { return dayOf(testNow()).Add(-time.Duration(n) * dayUnit) }

// newTestService da un servicio con base de datos de pruebas y reloj fijo.
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

// mk es un hábito de ejemplo creado hace created días.
func mk(id int64, name, kind string, target float64, f freq, created int) habit {
	return habit{ID: id, Created: ago(created).Add(10 * time.Hour), habitData: habitData{Name: name, Kind: kind, Target: target, Freq: f}}
}

// marked son marcas de los días de hace n días (cantidad 1).
func marked(days ...int) marks {
	m := marks{}
	for _, n := range days {
		m[isoDay(ago(n))] = 1
	}
	return m
}
