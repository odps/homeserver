package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // para medir portadas
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"homeserver/platform/app"
)

// Datos de los libros desde internet (portada, autores, sinopsis, año…): se
// buscan en Open Library y Google Books por ISBN o, si no hay, por título y
// autor. Por título solo vale un resultado que coincide de forma clara: mejor
// nada que los datos de otro libro.
//
//   - Cada libro que se añade se mejora solo: lo que falta se rellena y lo que
//     se encuentra mejor sustituye a lo que traía (sinopsis más larga, nombres
//     de autor completos, portada con más resolución, título si venía del
//     nombre del fichero). Ver mergeMeta.
//   - Los que ya había se revisan poco a poco; si alguien los editó a mano,
//     solo se rellena lo que falta.
//   - En «Editar datos» se puede buscar a mano y elegir entre varios resultados.

type metaCandidate struct {
	isbnInfo
	Source string `json:"source"`        // openlibrary o google
	Ref    string `json:"ref,omitempty"` // para pedir su portada al servidor
}

type coverRef struct {
	url string
	at  time.Time
}

// rememberCover guarda la URL de una portada encontrada y devuelve una
// referencia: el navegador pide la portada por referencia, nunca por URL.
func (c *catalog) rememberCover(u string) string {
	sum := sha256.Sum256([]byte(u))
	ref := hex.EncodeToString(sum[:8])
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.coverRefs == nil || len(c.coverRefs) > 500 {
		c.coverRefs = map[string]coverRef{}
	}
	c.coverRefs[ref] = coverRef{url: u, at: time.Now()}
	return ref
}

func (c *catalog) coverByRef(ref string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.coverRefs[ref]
	return e.url, ok && time.Since(e.at) < time.Hour
}

// lang2 pasa los códigos de idioma de Open Library (spa, eng…) a los de dos
// letras que usan los EPUB y Google Books.
func lang2(code string) string {
	for two, three := range languages {
		if three == code || (code == "fre" && two == "fr") || (code == "fra" && two == "fr") || (code == "ger" && two == "de") || (code == "deu" && two == "de") {
			return two
		}
	}
	return ""
}

// searchMeta busca por título (y autor, si hay) en las dos fuentes a la vez.
func (c *catalog) searchMeta(ctx context.Context, title, author string) ([]metaCandidate, error) {
	title, author = clean(title, 200), clean(author, 120)
	if title == "" {
		return nil, bad("falta el título")
	}
	var ol struct {
		Docs []struct {
			Title     string   `json:"title"`
			Subtitle  string   `json:"subtitle"`
			Authors   []string `json:"author_name"`
			Year      int      `json:"first_publish_year"`
			ISBN      []string `json:"isbn"`
			CoverID   int      `json:"cover_i"`
			Subjects  []string `json:"subject"`
			Languages []string `json:"language"`
			Publisher []string `json:"publisher"`
		} `json:"docs"`
	}
	var gb googleVolumes
	var olErr, gbErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		q := url.Values{"title": {title}, "limit": {"5"}, "fields": {"title,subtitle,author_name,first_publish_year,isbn,cover_i,subject,language,publisher"}}
		if author != "" {
			q.Set("author", author)
		}
		olErr = c.getJSON(ctx, c.urls.OpenLibrary+"/search.json?"+q.Encode(), &ol)
	}()
	go func() {
		defer wg.Done()
		q := `intitle:"` + strings.ReplaceAll(title, `"`, "") + `"`
		if author != "" {
			q += ` inauthor:"` + strings.ReplaceAll(author, `"`, "") + `"`
		}
		gbErr = c.getJSON(ctx, c.urls.GoogleBooks+"/books/v1/volumes?"+url.Values{"q": {q}, "maxResults": {"5"}}.Encode(), &gb)
	}()
	wg.Wait()
	if olErr != nil && gbErr != nil {
		return nil, olErr
	}
	var out []metaCandidate
	for _, d := range ol.Docs {
		m := metaCandidate{Source: "openlibrary", isbnInfo: isbnInfo{Title: d.Title, Authors: d.Authors}}
		if d.Subtitle != "" {
			m.Title += ": " + d.Subtitle
		}
		if d.Year > 0 {
			m.Published = fmt.Sprint(d.Year)
		}
		for _, s := range d.ISBN {
			if isbn, ok := normalizeISBN(s); ok && len(s) == 13 {
				m.ISBN = isbn
				break
			}
		}
		if len(d.Publisher) > 0 {
			m.Publisher = d.Publisher[0]
		}
		if len(d.Languages) > 0 {
			m.Language = lang2(d.Languages[0])
		}
		for _, s := range d.Subjects {
			if len(m.Genres) < 5 {
				m.Genres = append(m.Genres, s)
			}
		}
		if d.CoverID > 0 {
			m.coverURL = fmt.Sprintf("%s/b/id/%d-L.jpg", c.urls.Covers, d.CoverID)
		}
		out = append(out, m)
	}
	for _, it := range gb.Items {
		v := it.VolumeInfo
		m := metaCandidate{Source: "google", isbnInfo: isbnInfo{Title: v.Title, Authors: v.Authors, Publisher: v.Publisher,
			Published: yearOf(v.PublishedDate), Genres: v.Categories, Language: v.Language, Description: stripTags(v.Description)}}
		if v.Subtitle != "" {
			m.Title += ": " + v.Subtitle
		}
		for _, id := range v.IndustryIdentifiers {
			if isbn, ok := normalizeISBN(id.Identifier); ok && id.Type == "ISBN_13" {
				m.ISBN = isbn
			}
		}
		if v.ImageLinks.Thumbnail != "" {
			m.coverURL = strings.Replace(v.ImageLinks.Thumbnail, "http://", "https://", 1)
		}
		out = append(out, m)
	}
	for i := range out {
		if out[i].Authors == nil {
			out[i].Authors = []string{}
		}
		if out[i].coverURL != "" {
			out[i].HasCover = true
			out[i].Ref = c.rememberCover(out[i].coverURL)
		}
	}
	return out, nil
}

// Palabras que no cuentan al comparar títulos.
var titleStopwords = map[string]bool{"el": true, "la": true, "los": true, "las": true, "un": true, "una": true, "de": true, "del": true,
	"y": true, "the": true, "a": true, "an": true, "of": true, "and": true, "le": true, "les": true, "il": true, "lo": true}

func titleWords(t string) map[string]bool {
	if i := strings.IndexAny(t, ":(["); i > 0 {
		t = t[:i] // sin subtítulo
	}
	out := map[string]bool{}
	for _, w := range searchWords(t) {
		if !titleStopwords[w] {
			out[w] = true
		}
	}
	return out
}

func subsetOf(a, b map[string]bool) bool {
	for w := range a {
		if !b[w] {
			return false
		}
	}
	return true
}

// confident dice si un resultado es claramente el mismo libro: el título
// coincide (uno contiene al otro, sin subtítulos) y, si el libro tiene
// autores, comparten algún apellido. Sin autores, el título debe ser igual y
// de al menos tres palabras.
func confident(title string, authors []string, c isbnInfo) bool {
	bt, ct := titleWords(title), titleWords(c.Title)
	if len(bt) == 0 || len(ct) == 0 || !(subsetOf(bt, ct) || subsetOf(ct, bt)) {
		return false
	}
	if len(authors) == 0 {
		// Sin autor, un título corto es demasiado ambiguo («Competitive
		// Programming» lo tienen varios libros): hace falta uno largo e igual.
		return len(bt) >= 3 && subsetOf(bt, ct) && subsetOf(ct, bt)
	}
	mine := map[string]bool{}
	for _, w := range searchWords(strings.Join(authors, " ")) {
		if len([]rune(w)) >= 3 {
			mine[w] = true
		}
	}
	for _, w := range searchWords(strings.Join(c.Authors, " ")) {
		if mine[w] {
			return true
		}
	}
	return false
}

// metaGap es lo que tiene un libro de lo que se puede mejorar.
type metaGap struct {
	ID          int64
	SHA256      string
	FileName    string
	AddedAt     time.Time
	UpdatedAt   time.Time
	Title       string
	Authors     []string
	Genres      []string
	ISBN        *string
	HasCover    bool
	Description *string
	Published   *string
	Publisher   *string
	Language    *string
}

// edited: alguien cambió los datos a mano después de añadirlo (la portada o
// los datos actualizan updated_at; la lectura no).
func (g metaGap) edited() bool { return g.UpdatedAt.Sub(g.AddedAt) > time.Minute }

// fromFileName: el título salió del nombre del fichero, no del libro.
func (g metaGap) fromFileName() bool {
	base := strings.TrimSuffix(g.FileName, filepath.Ext(g.FileName))
	return g.Title == "Sin título" || strings.EqualFold(clean(base, 500), g.Title)
}

// metaUpdate son los datos que quedan tras mezclar los del libro con los
// encontrados.
type metaUpdate struct {
	Title       string
	Authors     []string
	Genres      []string
	ISBN        *string
	Description *string
	Published   *string
	Publisher   *string
	Language    *string
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// mergeMeta decide campo a campo. Con upgrade=false (libro editado a mano)
// solo se rellena lo vacío.
func mergeMeta(g metaGap, f isbnInfo, upgrade bool) metaUpdate {
	u := metaUpdate{Title: g.Title, Authors: g.Authors, Genres: g.Genres, ISBN: g.ISBN, Description: g.Description,
		Published: g.Published, Publisher: g.Publisher, Language: g.Language}
	found := metaUpdate{Title: clean(f.Title, 500), Authors: cleanList(f.Authors, 20, 200), Genres: cleanList(f.Genres, 30, 100),
		Description: optional(f.Description, 20000), Published: optional(f.Published, 40), Publisher: optional(f.Publisher, 300),
		Language: optional(f.Language, 35)}
	if v, ok := normalizeISBN(f.ISBN); ok {
		found.ISBN = &v
	}
	fill := func(cur **string, v *string) {
		if *cur == nil && v != nil {
			*cur = v
		}
	}
	fill(&u.ISBN, found.ISBN)
	fill(&u.Publisher, found.Publisher)
	fill(&u.Language, found.Language)
	fill(&u.Description, found.Description)
	fill(&u.Published, found.Published)
	if len(u.Authors) == 0 {
		u.Authors = found.Authors
	}
	if len(u.Genres) == 0 {
		u.Genres = found.Genres
	}
	if !upgrade {
		return u
	}
	if found.Title != "" && g.fromFileName() {
		u.Title = found.Title
	}
	// Sinopsis: la más completa, si está en el mismo idioma.
	sameLang := g.Language == nil || found.Language == nil || strings.EqualFold(lang2or(*g.Language), lang2or(*found.Language))
	if sameLang && found.Description != nil && len([]rune(*found.Description)) > len([]rune(deref(u.Description)))*6/5 {
		u.Description = found.Description
	}
	// Año: si lo que hay no es un año (una fecha completa, «s. f.»…).
	if found.Published != nil && yearOf(deref(u.Published)) != deref(u.Published) {
		if y := yearOf(deref(u.Published)); y != "" && y == *found.Published || y == "" {
			u.Published = found.Published
		}
	}
	// Autores: los nombres completos si son los mismos autores («Cervantes»
	// → «Miguel de Cervantes Saavedra»).
	if len(found.Authors) > 0 && len(found.Authors) >= len(u.Authors) {
		mine, theirs := map[string]bool{}, map[string]bool{}
		for _, w := range searchWords(strings.Join(u.Authors, " ")) {
			mine[w] = true
		}
		for _, w := range searchWords(strings.Join(found.Authors, " ")) {
			theirs[w] = true
		}
		if subsetOf(mine, theirs) && len(theirs) > len(mine) {
			u.Authors = found.Authors
		}
	}
	// Géneros: si apenas hay.
	if len(u.Genres) < 2 && len(found.Genres) > len(u.Genres) {
		u.Genres = found.Genres
	}
	return u
}

// lang2or deja «es-ES», «spa» o «es» en «es».
func lang2or(code string) string {
	code = strings.ToLower(code)
	if l := lang2(code); l != "" {
		return l
	}
	if i := strings.IndexAny(code, "-_"); i > 0 {
		return code[:i]
	}
	return code
}

// coverPixels mide una portada (0 si no se reconoce el formato).
func coverPixels(data []byte) int {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0
	}
	return cfg.Width * cfg.Height
}

// findMeta busca los datos del libro: por ISBN si lo tiene; si no, el primer
// resultado por título y autor que coincida claramente. ok=false: no hay.
func (a *api) findMeta(ctx context.Context, g metaGap) (isbnInfo, bool, error) {
	if g.ISBN != nil {
		info, err := a.catalog.lookupISBN(ctx, *g.ISBN)
		if errors.Is(err, errNotFound) {
			return info, false, nil
		}
		return info, err == nil, err
	}
	author := ""
	if len(g.Authors) > 0 {
		author = g.Authors[0]
	}
	cands, err := a.catalog.searchMeta(ctx, g.Title, author)
	if err != nil {
		return isbnInfo{}, false, err
	}
	// La primera coincidencia clara; lo que le falte se completa con otras
	// del mismo libro (Open Library no trae sinopsis, Google Books sí).
	var best isbnInfo
	found := false
	for _, c := range cands {
		if !confident(g.Title, g.Authors, c.isbnInfo) {
			continue
		}
		if !found {
			best, found = c.isbnInfo, true
			continue
		}
		if best.ISBN == "" {
			best.ISBN = c.ISBN
		}
		if best.Description == "" {
			best.Description = c.Description
		}
		if best.Published == "" {
			best.Published = c.Published
		}
		if best.Publisher == "" {
			best.Publisher = c.Publisher
		}
		if best.Language == "" {
			best.Language = c.Language
		}
		if best.coverURL == "" {
			best.coverURL = c.coverURL
		}
	}
	return best, found, nil
}

// autofill mejora los datos de un libro. Un fallo de red no lo marca como
// revisado: se reintentará en la siguiente vuelta.
func (a *api) autofill(ctx context.Context, g metaGap) error {
	info, ok, err := a.findMeta(ctx, g)
	if err != nil {
		return err
	}
	if !ok {
		return a.store.markMetaChecked(ctx, g.ID)
	}
	upgrade := !g.edited()
	saved, err := a.store.saveMeta(ctx, g, mergeMeta(g, info, upgrade))
	if err != nil || !saved {
		return err // !saved: alguien lo editó mientras se buscaba; manda lo suyo
	}
	if info.coverURL != "" && (!g.HasCover || upgrade) {
		// Las «sin portada» de las fuentes son imágenes diminutas.
		if data, _, err := a.catalog.fetchImage(ctx, info.coverURL); err == nil && len(data) > 1000 {
			b := Book{SHA256: g.SHA256}
			replace := !g.HasCover
			if !replace {
				cur, _ := os.ReadFile(a.files.coverPath(b))
				now, found := coverPixels(cur), coverPixels(data)
				replace = now > 0 && found > now*3/2 // bastante más resolución
			}
			if replace && a.files.saveCover(b, bytes.NewReader(data)) == nil {
				a.store.coverChanged(ctx, g.SHA256)
			}
		}
	}
	a.log.Info("datos del libro mejorados", "id", g.ID, "titulo", g.Title, "solo_huecos", !upgrade)
	return nil
}

// queueMeta pide completar un libro recién añadido (sin esperar).
func (a *api) queueMeta(id int64) {
	if a.metaQueue == nil {
		return
	}
	select {
	case a.metaQueue <- id:
	default: // cola llena: ya lo recogerá la vuelta periódica
	}
}

// metaWorker atiende los libros recién añadidos y, cada media hora, unos
// cuantos de los que ya había, despacio para no abusar de las fuentes.
func (a *api) metaWorker(ctx context.Context) {
	tick := time.NewTimer(time.Minute)
	defer tick.Stop()
	run := func(g metaGap) {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := a.autofill(cctx, g); err != nil {
			a.log.Warn("completar datos", "id", g.ID, "err", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-a.metaQueue:
			if g, err := a.store.metaGap(ctx, id); err == nil {
				run(g)
			}
		case <-tick.C:
			gaps, err := a.store.needingMeta(ctx, 20)
			if err != nil {
				a.log.Warn("libros sin datos", "err", err)
			}
			for _, g := range gaps {
				run(g)
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
			tick.Reset(30 * time.Minute)
		}
	}
}

// metaSearch es la búsqueda manual de «Editar datos».
func (a *api) metaSearch(w http.ResponseWriter, r *http.Request, _ app.Identity) {
	q := r.URL.Query()
	cands, err := a.catalog.searchMeta(r.Context(), q.Get("title"), q.Get("author"))
	if err != nil {
		a.catalogFail(w, err)
		return
	}
	if cands == nil {
		cands = []metaCandidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cands})
}

// metaCover enseña la portada de un resultado (a través del servidor: la CSP
// de la app no carga imágenes de fuera).
func (a *api) metaCover(w http.ResponseWriter, r *http.Request, _ app.Identity) {
	u, ok := a.catalog.coverByRef(r.URL.Query().Get("ref"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, ct, err := a.catalog.fetchImage(r.Context(), u)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}
