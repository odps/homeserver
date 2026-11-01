package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"strings"
	"testing"

	"homeserver/platform/app"
)

// bigCover es una portada JPEG de 300×450.
func bigCover(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 300, 450))
	for x := 0; x < 300; x++ {
		img.Set(x, x, color.RGBA{uint8(x), 80, 120, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestConfident(t *testing.T) {
	cases := []struct {
		title   string
		authors []string
		found   isbnInfo
		want    bool
	}{
		{"Don Quijote", []string{"Cervantes"}, isbnInfo{Title: "Don Quijote de la Mancha", Authors: []string{"Miguel de Cervantes"}}, true},
		{"El príncipe", []string{"Maquiavelo"}, isbnInfo{Title: "El Principe: edición crítica", Authors: []string{"Nicolás Maquiavelo"}}, true},
		{"Don Quijote", []string{"Cervantes"}, isbnInfo{Title: "Don Quijote de la Mancha", Authors: []string{"Avellaneda"}}, false},
		{"Don Quijote", nil, isbnInfo{Title: "Don Quijote de la Mancha"}, false}, // sin autor, el título debe ser igual
		{"Dune", nil, isbnInfo{Title: "Dune", Authors: []string{"Frank Herbert"}}, false},
		{"Competitive Programming", nil, isbnInfo{Title: "Competitive Programming", Authors: []string{"Otra Persona"}}, false},
		{"Designing Data-Intensive Applications", nil, isbnInfo{Title: "Designing Data-Intensive Applications", Authors: []string{"Martin Kleppmann"}}, true},
		{"Cálculo", []string{"Spivak"}, isbnInfo{Title: "Cálculo infinitesimal y geometría", Authors: []string{"Spivak"}}, true},
		{"Física", []string{"Tipler"}, isbnInfo{Title: "Química", Authors: []string{"Tipler"}}, false},
	}
	for _, c := range cases {
		if got := confident(c.title, c.authors, c.found); got != c.want {
			t.Errorf("confident(%q, %v, %q) = %v", c.title, c.authors, c.found.Title, got)
		}
	}
}

func TestAutofill(t *testing.T) {
	src := fakeSources(t, nil)
	h := newHarness(t)
	h.api.catalog = testCatalog(src)
	ctx := context.Background()
	upload := func(name string, meta map[string]any) Book {
		t.Helper()
		epub := zipBytes(t, map[string]string{"mimetype": "application/epub+zip", "META-INF/container.xml": "<c/>", "x": name})
		status, out := h.upload(t, name, epub, meta)
		if status != http.StatusCreated {
			t.Fatalf("subida: %d %s", status, out["error"])
		}
		var b Book
		json.Unmarshal(out["book"], &b)
		return b
	}
	run := func(id int64) Book {
		t.Helper()
		g, err := h.api.store.metaGap(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.api.autofill(ctx, g); err != nil {
			t.Fatal(err)
		}
		b, _ := h.api.store.get(ctx, app.Identity{User: h.user, Role: h.role}, id)
		return b
	}

	// Recién subido: se mejora lo que se encuentra mejor y se rellena lo que falta.
	b := upload("quijote.epub", map[string]any{"title": "Don Quijote", "authors": []string{"Cervantes"}, "description": "Corta.", "language": "es", "publisher": "Mía"})
	b = run(b.ID)
	if b.Title != "Don Quijote" || strings.Join(b.Authors, ",") != "Miguel de Cervantes Saavedra" || b.ISBN == nil || *b.ISBN != "9788420412146" ||
		b.Published == nil || *b.Published != "1605" || b.Description == nil || !strings.HasPrefix(*b.Description, "Las aventuras") ||
		b.Publisher == nil || *b.Publisher != "Mía" || strings.Join(b.Genres, ",") != "Novela,Caballería" {
		t.Fatalf("datos mejorados: %+v", b)
	}
	cover, _ := os.ReadFile(h.files.coverPath(b))
	if coverPixels(cover) != 300*450 {
		t.Fatalf("la portada de 1×1 no se ha cambiado por la de 300×450 (%d)", coverPixels(cover))
	}

	// Editado a mano: solo se rellena lo que falta.
	e := upload("quijote2.epub", map[string]any{"title": "Don Quijote", "authors": []string{"Cervantes"}, "description": "Mi resumen."})
	h.api.store.db.Exec(ctx, `UPDATE books SET added_at = now() - interval '1 day' WHERE id = $1`, e.ID)
	e = run(e.ID)
	if *e.Description != "Mi resumen." || strings.Join(e.Authors, ",") != "Cervantes" || e.ISBN == nil {
		t.Fatalf("libro editado: %+v", e)
	}

	// Sin coincidencia clara: no se toca, pero queda revisado.
	n := upload("raro.epub", map[string]any{"title": "Un libro que no existe", "authors": []string{"Nadie"}})
	n = run(n.ID)
	if n.ISBN != nil || n.Description != nil {
		t.Fatalf("sin coincidencia: %+v", n)
	}
	if gaps, _ := h.api.store.needingMeta(ctx, 10); len(gaps) != 0 {
		t.Fatalf("quedan libros por revisar: %d", len(gaps))
	}

	// Búsqueda manual y portada de un resultado.
	var res struct{ Items []metaCandidate }
	resp := h.do(t, "GET", "/api/meta/buscar?title=Don+Quijote&author=Cervantes", nil)
	json.NewDecoder(resp.Body).Decode(&res)
	if len(res.Items) != 3 || res.Items[0].Ref == "" || res.Items[0].Language != "es" || res.Items[2].Source != "google" {
		t.Fatalf("búsqueda manual: %+v", res.Items)
	}
	if resp := h.do(t, "GET", "/api/meta/portada?ref="+res.Items[0].Ref, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("portada del resultado: %d", resp.StatusCode)
	}
	if resp := h.do(t, "PUT", "/api/books/"+itoa(n.ID)+"/cover?ref="+res.Items[0].Ref, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("usar la portada del resultado: %d", resp.StatusCode)
	}
	if resp := h.do(t, "PUT", "/api/books/"+itoa(n.ID)+"/cover?ref=inventada", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("referencia inventada: %d", resp.StatusCode)
	}
}

func TestMergeAuthors(t *testing.T) {
	g := metaGap{Title: "What every BODY is saying", Authors: []string{"Navarro, Joe, 1953-"}}
	u := mergeMeta(g, isbnInfo{Title: "What every BODY is saying", Authors: []string{"Joe Navarro", "Marvin Karlins"}}, true)
	if strings.Join(u.Authors, ",") != "Joe Navarro,Marvin Karlins" {
		t.Fatalf("autores: %v", u.Authors)
	}
	// Otros autores no sustituyen a los que hay.
	u = mergeMeta(g, isbnInfo{Title: "What every BODY is saying", Authors: []string{"Otra Persona"}}, true)
	if strings.Join(u.Authors, ",") != "Navarro, Joe, 1953-" {
		t.Fatalf("autores distintos: %v", u.Authors)
	}
}

func TestBlankCoverAndAuthor(t *testing.T) {
	white := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	white.Set(10, 10, color.RGBA{250, 250, 250, 255}) // ruido de compresión
	var b bytes.Buffer
	jpeg.Encode(&b, white, nil)
	if !blankCover(b.Bytes()) {
		t.Error("una portada en blanco no se detecta")
	}
	if blankCover(bigCover(t)) {
		t.Error("una portada con dibujo se toma por blanca")
	}
	if blankCover([]byte("no es una imagen")) {
		t.Error("lo que no se puede leer no cuenta como en blanco")
	}
	for in, want := range map[string]string{"What every BODY is saying : an ex-FBI agent's guide": "What every BODY is saying",
		"Dune: edición especial": "Dune: edición especial", "Cálculo (volumen 1)": "Cálculo (volumen 1)", "Guía práctica (2.ª ed.)": "Guía práctica"} {
		if got := mainTitle(in); got != want {
			t.Errorf("mainTitle(%q) = %q, quería %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Navarro, Joe, 1953-": "Joe Navarro", "Hartley, Gregory": "Gregory Hartley",
		"Carl Gustav Jung": "Carl Gustav Jung", "Sunzi, active 6th century B.C.": "Sunzi", "": ""} {
		if got := searchAuthor(in); got != want {
			t.Errorf("searchAuthor(%q) = %q, quería %q", in, got, want)
		}
	}
}
