package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Catálogo de bibliotecas públicas legales: Project Gutenberg (catálogo OPDS),
// Wikisource (exportado a EPUB con ws-export) e Internet Archive (solo con
// licencia explícita de dominio público o Creative Commons). El servidor
// busca y descarga en nombre de la app; solo puede hablar con estos dominios.

type catalogItem struct {
	Source      string   `json:"source"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Language    string   `json:"language,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	Year        string   `json:"year,omitempty"`
	Downloads   int      `json:"downloads,omitempty"`
	HasCover    bool     `json:"has_cover"`
	Description string   `json:"description,omitempty"`
	BookID      int64    `json:"book_id,omitempty"` // ya está en la biblioteca de quien busca
}

type catalogPage struct {
	Items  []catalogItem `json:"items"`
	More   bool          `json:"more"`
	Failed []string      `json:"failed,omitempty"` // fuentes que no respondieron (búsqueda en todas)
}

// catalogURLs son las direcciones de cada fuente (sustituibles en los tests).
type catalogURLs struct {
	Gutenberg  string // https://www.gutenberg.org
	Wikisource string // https://%s.wikisource.org (con el idioma)
	WSExport   string // https://ws-export.wmcloud.org
	Archive    string // https://archive.org
	// Datos de libros por ISBN.
	OpenLibrary string // https://openlibrary.org
	GoogleBooks string // https://www.googleapis.com
	Covers      string // https://covers.openlibrary.org
}

var defaultCatalogURLs = catalogURLs{
	Gutenberg:   "https://www.gutenberg.org",
	Wikisource:  "https://%s.wikisource.org",
	WSExport:    "https://ws-export.wmcloud.org",
	Archive:     "https://archive.org",
	OpenLibrary: "https://openlibrary.org",
	GoogleBooks: "https://www.googleapis.com",
	Covers:      "https://covers.openlibrary.org",
}

// Dominios a los que puede conectarse el catálogo (y sus subdominios, para
// las redirecciones de descarga como ia800….us.archive.org).
var catalogHosts = []string{"gutenberg.org", "wikisource.org", "ws-export.wmcloud.org", "archive.org",
	"openlibrary.org", "googleapis.com", "books.google.com"}

type catalog struct {
	urls         catalogURLs
	client       *http.Client
	hosts        []string
	allowPrivate bool // solo en los tests: servidores locales por HTTP

	mu        sync.Mutex
	cache     map[string]cacheEntry
	isbns     map[string]isbnEntry
	coverRefs map[string]coverRef // portadas de las búsquedas de datos
}

type cacheEntry struct {
	at   time.Time
	page catalogPage
}

const userAgent = "homeserver-libros/1.0 (biblioteca personal; +https://github.com/odps/homeserver)"

func newCatalog(urls catalogURLs, hosts []string, allowPrivate bool) *catalog {
	c := &catalog{urls: urls, hosts: hosts, allowPrivate: allowPrivate, cache: map[string]cacheEntry{}, isbns: map[string]isbnEntry{}}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		// Defensa extra: aunque un dominio permitido resolviera a una dirección
		// interna, no se conecta a la red local ni al propio móvil.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, _ := net.SplitHostPort(address)
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				return fmt.Errorf("dirección no permitida: %s", host)
			}
			return nil
		}
	}
	c.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       60 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("demasiadas redirecciones")
			}
			if !c.allowed(req.URL) {
				return fmt.Errorf("redirección a un sitio no permitido: %s", req.URL.Host)
			}
			return nil
		},
	}
	return c
}

func (c *catalog) allowed(u *url.URL) bool {
	if u.Scheme != "https" && !(c.allowPrivate && u.Scheme == "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range c.hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

var errUpstream = errors.New("la biblioteca no responde; inténtalo más tarde")

// errBlocked: el operador ha cambiado la respuesta por su página de bloqueo
// (en España pasa con IP de Cloudflare durante los partidos de LaLiga), así
// que el certificado no es el de la web.
var errBlocked error = blockedError{}

type blockedError struct{}

func (blockedError) Error() string {
	return "tu conexión bloquea esta web ahora mismo (en España, el operador bloquea IP de Cloudflare durante los partidos de LaLiga); se volverá a intentar más tarde"
}
func (blockedError) Unwrap() error { return errUpstream }

// get hace un GET limitado a los dominios permitidos.
func (c *catalog) get(ctx context.Context, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !c.allowed(u) {
		return nil, fmt.Errorf("sitio no permitido: %s", rawURL)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		var cert *tls.CertificateVerificationError
		if errors.As(err, &cert) {
			return nil, errBlocked
		}
		return nil, fmt.Errorf("%w (%v)", errUpstream, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("%w (%s)", errUpstream, resp.Status)
	}
	return resp, nil
}

func (c *catalog) getJSON(ctx context.Context, rawURL string, v any) error {
	resp, err := c.get(ctx, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// Idiomas que ofrece la app (ISO 639-1) y su código en Internet Archive.
var languages = map[string]string{"es": "spa", "en": "eng", "fr": "fre", "de": "ger", "it": "ita", "pt": "por", "ca": "cat"}

var sources = map[string]bool{"gutenberg": true, "wikisource": true, "archive": true}

var (
	numericID = regexp.MustCompile(`^[0-9]{1,7}$`)
	archiveID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// search busca en una fuente. Sin texto, devuelve los más populares (donde
// la fuente lo permite). Las respuestas se guardan 10 minutos.
func (c *catalog) search(ctx context.Context, source, q, lang string, page int) (catalogPage, error) {
	if source == "todas" {
		return c.searchAll(ctx, q, lang, page)
	}
	if !sources[source] {
		return catalogPage{}, bad("fuente no válida")
	}
	if lang != "" && languages[lang] == "" {
		return catalogPage{}, bad("idioma no válido")
	}
	q = clean(q, 200)
	if page < 1 || page > 50 {
		page = 1
	}
	key := fmt.Sprintf("%s|%s|%s|%d", source, lang, strings.ToLower(q), page)
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && time.Since(e.at) < 10*time.Minute {
		c.mu.Unlock()
		return e.page, nil
	}
	c.mu.Unlock()

	var p catalogPage
	var err error
	switch source {
	case "gutenberg":
		p, err = c.searchGutenberg(ctx, q, lang, page)
	case "wikisource":
		p, err = c.searchWikisource(ctx, q, lang, page)
	case "archive":
		p, err = c.searchArchive(ctx, q, lang, page)
	}
	if err != nil {
		return p, err
	}
	c.mu.Lock()
	if len(c.cache) > 300 {
		c.cache = map[string]cacheEntry{}
	}
	c.cache[key] = cacheEntry{at: time.Now(), page: p}
	c.mu.Unlock()
	return p, nil
}

// searchAll busca a la vez en todas las fuentes y mezcla los resultados
// alternándolos. Si alguna falla, se muestran los de las demás.
func (c *catalog) searchAll(ctx context.Context, q, lang string, page int) (catalogPage, error) {
	names := []string{"gutenberg", "archive"}
	if clean(q, 200) != "" {
		names = append(names, "wikisource") // sin texto, Wikisource no tiene nada que mostrar
	}
	pages := make([]catalogPage, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages[i], errs[i] = c.search(ctx, n, q, lang, page)
		}()
	}
	wg.Wait()
	out := catalogPage{Items: []catalogItem{}}
	for i, n := range names {
		if errs[i] != nil {
			if errors.As(errs[i], new(badRequest)) {
				return catalogPage{}, errs[i]
			}
			out.Failed = append(out.Failed, n)
			continue
		}
		out.More = out.More || pages[i].More
	}
	if len(out.Failed) == len(names) {
		return catalogPage{}, errs[0]
	}
	for i := 0; ; i++ {
		added := false
		for _, p := range pages {
			if i < len(p.Items) {
				out.Items = append(out.Items, p.Items[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out, nil
}

// --- Project Gutenberg (catálogo OPDS oficial) ---

type atomLink struct {
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Href  string `xml:"href,attr"`
	Title string `xml:"title,attr"`
}

type atomEntry struct {
	ID      string `xml:"id"`
	Title   string `xml:"title"`
	Content struct {
		Body string `xml:",innerxml"`
	} `xml:"content"`
	Rights  string `xml:"rights"`
	Authors []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Categories []struct {
		Scheme string `xml:"scheme,attr"`
		Term   string `xml:"term,attr"`
	} `xml:"category"`
	Language string     `xml:"http://purl.org/dc/terms/ language"`
	Links    []atomLink `xml:"link"`
}

type atomFeed struct {
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

func (c *catalog) getFeed(ctx context.Context, rawURL string) (atomFeed, error) {
	var f atomFeed
	resp, err := c.get(ctx, rawURL)
	if err != nil {
		return f, err
	}
	defer resp.Body.Close()
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&f); err != nil {
		return f, fmt.Errorf("%w (%v)", errUpstream, err)
	}
	return f, nil
}

// Gutenberg escribe los autores como «Apellidos, Nombre»: se le da la vuelta.
func flipName(n string) string {
	if last, first, ok := strings.Cut(n, ", "); ok && !strings.Contains(first, ",") {
		return first + " " + last
	}
	return n
}

var (
	gutenbergEntryID = regexp.MustCompile(`/ebooks/([0-9]{1,7})\.opds$`)
	// En los resultados, el título de las obras no inglesas lleva el idioma detrás.
	gutenbergLangSuffix = regexp.MustCompile(`(\s*\([A-Z][a-z]+\))+$`)
	gutenbergSummary    = regexp.MustCompile(`(?s)Summary:\s*(.*?)\s*</p>`)
	gutenbergDownloads  = regexp.MustCompile(`Downloads: ([0-9]+)`)
)

func (c *catalog) searchGutenberg(ctx context.Context, q, lang string, page int) (catalogPage, error) {
	query := q
	if lang != "" {
		query = strings.TrimSpace(query + " l." + lang)
	}
	v := url.Values{}
	if query != "" {
		v.Set("query", query)
	}
	if q == "" {
		v.Set("sort_order", "downloads") // sin texto: los más leídos
	}
	if page > 1 {
		v.Set("start_index", strconv.Itoa((page-1)*25+1))
	}
	f, err := c.getFeed(ctx, c.urls.Gutenberg+"/ebooks/search.opds/?"+v.Encode())
	if err != nil {
		return catalogPage{}, err
	}
	p := catalogPage{Items: []catalogItem{}}
	for _, l := range f.Links {
		p.More = p.More || l.Rel == "next"
	}
	for _, e := range f.Entries {
		m := gutenbergEntryID.FindStringSubmatch(e.ID)
		if m == nil {
			continue // entradas de navegación
		}
		it := catalogItem{Source: "gutenberg", ID: m[1], Title: strings.Join(strings.Fields(gutenbergLangSuffix.ReplaceAllString(e.Title, "")), " "), Authors: []string{}, Language: lang, HasCover: true}
		if a := strings.TrimSpace(html.UnescapeString(e.Content.Body)); a != "" {
			it.Authors = append(it.Authors, a)
		}
		p.Items = append(p.Items, it)
	}
	return p, nil
}

// resolveGutenberg lee la ficha OPDS del libro: comprueba que es un texto de
// dominio público y elige el mejor EPUB (EPUB 3 con imágenes si lo hay).
func (c *catalog) resolveGutenberg(ctx context.Context, id string) (download, error) {
	f, err := c.getFeed(ctx, c.urls.Gutenberg+"/ebooks/"+id+".opds")
	if err != nil {
		return download{}, err
	}
	base, _ := url.Parse(c.urls.Gutenberg + "/")
	abs := func(href string) string {
		u, err := base.Parse(href)
		if err != nil {
			return ""
		}
		return u.String()
	}
	var best string
	rank := 0
	var e atomEntry
	for _, entry := range f.Entries {
		for _, l := range entry.Links {
			if l.Rel != "http://opds-spec.org/acquisition" || l.Type != "application/epub+zip" {
				continue
			}
			r := 1
			switch {
			case strings.HasSuffix(l.Href, ".epub3.images"):
				r = 3
			case strings.HasSuffix(l.Href, ".epub.images"):
				r = 2
			}
			if r > rank {
				best, rank, e = abs(l.Href), r, entry
			}
		}
	}
	text := false
	for _, cat := range e.Categories {
		text = text || cat.Scheme == "http://purl.org/dc/terms/DCMIType" && cat.Term == "Text"
	}
	if best == "" || !text || !strings.Contains(strings.ToLower(e.Rights), "public domain") {
		return download{}, bad("este libro no está disponible en EPUB de dominio público")
	}
	// Cada edición (con y sin imágenes) es una entrada; los metadatos completos
	// pueden estar solo en una de ellas.
	for _, entry := range f.Entries {
		if len(entry.Authors) > 0 {
			e = entry
			break
		}
	}
	meta := catalogItem{Source: "gutenberg", ID: id, Title: strings.Join(strings.Fields(e.Title), " "), Authors: []string{}, Language: e.Language}
	for _, a := range e.Authors {
		meta.Authors = append(meta.Authors, flipName(a.Name))
	}
	// Las categorías de las estanterías («Category: Novels») son mejores géneros
	// que los encabezamientos de materia de la Biblioteca del Congreso.
	for _, l := range e.Links {
		if g, ok := strings.CutPrefix(l.Title, "In Category: "); ok && l.Rel == "related" {
			meta.Subjects = append(meta.Subjects, strings.TrimSuffix(g, "…"))
		}
	}
	if len(meta.Subjects) == 0 {
		for _, cat := range e.Categories {
			if cat.Scheme == "http://purl.org/dc/terms/LCSH" {
				meta.Subjects = append(meta.Subjects, cat.Term)
			}
		}
	}
	if len(meta.Subjects) > 5 {
		meta.Subjects = meta.Subjects[:5]
	}
	if m := gutenbergSummary.FindStringSubmatch(e.Content.Body); m != nil {
		meta.Description = strings.TrimSpace(strings.TrimSuffix(html.UnescapeString(stripTags(m[1])), "(This is an automatically generated summary.)"))
	}
	if m := gutenbergDownloads.FindStringSubmatch(e.Content.Body); m != nil {
		meta.Downloads, _ = strconv.Atoi(m[1])
	}
	cover := ""
	for _, l := range e.Links {
		if l.Rel == "http://opds-spec.org/image" {
			cover = abs(l.Href)
		}
	}
	return download{url: best, fileName: "gutenberg-" + id + ".epub", meta: meta, cover: cover}, nil
}

// --- Wikisource ---

func (c *catalog) wikisourceBase(lang string) string { return fmt.Sprintf(c.urls.Wikisource, lang) }

func (c *catalog) searchWikisource(ctx context.Context, q, lang string, page int) (catalogPage, error) {
	if lang == "" {
		lang = "es"
	}
	if q == "" {
		return catalogPage{Items: []catalogItem{}}, nil // Wikisource no tiene «populares»
	}
	const per = 20
	v := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {q}, "srnamespace": {"0"}, "srlimit": {strconv.Itoa(per)},
		"sroffset": {strconv.Itoa((page - 1) * per)}, "srprop": {"wordcount"}, "format": {"json"}}
	var res struct {
		Continue *struct{} `json:"continue"`
		Query    struct {
			Search []struct {
				Title     string `json:"title"`
				WordCount int    `json:"wordcount"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := c.getJSON(ctx, c.wikisourceBase(lang)+"/w/api.php?"+v.Encode(), &res); err != nil {
		return catalogPage{}, err
	}
	p := catalogPage{Items: []catalogItem{}, More: res.Continue != nil}
	for _, r := range res.Query.Search {
		// Las subpáginas («Obra/Capítulo 3») no son libros por sí solas.
		if strings.Contains(r.Title, "/") {
			continue
		}
		p.Items = append(p.Items, catalogItem{Source: "wikisource", ID: lang + ":" + r.Title, Title: r.Title, Authors: []string{}, Language: lang})
	}
	return p, nil
}

// --- Internet Archive ---

// archiveLicensed: solo dominio público o Creative Commons explícitos. El
// resto del archivo mezcla obras libres con subidas sin permiso.
func archiveLicensed(license string) bool {
	l := strings.ToLower(license)
	return strings.Contains(l, "publicdomain") || strings.Contains(l, "creativecommons.org")
}

// Los campos de Internet Archive pueden ser una cadena o una lista.
type stringOrList []string

func (s *stringOrList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return nil // un campo raro no estropea el resultado
	}
	*s = many
	return nil
}

func first(s stringOrList) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func (c *catalog) searchArchive(ctx context.Context, q, lang string, page int) (catalogPage, error) {
	query := "mediatype:texts AND format:EPUB AND (licenseurl:*publicdomain* OR licenseurl:*creativecommons*)"
	if q != "" {
		// Se quitan los caracteres con significado en la sintaxis de búsqueda.
		safe := strings.Map(func(r rune) rune {
			if strings.ContainsRune(`:()[]{}"\/^~*?!+-&|`, r) {
				return ' '
			}
			return r
		}, q)
		query = "(" + safe + ") AND " + query
	}
	if code := languages[lang]; code != "" {
		query += " AND language:(" + code + ")"
	}
	v := url.Values{"q": {query}, "rows": {"20"}, "page": {strconv.Itoa(page)}, "output": {"json"}, "sort[]": {"downloads desc"}}
	for _, f := range []string{"identifier", "title", "creator", "language", "year", "licenseurl", "downloads"} {
		v.Add("fl[]", f)
	}
	var res struct {
		Response struct {
			NumFound int `json:"numFound"`
			Docs     []struct {
				Identifier string       `json:"identifier"`
				Title      stringOrList `json:"title"`
				Creator    stringOrList `json:"creator"`
				Language   stringOrList `json:"language"`
				Year       stringOrList `json:"year"`
				License    stringOrList `json:"licenseurl"`
				Downloads  int          `json:"downloads"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := c.getJSON(ctx, c.urls.Archive+"/advancedsearch.php?"+v.Encode(), &res); err != nil {
		return catalogPage{}, err
	}
	p := catalogPage{Items: []catalogItem{}, More: res.Response.NumFound > page*20}
	for _, d := range res.Response.Docs {
		if !archiveID.MatchString(d.Identifier) || !archiveLicensed(first(d.License)) {
			continue
		}
		authors := []string{}
		for _, a := range d.Creator {
			authors = append(authors, a)
		}
		p.Items = append(p.Items, catalogItem{Source: "archive", ID: d.Identifier, Title: first(d.Title), Authors: authors,
			Language: first(d.Language), Year: first(d.Year), Downloads: d.Downloads, HasCover: true})
	}
	return p, nil
}

// --- Descarga ---

// download es lo necesario para importar un libro de una fuente.
type download struct {
	url      string
	fileName string
	meta     catalogItem
	cover    string // URL de la portada en la fuente (opcional)
}

// resolve averigua, a partir del id de la fuente, de dónde se descarga el
// libro. Nunca se usa una URL que mande el cliente.
func (c *catalog) resolve(ctx context.Context, source, id string) (download, error) {
	switch source {
	case "gutenberg":
		if !numericID.MatchString(id) {
			return download{}, bad("id no válido")
		}
		return c.resolveGutenberg(ctx, id)
	case "wikisource":
		lang, title, ok := strings.Cut(id, ":")
		if !ok || languages[lang] == "" || title == "" || len(title) > 300 {
			return download{}, bad("id no válido")
		}
		v := url.Values{"lang": {lang}, "page": {strings.ReplaceAll(title, " ", "_")}, "format": {"epub-3"}}
		return download{url: c.urls.WSExport + "/?" + v.Encode(), fileName: "wikisource-" + lang + ".epub",
			meta: catalogItem{Source: source, ID: id, Title: title, Authors: []string{}, Language: lang}}, nil
	case "archive":
		if !archiveID.MatchString(id) {
			return download{}, bad("id no válido")
		}
		var md struct {
			Metadata struct {
				Title    stringOrList `json:"title"`
				Creator  stringOrList `json:"creator"`
				Language stringOrList `json:"language"`
				Year     stringOrList `json:"year"`
				License  stringOrList `json:"licenseurl"`
				Subject  stringOrList `json:"subject"`
			} `json:"metadata"`
			Files []struct {
				Name string `json:"name"`
			} `json:"files"`
		}
		if err := c.getJSON(ctx, c.urls.Archive+"/metadata/"+id, &md); err != nil {
			return download{}, err
		}
		if !archiveLicensed(first(md.Metadata.License)) {
			return download{}, bad("este documento no tiene una licencia libre explícita")
		}
		for _, f := range md.Files {
			if strings.HasSuffix(strings.ToLower(f.Name), ".epub") && !strings.Contains(f.Name, "/") {
				meta := catalogItem{Source: source, ID: id, Title: first(md.Metadata.Title), Authors: md.Metadata.Creator,
					Language: first(md.Metadata.Language), Year: first(md.Metadata.Year), Subjects: md.Metadata.Subject}
				if meta.Authors == nil {
					meta.Authors = []string{}
				}
				return download{url: c.urls.Archive + "/download/" + id + "/" + url.PathEscape(f.Name), fileName: f.Name, meta: meta,
					cover: c.urls.Archive + "/services/img/" + id}, nil
			}
		}
		return download{}, bad("este documento no tiene EPUB")
	}
	return download{}, bad("fuente no válida")
}

// coverURL es la portada de un resultado de búsqueda.
func (c *catalog) coverURL(source, id string) (string, error) {
	switch {
	case source == "gutenberg" && numericID.MatchString(id):
		return fmt.Sprintf("%s/cache/epub/%s/pg%s.cover.medium.jpg", c.urls.Gutenberg, id, id), nil
	case source == "archive" && archiveID.MatchString(id):
		return c.urls.Archive + "/services/img/" + id, nil
	}
	return "", errNotFound
}

// fetchImage descarga una imagen pequeña (portadas).
func (c *catalog) fetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	resp, err := c.get(ctx, rawURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil || len(data) > maxCoverBytes {
		return nil, "", errors.New("portada no válida")
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", errors.New("la portada no es una imagen")
	}
	return data, ct, nil
}
