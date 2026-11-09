package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Datos de ejemplo genéricos (el repositorio es público): vehículos,
// gasolineras y direcciones inventados. Nunca se llama a la red real.

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

// El viernes 6 de noviembre de 2026 a mediodía.
func testNow() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, madrid) }

func day(s string) time.Time {
	d, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

// fakeMinisterio hace del servicio REST del Ministerio con su formato real
// (comprobado contra el de verdad): claves con tildes y espacios, precios
// con coma decimal y la fecha como «06/11/2026 11:30:00».
type fakeMinisterio struct {
	*httptest.Server
	mu    sync.Mutex
	calls map[string]int
	fail  bool
	shift float64 // se suma a los precios (para ver que se actualizan)
}

func fakeStation(id, rotulo, dir, loc, venta string, prices map[string]string) map[string]string {
	e := map[string]string{"C.P.": "46000", "Dirección": dir, "Horario": "L-D: 24H", "Latitud": "39,000000", "Localidad": loc,
		"Longitud (WGS84)": "-0,300000", "Margen": "D", "Municipio": "Valencia", "Provincia": "VALENCIA / VALÈNCIA", "Remisión": "OM",
		"Rótulo": rotulo, "Tipo Venta": venta, "% BioEtanol": "0,0", "% Éster metílico": "0,0", "IDEESS": id, "IDMunicipio": "7214",
		"IDProvincia": "46", "IDCCAA": "10"}
	for _, p := range products {
		e[p.Field] = ""
	}
	for k, v := range prices {
		e[k] = v
	}
	return e
}

func newFakeMinisterio(t *testing.T) *fakeMinisterio {
	f := &fakeMinisterio{calls: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMinisterio) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeMinisterio) set(fn func(*fakeMinisterio)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeMinisterio) price(base float64) string {
	return strings.Replace(fmt.Sprintf("%.3f", base+f.shift), ".", ",", 1)
}

func (f *fakeMinisterio) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/PreciosCarburantes")
	f.calls[path]++
	if f.fail || r.Header.Get("User-Agent") == "" {
		http.Error(w, "no", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch {
	case path == "/Listados/Municipios/":
		list := []map[string]string{
			{"IDMunicipio": "7214", "IDProvincia": "46", "IDCCAA": "10", "Municipio": "Valencia", "Provincia": "VALENCIA / VALÈNCIA", "CCAA": "Comunidad Valenciana"},
			{"IDMunicipio": "7215", "IDProvincia": "46", "IDCCAA": "10", "Municipio": "Vallada", "Provincia": "VALENCIA / VALÈNCIA", "CCAA": "Comunidad Valenciana"},
			{"IDMunicipio": "4354", "IDProvincia": "28", "IDCCAA": "13", "Municipio": "Madrid", "Provincia": "MADRID", "CCAA": "Madrid"},
			{"IDMunicipio": "900", "IDProvincia": "24", "IDCCAA": "07", "Municipio": "Valencia de Ejemplo", "Provincia": "PROVINCIA DE EJEMPLO", "CCAA": "Ejemplo"},
		}
		for i := 1; i <= 120; i++ {
			list = append(list, map[string]string{"IDMunicipio": fmt.Sprint(10000 + i), "IDProvincia": "99", "Municipio": fmt.Sprintf("Pueblo %d", i), "Provincia": "PROVINCIA DE EJEMPLO"})
		}
		json.NewEncoder(w).Encode(list)
	case path == "/EstacionesTerrestres/FiltroMunicipio/7214":
		json.NewEncoder(w).Encode(map[string]any{"Fecha": "06/11/2026 11:30:00", "Nota": "Archivo de ejemplo", "ResultadoConsulta": "OK",
			"ListaEESSPrecio": []map[string]string{
				fakeStation("1", "GASOLINERA EJEMPLO", "AVENIDA DE EJEMPLO, 1", "VALENCIA", "P",
					map[string]string{"Precio Gasolina 95 E5": f.price(1.459), "Precio Gasoleo A": f.price(1.389)}),
				fakeStation("2", "BAJOCOSTE EJEMPLO", "CALLE FALSA, 123", "VALENCIA", "P",
					map[string]string{"Precio Gasolina 95 E5": f.price(1.399), "Precio Gasoleo A": f.price(1.349), "Precio Gasolina 98 E5": f.price(1.559)}),
				fakeStation("3", "COOPERATIVA EJEMPLO", "CAMINO DEL MEDIO, S/N", "VALENCIA", "R",
					map[string]string{"Precio Gasolina 95 E5": f.price(1.199)}),
				fakeStation("4", "ESTACION SOLO GASOLINA", "PLAZA MAYOR DE EJEMPLO, 4", "VALENCIA", "P",
					map[string]string{"Precio Gasolina 95 E5": f.price(1.519)}),
				fakeStation("5", "LA DEL PUEBLO", "CARRETERA DE EJEMPLO KM 2", "PUEBLO DE EJEMPLO", "P",
					map[string]string{"Precio Gasolina 95 E5": f.price(1.489), "Precio Gasoleo A": f.price(1.299), "Precio Adblue": "0,899"}),
			}})
	case strings.HasPrefix(path, "/EstacionesTerrestres/FiltroMunicipio/"):
		json.NewEncoder(w).Encode(map[string]any{"Fecha": "06/11/2026 11:30:00", "ResultadoConsulta": "OK",
			"ListaEESSPrecio": []map[string]string{
				fakeStation("9", "OTRA EJEMPLO", "CALLE DE OTRO SITIO, 9", "OTRO SITIO", "P", map[string]string{"Precio Gasolina 95 E5": f.price(1.549)}),
			}})
	default:
		http.NotFound(w, r)
	}
}

func newTestService(t *testing.T) (*service, *fakeMinisterio, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := &clock{t: testNow()}
	fm := newFakeMinisterio(t)
	f := newFuelSource(fm.URL+"/PreciosCarburantes", true)
	f.now = c.now
	return &service{store: store{pool, box}, fuel: f, log: slog.New(slog.DiscardHandler), now: c.now}, fm, c
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

func near(a, b float64) bool { return a-b < 0.005 && b-a < 0.005 }
