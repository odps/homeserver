package main

import (
	"bytes"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// sampleArticles son artículos de ejemplo (genéricos: el repositorio es
// público) para ver las páginas sin base de datos.
func sampleArticles() []article {
	now := testNow()
	mk := func(id int64, title, site string, min int, ago time.Duration, tags ...string) article {
		return article{ID: id, State: stateOK, Created: now.Add(-ago), meta: meta{URL: "https://" + strings.ToLower(strings.ReplaceAll(site, " ", "")) + ".ejemplo.es/articulo",
			Title: title, Site: site, Minutes: min, Tags: tags,
			Excerpt: "Un extracto de ejemplo que cuenta de qué va el artículo en dos o tres líneas, para decidir si leerlo ahora o más tarde."}}
	}
	list := []article{
		mk(1, "Cómo cultivar tomates en el balcón", "Huerto de Ejemplo", 4, time.Hour, "huerto", "verano"),
		mk(2, "Pan de masa madre en casa: guía larga con un título que ocupa varias líneas en el móvil", "Cocina de Ejemplo", 12, 30*time.Hour, "cocina"),
		mk(3, "Rutas en bicicleta por la sierra", "Rutas de Ejemplo", 25, 9*24*time.Hour),
		{ID: 4, State: stateError, Created: now.Add(-2 * time.Hour), meta: meta{URL: "https://noticias.ejemplo.es/suscriptores/123", Title: "Solo para suscriptores", Err: errNoText.msg}},
		{ID: 5, State: stateQueued, Created: now.Add(-time.Minute), meta: meta{URL: "https://blog.ejemplo.es/entrada"}},
	}
	list[1].Read, list[1].Favorite = true, true
	return list
}

func sampleBlocks(t *testing.T) []block {
	u, _ := url.Parse("https://huerto.ejemplo.es/articulo/tomates")
	ex, err := extract(&page{URL: u, ContentType: "text/html", Body: []byte(articleHTML("Cómo cultivar tomates en el balcón", "Huerto de Ejemplo", "Ana Ejemplo", "los tomates del balcón", 7))}, testNow())
	if err != nil {
		t.Fatal(err)
	}
	return ex.Blocks
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con LEER_PREVIEW_DIR escribe las páginas y lo
// estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	c := &clock{t: testNow()}
	w := &web{svc: &service{log: slog.New(slog.DiscardHandler), now: c.now}}
	arts := sampleArticles()
	rows := func(list ...article) []row {
		var out []row
		for _, a := range list {
			out = append(out, w.rowOf(a, c.now()))
		}
		return out
	}
	chips := []tagChip{{Name: "huerto", N: 3}, {Name: "cocina", N: 2}, {Name: "verano", N: 1}}
	cts := counts{Pending: 5, Unread: 4, Favorite: 1, Archive: 7}
	pending := pageData{Name: "Ana", Tab: listPending, List: listPending, Heading: "Pendientes", Lead: "4 por leer", Rows: rows(arts...), Total: 5, Counts: cts, Tags: chips}
	formErr := pending
	formErr.Error, formErr.FormURL, formErr.FormTags = errBlocked.msg, "https://192.168.1.1/", "casa"
	many := pending
	many.Total = 340
	search := pageData{Name: "Ana", Tab: listPending, List: listAll, Heading: "Resultados", Lead: "1 artículo encontrado", Query: "tomates", Searched: true,
		Rows: rows(arts[0]), Total: 1, Counts: cts, Tags: chips}
	byTag := search
	byTag.Query, byTag.Tag = "", "huerto"
	byTag.Tags = []tagChip{{Name: "huerto", N: 3, On: true}, {Name: "cocina", N: 2}}
	noResults := search
	noResults.Rows, noResults.Total, noResults.Lead, noResults.Query = nil, 0, "0 artículos encontrados", "nada"
	fav := pageData{Name: "Ana", Tab: listFavorite, List: listFavorite, Title: "Favoritos", Heading: "Favoritos", Lead: "1 artículo", Rows: rows(arts[1]), Total: 1, Counts: cts}
	archived := arts[0]
	archived.Archived, archived.Read = true, true
	archive := pageData{Name: "Ana", Tab: listArchive, List: listArchive, Title: "Archivo", Heading: "Archivo", Lead: "1 artículo", Rows: rows(archived), Total: 1, Counts: cts}

	blocks := sampleBlocks(t)
	reader := func(a article, b []block, size string, confirm bool, msg string) pageData {
		v := w.artViewOf(a, b)
		tab := listPending
		if a.Archived {
			tab = listArchive
		}
		return pageData{Name: "Ana", Tab: tab, Title: v.Title, Art: v, Size: size, Sizes: sizeOptions(size), Confirm: confirm, Error: msg}
	}
	full := arts[0]
	full.Author, full.Published = "Ana Ejemplo", "2026-10-20"
	full.Lang = "es-ES"
	big := arts[1]
	big.Archived, big.Truncated, big.Lang = true, true, `en" onclick="x`

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"pendientes", "lista.html", pending},
		{"pendientes-error", "lista.html", formErr},
		{"pendientes-muchos", "lista.html", many},
		{"pendientes-vacio", "lista.html", pageData{Name: "Ana", Tab: listPending, List: listPending, Heading: "Pendientes", Lead: "0 por leer"}},
		{"pendientes-todo-archivado", "lista.html", pageData{Name: "Ana", Tab: listPending, List: listPending, Heading: "Pendientes", Lead: "0 por leer", Counts: counts{Archive: 3}}},
		{"busqueda", "lista.html", search},
		{"busqueda-etiqueta", "lista.html", byTag},
		{"busqueda-vacia", "lista.html", noResults},
		{"favoritos", "lista.html", fav},
		{"favoritos-vacio", "lista.html", pageData{Name: "Ana", Tab: listFavorite, List: listFavorite, Heading: "Favoritos", Lead: "0 artículos"}},
		{"archivo", "lista.html", archive},
		{"archivo-vacio", "lista.html", pageData{Name: "Ana", Tab: listArchive, List: listArchive, Heading: "Archivo", Lead: "0 artículos"}},
		{"articulo", "articulo.html", reader(full, blocks, "m", false, "")},
		{"articulo-grande", "articulo.html", reader(big, blocks, "xl", false, "")},
		{"articulo-error", "articulo.html", reader(arts[3], nil, "m", false, "")},
		{"articulo-cola", "articulo.html", reader(arts[4], nil, "s", false, "")},
		{"articulo-borrar", "articulo.html", reader(full, blocks[:2], "l", true, "")},
		{"articulo-etiquetas-mal", "articulo.html", reader(full, blocks[:2], "m", false, "Como mucho 10 etiquetas por artículo.")},
		{"ajustes", "ajustes.html", pageData{Name: "Ana", Tab: "ajustes", Title: "Ajustes", Prefs: defaultPrefs(), Sizes: sizeOptions("m"), Flash: "Ajustes guardados."}},
	}
	wants := map[string][]string{
		"pendientes": {"4 por leer", `action="/leer/guardar"`, `name="url"`, `action="/leer/buscar"`, `name="etiqueta" value="huerto"`,
			`href="/leer/articulo/1"`, "Huerto de Ejemplo · 4 min · hoy", "· 12 min · ayer", "30 oct", "No se ha podido leer la página", "Descargando…", "blog.ejemplo.es",
			`class="row read"`, `<span class="tag">huerto</span>`, `name="campo" value="archivado"`, `name="volver" value="pendientes"`, "Archivar", `aria-pressed="true"`},
		"pendientes-error":          {"Esa dirección es de la red local", `value="https://192.168.1.1/"`, `value="casa"`},
		"pendientes-muchos":         {"Se ven los 5 más recientes de 340"},
		"pendientes-vacio":          {"Aún no has guardado nada", `action="/leer/guardar"`},
		"pendientes-todo-archivado": {"Nada pendiente."},
		"busqueda":                  {"Resultados", "«tomates»", "1 artículo encontrado en todo lo guardado", `value="tomates"`, "Quitar la búsqueda"},
		"busqueda-etiqueta":         {"etiqueta «huerto»", `class="chip on"`},
		"busqueda-vacia":            {"No ha salido nada"},
		"favoritos":                 {"Favoritos", "★", `name="volver" value="favoritos"`},
		"favoritos-vacio":           {"Sin favoritos"},
		"archivo":                   {"Devolver", "archivado", `name="volver" value="archivo"`},
		"archivo-vacio":             {"El archivo está vacío"},
		"articulo": {"Cómo cultivar tomates en el balcón", "Huerto de Ejemplo", "Ana Ejemplo", "20 oct 2026", "4 min de lectura", "Marcar leído",
			`href="https://huertodeejemplo.ejemplo.es/articulo" rel="noopener noreferrer nofollow"`, "<h2>Lo básico de los tomates del balcón</h2>",
			"<blockquote>Lo que bien empieza\nbien acaba.</blockquote>", "<pre>paso 1:  regar", "&lt;script&gt;alert(&#34;escapado&#34;)&lt;/script&gt;",
			"Terminado: archivar", `action="/leer/articulo/1/etiquetas"`, `value="huerto, verano"`, `action="/leer/tamano"`, `class="tab-pendientes size-m"`,
			`href="/leer/articulo/1?borrar=1#borrar"`, `href="/leer/"`, `<div class="text" lang="es-ES">`},
		"articulo-grande":        {`class="tab-archivo size-xl"`, `<div class="text" lang="es">`, "Devolver a pendientes", "muy largo", `href="/leer/archivo"`, "leído", `aria-pressed="true" aria-label="Muy grande"`},
		"articulo-error":         {"No se ha podido leer la página.", "suscripción", `action="/leer/articulo/4/reintentar"`, "noticias.ejemplo.es"},
		"articulo-cola":          {"Aún se está descargando", "Descargar ahora", "blog.ejemplo.es"},
		"articulo-borrar":        {"¿Borrar este artículo?", `name="confirmar" value="1"`, `action="/leer/articulo/1/borrar"`},
		"articulo-etiquetas-mal": {"Como mucho 10 etiquetas", "<details class=\"panel fold\" open>"},
		"ajustes":                {`name="tamano" value="m" checked`, `name="resumen" value="1" checked`, `name="semanal" value="1">`, "Con Talia", "Ajustes guardados."},
	}
	nots := map[string][]string{
		"articulo":         {"Reintentar", "¿Borrar este artículo?", "Devolver a pendientes"},
		"articulo-error":   {`class="text"`},
		"busqueda":         {`action="/leer/guardar"`},
		"favoritos":        {`action="/leer/guardar"`},
		"pendientes-vacio": {`action="/leer/buscar"`},
	}

	dir := os.Getenv("LEER_PREVIEW_DIR")
	base := "/leer"
	if dir != "" {
		base = "."
		os.MkdirAll(dir, 0o755)
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base = "v0.0.0-test", base
			if d.Size == "" {
				d.Size = "m"
			}
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script", "<img", "<iframe", "javascript:", "onerror"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s contiene %q", p.file, bad)
				}
			}
			if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
				t.Errorf("%s tiene un manejador de eventos", p.file)
			}
			if !strings.Contains(out, `aria-current="page"`) {
				t.Errorf("%s: falta la pestaña activa", p.file)
			}
			if dir == "" {
				for _, want := range wants[p.file] {
					if !strings.Contains(out, want) {
						t.Errorf("%s: falta %q", p.file, want)
					}
				}
				for _, not := range nots[p.file] {
					if strings.Contains(out, not) {
						t.Errorf("%s: sobra %q", p.file, not)
					}
				}
			} else if err := os.WriteFile(filepath.Join(dir, p.file+".html"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
	if dir != "" {
		static, _ := fs.Sub(webFS, "web/static")
		os.MkdirAll(filepath.Join(dir, "static"), 0o755)
		for _, n := range []string{"app.css", "icon.svg"} {
			b, _ := fs.ReadFile(static, n)
			os.WriteFile(filepath.Join(dir, "static", n), b, 0o644)
		}
	}
}

func TestDates(t *testing.T) {
	now := testNow()
	for in, want := range map[time.Time]string{
		now.Add(-time.Hour):          "hoy",
		now.Add(-11 * time.Hour):     "ayer",
		now.Add(-9 * 24 * time.Hour): "30 oct",
		now.AddDate(-1, 0, 0):        "8 nov 2025",
	} {
		if got := savedES(in, now); got != want {
			t.Errorf("savedES(%v) = %q; quería %q", in, got, want)
		}
	}
	if publishedES("2026-09-05") != "5 sept 2026" || publishedES("") != "" {
		t.Error("fecha de publicación")
	}
	for in, want := range map[string]string{"pendientes": "/", "favoritos": "/favoritos", "archivo": "/archivo", "articulo": "/articulo/7",
		"https://otro.ejemplo.es/": "/", "//otro.ejemplo.es": "/"} {
		if got := backTo(in, 7); got != want {
			t.Errorf("backTo(%q) = %q", in, got)
		}
	}
}
