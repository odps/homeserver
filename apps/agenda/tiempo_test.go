package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"homeserver/platform/geo"
)

func TestWeatherInSummary(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/search":
			asked = r.URL.Query().Get("name")
			io.WriteString(w, `{"results":[{"name":"Madrid","latitude":40.42,"longitude":-3.70}]}`)
		case "/v1/forecast":
			io.WriteString(w, `{"current":{"temperature_2m":10,"weather_code":3},"hourly":{"time":["2026-11-03T18:00"],"precipitation_probability":[70]},
				"daily":{"time":["2026-11-03"],"weather_code":[61],"temperature_2m_max":[17],"temperature_2m_min":[9],
				"precipitation_probability_max":[70],"precipitation_sum":[3],"wind_speed_10m_max":[12]}}`)
		}
	}))
	defer srv.Close()
	svc := newTestService(t)
	svc.geo = geo.New()
	svc.geo.Meteo, svc.geo.Geocoding = srv.URL, srv.URL
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	day := time.Date(2026, 11, 3, 7, 0, 0, 0, madrid)

	// Sin casa ni ciudad, no hay tiempo.
	if text, _ := svc.summary(ctx, "ana", day, false); strings.Contains(text, "Tiempo") {
		t.Fatalf("sin ciudad: %s", text)
	}
	p, _ := svc.store.prefs(ctx, "ana")
	p.Home = "Calle Mayor 3, 28013 Madrid"
	svc.store.savePrefs(ctx, "ana", p)
	text, _ := svc.summary(ctx, "ana", day, false)
	if !strings.Contains(text, "Tiempo en Madrid: lluvia, 9–17 °C; lluvia probable 18:00–19:00 (70 %).") {
		t.Fatalf("resumen:\n%s", text)
	}
	if asked != "Madrid" {
		t.Fatalf("a Open-Meteo solo debe ir la ciudad, no la dirección: %q", asked)
	}
	if err := svc.changeSettings(ctx, "ana", settingsIn{CiudadTiempo: ptr("Sevilla")}); err != nil {
		t.Fatal(err)
	}
	svc.summary(ctx, "ana", day, false)
	if asked != "Sevilla" {
		t.Fatalf("la ciudad elegida manda: %q", asked)
	}
}
