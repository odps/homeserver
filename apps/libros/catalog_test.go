package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"homeserver/platform/dbtest"
)

// fakeSources imita el OPDS de Gutenberg, Wikisource, ws-export e Internet Archive.
func fakeSources(t *testing.T, epub []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/ebooks/search.opds/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != "quijote l.es" {
			t.Errorf("consulta a Gutenberg: %q", got)
		}
		w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><link rel="self" href="/x"/>
			<entry><id>https://www.gutenberg.org/ebooks/2000.opds</id><title>Don Quijote (Spanish)</title><content type="text">Miguel de Cervantes Saavedra</content></entry>
			<entry><id>https://www.gutenberg.org/ebooks/search.opds/?sort_order=title</id><title>Ordenar por título</title></entry></feed>`))
	})
	book := func(rights, kind string) string {
		return `<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dcterms="http://purl.org/dc/terms/"><entry>
			<title>Don Quijote</title><content type="xhtml"><div><p>Summary:
			"Don Quijote" es una novela &amp; más. (This is an automatically generated summary.)</p><p>Downloads: 1234</p></div></content>
			<rights>` + rights + `</rights><author><name>Cervantes Saavedra, Miguel de</name></author>
			<category scheme="http://purl.org/dc/terms/LCSH" term="Spain -- Fiction"/><category scheme="http://purl.org/dc/terms/DCMIType" term="` + kind + `"/>
			<dcterms:language>es</dcterms:language>
			<link type="application/epub+zip" rel="http://opds-spec.org/acquisition" href="` + base + `/epub/2000.epub.noimages"/>
			<link type="image/jpeg" rel="http://opds-spec.org/image" href="/cover.jpg"/>
			<link type="application/atom+xml;profile=opds-catalog" rel="related" href="/ebooks/bookshelf/645.opds" title="In Category: Novels…"/>
			</entry><entry><title>Don Quijote</title><rights>` + rights + `</rights>
			<category scheme="http://purl.org/dc/terms/DCMIType" term="` + kind + `"/>
			<link type="application/epub+zip" rel="http://opds-spec.org/acquisition" href="/epub/2000"/>
			<link type="application/epub+zip" rel="http://opds-spec.org/acquisition" href="` + base + `/ebooks/2000.epub3.images"/></entry></feed>`
	}
	mux.HandleFunc("/ebooks/2000.opds", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(book("Public domain in the USA.", "Text")))
	})
	mux.HandleFunc("/ebooks/9.opds", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(book("Copyrighted.", "Text"))) })
	mux.HandleFunc("/ebooks/10.opds", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(book("Public domain in the USA.", "Sound")))
	})
	mux.HandleFunc("/ebooks/2000.epub3.images", func(w http.ResponseWriter, r *http.Request) { w.Write(epub) })
	mux.HandleFunc("/epub/2000", func(w http.ResponseWriter, r *http.Request) { w.Write(epub) })
	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write(pngPixel) })
	mux.HandleFunc("/evil", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://intranet.example/secreto", http.StatusFound)
	})
	mux.HandleFunc("/es/w/api.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"query":{"search":[{"title":"Don Quijote de la Mancha"},{"title":"Don Quijote de la Mancha/Capítulo 1"}]}}`))
	})
	mux.HandleFunc("/advancedsearch.php", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "licenseurl") {
			t.Errorf("búsqueda en Internet Archive sin filtro de licencia: %s", r.URL.Query().Get("q"))
		}
		w.Write([]byte(`{"response":{"numFound":2,"docs":[
			{"identifier":"libre1","title":"Libro libre","creator":["Autor"],"licenseurl":"http://creativecommons.org/publicdomain/mark/1.0/"},
			{"identifier":"dudoso","title":"Sin licencia","creator":"X"}]}}`))
	})
	// Open Library y Google Books (datos por ISBN).
	mux.HandleFunc("/api/books", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("bibkeys") != "ISBN:9780306406157" {
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`{"ISBN:9780306406157":{"title":"Polymer Chemistry","authors":[{"name":"Ana Autora"}],
			"publishers":[{"name":"Plenum"}],"publish_date":"March 1983","cover":{"large":"` + base + `/cover.jpg"}}}`))
	})
	mux.HandleFunc("/books/v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); strings.Contains(q, `intitle:"Don Quijote"`) {
			w.Write([]byte(`{"items":[{"volumeInfo":{"title":"Don Quijote de la Mancha","authors":["Miguel de Cervantes"],
				"description":"Las aventuras del ingenioso hidalgo y de su escudero Sancho Panza por la Mancha, contadas con todo detalle.",
				"language":"es","publishedDate":"2004-03-01","industryIdentifiers":[{"type":"ISBN_13","identifier":"9788420412146"}]}}]}`))
			return
		}
		w.Write([]byte(`{"items":[{"volumeInfo":{"title":"Otro","description":"<p>Química &amp; polímeros</p>","language":"en"}}]}`))
	})
	// Open Library: búsqueda por título y portadas por id.
	mux.HandleFunc("/search.json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("title") != "Don Quijote" {
			w.Write([]byte(`{"docs":[]}`))
			return
		}
		w.Write([]byte(`{"docs":[{"title":"Don Quijote de la Mancha","author_name":["Miguel de Cervantes Saavedra"],"first_publish_year":1605,
			"isbn":["8420412147","9788420412146"],"cover_i":123,"language":["spa"],"publisher":["Alfaguara"],"subject":["Novela","Caballería"]},
			{"title":"Quijote para niños","author_name":["Otra Persona"],"cover_i":7}]}`))
	})
	mux.HandleFunc("/b/id/123-L.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write(bigCover(t)) })
	// OpenStax: el catálogo de cada idioma de una vez.
	mux.HandleFunc("/apps/cms/api/v2/pages/", func(w http.ResponseWriter, r *http.Request) {
		book := func(id int, title, locale, state, license string) string {
			return fmt.Sprintf(`{"id":%d,"title":%q,"book_state":%q,"high_resolution_pdf_url":"%s/openstax/%d.pdf","license_name":%q,
				"publish_date":"2022-03-24","description":"<p>Libro de <i>texto</i></p>","authors":[{"value":{"name":"Gilbert Strang"}}],
				"meta":{"slug":"libro-%d","locale":%q}}`, id, title, state, base, id, license, id, locale)
		}
		switch r.URL.Query().Get("locale") {
		case "es":
			w.Write([]byte(`{"items":[` + book(578, "Cálculo volumen 1", "es", "live", "Creative Commons Attribution-NonCommercial-ShareAlike License") + "," +
				book(579, "Física antigua", "es", "retired", "Creative Commons Attribution License") + "]}"))
		default:
			w.Write([]byte(`{"items":[` + book(100, "Calculus Volume 1", "en", "live", "Creative Commons Attribution License") + "]}"))
		}
	})
	mux.HandleFunc("/openstax/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("%PDF-1.7\n% libro de prueba\n")) })
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func testCatalog(srv *httptest.Server) *catalog {
	u, _ := url.Parse(srv.URL)
	urls := catalogURLs{Gutenberg: srv.URL, Wikisource: srv.URL + "/%s", WSExport: srv.URL, Archive: srv.URL,
		OpenLibrary: srv.URL, GoogleBooks: srv.URL, Covers: srv.URL, OpenStax: srv.URL}
	return newCatalog(urls, []string{u.Hostname()}, true)
}

func TestCatalogSearch(t *testing.T) {
	srv := fakeSources(t, nil)
	c := testCatalog(srv)
	ctx := context.Background()

	p, err := c.search(ctx, "gutenberg", "quijote", "es", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != "2000" || p.Items[0].Title != "Don Quijote" || p.Items[0].Authors[0] != "Miguel de Cervantes Saavedra" || !p.Items[0].HasCover {
		t.Fatalf("Gutenberg (sin entradas de navegación ni idioma en el título): %+v", p.Items)
	}
	p, _ = c.search(ctx, "archive", "x", "es", 1)
	if len(p.Items) != 1 || p.Items[0].ID != "libre1" {
		t.Fatalf("Internet Archive solo con licencia libre: %+v", p.Items)
	}
	p, _ = c.search(ctx, "wikisource", "quijote", "es", 1)
	if len(p.Items) != 1 || p.Items[0].ID != "es:Don Quijote de la Mancha" {
		t.Fatalf("Wikisource sin subpáginas: %+v", p.Items)
	}
	p, err = c.search(ctx, "todas", "quijote", "es", 1)
	if err != nil || len(p.Items) != 3 || p.Items[0].Source != "gutenberg" || p.Items[1].Source != "archive" || p.Items[2].Source != "wikisource" {
		t.Fatalf("búsqueda en todas: %v %+v", err, p)
	}
	if _, err := c.search(ctx, "libgen", "x", "es", 1); err == nil {
		t.Fatal("acepta una fuente desconocida")
	}
	if _, err := c.search(ctx, "gutenberg", "x", "klingon", 1); err == nil {
		t.Fatal("acepta un idioma desconocido")
	}
}

func TestCatalogSafety(t *testing.T) {
	srv := fakeSources(t, nil)
	c := testCatalog(srv)
	ctx := context.Background()
	for _, id := range []string{"../../etc", "1 OR 1", ""} {
		if _, err := c.resolve(ctx, "gutenberg", id); err == nil {
			t.Errorf("acepta el id %q", id)
		}
	}
	if _, err := c.resolve(ctx, "gutenberg", "9"); err == nil {
		t.Error("deja importar un libro con copyright")
	}
	if _, err := c.resolve(ctx, "gutenberg", "10"); err == nil {
		t.Error("deja importar un audiolibro")
	}
	dl, err := c.resolve(ctx, "gutenberg", "2000")
	if err != nil || dl.url != srv.URL+"/ebooks/2000.epub3.images" || dl.cover != srv.URL+"/cover.jpg" || dl.meta.Subjects[0] != "Novels" ||
		dl.meta.Description != `"Don Quijote" es una novela & más.` || dl.meta.Downloads != 1234 || dl.meta.Language != "es" {
		t.Errorf("ficha de Gutenberg: %v %+v", err, dl)
	}
	// La página de bloqueo del operador (certificado que no es el de la web).
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	tu, _ := url.Parse(tlsSrv.URL)
	if _, err := newCatalog(catalogURLs{}, []string{tu.Hostname()}, true).get(ctx, tlsSrv.URL+"/x"); !errors.Is(err, errBlocked) || !errors.Is(err, errUpstream) {
		t.Errorf("certificado ajeno: %v", err)
	}
	if _, err := c.get(ctx, "https://intranet.example/x"); err == nil {
		t.Error("descarga de un dominio no permitido")
	}
	if _, err := c.get(ctx, srv.URL+"/evil"); err == nil {
		t.Error("sigue una redirección a un dominio no permitido")
	}
	// Sin el permiso de los tests, ni siquiera un dominio permitido puede
	// resolver a una dirección local.
	u, _ := url.Parse(srv.URL)
	strict := newCatalog(catalogURLs{Gutenberg: srv.URL}, []string{u.Hostname()}, false)
	if _, err := strict.get(ctx, srv.URL+"/ebooks/2000.opds"); err == nil {
		t.Error("el cliente estricto acepta HTTP o una dirección local")
	}
}

func TestCatalogImport(t *testing.T) {
	epub := zipBytes(t, map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/c.opf"/></rootfiles></container>`,
		"OEBPS/c.opf":            `<package><metadata><title>Título del EPUB</title><creator>Otro</creator><language>es</language></metadata><manifest></manifest></package>`,
	})
	srv := fakeSources(t, epub)
	h := newHarness(t)
	// Sustituye el catálogo del arnés por el que apunta a las fuentes falsas.
	pool := dbtest.Migrated(t, migrations)
	f, _ := newFiles(t.TempDir())
	a := &api{store: store{pool}, files: f, log: slog.New(slog.NewTextHandler(io.Discard, nil)), catalog: testCatalog(srv)}
	mux := http.NewServeMux()
	a.routes(mux)
	h.srv.Config.Handler = mux

	resp := h.do(t, "POST", "/api/catalog/import", map[string]any{"source": "gutenberg", "id": "2000", "visibility": "compartido"})
	out := decode[struct {
		Book      Book
		Duplicate bool
	}](t, resp)
	b := out.Book
	if resp.StatusCode != http.StatusCreated || b.Title != "Don Quijote" || b.Authors[0] != "Miguel de Cervantes Saavedra" ||
		b.Source == nil || *b.Source != "gutenberg:2000" || !b.HasCover || b.Visibility != "compartido" || b.Owner != "ana" {
		t.Fatalf("importación: %d %+v", resp.StatusCode, b)
	}
	// La búsqueda lo marca como «ya en tu biblioteca» y otra importación es un duplicado.
	page := decode[catalogPage](t, h.do(t, "GET", "/api/catalog/search?source=gutenberg&q=quijote&lang=es", nil))
	if len(page.Items) != 1 || page.Items[0].BookID != b.ID {
		t.Fatalf("marca de biblioteca: %+v", page.Items)
	}
	again := decode[struct{ Duplicate bool }](t, h.do(t, "POST", "/api/catalog/import", map[string]any{"source": "gutenberg", "id": "2000"}))
	if !again.Duplicate {
		t.Fatal("la segunda importación no es un duplicado")
	}
	if resp := h.do(t, "POST", "/api/catalog/import", map[string]any{"source": "gutenberg", "id": "9"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("importar con copyright: %d", resp.StatusCode)
	}
}

func TestReadEPUBMeta(t *testing.T) {
	dir := t.TempDir()
	f, _ := newFiles(dir)
	data := zipBytes(t, map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="x/p.opf"/></rootfiles></container>`,
		"x/p.opf": `<package><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>T</dc:title><dc:creator>A</dc:creator>
			<meta name="cover" content="img"/></metadata><manifest><item id="img" href="c.png" media-type="image/png"/></manifest></package>`,
		"x/c.png": string(pngPixel),
	})
	up, _ := f.receive(strings.NewReader(string(data)), maxBookBytes)
	defer up.discard()
	m := readEPUBMeta(up.tmp)
	if m.Title != "T" || len(m.Authors) != 1 || m.cover == nil {
		t.Fatalf("metadatos del EPUB: %+v", m)
	}
	if got := readEPUBMeta("no-existe"); got.Title != "" {
		t.Fatal("un fichero inexistente debería dar metadatos vacíos")
	}
}

func TestOpenStax(t *testing.T) {
	src := fakeSources(t, nil)
	c := testCatalog(src)
	ctx := context.Background()
	// En español solo los vigentes; sin idioma, los de todos; por palabras sin acentos.
	p, err := c.search(ctx, "openstax", "calculo", "es", 1)
	if err != nil || len(p.Items) != 1 || p.Items[0].ID != "578" || p.Items[0].Authors[0] != "Gilbert Strang" || p.Items[0].Description != "Libro de texto" || p.Items[0].Year != "2022" {
		t.Fatalf("búsqueda en español: %v %+v", err, p.Items)
	}
	if p, _ := c.search(ctx, "openstax", "", "", 1); len(p.Items) != 2 {
		t.Fatalf("sin idioma: %+v", p.Items)
	}
	if p, _ := c.search(ctx, "openstax", "fisica", "es", 1); len(p.Items) != 0 {
		t.Fatalf("aparece un libro retirado: %+v", p.Items)
	}
	if p, _ := c.search(ctx, "openstax", "", "fr", 1); len(p.Items) != 0 {
		t.Fatalf("en francés no hay libros de OpenStax: %+v", p.Items)
	}
	dl, err := c.resolve(ctx, "openstax", "578")
	if err != nil || !strings.HasSuffix(dl.url, "/openstax/578.pdf") || dl.fileName != "libro-578.pdf" || dl.meta.Title != "Cálculo volumen 1" {
		t.Fatalf("descarga: %v %+v", err, dl)
	}
	if _, err := c.resolve(ctx, "openstax", "579"); err == nil {
		t.Fatal("se descarga un libro retirado")
	}
	// Importarlo a la biblioteca.
	h := newHarness(t)
	h.api.catalog = c
	resp := h.do(t, "POST", "/api/catalog/import", map[string]any{"source": "openstax", "id": "578"})
	out := decode[struct{ Book Book }](t, resp)
	if resp.StatusCode != http.StatusCreated || out.Book.Format != "pdf" || out.Book.Title != "Cálculo volumen 1" || out.Book.Source == nil || *out.Book.Source != "openstax:578" {
		t.Fatalf("importar: %d %+v", resp.StatusCode, out.Book)
	}
}
