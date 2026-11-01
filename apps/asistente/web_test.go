package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchAndRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/res/v1/web/search":
			if r.Header.Get("X-Subscription-Token") != "clave" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"web":{"results":[{"title":"El lenguaje <strong>Go</strong>","url":"https://es.wikipedia.org/wiki/Go","description":"Go es un lenguaje &amp; más"}]}}`))
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
	w.braveURL = srv.URL
	ctx := context.Background()
	res, err := w.search(ctx, "go", "clave")
	if err != nil || len(res) != 1 || res[0].URL != "https://es.wikipedia.org/wiki/Go" || res[0].Title != "El lenguaje Go" || res[0].Snippet != "Go es un lenguaje & más" {
		t.Fatalf("Brave: %v %+v", err, res)
	}
	if _, err := w.search(ctx, "go", ""); err == nil {
		t.Fatal("busca sin clave")
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
	// Si al modelo se le escapa Markdown, llega como texto plano.
	in := "# Título\n**negrita** y *cursiva* con <script> & [[fuente]](https://a.es/x?b=1&c=2)\n- uno\n> cita\n\n| Libro | Autor |\n|---|---|\n| On War | Clausewitz |\n| El príncipe | Maquiavelo |\n```\ncódigo\n```"
	got := plainText(in)
	want := "Título\nnegrita y cursiva con <script> & fuente (https://a.es/x?b=1&c=2)\n• uno\ncita\n\n• On War — Clausewitz\n• El príncipe — Maquiavelo\ncódigo"
	if got != want {
		t.Errorf("texto plano:\n%s\nesperado:\n%s", got, want)
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
