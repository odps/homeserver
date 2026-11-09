package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestFuelSource(t *testing.T) {
	c := &clock{t: testNow()}
	fm := newFakeMinisterio(t)
	f := newFuelSource(fm.URL+"/PreciosCarburantes", true)
	f.now = c.now
	ctx := context.Background()

	ms, err := f.municipios(ctx)
	if err != nil || len(ms) != 124 || ms[0].ID != 7214 || ms[0].Name != "Valencia" || ms[0].Province != "Valencia / València" {
		t.Fatalf("municipios: %d %+v %v", len(ms), ms[:1], err)
	}

	p, err := f.stations(ctx, 7214)
	if err != nil {
		t.Fatal(err)
	}
	// La de venta restringida no sale; el AdBlue no es un carburante.
	if len(p.Stations) != 4 || !p.Fetched.Equal(c.now()) || !p.Updated.Equal(time.Date(2026, 11, 6, 11, 30, 0, 0, madrid)) {
		t.Fatalf("estaciones: %d %v %v", len(p.Stations), p.Fetched, p.Updated)
	}
	s := p.Stations[1]
	if s.ID != "2" || s.Brand != "BAJOCOSTE EJEMPLO" || s.Address != "Calle Falsa, 123" || s.Town != "Valencia" || s.Hours != "L-D: 24H" ||
		s.Prices["g95"] != 1.399 || s.Prices["goa"] != 1.349 || s.Prices["g98"] != 1.559 || len(s.Prices) != 3 {
		t.Fatalf("estación: %+v", s)
	}
	best, avg := p.cheapest("g95", 2)
	if len(best) != 2 || best[0].Brand != "BAJOCOSTE EJEMPLO" || best[1].Price != 1.459 || !near(avg, (1.459+1.399+1.519+1.489)/4) {
		t.Fatalf("más baratas: %+v %v", best, avg)
	}
	if best, _ := p.cheapest("goa", 5); len(best) != 3 || best[0].Brand != "LA DEL PUEBLO" {
		t.Fatalf("diésel: %+v", best)
	}
	if best, avg := p.cheapest("glp", 5); best != nil || avg != 0 {
		t.Fatal("GLP: ninguna")
	}

	// Si falla, se espera antes de volver a pedir.
	fm.set(func(f *fakeMinisterio) { f.fail = true })
	before := fm.count("/EstacionesTerrestres/FiltroMunicipio/7214")
	if _, err := f.stations(ctx, 7214); !errors.Is(err, errSourceWait) {
		t.Fatalf("caído: %v", err)
	}
	if _, err := f.stations(ctx, 7214); !errors.Is(err, errSourceWait) || fm.count("/EstacionesTerrestres/FiltroMunicipio/7214") != before+1 {
		t.Fatal("tras un fallo se vuelve a pedir enseguida")
	}
	if st := f.state(); st.Err != "error 503" || st.Until.Sub(c.now()) != 5*time.Minute {
		t.Fatalf("estado: %+v", st)
	}
	fm.set(func(f *fakeMinisterio) { f.fail = false })
	c.add(6 * time.Minute)
	if _, err := f.stations(ctx, 7214); err != nil || f.state().Err != "" {
		t.Fatalf("pasada la espera vuelve: %v", err)
	}
	if _, err := f.stations(ctx, 0); err == nil {
		t.Fatal("municipio 0")
	}
}

func TestAllowedHosts(t *testing.T) {
	f := newFuelSource(defaultFuelURL, false)
	for raw, ok := range map[string]bool{
		"https://sedeaplicaciones.minetur.gob.es/ServiciosRESTCarburantes/PreciosCarburantes/Listados/Municipios/": true,
		"http://sedeaplicaciones.minetur.gob.es/x": false, "https://evil.example/x": false, "https://sedeaplicaciones.minetur.gob.es.evil.example/": false,
		"https://otro.minetur.gob.es/": false, "https://127.0.0.1/": false,
	} {
		u, _ := url.Parse(raw)
		if f.allowed(u) != ok {
			t.Errorf("%s: %v", raw, !ok)
		}
	}
	// Ni a otro sitio ni a la red local, aunque se lo pidan.
	bad := newFuelSource("https://localhost:1", false)
	if err := bad.getJSON(context.Background(), "/x", &struct{}{}); err == nil {
		t.Fatal("se conecta a localhost")
	}
	for addr, ok := range map[string]bool{"127.0.0.1:443": false, "192.168.1.10:443": false, "100.101.102.103:443": false, "[::1]:443": false, "104.16.0.1:443": true} {
		if (dialCheck(addr) == nil) != ok {
			t.Errorf("dialCheck(%s)", addr)
		}
	}
}

// Contra el servicio de verdad: COCHE_LIVE=1 go test ./apps/coche -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("COCHE_LIVE") == "" {
		t.Skip("sin COCHE_LIVE")
	}
	f, ctx := newFuelSource(defaultFuelURL, false), context.Background()
	ms, err := f.municipios(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d municipios; buscar «valencia»: %+v", len(ms), searchMunicipios(ms, "valencia", 3))
	p, err := f.stations(ctx, defaultMunicipio.ID)
	if err != nil {
		t.Fatal(err)
	}
	best, avg := p.cheapest("g95", 3)
	t.Logf("%d estaciones, precios de %v; gasolina 95 media %.3f; la más barata %+v", len(p.Stations), p.Updated, avg, best)
	if len(p.Stations) == 0 || len(best) == 0 || p.Updated.IsZero() {
		t.Fatal("respuesta vacía")
	}
}
