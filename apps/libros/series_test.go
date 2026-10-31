package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeComics imita la API de Asura y las rutas de datos de Flame.
func fakeComics(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	chapters := 3 // capítulos que publica Asura (los tests lo suben)
	mux.HandleFunc("/api/series", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"slug":"killer-pietro","title":"Killer Pietro","cover":"` + base + `/c.png","status":"ongoing","type":"manhwa","chapter_count":3},
			{"slug":"../evil","title":"Mal"}],"meta":{"has_more":true}}`))
	})
	mux.HandleFunc("/api/series/killer-pietro", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"series":{"slug":"killer-pietro","title":"Killer Pietro","description":"<p>Un asesino &amp; más</p>","cover":"` + base + `/c.png",
			"status":"ongoing","type":"manhwa","author":"kim junghyun","artist":"_","public_url":"/comics/killer-pietro-3ebcc5a9","genres":[{"name":"Action"}]}}`))
	})
	mux.HandleFunc("/api/series/killer-pietro/chapters", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for n := chapters; n >= 1; n-- {
			if n < chapters {
				b.WriteString(",")
			}
			early := ""
			if n == chapters {
				early = `,"is_premium":true,"early_access_until":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"`
			}
			b.WriteString(`{"number":` + strconv.Itoa(n) + `,"published_at":"2026-10-0` + strconv.Itoa(n%9+1) + `T10:00:00Z"` + early + `}`)
		}
		b.WriteString(`]}`)
		w.Write([]byte(b.String()))
	})
	build := "v2"
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`<script id="__NEXT_DATA__">{"buildId":"` + build + `"}</script>`))
	})
	mux.HandleFunc("/_next/data/v2/browse.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pageProps":{"series":[{"series_id":2,"title":"Omniscient Reader's Viewpoint","altTitles":["ORV"],"status":"Hiatus","type":"Manhwa","cover":"thumbnail.png"},
			{"series_id":"165","title":"30 Years Have Passed Since the Prologue","cover":"../../x"}]}}`))
	})
	mux.HandleFunc("/_next/data/v2/series/2.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pageProps":{"series":{"series_id":"2","title":"Omniscient Reader's Viewpoint","description":"<p>Lector</p>","type":"Manhwa",
			"tags":["Action"],"author":["Sing-Shong"],"artist":["Sleepy-C"],"status":"Hiatus","cover":"thumbnail.png"},
			"chapters":[{"chapter":"1.50","title":" Extra ","release_date":1779213574,"token":"364db6fd6bef182e"},
			{"chapter":"0.00","title":"Prologue","release_date":1611792000,"token":"0c9db8012fbd1257"},{"chapter":"2","token":"<script>"}]}}`))
	})
	// Ruido: un PNG grande (como las portadas de Flame) que no se comprime.
	big := image.NewRGBA(image.Rect(0, 0, 600, 900))
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range big.Pix {
		big.Pix[i] = byte(rnd.IntN(256))
	}
	big.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var pngData bytes.Buffer
	png.Encode(&pngData, big)
	mux.HandleFunc("/c.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pngData.Bytes()) })
	mux.HandleFunc("/uploads/images/series/2/thumbnail.png", func(w http.ResponseWriter, r *http.Request) { w.Write(pngPixel) })
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv, &chapters
}

func newTestSeries(t *testing.T, h *harness) (*seriesService, *int) {
	t.Helper()
	srv, chapters := fakeComics(t)
	u, _ := url.Parse(srv.URL)
	urls := seriesURLs{AsuraAPI: srv.URL, AsuraSite: "https://asurascans.com", FlameSite: srv.URL, FlameCDN: srv.URL}
	sv, err := newSeriesService(h.api.store.db, t.TempDir(), urls, []string{u.Hostname()}, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h.api.series = sv
	mux := http.NewServeMux()
	h.api.routes(mux)
	h.srv.Config.Handler = mux
	return sv, chapters
}

func TestSeriesTracking(t *testing.T) {
	h := newHarness(t)
	sv, chapters := newTestSeries(t, h)

	// Búsqueda en las dos fuentes, alternando resultados y sin ids raros.
	res := decode[struct {
		Items  []seriesItem
		More   bool
		Failed []string
	}](t, h.do(t, "GET", "/api/series/buscar?source=todas", nil))
	if len(res.Items) != 3 || res.Items[0].Source != "asura" || res.Items[1].Source != "flame" || !res.More || len(res.Failed) != 0 {
		t.Fatalf("búsqueda: %+v", res)
	}
	if res.Items[2].HasCover {
		t.Error("una portada con ruta rara no debería usarse")
	}
	if p := decode[struct{ Items []seriesItem }](t, h.do(t, "GET", "/api/series/buscar?source=flame&q=orv", nil)); len(p.Items) != 1 || p.Items[0].ID != "2" {
		t.Fatalf("buscar en Flame por título alternativo: %+v", p)
	}
	// Portada de un resultado: reducida a JPEG si es grande.
	resp := h.do(t, "GET", "/api/series/portada?source=asura&id=killer-pietro", nil)
	img, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || http.DetectContentType(img) != "image/jpeg" || len(img) > 300<<10 {
		t.Fatalf("portada reducida: %d %s %d bytes", resp.StatusCode, http.DetectContentType(img), len(img))
	}
	if resp := h.do(t, "GET", "/api/series/portada?source=asura&id=otra", nil); resp.StatusCode != 404 {
		t.Fatalf("portada de algo que no se ha buscado: %d", resp.StatusCode)
	}

	// Seguir una serie de Asura.
	type detail struct {
		Series   Series
		Chapters []Chapter
	}
	d := decode[detail](t, h.do(t, "POST", "/api/series", map[string]string{"source": "asura", "id": "killer-pietro"}))
	s := d.Series
	if !s.Followed || s.Title != "Killer Pietro" || s.URL != "https://asurascans.com/comics/killer-pietro-3ebcc5a9" || len(d.Chapters) != 3 ||
		s.Unread != 3 || s.Next == nil || s.Next.Number != 1 || s.Description != "Un asesino & más" || len(s.Authors) != 1 || s.Authors[0] != "Kim Junghyun" {
		t.Fatalf("seguir: %+v", d)
	}
	if d.Chapters[0].Number != 3 || d.Chapters[0].EarlyUntil == nil || d.Chapters[0].URL != s.URL+"/chapter/3" || d.Chapters[1].EarlyUntil != nil {
		t.Fatalf("capítulos: %+v", d.Chapters)
	}
	id := itoa(s.ID)
	r := decode[Series](t, h.do(t, "PUT", "/api/series/"+id+"/leido", map[string]any{"number": 2}))
	if r.Unread != 1 || r.Next.Number != 3 || *r.LastRead != 2 {
		t.Fatalf("leído hasta el 2: %+v", r)
	}

	// Sale un capítulo nuevo: lo detecta la comprobación periódica.
	*chapters = 4
	ctx, cancel := context.WithCancel(context.Background())
	h.api.store.db.Exec(ctx, `UPDATE comic_series SET checked_at = now() - interval '1 hour'`)
	done := make(chan struct{})
	go func() { sv.watch(ctx, 30*time.Minute); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		list := decode[[]Series](t, h.do(t, "GET", "/api/series", nil))
		if len(list) == 1 && list[0].Unread == 2 && *list[0].LatestNumber == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no se detectó el capítulo nuevo: %+v", list)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	// Flame: la web se ha redesplegado (el buildId guardado da 404 y se lee
	// el nuevo), capítulos con decimales y tokens raros fuera.
	fl := sv.sources["flame"].(*flame)
	fl.mu.Lock()
	fl.build, fl.buildAt = "v1", time.Now()
	fl.mu.Unlock()
	d = decode[detail](t, h.do(t, "POST", "/api/series", map[string]string{"source": "flame", "id": "2"}))
	if d.Series.Title != "Omniscient Reader's Viewpoint" || len(d.Chapters) != 2 || d.Chapters[0].Number != 1.5 || d.Chapters[0].Title != "Extra" ||
		d.Chapters[1].URL != d.Series.URL+"/0c9db8012fbd1257" || len(d.Series.Authors) != 2 {
		t.Fatalf("Flame: %+v", d)
	}

	// Cada persona lleva lo suyo.
	h.as("luis", "usuario")
	if list := decode[[]Series](t, h.do(t, "GET", "/api/series", nil)); len(list) != 0 {
		t.Fatalf("luis ve las series de ana: %+v", list)
	}
	if g := decode[Series](t, h.do(t, "GET", "/api/series/"+id, nil)); g.Followed || g.LastRead != nil {
		// La ficha la puede ver, pero sin el progreso de ana.
		t.Fatalf("ficha vista por luis: %+v", g)
	}
	if resp := h.do(t, "PUT", "/api/series/"+id+"/leido", map[string]any{"number": 1}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("apuntar sin seguir: %d", resp.StatusCode)
	}
	for _, in := range []map[string]string{{"source": "asura", "id": "../x"}, {"source": "mangadex", "id": "1"}, {"source": "flame", "id": "abc"}} {
		if resp := h.do(t, "POST", "/api/series", in); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("seguir %v: %d", in, resp.StatusCode)
		}
	}
	h.as("ana", "admin")
	if resp := h.do(t, "DELETE", "/api/series/"+id, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("dejar de seguir: %d", resp.StatusCode)
	}
	if list := decode[[]Series](t, h.do(t, "GET", "/api/series", nil)); len(list) != 1 {
		t.Fatalf("tras dejar de seguir: %+v", list)
	}
}
