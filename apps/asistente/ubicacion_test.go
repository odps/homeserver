package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeGeo(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/interpreter":
			io.WriteString(w, `{"elements":[{"type":"node","lat":40.4303,"lon":-3.7013,"tags":{"name":"Farmacia Sol","addr:street":"Calle Real","addr:housenumber":"5","opening_hours":"24/7"}}]}`)
		case "/reverse":
			io.WriteString(w, `{"address":{"suburb":"Chamberí","city":"Madrid"}}`)
		case "/v1/search":
			io.WriteString(w, `{"results":[{"name":"Sevilla","latitude":37.38,"longitude":-5.98}]}`)
		case "/v1/forecast":
			io.WriteString(w, `{"current":{"temperature_2m":21,"weather_code":0},"daily":{"time":["2026-11-03"],"weather_code":[0],
				"temperature_2m_max":[24],"temperature_2m_min":[12],"precipitation_probability_max":[0],"precipitation_sum":[0],"wind_speed_10m_max":[8]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLocationAndPlaces(t *testing.T) {
	a, tgf, c := newTestAgent(t)
	ctx := context.Background()
	g := fakeGeo(t)
	a.geo.Overpass, a.geo.Nominatim, a.geo.Meteo, a.geo.Geocoding = g.URL+"/interpreter", g.URL, g.URL, g.URL
	a.refresh(ctx, true)
	tg := a.tg(c)

	// Sin ubicación: la herramienta pide que la comparta.
	if out := a.runTool(ctx, toolMap(a), toolCall{Function: toolFunc("lugares_cerca", `{"que":"farmacia"}`)}); !strings.Contains(out, "comparta su ubicación") {
		t.Fatalf("sin ubicación: %s", out)
	}
	m := msg(42, "")
	m.Location = &tgLocation{Latitude: 40.4301234, Longitude: -3.7012345}
	a.handle(ctx, tg, c, m)
	if !strings.Contains(tgf.last(), "Tengo tu ubicación (Chamberí, Madrid)") || !strings.Contains(tgf.last(), "3 horas") {
		t.Fatalf("al compartirla: %q", tgf.last())
	}
	var dump string
	a.store.db.QueryRow(ctx, `SELECT string_agg(x::text, ' ') FROM locations x`).Scan(&dump)
	if strings.Contains(dump, "40.43") || strings.Contains(dump, "Chamber") {
		t.Fatalf("la ubicación se guarda en claro: %s", dump)
	}

	// «Farmacia cerca»: el modelo ve la zona y los lugares, nunca las coordenadas.
	llmSaw.Reset()
	a.handle(ctx, tg, c, msg(42, "¿hay alguna farmacia cerca?"))
	if !strings.Contains(tgf.last(), "Farmacia Sol") || !strings.Contains(tgf.last(), "google.com/maps") {
		t.Fatalf("farmacia: %q", tgf.last())
	}
	if saw := llmSaw.String(); strings.Contains(saw, "40.430") || strings.Contains(saw, "-3.701") || !strings.Contains(saw, "Chamberí") {
		t.Fatalf("al modelo le han llegado coordenadas o le falta la zona: %s", saw)
	}

	// Tiempo de otra ciudad y ruta desde aquí.
	if out := a.runTool(ctx, toolMap(a), toolCall{Function: toolFunc("tiempo", `{"lugar":"Sevilla","dias":1}`)}); !strings.Contains(out, `"lugar":"Sevilla"`) || !strings.Contains(out, `"cielo":"despejado"`) {
		t.Fatalf("tiempo: %s", out)
	}
	if out := a.runTool(ctx, toolMap(a), toolCall{Function: toolFunc("como_llegar", `{"destino":"Calle Real 5, Madrid"}`)}); !strings.Contains(out, "origin=40.43012") {
		t.Fatalf("ruta: %s", out)
	}

	// Ubicación en tiempo real: las actualizaciones llegan como mensajes editados.
	live := msg(42, "")
	live.Location = &tgLocation{Latitude: 40.5, Longitude: -3.6, LivePeriod: 3600}
	a.handle(ctx, tg, c, live)
	if !strings.Contains(tgf.last(), "mientras la compartas") {
		t.Fatalf("en tiempo real: %q", tgf.last())
	}
	n := len(tgf.sent)
	upd := msg(42, "")
	upd.Location = &tgLocation{Latitude: 40.6, Longitude: -3.5}
	a.gotLocation(ctx, tg, 42, upd, true)
	if l, ok := a.store.location(ctx, 42); !ok || l.Lat != 40.6 || !l.Live || l.Until.Before(time.Now().Add(50*time.Minute)) || len(tgf.sent) != n {
		t.Fatalf("actualización: %+v %v", l, ok)
	}

	a.handle(ctx, tg, c, msg(42, "/ubicacion"))
	if !strings.Contains(tgf.last(), "Chamberí") {
		t.Fatalf("/ubicacion: %q", tgf.last())
	}
	a.handle(ctx, tg, c, msg(42, "/sinubicacion"))
	if _, ok := a.store.location(ctx, 42); ok {
		t.Fatal("/sinubicacion no la borra")
	}
	// Caduca.
	a.store.saveLocation(ctx, 42, location{Lat: 1, Lon: 1, At: time.Now().Add(-4 * time.Hour), Until: time.Now().Add(-time.Hour)})
	if _, ok := a.store.location(ctx, 42); ok {
		t.Fatal("una ubicación caducada sigue valiendo")
	}
}

func toolMap(a *agent) map[string]tool {
	m := map[string]tool{}
	for _, t := range a.builtinTools() {
		m[t.def.Function.Name] = t
	}
	return m
}

func toolFunc(name, args string) (f struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}) {
	f.Name, f.Arguments = name, args
	return f
}
