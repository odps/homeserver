package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"homeserver/platform/dbtest"
)

func rss(title string, items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>` + title + `</title>` + strings.Join(items, "") + `</channel></rss>`
}

func rssEntry(title, link, desc string, ago time.Duration) string {
	return fmt.Sprintf(`<item><title><![CDATA[%s]]></title><link>%s</link><description><![CDATA[<p>%s</p>]]></description><pubDate>%s</pubDate></item>`,
		title, link, desc, time.Now().Add(-ago).Format(time.RFC1123Z))
}

func TestParseFeeds(t *testing.T) {
	now := time.Now()
	f, err := parseFeed([]byte(rss("Medio", rssEntry("Titular &amp; más", "https://a.es/1", "Entradilla <b>con</b> etiquetas", time.Hour),
		`<item><title>Sin enlace</title></item>`)), now)
	if err != nil || len(f.Items) != 1 || f.Items[0].Title != "Titular & más" || f.Items[0].Summary != "Entradilla con etiquetas" || f.Title != "Medio" {
		t.Fatalf("RSS: %v %+v", err, f)
	}
	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title><entry><title>Entrada</title><link rel="alternate" href="https://b.es/e"/>
		<summary>Resumen</summary><updated>2026-10-31T10:00:00Z</updated></entry></feed>`
	if f, err := parseFeed([]byte(atom), now); err != nil || len(f.Items) != 1 || f.Items[0].Link != "https://b.es/e" || f.Items[0].Published.Year() != 2026 {
		t.Fatalf("Atom: %v %+v", err, f)
	}
	// Google News: el medio va en <source> y al final del titular.
	gn := rss("Google News", `<item><title>Sale Go 1.27 - Blog Go</title><link>https://news.google.com/x</link><source url="https://go.dev">Blog Go</source>
		<description>&lt;a href="https://news.google.com/x"&gt;Sale Go 1.27&lt;/a&gt;&amp;nbsp;&amp;nbsp;&lt;font&gt;Blog Go&lt;/font&gt;</description></item>`)
	if f, _ := parseFeed([]byte(gn), now); len(f.Items) != 1 || f.Items[0].Title != "Sale Go 1.27" || f.Items[0].Source != "Blog Go" || f.Items[0].Summary != "" {
		t.Fatalf("Google News: %+v", f)
	}
	if _, err := parseFeed([]byte("<html><body>hola</body></html>"), now); err == nil {
		t.Fatal("una página HTML pasa por feed")
	}
	if _, err := validFeedURL("http://x.es/rss", false); err == nil {
		t.Fatal("acepta un feed por HTTP")
	}
	if _, err := newFetcher(false).fetch(context.Background(), "https://127.0.0.1:1/rss"); err == nil || !strings.Contains(err.Error(), "no permitida") {
		t.Fatalf("el cliente entra en el propio servidor: %v", err)
	}
}

func TestGroup(t *testing.T) {
	now := time.Now()
	items := []item{
		{Source: "A", Title: "Sánchez anuncia un nuevo plan de vivienda para jóvenes", Published: now},
		{Source: "B", Title: "El Gobierno: Sánchez anuncia plan de vivienda para los jóvenes", Published: now.Add(-time.Hour)},
		{Source: "C", Title: "Sánchez anuncia el plan de vivienda joven", Published: now.Add(-2 * time.Hour)},
		{Source: "A", Title: "Lluvias torrenciales en Valencia", Published: now.Add(-30 * time.Minute)},
		{Source: "B", Title: "El Madrid gana en el último minuto", Published: now.Add(-10 * time.Minute)},
	}
	cs := group(items)
	rankClusters(cs)
	if len(cs) != 3 || len(cs[0].sources) != 3 || !strings.Contains(cs[0].lead().Title, "vivienda") {
		t.Fatalf("grupos: %d, primero con %d medios: %q", len(cs), len(cs[0].sources), cs[0].lead().Title)
	}
	if cs[1].lead().Source != "B" { // a igualdad de medios, lo más reciente
		t.Fatalf("orden: %q", cs[1].lead().Title)
	}
}

type harness struct {
	svc  *service
	srv  *httptest.Server
	news *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/a.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Medio A", rssEntry("Sánchez anuncia un nuevo plan de vivienda para jóvenes", "https://a.es/vivienda", "El plan incluye ayudas.", time.Hour),
			rssEntry("Lluvias torrenciales en Valencia", "https://a.es/lluvias", "Alerta roja.", 2*time.Hour)))
	})
	mux.HandleFunc("/b.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Medio B", rssEntry("El Gobierno: Sánchez anuncia plan de vivienda para los jóvenes", "https://b.es/vivienda", "", 90*time.Minute)))
	})
	mux.HandleFunc("/c.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<feed xmlns="http://www.w3.org/2005/Atom"><title>Medio C</title><entry><title>Sánchez anuncia el plan de vivienda joven</title>
			<link href="https://c.es/vivienda"/><updated>`+time.Now().Add(-3*time.Hour).Format(time.RFC3339)+`</updated></entry></feed>`)
	})
	mux.HandleFunc("/gnews", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Query().Get("q"), "golang") {
			io.WriteString(w, rss("Google News"))
			return
		}
		io.WriteString(w, rss("Google News", `<item><title>Sale Go 1.27 con mejoras - Blog Go</title><link>https://news.google.com/go127</link>
			<source url="https://go.dev">Blog Go</source><pubDate>`+time.Now().Add(-time.Hour).Format(time.RFC1123Z)+`</pubDate></item>`))
	})
	mux.HandleFunc("/propio.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Mi blog", rssEntry("Novedades de mi hobby", "https://mi.blog/1", "Texto.", time.Hour)))
	})
	news := httptest.NewServer(mux)
	t.Cleanup(news.Close)

	oldGeneral, oldGN := generalSources, googleNewsBase
	generalSources = []generalSource{{"a", "Medio A", news.URL + "/a.xml"}, {"b", "Medio B", news.URL + "/b.xml"}, {"c", "Medio C", news.URL + "/c.xml"}}
	googleNewsBase = news.URL + "/gnews"
	t.Cleanup(func() { generalSources, googleNewsBase = oldGeneral, oldGN })

	pool := dbtest.Migrated(t, migrations)
	svc := &service{store: store{pool}, fetch: newFetcher(true), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		summarizer: func(ctx context.Context, prompt string) (string, error) {
			if !strings.Contains(prompt, "vivienda") {
				return "", fmt.Errorf("prompt sin noticias: %s", prompt)
			}
			return "Hoy destaca el plan de vivienda.", nil
		}}
	mux2 := http.NewServeMux()
	(&web{svc: svc}).routes(mux2)
	srv := httptest.NewServer(mux2)
	t.Cleanup(srv.Close)
	return &harness{svc: svc, srv: srv, news: news}
}

func (h *harness) do(t *testing.T, method, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("X-Auth-User", "ana")
	req.Header.Set("X-Auth-Name", "Ana")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestDigestFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Sin identidad de la pasarela no se entra.
	if resp, _ := http.Get(h.srv.URL + "/"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", resp.StatusCode)
	}
	// Un interés con búsqueda y un feed propio en él.
	if resp, body := h.do(t, "POST", "/temas", url.Values{"name": {"Programación"}, "query": {"golang"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("añadir interés: %d %s", resp.StatusCode, body)
	}
	topics, _ := h.svc.store.topics(ctx, "ana")
	if len(topics) != 1 {
		t.Fatalf("intereses: %+v", topics)
	}
	if resp, body := h.do(t, "POST", fmt.Sprintf("/temas/%d/feeds", topics[0].ID), url.Values{"url": {h.news.URL + "/propio.xml"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("añadir feed: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(t, "POST", fmt.Sprintf("/temas/%d/feeds", topics[0].ID), url.Values{"url": {h.news.URL + "/no-existe"}}); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no se ha podido leer el feed") {
		t.Fatalf("feed que no existe: %d", resp.StatusCode)
	}

	h.svc.collect(ctx)
	_, body := h.do(t, "GET", "/", nil)
	for _, want := range []string{"Buenos días, Ana", "Hoy destaca el plan de vivienda.", "Lo más importante", "Sánchez anuncia", "y 2 más",
		"Lluvias torrenciales", "Programación", "Sale Go 1.27 con mejoras", "Blog Go", "Novedades de mi hobby", `href="https://a.es/vivienda"`} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en el digest", want)
		}
	}
	if strings.Count(body, "plan de vivienda") > 2 { // agrupadas: una sola noticia (más el resumen)
		t.Error("la misma historia sale varias veces")
	}
	// El texto para Telegram.
	_, text := h.do(t, "GET", "/api/digest/texto?por_seccion=3", nil)
	if !strings.HasPrefix(text, "Hoy destaca") || !strings.Contains(text, "• Sale Go 1.27 con mejoras (Blog Go)") || !strings.Contains(text, "y 2 más") {
		t.Fatalf("texto: %s", text)
	}
	// Ajustes: sin resumen de IA y sin el medio A.
	resp, _ := h.do(t, "POST", "/ajustes", url.Values{"digest_at": {"06:30"}, "top_count": {"5"}, "topic_count": {"3"}, "src_b": {"1"}, "src_c": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("ajustes: %d", resp.StatusCode)
	}
	if p, _ := h.svc.store.prefs(ctx, "ana"); p.DigestAt != "06:30" || p.AISummary || len(p.Disabled) != 1 || p.Disabled[0] != "a" {
		t.Fatalf("preferencias: %+v", p)
	}
	if resp, _ := h.do(t, "POST", "/ajustes", url.Values{"digest_at": {"25:00"}, "top_count": {"5"}, "topic_count": {"3"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("acepta una hora imposible")
	}
	h.do(t, "POST", "/rehacer", url.Values{})
	_, body = h.do(t, "GET", "/", nil)
	if strings.Contains(body, "Lluvias torrenciales") || strings.Contains(body, "Hoy destaca") || !strings.Contains(body, "Sánchez anuncia") {
		t.Fatal("el digest no respeta los ajustes")
	}
	// Días anteriores y ajustes se ven.
	if _, body := h.do(t, "GET", "/dias", nil); !strings.Contains(body, "/dia/"+time.Now().In(madrid).Format("2006-01-02")) {
		t.Fatal("no aparece el día de hoy")
	}
	if _, body := h.do(t, "GET", "/ajustes", nil); !strings.Contains(body, "Mi blog") || !strings.Contains(body, "«golang»") {
		t.Fatal("los ajustes no muestran el interés y su feed")
	}
}

func TestDueDigests(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.svc.store.ensureUser(ctx, "ana")
	h.svc.collect(ctx)
	// A las 06:00 de Madrid aún no toca (por defecto a las 07:00); a las 07:05, sí.
	day := time.Now().In(madrid)
	early := time.Date(day.Year(), day.Month(), day.Day(), 6, 0, 0, 0, madrid)
	h.svc.dueDigests(ctx, early)
	if _, err := h.svc.store.digest(ctx, "ana", day.Format("2006-01-02")); err == nil {
		t.Fatal("se prepara antes de su hora")
	}
	h.svc.dueDigests(ctx, early.Add(65*time.Minute))
	d, err := h.svc.store.digest(ctx, "ana", day.Format("2006-01-02"))
	if err != nil || len(d.Sections) == 0 {
		t.Fatalf("a su hora: %v %+v", err, d)
	}
}
