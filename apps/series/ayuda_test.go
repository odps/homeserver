package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"log/slog"
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

// Datos de ejemplo genéricos (el repositorio es público): tres series
// inventadas con el formato real de TVmaze (testdata/). Nunca se llama a la
// red real.

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

// El lunes 9 de noviembre de 2026 a las 8:30 en Madrid.
func testNow() time.Time { return time.Date(2026, 11, 9, 8, 30, 0, 0, madrid) }

func at(h, m int) time.Time { return time.Date(2026, 11, 9, h, m, 0, 0, madrid) }

func day(s string) time.Time {
	d, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

// fakeTV hace de TVmaze (API y carteles).
type fakeTV struct {
	*httptest.Server
	mu      sync.Mutex
	shows   map[int64]string // id → JSON completo (con _embedded)
	calls   map[string]int
	limited bool // responde 429
}

func loadFixture(t *testing.T, id string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/show_" + id + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFakeTV(t *testing.T) *fakeTV {
	f := &fakeTV{shows: map[int64]string{}, calls: map[string]int{}}
	for id, n := range map[int64]string{1001: "1001", 1002: "1002", 1003: "1003"} {
		f.shows[id] = loadFixture(t, n)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTV) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

// edit cambia el JSON de una serie.
func (f *fakeTV) edit(id int64, fn func(m map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	json.Unmarshal([]byte(f.shows[id]), &m)
	fn(m)
	b, _ := json.Marshal(m)
	f.shows[id] = string(b)
}

// testPNG es un cartel de 300×420 píxeles.
func testPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 300, 420))
	for y := 0; y < 420; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func (f *fakeTV) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	f.calls[p]++
	if f.limited {
		http.Error(w, "too many", http.StatusTooManyRequests)
		return
	}
	// Las URLs de los carteles apuntan a este servidor.
	fix := func(s string) string { return strings.ReplaceAll(s, "https://static.tvmaze.com", f.URL) }
	switch {
	case p == "/search/shows":
		q := strings.ToLower(r.URL.Query().Get("q"))
		out := []map[string]any{}
		for _, id := range []int64{1001, 1002, 1003} {
			var m map[string]any
			json.Unmarshal([]byte(fix(f.shows[id])), &m)
			delete(m, "_embedded")
			if strings.Contains(strings.ToLower(m["name"].(string)), q) {
				out = append(out, map[string]any{"score": 0.9, "show": m})
			}
		}
		json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/shows/"):
		var id int64
		for _, k := range []int64{1001, 1002, 1003} {
			if p == "/shows/"+idStr(k) {
				id = k
			}
		}
		if id == 0 || r.URL.Query().Get("embed") != "episodes" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(fix(f.shows[id])))
	case p == "/updates/shows":
		out := map[string]int64{"999": 1}
		for id, js := range f.shows {
			var m struct {
				Updated int64 `json:"updated"`
			}
			json.Unmarshal([]byte(js), &m)
			out[idStr(id)] = m.Updated
		}
		json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/uploads/images/"):
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG())
	default:
		http.NotFound(w, r)
	}
}

func testURLs(f *fakeTV) tvURLs { return tvURLs{API: f.URL, Images: f.URL} }

func newTestTV(f *fakeTV, c *clock) *tvmaze {
	tv := newTVmaze(testURLs(f), true)
	tv.gap, tv.now = 0, c.now
	return tv
}

// newTestService da un servicio con base de datos de pruebas y TVmaze falso.
func newTestService(t *testing.T) (*service, *fakeTV, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := &clock{t: testNow()}
	f := newFakeTV(t)
	return &service{store: store{pool, box}, tv: newTestTV(f, c), log: slog.New(slog.DiscardHandler), now: c.now}, f, c
}

// noPlain falla si algún texto aparece en claro en la tabla: tal cual (en
// columnas de texto) o dentro de un bytea (que se vuelca en hexadecimal).
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	if dump == "" {
		t.Errorf("%s está vacía: la prueba no comprueba nada", table)
	}
	low := strings.ToLower(dump)
	for _, s := range texts {
		// Lo que solo tiene cifras hexadecimales saldría por azar en los
		// volcados de bytea: de eso solo se busca su forma en hexadecimal.
		plain := strings.Trim(strings.ToLower(s), "0123456789abcdef") != "" && strings.Contains(low, strings.ToLower(s))
		if plain || strings.Contains(low, hex.EncodeToString([]byte(s))) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}
