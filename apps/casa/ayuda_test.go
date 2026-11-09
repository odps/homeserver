package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Datos de ejemplo genéricos (el repositorio es público). Los precios del
// 9 de noviembre de 2026 son una respuesta real de REE, recortada
// (testdata); los de otros días, inventados. Nunca se llama a la red real.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// El lunes 9 de noviembre de 2026 a mediodía.
func testNow() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, madrid) }

func at(day string, hh, mm int) time.Time {
	d := day0(day)
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, madrid)
}

func day0(s string) time.Time {
	d, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.0005 }

// tomorrowPrices son los precios inventados del 10 de noviembre (€/kWh):
// muy baratos a mediodía y caros por la tarde.
var tomorrowPrices = []float64{0.12, 0.11, 0.10, 0.09, 0.09, 0.10, 0.13, 0.18, 0.22, 0.20, 0.15, 0.08,
	0.06, 0.05, 0.07, 0.12, 0.18, 0.24, 0.30, 0.33, 0.31, 0.26, 0.20, 0.15}

// fixtureReply es la respuesta real de REE del 9 de noviembre de 2026.
func fixtureReply(t *testing.T) reeReply {
	t.Helper()
	b, err := os.ReadFile("testdata/ree_2026-11-09.json")
	if err != nil {
		t.Fatal(err)
	}
	var r reeReply
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// fakeREE hace de apidatos.ree.es.
type fakeREE struct {
	*httptest.Server
	mu        sync.Mutex
	published map[string]bool // días publicados además del 9
	fail      int             // responde este código en vez de los datos
	calls     int
	last      string // última consulta
}

func newFakeREE(t *testing.T) *fakeREE {
	fixture, err := os.ReadFile("testdata/ree_2026-11-09.json")
	if err != nil {
		t.Fatal(err)
	}
	var real map[string]any
	json.Unmarshal(fixture, &real)
	f := &fakeREE{published: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		f.last = r.URL.RawQuery
		if r.URL.Path != reePath || r.URL.Query().Get("time_trunc") != "hour" || r.URL.Query().Get("geo_ids") != reeGeo {
			http.NotFound(w, r)
			return
		}
		if f.fail != 0 {
			w.WriteHeader(f.fail)
			w.Write([]byte(`{"errors":[{"status":"502","title":"Error Interno"}]}`))
			return
		}
		from, err1 := parseDay(strings.TrimSuffix(r.URL.Query().Get("start_date"), "T00:00"))
		to, err2 := parseDay(strings.TrimSuffix(r.URL.Query().Get("end_date"), "T23:59"))
		if err1 != nil || err2 != nil {
			http.Error(w, "fechas", 400)
			return
		}
		var values []map[string]any
		for d := from; !d.After(to); d = d.Add(dayUnit) {
			switch {
			case isoDay(d) == "2026-11-09":
				for _, inc := range real["included"].([]any) {
					m := inc.(map[string]any)
					if m["id"] == reePVPC {
						for _, v := range m["attributes"].(map[string]any)["values"].([]any) {
							values = append(values, v.(map[string]any))
						}
					}
				}
			case f.published[isoDay(d)]:
				for i, p := range tomorrowPrices {
					t := midnight(d).Add(time.Duration(i) * time.Hour)
					values = append(values, map[string]any{"value": p * 1000, "datetime": t.Format("2006-01-02T15:04:05.000-07:00")})
				}
			}
		}
		if len(values) == 0 {
			w.WriteHeader(502)
			w.Write([]byte(`{"errors":[{"status":"502","title":"Error Interno","detail":"Los datos solicitados no están disponibles"}]}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"included": []any{
			map[string]any{"type": "PVPC", "id": reePVPC, "attributes": map[string]any{"values": values}},
			map[string]any{"type": "Precio mercado spot", "id": "600", "attributes": map[string]any{"values": []any{map[string]any{"value": 1, "datetime": "2026-11-09T00:00:00.000+01:00"}}}},
		}})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeREE) set(fn func(*fakeREE)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeREE) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTestService da un servicio con base de datos de pruebas y el REE
// falso.
func newTestService(t *testing.T) (*service, *fakeREE, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := &clock{t: testNow()}
	f := newFakeREE(t)
	r := newREE(f.URL, true)
	r.now = c.now
	return &service{store: store{pool, box}, ree: r, log: slog.New(slog.DiscardHandler), now: c.now}, f, c
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
