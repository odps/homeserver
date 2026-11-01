package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"

	"homeserver/platform/app"
)

func (a *api) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/catalog/search", a.user(a.catalogSearch))
	mux.HandleFunc("GET /api/catalog/cover", a.user(a.catalogCover))
	mux.HandleFunc("POST /api/catalog/import", a.user(a.catalogImport))
}

func (a *api) catalogFail(w http.ResponseWriter, err error) {
	if errors.Is(err, errUpstream) {
		a.log.Warn("catálogo", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": errUpstream.Error()})
		return
	}
	a.fail(w, err)
}

func (a *api) catalogSearch(w http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	p, err := a.catalog.search(r.Context(), q.Get("source"), q.Get("q"), q.Get("lang"), page)
	if err != nil {
		a.catalogFail(w, err)
		return
	}
	// Marca los que la persona ya tiene (o ve compartidos) en la biblioteca.
	keys := make([]string, len(p.Items))
	for i, it := range p.Items {
		keys[i] = it.Source + ":" + it.ID
	}
	have, err := a.store.idsBySource(r.Context(), who, keys)
	if err != nil {
		a.fail(w, err)
		return
	}
	items := make([]catalogItem, len(p.Items))
	for i, it := range p.Items {
		it.BookID = have[keys[i]]
		items[i] = it
	}
	writeJSON(w, http.StatusOK, catalogPage{Items: items, More: p.More})
}

// catalogCover sirve la portada a través del servidor: la CSP de la app no
// carga imágenes de fuera y así la fuente no ve la IP de quien navega.
func (a *api) catalogCover(w http.ResponseWriter, r *http.Request, _ app.Identity) {
	u, err := a.catalog.coverURL(r.URL.Query().Get("source"), r.URL.Query().Get("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, ct, err := a.catalog.fetchImage(r.Context(), u)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(data)
}

// catalogImport descarga un libro de una fuente pública directamente a la
// biblioteca de la persona (no pasa por su dispositivo).
func (a *api) catalogImport(w http.ResponseWriter, r *http.Request, who app.Identity) {
	var in struct {
		Source     string `json:"source"`
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		a.fail(w, bad("datos no válidos"))
		return
	}
	b, duplicate, err := a.importBook(r.Context(), who, in.Source, in.ID, in.Visibility)
	if err != nil {
		a.catalogFail(w, err)
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"book": b, "duplicate": duplicate})
}

// importBook trae un libro del catálogo a la biblioteca (también lo usa la
// tool MCP). Si ya estaba, devuelve el que había.
func (a *api) importBook(ctx context.Context, who app.Identity, source, id, visibility string) (Book, bool, error) {
	dl, err := a.catalog.resolve(ctx, source, id)
	if err != nil {
		return Book{}, false, err
	}
	resp, err := a.catalog.get(ctx, dl.url)
	if err != nil {
		return Book{}, false, err
	}
	up, err := a.files.receive(resp.Body, maxBookBytes)
	resp.Body.Close()
	if err != nil {
		return Book{}, false, bad("%s", err)
	}
	defer func() {
		if up != nil {
			up.discard()
		}
	}()
	if existing, err := a.store.findCopy(ctx, who, up.sha256); err == nil {
		return existing, true, nil
	} else if !errors.Is(err, errNotFound) {
		return Book{}, false, err
	}
	format, err := detectFormat(up, dl.fileName)
	if err != nil {
		return Book{}, false, bad("la fuente no ha devuelto un libro válido: %s", err)
	}
	if !visibilities[visibility] {
		visibility = "privado"
	}
	src := source + ":" + id
	b := Book{SHA256: up.sha256, Format: format, FileName: clean(dl.fileName, 300), Size: up.size, Owner: who.User, Visibility: visibility, Source: &src}

	// Metadatos: los de la fuente y, donde falten, los del propio EPUB.
	m := dl.meta
	var em epubMeta
	if format == "epub" {
		em = readEPUBMeta(up.tmp)
	}
	meta := metaIn{Title: m.Title, Authors: m.Authors, Genres: m.Subjects, Language: m.Language, Published: m.Year, Description: m.Description, ISBN: em.ISBN}
	if len(meta.Authors) == 0 {
		meta.Authors = em.Authors
	}
	if meta.Title == "" || source == "wikisource" && em.Title != "" {
		meta.Title = em.Title
	}
	if meta.Language == "" {
		meta.Language = em.Language
	}
	if len(meta.Genres) == 0 {
		meta.Genres = em.Subjects
	}
	if meta.Description == "" {
		meta.Description = stripTags(em.Description)
	}
	meta.apply(&b)
	if b.Title == "" {
		b.Title = "Sin título"
	}
	if err := a.files.commit(up, b); err != nil {
		return Book{}, false, err
	}
	up = nil

	// Portada: la del EPUB o, si no trae, la de la fuente.
	cover := em.cover
	if cover == nil && dl.cover != "" {
		cover, _, _ = a.catalog.fetchImage(ctx, dl.cover)
	}
	if cover != nil && a.files.saveCover(b, bytes.NewReader(cover)) == nil {
		b.HasCover = true
	} else if a.files.hasCover(b) {
		b.HasCover = true
	}
	newID, err := a.store.insert(ctx, b)
	if err != nil {
		a.removeFileIfUnused(ctx, b)
		return Book{}, false, err
	}
	saved, err := a.store.get(ctx, who, newID)
	if err != nil {
		return Book{}, false, err
	}
	a.log.Info("libro importado", "id", newID, "usuario", who.User, "fuente", src, "titulo", b.Title, "bytes", b.Size)
	return saved, false, nil
}

// stripTags quita etiquetas HTML sencillas de las descripciones de los EPUB.
func stripTags(s string) string {
	var out strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
			out.WriteRune(' ')
		case !in:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(html.UnescapeString(out.String()))
}
