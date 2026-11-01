package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDuckDuckGoAndRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html/":
			w.Write([]byte(`<div><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fes.wikipedia.org%2Fwiki%2FGo&amp;rut=x">El lenguaje <b>Go</b></a>
				<a class="result__snippet" href="#">Go es un lenguaje &amp; más</a></div>
				<a class="result__a" href="https://duckduckgo.com/y.js?ad=1">Anuncio</a>`))
		case "/pagina":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>Mi &amp; página</title><style>.x{}</style></head><body><nav>menú</nav>
				<p>Primer párrafo.</p><script>alert(1)</script><p>Segundo &lt;párrafo&gt;.</p></body></html>`))
		case "/binario":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0, 1, 2})
		}
	}))
	defer srv.Close()
	w := newWebClient(true)
	w.ddgURL = srv.URL
	ctx := context.Background()
	res, err := w.search(ctx, "go", "")
	if err != nil || len(res) != 1 || res[0].URL != "https://es.wikipedia.org/wiki/Go" || res[0].Title != "El lenguaje Go" || res[0].Snippet != "Go es un lenguaje & más" {
		t.Fatalf("DuckDuckGo: %v %+v", err, res)
	}
	title, text, err := w.read(ctx, srv.URL+"/pagina")
	if err != nil || title != "Mi & página" || !strings.Contains(text, "Primer párrafo.") || !strings.Contains(text, "Segundo <párrafo>.") ||
		strings.Contains(text, "alert") || strings.Contains(text, "menú") {
		t.Fatalf("leer página: %v %q %q", err, title, text)
	}
	if _, _, err := w.read(ctx, srv.URL+"/binario"); err == nil {
		t.Fatal("lee un fichero binario")
	}
	// El cliente de verdad no entra en la red local ni en el propio móvil.
	strict := newWebClient(false)
	for _, u := range []string{srv.URL + "/pagina", "http://192.168.1.1/", "http://100.100.100.100/", "file:///etc/passwd", "ftp://x/"} {
		if _, _, err := strict.read(ctx, u); err == nil {
			t.Errorf("lee %s", u)
		}
	}
}

func TestTelegramFormat(t *testing.T) {
	in := "# Título\n**negrita** y *cursiva* con <script> & [enlace](https://a.es/x?b=1&c=2)\n- uno\n```\ncódigo <b>\n```"
	got := toTelegramHTML(in)
	for _, want := range []string{"<b>Título</b>", "<b>negrita</b>", "<i>cursiva</i>", "&lt;script&gt; &amp;", `<a href="https://a.es/x?b=1&amp;c=2">enlace</a>`, "• uno", "<pre>código &lt;b&gt;</pre>"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en %q", want, got)
		}
	}
	parts := splitMessage(strings.Repeat("palabra ", 1000)+"\n\n"+strings.Repeat("otra ", 500), 3500)
	for _, p := range parts {
		if len(p) > 3500 {
			t.Fatalf("trozo demasiado largo: %d", len(p))
		}
	}
	if len(parts) < 2 {
		t.Fatal("no se parte un mensaje largo")
	}
}

func TestPickModel(t *testing.T) {
	groq := []string{"whisper-large-v3", "llama-3.3-70b-versatile", "openai/gpt-oss-120b", "meta-llama/llama-guard-4-12b"}
	if m := pickModel("groq", groq); m != "openai/gpt-oss-120b" {
		t.Fatalf("groq: %q", m)
	}
	gemini := []string{"gemini-2.5-flash", "gemini-3.8-flash", "gemini-3.8-flash-lite", "gemini-2.5-flash-image", "gemini-embedding-001"}
	if m := pickModel("gemini", gemini); m != "gemini-3.8-flash" {
		t.Fatalf("gemini: %q", m)
	}
	if m := pickModel("custom", groq); m != "" {
		t.Fatalf("sin preferencia: %q", m)
	}
	s := schemaFor(map[string]any{"type": "object", "$schema": "x"})
	if _, ok := s["properties"]; !ok || s["$schema"] != nil {
		t.Fatalf("esquema: %v", s)
	}
}
