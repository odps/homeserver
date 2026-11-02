package geo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPlacesQuery(t *testing.T) {
	for what, want := range map[string]string{
		"farmacias cerca":      `["amenity"="pharmacy"]`,
		"una barbería":         `["shop"="hairdresser"]`,
		"tomar algo en un bar": `["amenity"~"^(bar|pub)$"]`,
		"hamburguesería":       `["amenity"="fast_food"]`,
		"centro de salud":      `["amenity"~"^(clinic|doctors)$"]`,
		"Mercadona":            `["name"~"Mercadona",i]`,
		`x"];out;node(1);("`:   `["name"~"x out node 1",i]`,
	} {
		q, _, _, _ := placesQuery(what, 40.4301234, -3.7012345, 800)
		if !strings.Contains(q, want) {
			t.Errorf("%q: falta %s en %s", what, want, q)
		}
		if !strings.Contains(q, "(around:800,40.430,-3.701)") {
			t.Errorf("%q: las coordenadas deben ir redondeadas: %s", what, q)
		}
	}
	if q, _, phrase, _ := placesQuery("una barbería", 40, -3, 800); phrase != "hairdresser" || strings.Contains(q, `"bar|pub"`) {
		t.Error("«barbería» no es un bar")
	}
}

func TestPlacesWeatherAndZone(t *testing.T) {
	var gotQuery, gotMeteo, gotReverse string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("sin User-Agent")
		}
		switch {
		case r.URL.Path == "/interpreter":
			b, _ := io.ReadAll(r.Body)
			v, _ := url.ParseQuery(string(b))
			gotQuery = v.Get("data")
			io.WriteString(w, `{"elements":[
				{"type":"node","lat":40.4310,"lon":-3.7010,"tags":{"name":"Farmacia Lejos","amenity":"pharmacy"}},
				{"type":"way","center":{"lat":40.4302,"lon":-3.7012},"tags":{"name":"Farmacia Cerca","addr:street":"Calle Real","addr:housenumber":"5","opening_hours":"Mo-Sa 09:00-22:00","phone":"+34 910 000 000"}},
				{"type":"node","lat":40.4302,"lon":-3.7012,"tags":{"name":"Farmacia Cerca"}},
				{"type":"node","lat":40.45,"lon":-3.70,"tags":{}}]}`)
		case r.URL.Path == "/reverse":
			gotReverse = r.URL.RawQuery
			io.WriteString(w, `{"address":{"suburb":"Chamberí","city":"Madrid","road":"Calle Secreta"}}`)
		case r.URL.Path == "/v1/search":
			io.WriteString(w, `{"results":[{"name":"Sevilla","admin1":"Andalucía","latitude":37.38,"longitude":-5.98}]}`)
		case r.URL.Path == "/v1/forecast":
			gotMeteo = r.URL.RawQuery
			io.WriteString(w, `{"current":{"temperature_2m":14.2,"weather_code":3},
				"hourly":{"time":["2026-11-03T16:00","2026-11-03T17:00","2026-11-03T18:00","2026-11-03T19:00","2026-11-04T08:00"],
					"precipitation_probability":[20,60,80,10,90]},
				"daily":{"time":["2026-11-03","2026-11-04"],"weather_code":[61,0],"temperature_2m_max":[17.4,20],"temperature_2m_min":[9.1,8],
					"precipitation_probability_max":[80,90],"precipitation_sum":[4.2,1],"wind_speed_10m_max":[45,10]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New()
	c.Overpass, c.Nominatim, c.Meteo, c.Geocoding = srv.URL+"/interpreter", srv.URL, srv.URL, srv.URL
	ctx := context.Background()

	ps, err := c.Places(ctx, "farmacia", 40.4301234, -3.7012345, 1000)
	if err != nil || len(ps) != 2 || ps[0].Name != "Farmacia Cerca" || ps[0].Street != "Calle Real 5" || ps[0].Meters > 30 ||
		ps[1].Name != "Farmacia Lejos" || !strings.Contains(ps[0].Maps, "google.com/maps") {
		t.Fatalf("lugares: %+v %v", ps, err)
	}
	if !strings.Contains(gotQuery, "40.430,-3.701") || strings.Contains(gotQuery, "40.4301234") {
		t.Errorf("a Overpass solo deben ir coordenadas redondeadas: %s", gotQuery)
	}

	if z, err := c.Zone(ctx, 40.4301234, -3.7012345); err != nil || z != "Chamberí, Madrid" {
		t.Fatalf("zona: %q %v", z, err)
	}
	if strings.Contains(gotReverse, "40.4301") {
		t.Errorf("a Nominatim deben ir coordenadas redondeadas: %s", gotReverse)
	}

	lat, lon, label, err := c.City(ctx, "sevilla")
	if err != nil || label != "Sevilla (Andalucía)" || lat != 37.38 || lon != -5.98 {
		t.Fatalf("ciudad: %v %v %q %v", lat, lon, label, err)
	}
	f, err := c.Weather(ctx, 40.4301234, -3.7012345, 2)
	if err != nil || len(f.Days) != 2 || f.Sky != "nublado" || f.Days[0].RainHours != "17:00–19:00" || f.Days[1].RainHours != "08:00–09:00" {
		t.Fatalf("tiempo: %+v %v", f, err)
	}
	if !strings.Contains(gotMeteo, "latitude=40.43&longitude=-3.70") {
		t.Errorf("a Open-Meteo deben ir coordenadas a 1 km: %s", gotMeteo)
	}
	if l := f.Days[0].Line(); l != "lluvia, 9–17 °C; lluvia probable 17:00–19:00 (80 %); viento fuerte (hasta 45 km/h)" {
		t.Errorf("línea: %q", l)
	}
	if l := (Day{Sky: "despejado", Min: 5, Max: 12}).Line(); l != "despejado, 5–12 °C; sin lluvia" {
		t.Errorf("línea sin lluvia: %q", l)
	}
	if d := Distance(40.4168, -3.7038, 41.3874, 2.1686); d < 480000 || d > 520000 {
		t.Errorf("Madrid–Barcelona: %.0f m", d)
	}
}
