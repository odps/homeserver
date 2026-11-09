package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func pageOf(t *testing.T, body, ct string) *page {
	t.Helper()
	u, _ := url.Parse("https://huerto.ejemplo.es/articulo/tomates")
	return &page{URL: u, ContentType: ct, Body: []byte(body)}
}

func TestExtract(t *testing.T) {
	ex, err := extract(pageOf(t, articleHTML("Cómo cultivar tomates en el balcón", "Huerto de Ejemplo", "Ana Ejemplo", "los tomates del balcón", 6), "text/html; charset=utf-8"), testNow())
	if err != nil {
		t.Fatal(err)
	}
	if ex.Title != "Cómo cultivar tomates en el balcón" || ex.Site != "Huerto de Ejemplo" || ex.Author != "Ana Ejemplo" || ex.Published != "2026-10-20" {
		t.Fatalf("metadatos: %+v", ex)
	}
	if ex.Image != "https://cdn.ejemplo.invalid/portada.jpg" || ex.Lang != "es" || ex.Excerpt == "" {
		t.Fatalf("imagen, idioma o extracto: %q %q %q", ex.Image, ex.Lang, ex.Excerpt)
	}
	if ex.Words < 200 || ex.Minutes < 1 || ex.Minutes != (ex.Words+wordsPerMin-1)/wordsPerMin {
		t.Fatalf("palabras %d, minutos %d", ex.Words, ex.Minutes)
	}
	kinds := map[string]int{}
	for _, b := range ex.Blocks {
		kinds[b.Kind]++
	}
	if kinds["p"] < 6 || kinds["h2"] != 1 || kinds["ul"] != 1 || kinds["cita"] != 1 || kinds["pre"] != 1 {
		t.Fatalf("bloques: %v", kinds)
	}
	// El título no se repite como primer bloque.
	if ex.Blocks[0].Kind != "p" {
		t.Fatalf("primer bloque: %+v", ex.Blocks[0])
	}
	all, _ := json.Marshal(ex.Blocks)
	text := string(all)
	for _, bad := range []string{"rastreo", "no-debe-salir", "alert(1)", "alert(2)", "alert(3)", "javascript", "onerror", "pixel.gif",
		"Anuncio", "Contacto del menú", "aviso legal", "iframe", "color:red", "<img", "<a ", "href"} {
		if strings.Contains(text, bad) {
			t.Errorf("el texto guarda %q", bad)
		}
	}
	// Lo que en la página era texto («<script>» escrito) sigue siendo texto.
	var found bool
	for _, b := range ex.Blocks {
		if strings.Contains(b.Text, `<script>alert("escapado")</script> aparece como texto`) {
			found = true
		}
		if b.Kind == "ul" && (len(b.Items) != 2 || b.Items[1] != "Después, probar poco a poco.") {
			t.Errorf("lista: %q", b.Items)
		}
		if b.Kind == "cita" && b.Text != "Lo que bien empieza\nbien acaba." {
			t.Errorf("cita: %q", b.Text)
		}
		if b.Kind == "pre" && b.Text != "paso 1:  regar\npaso 2:  esperar" {
			t.Errorf("código: %q", b.Text)
		}
		if strings.HasPrefix(b.Text, "Un poema corto") && b.Text != "Un poema corto:\nprimera línea\nsegunda línea" {
			t.Errorf("saltos de línea: %q", b.Text)
		}
	}
	if !found {
		t.Error("falta el texto con «<script>» escrito")
	}

	// Texto plano para Talia.
	plain, cut := plainText(ex.Blocks, maxToolText)
	if cut || !strings.Contains(plain, "## Lo básico de los tomates del balcón") || !strings.Contains(plain, "- Primero, mirar bien.") ||
		!strings.Contains(plain, "> Lo que bien empieza\n> bien acaba.") {
		t.Fatalf("texto plano: %s", plain)
	}
	if short, cut := plainText(ex.Blocks, 100); !cut || len([]rune(short)) > 101 {
		t.Fatalf("recorte: %d %v", len([]rune(short)), cut)
	}
}

func TestExtractCharsetAndFailures(t *testing.T) {
	s := newSite(t)
	f := testFetcher(s)
	p, err := f.get(t.Context(), s.url("/latin1"))
	if err != nil {
		t.Fatal(err)
	}
	ex, err := extract(p, testNow())
	if err != nil || ex.Title != "Canción de otoño" || !strings.Contains(ex.Blocks[0].Text, "la canción española") {
		t.Fatalf("ISO-8859-1: %q %v", ex.Title, err)
	}

	// Sin texto: error, pero con el título de la página.
	ex, err = extract(pageOf(t, `<html><head><title>Solo un título</title></head><body><p>Hola.</p></body></html>`, "text/html"), testNow())
	if err != errNoText || ex.Title != "Solo un título" {
		t.Fatalf("sin texto: %q %v", ex.Title, err)
	}
	// Basura.
	for _, junk := range []string{"", "\x00\x01\x02", "<<<>>>", strings.Repeat("<div>", 20000), "<html><body><table><tr><td>a</td></tr></table></body></html>"} {
		if _, err := extract(pageOf(t, junk, "text/html"), testNow()); err != errNoText {
			t.Errorf("basura %.20q: %v", junk, err)
		}
	}
	// Una fecha imposible no se guarda.
	future := strings.Replace(articleHTML("Del futuro", "S", "", "el futuro", 5), "2026-10-20", "2099-01-01", 1)
	if ex, err := extract(pageOf(t, future, "text/html"), testNow()); err != nil || ex.Published != "" {
		t.Fatalf("fecha futura: %q %v", ex.Published, err)
	}
}

func TestCleanTags(t *testing.T) {
	got, err := cleanTags([]string{" #Cocina, viajes ", "cocina", "Viajes,  largo   plazo", ""})
	if err != nil || strings.Join(got, "|") != "Cocina|viajes|largo plazo" {
		t.Fatalf("etiquetas: %q %v", got, err)
	}
	if _, err := cleanTags([]string{strings.Repeat("x", 31)}); !isInput(err) {
		t.Fatal("etiqueta larga")
	}
	if _, err := cleanTags([]string{"a,b,c,d,e,f,g,h,i,j,k"}); !isInput(err) {
		t.Fatal("demasiadas etiquetas")
	}
	if got, changed := mergeTags([]string{"Cocina"}, []string{"cocina", "Pan"}); !changed || strings.Join(got, "|") != "Cocina|Pan" {
		t.Fatalf("juntar: %q", got)
	}
}
