package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/url"
	"testing"
	"time"
)

func TestTVmazeClient(t *testing.T) {
	c := &clock{t: testNow()}
	f := newFakeTV(t)
	tv := newTestTV(f, c)
	ctx := context.Background()

	list, err := tv.search(ctx, "serie")
	if err != nil || len(list) != 3 {
		t.Fatalf("buscar: %+v %v", list, err)
	}
	if l := list[0]; l.TVmazeID != 1001 || l.Name != "Serie de Ejemplo" || l.Year != "2025" || l.Status != "Running" || l.Network != "Canal de Ejemplo" || !l.HasPoster {
		t.Fatalf("resultado: %+v", l)
	}
	if l := list[1]; l.Network != "Plataforma de Ejemplo" || l.HasPoster || l.Language != "Spanish" {
		t.Fatalf("resultado de plataforma: %+v", l)
	}
	if list, err := tv.search(ctx, "nada de nada"); err != nil || len(list) != 0 {
		t.Fatalf("sin resultados: %+v %v", list, err)
	}

	d, err := tv.show(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Serie de Ejemplo" || d.URL != "https://www.tvmaze.com/shows/1001/serie-de-ejemplo" || d.Runtime != 60 || d.Updated != 1790000000 ||
		d.Summary != "Una serie inventada para las pruebas & nada más. Segundo párrafo." || len(d.Genres) != 2 || d.Premiered != "2025-01-06" || d.Ended != "" {
		t.Fatalf("serie: %+v", d)
	}
	// Sin el especial, en orden.
	if len(d.Episodes) != 7 || d.Episodes[0].ID != 50001 || d.Episodes[3].ID != 50101 {
		t.Fatalf("capítulos: %+v", d.Episodes)
	}
	if _, ok := d.episode(50009); ok {
		t.Fatal("el especial no entra")
	}
	// Con hora: el instante de TVmaze; sin fecha: nada.
	e2, _ := d.find(2, 2)
	if !e2.At.Equal(time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)) || e2.Date != "2026-11-08" {
		t.Fatalf("2x02: %+v", e2)
	}
	if e4, _ := d.find(2, 4); e4.Date != "" || !e4.At.IsZero() || !e4.day().IsZero() {
		t.Fatalf("2x04 sin fecha: %+v", e4)
	}
	d2, err := tv.show(ctx, 1002)
	if err != nil || d2.Runtime != 32 || d2.Image != "" || d2.Network != "Plataforma de Ejemplo" {
		t.Fatalf("serie de plataforma: %+v %v", d2, err)
	}
	// Sin hora (plataformas), solo la fecha: TVmaze pone un instante inventado.
	if e := d2.Episodes[2]; !e.At.IsZero() || e.Date != "2026-11-09" {
		t.Fatalf("sin hora: %+v", e)
	}
	if _, err := tv.show(ctx, 4040); !errors.Is(err, errNoMatch) {
		t.Fatalf("la que no existe: %v", err)
	}

	upd, err := tv.updates(ctx, "week")
	if err != nil || upd[1001] != 1790000000 || upd[999] != 1 {
		t.Fatalf("cambios: %v %v", upd, err)
	}

	// El cartel se vuelve a codificar en JPEG, más pequeño.
	img, err := tv.poster(ctx, f.URL+"/uploads/images/medium_portrait/1/1001.png")
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(img))
	if err != nil || format != "jpeg" || cfg.Height != posterMax || cfg.Width != 300*posterMax/420 {
		t.Fatalf("cartel: %s %+v %v", format, cfg, err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(img)); err != nil {
		t.Fatal(err)
	}
	// Solo de los carteles de TVmaze.
	for _, bad := range []string{"https://evil.example.com/x.png", "http://127.0.0.1:1/x.png", "file:///etc/passwd", f.URL + "@evil.example.com/x.png"} {
		if _, err := tv.poster(ctx, bad); err == nil {
			t.Errorf("acepta el cartel %q", bad)
		}
	}

	// Si se queja del límite, se espera sin insistir.
	f.mu.Lock()
	f.limited = true
	f.mu.Unlock()
	if _, err := tv.show(ctx, 1001); !errors.Is(err, errSourceWait) {
		t.Fatalf("429: %v", err)
	}
	n := f.count("/shows/1001")
	if _, err := tv.show(ctx, 1001); !errors.Is(err, errSourceWait) || f.count("/shows/1001") != n {
		t.Fatal("insiste tras un 429")
	}
	if _, msg, until := tv.status(); msg == "" || !until.After(c.now()) {
		t.Fatalf("estado: %q %v", msg, until)
	}
	f.mu.Lock()
	f.limited = false
	f.mu.Unlock()
	c.set(c.now().Add(10 * time.Minute))
	if _, err := tv.show(ctx, 1001); err != nil {
		t.Fatalf("pasada la espera: %v", err)
	}
	if _, msg, _ := tv.status(); msg != "" {
		t.Fatalf("sigue con error: %q", msg)
	}
}

// TestAllowed comprueba los sitios a los que se puede conectar de verdad.
func TestAllowed(t *testing.T) {
	tv := newTVmaze(defaultURLs, false)
	for raw, want := range map[string]bool{
		"https://api.tvmaze.com/shows/1":                       true,
		"https://static.tvmaze.com/uploads/images/a.jpg":       true,
		"http://api.tvmaze.com/shows/1":                        false,
		"https://api.tvmaze.com.evil.example/x":                false,
		"https://evil.example/api.tvmaze.com":                  false,
		"https://www.tvmaze.com/shows/1":                       false,
		"https://user@api.tvmaze.com/x":                        false,
		"https://api.tvmaze.com:8443/x":                        false,
		"https://API.TVMAZE.COM/shows/1":                       true,
		"https://static.tvmaze.com.evil.example/uploads/a.jpg": false,
	} {
		u, _ := url.Parse(raw)
		if got := tv.allowed(u); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
	if _, err := tv.poster(context.Background(), "https://api.tvmaze.com/x.jpg"); err == nil {
		t.Error("un cartel solo viene de static.tvmaze.com")
	}
	for _, a := range []string{"127.0.0.1:443", "10.0.0.2:443", "192.168.1.5:443", "100.101.102.103:443", "[::1]:443", "169.254.1.1:443"} {
		if dialCheck(a) == nil {
			t.Errorf("deja conectar a %s", a)
		}
	}
	if dialCheck("8.8.8.8:443") != nil {
		t.Error("no deja conectar a una IP pública")
	}
}

func TestConvert(t *testing.T) {
	// Lo raro de TVmaze no se cuela: HTML, enlaces de fuera, fechas malas.
	var sh tvShow
	json.Unmarshal([]byte(`{"id": 7, "name": " <b>X</b> ", "url": "https://evil.example/shows/7", "premiered": "2020-13-45",
		"summary": "<p>a<script>b</script></p>", "_embedded": {"episodes": [
		{"id": 1, "season": 1, "number": 2, "airdate": "2020-01-02", "airtime": "25:00", "airstamp": "2020-01-02T12:00:00+00:00"},
		{"id": 2, "season": 1, "number": 1, "airdate": "2020-01-01", "airtime": "20:30", "airstamp": "2020-01-01T19:30:00+00:00"},
		{"id": 3, "season": 0, "number": 1, "airdate": "2020-01-01"},
		{"id": 2, "season": 1, "number": 1}]}}`), &sh)
	d, err := convert(sh)
	if err != nil {
		t.Fatal(err)
	}
	if d.URL != "" || d.Premiered != "" || d.Summary != "ab" || len(d.Episodes) != 2 || d.Episodes[0].ID != 2 {
		t.Fatalf("convert: %+v", d)
	}
	if !d.Episodes[1].At.IsZero() || d.Episodes[0].At.IsZero() {
		t.Fatalf("horas: %+v", d.Episodes)
	}
	if _, err := convert(tvShow{ID: 3}); err == nil {
		t.Fatal("sin nombre")
	}
}
