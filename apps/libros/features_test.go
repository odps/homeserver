package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestNormalizeISBN(t *testing.T) {
	for in, want := range map[string]string{
		"0-306-40615-2":          "9780306406157",
		"978-0-306-40615-7":      "9780306406157",
		"urn:isbn:9780306406157": "9780306406157",
		"ISBN 0 8044 2957 X":     "9780804429573",
	} {
		if got, ok := normalizeISBN(in); !ok || got != want {
			t.Errorf("normalizeISBN(%q) = %q, %v; quiero %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0-306-40615-3", "9780306406158", "1234567890123", "abc", "97803064061577"} {
		if got, ok := normalizeISBN(in); ok {
			t.Errorf("normalizeISBN(%q) acepta %q", in, got)
		}
	}
}

func TestSettingsSync(t *testing.T) {
	h := newHarness(t)
	d := decode[settingsDoc](t, h.do(t, "GET", "/api/settings", nil))
	if d.UpdatedAt != nil || d.Reader != defaultReader {
		t.Fatalf("ajustes iniciales: %+v", d)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	dark := defaultReader
	dark.Theme, dark.Size = "dark", 130
	d = decode[settingsDoc](t, h.do(t, "PUT", "/api/settings", map[string]any{"reader": dark, "updated_at": now}))
	if d.Reader != dark || d.UpdatedAt == nil {
		t.Fatalf("guardar ajustes: %+v", d)
	}
	// Un dispositivo que estuvo sin conexión no pisa un cambio posterior.
	old := defaultReader
	d = decode[settingsDoc](t, h.do(t, "PUT", "/api/settings", map[string]any{"reader": old, "updated_at": now.Add(-time.Hour)}))
	if d.Reader != dark {
		t.Fatalf("un cambio antiguo pisó al nuevo: %+v", d)
	}
	bad := dark
	bad.Theme = "neón"
	if resp := h.do(t, "PUT", "/api/settings", map[string]any{"reader": bad}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ajustes no válidos: %d", resp.StatusCode)
	}
	// Son de cada persona.
	h.as("luis", "usuario")
	if d := decode[settingsDoc](t, h.do(t, "GET", "/api/settings", nil)); d.Reader != defaultReader {
		t.Fatalf("luis ve los ajustes de ana: %+v", d)
	}
}

func TestISBNAndCover(t *testing.T) {
	h := newHarness(t)
	h.api.catalog = testCatalog(fakeSources(t, nil))
	epub := zipBytes(t, map[string]string{"mimetype": "application/epub+zip", "META-INF/container.xml": "<c/>"})
	status, out := h.upload(t, "q.epub", epub, map[string]any{"title": "Química", "isbn": "urn:isbn:0306406152"})
	if status != http.StatusCreated {
		t.Fatalf("subida: %d %s", status, out["error"])
	}
	var b Book
	if err := json.Unmarshal(out["book"], &b); err != nil || b.ISBN == nil || *b.ISBN != "9780306406157" {
		t.Fatalf("ISBN extraído al subir: %v %+v", err, b.ISBN)
	}
	id := itoa(b.ID)
	if resp := h.do(t, "PATCH", "/api/books/"+id, map[string]any{"title": "Química", "isbn": "123"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ISBN no válido: %d", resp.StatusCode)
	}
	edited := decode[Book](t, h.do(t, "PATCH", "/api/books/"+id, map[string]any{"title": "Química", "isbn": "978-0-306-40615-7", "authors": []string{"Ana"}}))
	if edited.ISBN == nil || *edited.ISBN != "9780306406157" || edited.Authors[0] != "Ana" {
		t.Fatalf("editar: %+v", edited)
	}

	info := decode[isbnInfo](t, h.do(t, "GET", "/api/isbn/0-306-40615-2", nil))
	if info.Title != "Polymer Chemistry" || info.Authors[0] != "Ana Autora" || info.Publisher != "Plenum" || info.Published != "1983" ||
		info.Description != "Química & polímeros" || !info.HasCover {
		t.Fatalf("datos del ISBN: %+v", info)
	}
	if resp := h.do(t, "GET", "/api/isbn/9780306406158", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ISBN con dígito de control mal: %d", resp.StatusCode)
	}

	// Portada desde el ISBN y portada subida a mano; la URL cambia (updated_at).
	time.Sleep(10 * time.Millisecond)
	c := decode[Book](t, h.do(t, "PUT", "/api/books/"+id+"/cover?isbn=9780306406157", nil))
	if !c.HasCover || !c.UpdatedAt.After(edited.UpdatedAt) {
		t.Fatalf("portada por ISBN: %+v", c)
	}
	req, _ := http.NewRequest("PUT", h.srv.URL+"/api/books/"+id+"/cover", bytes.NewReader(pngPixel))
	h.identify(req)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("portada subida: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("PUT", h.srv.URL+"/api/books/"+id+"/cover", bytes.NewReader([]byte("no es una imagen")))
	h.identify(req)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("portada que no es imagen: %d", resp.StatusCode)
	}
	// Otra persona no puede cambiar la portada de un libro privado ajeno.
	h.as("luis", "usuario")
	if resp := h.do(t, "PUT", "/api/books/"+id+"/cover?isbn=9780306406157", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("portada de un libro ajeno: %d", resp.StatusCode)
	}
}
