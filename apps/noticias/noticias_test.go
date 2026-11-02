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
	"homeserver/platform/seal"
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
	mux.HandleFunc("/gn/search", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Query().Get("q"), "golang") {
			io.WriteString(w, rss("Google News"))
			return
		}
		io.WriteString(w, rss("Google News", `<item><title>Sale Go 1.27 con mejoras - Blog Go</title><link>https://news.google.com/go127</link>
			<source url="https://go.dev">Blog Go</source><pubDate>`+time.Now().Add(-time.Hour).Format(time.RFC1123Z)+`</pubDate></item>`))
	})
	mux.HandleFunc("/w1.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("World 1", rssEntry("UN Security Council votes on Sudan ceasefire resolution", "https://w1.com/sudan", "Vote due.", time.Hour)))
	})
	mux.HandleFunc("/w2.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("World 2", rssEntry("Security Council votes Sudan ceasefire resolution today", "https://w2.com/sudan", "", 2*time.Hour)))
	})
	mux.HandleFunc("/tec.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Tec", rssEntry("Nuevo chip cuántico récord", "https://tec.es/chip", "", time.Hour)))
	})
	mux.HandleFunc("/hn.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Hacker News", rssEntry("Primero en HN", "https://hn.example/1", "", 5*time.Hour), rssEntry("Segundo en HN", "https://hn.example/2", "", time.Minute)))
	})
	mux.HandleFunc("/reddit/r/golang/top/.rss", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<feed xmlns="http://www.w3.org/2005/Atom"><title>top scoring links : golang</title><entry><title>Go 1.27 is out</title>
			<link href="https://www.reddit.com/r/golang/comments/1"/><updated>`+time.Now().Format(time.RFC3339)+`</updated></entry></feed>`)
	})
	mux.HandleFunc("/propio.xml", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Mi blog", rssEntry("Novedades de mi hobby", "https://mi.blog/1", "Texto.", time.Hour)))
	})
	news := httptest.NewServer(mux)
	t.Cleanup(news.Close)

	oldGeneral, oldGN, oldPresets, oldReddit := generalSources, googleNewsRoot, presets, redditRoot
	generalSources = []generalSource{{"a", "Medio A", news.URL + "/a.xml", "es"}, {"b", "Medio B", news.URL + "/b.xml", "es"}, {"c", "Medio C", news.URL + "/c.xml", "es"},
		{"w1", "World 1", news.URL + "/w1.xml", "mundo"}, {"w2", "World 2", news.URL + "/w2.xml", "mundo"}}
	googleNewsRoot, redditRoot = news.URL+"/gn", news.URL+"/reddit"
	presets = []preset{{"tecnologia", "Tecnología", "tema", feeds(news.URL + "/tec.xml"), ""}, {"hackernews", "Hacker News", "redes", feeds(news.URL + "/hn.xml"), ""}}
	t.Cleanup(func() { generalSources, googleNewsRoot, presets, redditRoot = oldGeneral, oldGN, oldPresets, oldReddit })

	box, _ := seal.Load("")
	pool := dbtest.MigratedSteps(t, migrations, steps(box))
	svc := &service{store: store{pool, box}, fetch: newFetcher(true), log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		summarizer: func(ctx context.Context, prompt string) (string, error) {
			if !strings.Contains(prompt, "vivienda") {
				return "", fmt.Errorf("prompt sin noticias: %s", prompt)
			}
			return "Hoy destaca el plan de vivienda.", nil
		}}
	// Las recogidas en segundo plano acaban antes de deshacer los cambios de arriba.
	t.Cleanup(svc.bg.Wait)
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
	for _, want := range []string{"Buenos días, Ana", "Hoy destaca el plan de vivienda.", "Lo más importante", "Sánchez anuncia", "3 medios",
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
	if !strings.HasPrefix(text, "• Hoy destaca") || !strings.Contains(text, "TITULARES") || !strings.Contains(text, "• Sale Go 1.27 con mejoras (Blog Go)") || !strings.Contains(text, "y 2 más") {
		t.Fatalf("texto: %s", text)
	}
	// Ajustes, por partes: sin resumen de IA, sin el medio A, categorías y subforos.
	resp, _ := h.do(t, "POST", "/ajustes", url.Values{"part": {"digest"}, "digest_at": {"06:30"}, "top_count": {"5"}, "world_count": {"4"}, "topic_count": {"3"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("ajustes: %d", resp.StatusCode)
	}
	h.do(t, "POST", "/ajustes", url.Values{"part": {"medios"}, "src_b": {"1"}, "src_c": {"1"}, "src_w1": {"1"}, "src_w2": {"1"}})
	resp, _ = h.do(t, "POST", "/ajustes", url.Values{"part": {"secciones"}, "preset_tecnologia": {"1"}, "preset_hackernews": {"1"}, "reddit": {"r/golang https://www.reddit.com/r/worldnews/ golang"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("secciones: %d", resp.StatusCode)
	}
	p, _ := h.svc.store.prefs(ctx, "ana")
	if p.DigestAt != "06:30" || p.AISummary || p.WorldCount != 4 || len(p.Disabled) != 1 || p.Disabled[0] != "a" || p.World ||
		strings.Join(p.Presets, ",") != "tecnologia,hackernews" || strings.Join(p.Reddit, ",") != "golang,worldnews" {
		t.Fatalf("preferencias: %+v", p)
	}
	if resp, body := h.do(t, "POST", "/ajustes", url.Values{"part": {"secciones"}, "reddit": {"no vale!"}}); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no tienen un nombre válido") {
		t.Fatal("acepta un subforo con un nombre imposible")
	}
	if resp, _ := h.do(t, "POST", "/ajustes", url.Values{"part": {"digest"}, "digest_at": {"25:00"}, "top_count": {"5"}, "world_count": {"5"}, "topic_count": {"3"}}); resp.StatusCode != http.StatusBadRequest {
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

// Detrás de la pasarela: los enlaces llevan /noticias, las redirecciones no
// (la pasarela se lo añade; si no, quedaría /noticias/noticias/…).
func TestPrefix(t *testing.T) {
	h := newHarness(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("POST", h.srv.URL+"/ajustes", strings.NewReader("part=digest&digest_at=07:00&top_count=10&world_count=8&topic_count=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Auth-User", "ana")
	req.Header.Set("X-Forwarded-Prefix", "/noticias")
	resp, err := client.Do(req)
	if err != nil || resp.Header.Get("Location") != "/ajustes?ok=ajustes" {
		t.Fatalf("redirección: %v %q", err, resp.Header.Get("Location"))
	}
	req, _ = http.NewRequest("GET", h.srv.URL+"/ajustes", nil)
	req.Header.Set("X-Auth-User", "ana")
	req.Header.Set("X-Forwarded-Prefix", "/noticias")
	resp, _ = client.Do(req)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `action="/noticias/ajustes"`) || !strings.Contains(string(b), `href="/noticias/static/app.css"`) {
		t.Fatal("los enlaces no llevan el prefijo")
	}
}

func TestSectionsOrder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.svc.redditPause = 0
	h.do(t, "POST", "/temas", url.Values{"name": {"Programación"}, "query": {"golang"}})
	h.do(t, "POST", "/ajustes", url.Values{"part": {"secciones"}, "world": {"1"}, "preset_tecnologia": {"1"}, "preset_hackernews": {"1"}, "reddit": {"golang"}})
	h.svc.collect(ctx)
	d, err := h.svc.prepare(ctx, "ana", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, s := range d.Sections {
		titles = append(titles, s.Title)
	}
	if got := strings.Join(titles, " | "); got != "Lo más importante | En el mundo | Tecnología | Programación | Hacker News | r/golang" {
		t.Fatalf("secciones: %s", got)
	}
	world := d.Sections[1]
	if len(world.Items) != 1 || len(world.Items[0].Others) != 1 { // las dos agencias, agrupadas
		t.Fatalf("en el mundo: %+v", world.Items)
	}
	hn := d.Sections[4]
	if hn.Kind != "red" || hn.Items[0].Title != "Primero en HN" { // manda el orden del feed, no la fecha
		t.Fatalf("hacker news: %+v", hn)
	}
	if r := d.Sections[5]; r.Items[0].Source != "r/golang" || r.Items[0].Title != "Go 1.27 is out" {
		t.Fatalf("reddit: %+v", r.Items)
	}
	_, body := h.do(t, "GET", "/", nil)
	if !strings.Contains(body, `class="rank"`) || !strings.Contains(body, `href="#s5"`) {
		t.Fatal("la página no muestra el índice o las listas por posición")
	}
}

func TestSocialFormats(t *testing.T) {
	now := time.Now()
	m, err := parseJSONFeed("https://m.social/api/v1/trends/links?limit=20", []byte(`[{"url":"https://forbes.com/a","title":"Madrid sin coches","description":"Más ventas","provider_name":"Forbes"}]`), now)
	if err != nil || len(m.Items) != 1 || m.Items[0].Source != "Forbes" || m.Items[0].Summary != "Más ventas" {
		t.Fatalf("Mastodon: %v %+v", err, m)
	}
	b, err := parseJSONFeed("https://x/xrpc/app.bsky.unspecced.getTrendingTopics", []byte(`{"topics":[{"topic":"halloween","displayName":"Halloween","link":"/profile/trending.bsky.app/feed/1"}]}`), now)
	if err != nil || len(b.Items) != 1 || b.Items[0].Link != "https://bsky.app/profile/trending.bsky.app/feed/1" || b.Items[0].Title != "Halloween" {
		t.Fatalf("Bluesky: %v %+v", err, b)
	}
	trends := `<rss xmlns:ht="https://trends.google.com/trending/rss" version="2.0"><channel><title>Daily Search Trends</title><item><title>braulio vázquez</title>
		<ht:approx_traffic>2000+</ht:approx_traffic><link>https://trends.google.com/trending/rss?geo=ES</link>
		<ht:news_item><ht:news_item_title>Braulio, nuevo director</ht:news_item_title><ht:news_item_url>https://marca.com/b</ht:news_item_url><ht:news_item_source>MARCA</ht:news_item_source></ht:news_item></item></channel></rss>`
	g, err := parseFeed([]byte(trends), now)
	if err != nil || len(g.Items) != 1 || g.Items[0].Link != "https://marca.com/b" || g.Items[0].Source != "MARCA" || g.Items[0].Summary != "2000+ búsquedas · Braulio, nuevo director" {
		t.Fatalf("Google Tendencias: %v %+v", err, g)
	}
	for in, want := range map[string]string{"golang": "golang", "r/golang": "golang", "https://www.reddit.com/r/golang/top/": "golang", "no vale": "", "x": ""} {
		if got := cleanSubreddit(in); got != want {
			t.Errorf("cleanSubreddit(%q) = %q", in, got)
		}
	}
}

func TestSocialSummary(t *testing.T) {
	for in, want := range map[string]string{
		"Article URL: https://a.com/x Comments URL: https://news.ycombinator.com/item?id=1 Points: 51 # Comments: 22": "51 puntos · 22 comentarios",
		"Un texto del post submitted by /u/fulano [link] [comments]":                                                  "Un texto del post",
		"Entradilla normal": "Entradilla normal",
	} {
		if got := socialSummary(in); got != want {
			t.Errorf("socialSummary(%q) = %q", in, got)
		}
	}
}
