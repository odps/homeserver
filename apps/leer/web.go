package main

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST y sin JavaScript:
// pendientes (con el formulario para guardar), favoritos, archivo, el lector
// y los ajustes. Las búsquedas y los filtros por etiqueta también van por
// POST: lo que busca la persona no queda en ninguna URL. El texto de los
// artículos son bloques de texto plano que las plantillas escapan: nunca se
// sirve HTML descargado, ni se cargan imágenes de fuera.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 16 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /leer detrás de la pasarela
	Name    string
	Tab     string // pendientes, favoritos, archivo, ajustes
	Title   string
	Flash   string
	Error   string
	Size    string // tamaño de letra del lector: s, m, l, xl

	// Listas
	List     string
	Heading  string
	Lead     string
	Rows     []row
	Total    int
	Counts   counts
	Tags     []tagChip
	Query    string
	Tag      string
	Searched bool
	FormURL  string
	FormTags string

	// Lector
	Art     *artView
	Confirm bool
	Sizes   []option

	// Ajustes
	Prefs prefs
}

type option struct {
	Key, Label string
	Selected   bool
}

type tagChip struct {
	Name string
	N    int
	On   bool
}

type row struct {
	ID                       int64
	Title, Site, Saved       string
	Excerpt                  string
	Minutes                  int
	Tags                     []string
	Read, Favorite, Archived bool
	Failed, Queued           bool
}

type artView struct {
	ID                       int64
	Title, Site, Author      string
	Date, Saved, URL         string
	Minutes                  int
	Tags                     []string
	TagsText                 string
	Read, Favorite, Archived bool
	Failed, Queued           bool
	Err                      string
	Blocks                   []block
	Truncated                bool
	Back, BackLabel          string
	Lang                     string // idioma del texto (para partir palabras)
}

type web struct{ svc *service }

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.listPage(listPending)))
	mux.HandleFunc("GET /favoritos", w.user(w.listPage(listFavorite)))
	mux.HandleFunc("GET /archivo", w.user(w.listPage(listArchive)))
	mux.HandleFunc("POST /buscar", w.user(w.searchPost))
	mux.HandleFunc("POST /guardar", w.user(w.savePost))
	mux.HandleFunc("GET /articulo/{id}", w.user(w.articlePage))
	mux.HandleFunc("POST /articulo/{id}/marcar", w.user(w.markPost))
	mux.HandleFunc("POST /articulo/{id}/etiquetas", w.user(w.tagsPost))
	mux.HandleFunc("POST /articulo/{id}/reintentar", w.user(w.retryPost))
	mux.HandleFunc("POST /articulo/{id}/borrar", w.user(w.deletePost))
	mux.HandleFunc("GET /ajustes", w.user(w.prefsPage))
	mux.HandleFunc("POST /ajustes", w.user(w.prefsPost))
	mux.HandleFunc("POST /tamano", w.user(w.sizePost))
	mux.HandleFunc("GET /api/avisos", w.user(w.notices))
	mux.HandleFunc("POST /interno/hoy", w.internalToday)
}

// user exige la identidad que pone la pasarela.
func (w *web) user(h func(http.ResponseWriter, *http.Request, app.Identity)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		who := app.User(r)
		if !who.IsUser() {
			http.Error(rw, "entra desde la pasarela", http.StatusUnauthorized)
			return
		}
		h(rw, r, who)
	}
}

func base(r *http.Request) string { return strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/") }

func (w *web) render(rw http.ResponseWriter, r *http.Request, name string, d pageData) {
	w.renderCode(rw, r, name, d, http.StatusOK)
}

// renderCode es render con otro estado HTTP (422 si hay que corregir algo).
func (w *web) renderCode(rw http.ResponseWriter, r *http.Request, name string, d pageData, code int) {
	d.Version, d.Base = version.Version, base(r)
	if d.Flash == "" {
		d.Flash = flashes[r.URL.Query().Get("ok")]
	}
	if d.Size == "" {
		d.Size = "m"
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Security-Policy", csp)
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	rw.WriteHeader(code)
	if err := pages.ExecuteTemplate(rw, name, d); err != nil {
		w.svc.log.Error("plantilla", "pagina", name, "err", err)
	}
}

// fail registra un error interno (sin datos de la persona) y lo dice.
func (w *web) fail(rw http.ResponseWriter, op string, err error) {
	w.svc.log.Error("leer web", "op", op, "err", err)
	http.Error(rw, "Algo ha fallado. Prueba otra vez.", http.StatusInternalServerError)
}

func notFound(rw http.ResponseWriter) { http.Error(rw, "No existe.", http.StatusNotFound) }

func badRequest(rw http.ResponseWriter) {
	http.Error(rw, "Petición no válida.", http.StatusBadRequest)
}

// form lee un formulario pequeño.
func form(rw http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(rw, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		badRequest(rw)
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// back vuelve a una página de la app (la pasarela pone el prefijo).
func back(rw http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(rw, r, to, http.StatusSeeOther)
}

// backTo es la página a la que se vuelve tras un botón: solo las de la app.
func backTo(volver string, id int64) string {
	switch volver {
	case "favoritos":
		return "/favoritos"
	case "archivo":
		return "/archivo"
	case "articulo":
		return "/articulo/" + strconv.FormatInt(id, 10)
	}
	return "/"
}

var flashes = map[string]string{
	"guardado":    "Guardado.",
	"ya-estaba":   "Ya lo tenías guardado: está arriba del todo en pendientes si lo habías archivado.",
	"sin-texto":   "Guardado, pero no se ha podido leer la página: lo tienes con su enlace y puedes reintentarlo.",
	"leido":       "Marcado como leído.",
	"no-leido":    "Marcado como no leído.",
	"favorito":    "En favoritos.",
	"no-favorito": "Quitado de favoritos.",
	"archivado":   "Archivado.",
	"devuelto":    "Ha vuelto a pendientes.",
	"etiquetas":   "Etiquetas guardadas.",
	"reintentado": "Descargado de nuevo.",
	"sigue-mal":   "Sigue sin poder leerse.",
	"borrado":     "Borrado.",
	"ajustes":     "Ajustes guardados.",
}

// ---- Fechas ----

var months = []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"}

func dateES(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.Itoa(t.Day()) + " " + months[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

// savedES dice cuándo se guardó: hoy, ayer o la fecha.
func savedES(t, now time.Time) string {
	y1, m1, d1 := t.In(now.Location()).Date()
	y2, m2, d2 := now.Date()
	a := time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)
	b := time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC)
	switch b.Sub(a) / (24 * time.Hour) {
	case 0:
		return "hoy"
	case 1:
		return "ayer"
	}
	if y1 == y2 {
		return strconv.Itoa(d1) + " " + months[m1-1]
	}
	return dateES(t)
}

func publishedES(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return ""
	}
	return dateES(t)
}

// ---- Listas ----

func (w *web) rowOf(a article, now time.Time) row {
	return row{ID: a.ID, Title: a.title(), Site: a.site(), Saved: savedES(a.Created, now), Excerpt: oneLine(a.Excerpt, 180),
		Minutes: a.Minutes, Tags: a.Tags, Read: a.Read, Favorite: a.Favorite, Archived: a.Archived,
		Failed: a.State == stateError, Queued: a.State == stateQueued}
}

var headings = map[string]string{listPending: "Pendientes", listFavorite: "Favoritos", listArchive: "Archivo", listAll: "Resultados"}

const listLimit = 300

// listData prepara una lista (con búsqueda o etiqueta, si las hay).
func (w *web) listData(r *http.Request, who app.Identity, list, query, tag string) (pageData, error) {
	ctx := r.Context()
	if !validList(list) {
		list = listPending
	}
	tab := list
	if list == listAll {
		tab = ""
	}
	d := pageData{Name: who.Name, Tab: tab, List: list, Heading: headings[list], Query: query, Tag: tag,
		Searched: query != "" || tag != ""}
	if list != listPending {
		d.Title = d.Heading
	}
	var err error
	if d.Counts, err = w.svc.store.counts(ctx, who.User); err != nil {
		return d, err
	}
	arts, total, err := w.svc.store.list(ctx, who.User, filter{List: list, Query: query, Tag: tag, Limit: listLimit})
	if err != nil {
		return d, err
	}
	now := w.svc.clock()
	for _, a := range arts {
		d.Rows = append(d.Rows, w.rowOf(a, now))
	}
	d.Total = total
	tags, err := w.svc.allTags(ctx, who.User)
	if err != nil {
		return d, err
	}
	for _, t := range tags {
		d.Tags = append(d.Tags, tagChip{Name: t.Name, N: t.N, On: tag != "" && strings.EqualFold(t.Name, tag)})
	}
	switch {
	case d.Searched:
		d.Lead = plural(total, "artículo encontrado", "artículos encontrados")
	case list == listPending:
		d.Lead = plural(d.Counts.Unread, "por leer", "por leer")
		if d.Counts.Unread == 0 && d.Counts.Pending > 0 {
			d.Lead = "Todo leído."
		}
	default:
		d.Lead = plural(total, "artículo", "artículos")
	}
	return d, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func (w *web) listPage(list string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		d, err := w.listData(r, who, list, "", "")
		if err != nil {
			w.fail(rw, "lista", err)
			return
		}
		w.render(rw, r, "lista.html", d)
	}
}

// searchPost busca por palabras o filtra por una etiqueta dentro de una
// lista.
func (w *web) searchPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	q := oneLine(r.PostForm.Get("q"), 200)
	tag := oneLine(r.PostForm.Get("etiqueta"), maxTagLen)
	from := r.PostForm.Get("lista")
	if !validList(from) || from == listAll {
		from = listPending
	}
	// Se busca en todo (pendientes, favoritos y archivo).
	list := from
	if q != "" || tag != "" {
		list = listAll
	}
	d, err := w.listData(r, who, list, q, tag)
	if err != nil {
		w.fail(rw, "buscar", err)
		return
	}
	d.Tab = from
	w.render(rw, r, "lista.html", d)
}

func (w *web) savePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	raw, tags := r.PostForm.Get("url"), r.PostForm.Get("etiquetas")
	a, existed, err := w.svc.save(r.Context(), who.User, raw, []string{tags}, "web")
	switch {
	case isInput(err):
		d, lerr := w.listData(r, who, listPending, "", "")
		if lerr != nil {
			w.fail(rw, "guardar", lerr)
			return
		}
		d.Error, d.FormURL, d.FormTags = err.Error(), oneLine(raw, maxURL), oneLine(tags, 400)
		w.renderCode(rw, r, "lista.html", d, http.StatusUnprocessableEntity)
	case err != nil:
		w.fail(rw, "guardar", err)
	case existed:
		back(rw, r, "/?ok=ya-estaba")
	case a.State != stateOK:
		back(rw, r, "/?ok=sin-texto")
	default:
		back(rw, r, "/?ok=guardado")
	}
}

// ---- Lector ----

func (w *web) artViewOf(a article, blocks []block) *artView {
	v := &artView{ID: a.ID, Title: a.title(), Site: a.site(), Author: a.Author, Date: publishedES(a.Published),
		Saved: savedES(a.Created, w.svc.clock()), URL: a.URL, Minutes: a.Minutes, Tags: a.Tags, TagsText: strings.Join(a.Tags, ", "),
		Read: a.Read, Favorite: a.Favorite, Archived: a.Archived, Failed: a.State == stateError, Queued: a.State == stateQueued,
		Err: a.Err, Blocks: blocks, Truncated: a.Truncated, Back: "/", BackLabel: "Pendientes", Lang: langTag(a.Lang)}
	if a.Final != "" {
		v.URL = a.Final
	}
	if a.Archived {
		v.Back, v.BackLabel = "/archivo", "Archivo"
	}
	return v
}

var reLang = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})?$`)

// langTag es el idioma que dice la página, si tiene forma de serlo; si no,
// español.
func langTag(l string) string {
	if reLang.MatchString(l) {
		return l
	}
	return "es"
}

func sizeOptions(sel string) []option {
	var out []option
	for _, s := range sizes {
		out = append(out, option{Key: s.Key, Label: s.Label, Selected: s.Key == sel})
	}
	return out
}

func (w *web) renderArticle(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, msg string, tagsText *string) {
	ctx := r.Context()
	a, err := w.svc.store.get(ctx, who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "articulo", err)
		return
	}
	blocks, err := w.svc.store.body(ctx, who.User, id)
	if err != nil {
		w.fail(rw, "articulo", err)
		return
	}
	p, err := w.svc.store.prefs(ctx, who.User)
	if err != nil {
		w.fail(rw, "articulo", err)
		return
	}
	v := w.artViewOf(a, blocks)
	if tagsText != nil {
		v.TagsText = *tagsText
	}
	tab := listPending
	if a.Archived {
		tab = listArchive
	}
	d := pageData{Name: who.Name, Tab: tab, Title: v.Title, Art: v, Size: p.Size, Sizes: sizeOptions(p.Size), Error: msg,
		Confirm: r.URL.Query().Get("borrar") == "1"}
	code := http.StatusOK
	if msg != "" {
		code = http.StatusUnprocessableEntity
	}
	w.renderCode(rw, r, "articulo.html", d, code)
}

func (w *web) articlePage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	w.renderArticle(rw, r, who, id, "", nil)
}

func (w *web) markPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	on := r.PostForm.Get("valor") == "1"
	var f flags
	var flash string
	switch r.PostForm.Get("campo") {
	case "leido":
		f.Read, flash = &on, map[bool]string{true: "leido", false: "no-leido"}[on]
	case "favorito":
		f.Favorite, flash = &on, map[bool]string{true: "favorito", false: "no-favorito"}[on]
	case "archivado":
		f.Archived, flash = &on, map[bool]string{true: "archivado", false: "devuelto"}[on]
	default:
		badRequest(rw)
		return
	}
	_, err := w.svc.mark(r.Context(), who.User, id, f)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "marcar", err)
	default:
		back(rw, r, backTo(r.PostForm.Get("volver"), id)+"?ok="+flash)
	}
}

func (w *web) tagsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	raw := r.PostForm.Get("etiquetas")
	_, err := w.svc.setTags(r.Context(), who.User, id, []string{raw})
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		text := oneLine(raw, 400)
		w.renderArticle(rw, r, who, id, err.Error(), &text)
	case err != nil:
		w.fail(rw, "etiquetas", err)
	default:
		back(rw, r, "/articulo/"+strconv.FormatInt(id, 10)+"?ok=etiquetas")
	}
}

func (w *web) retryPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	a, err := w.svc.retry(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "reintentar", err)
	case a.State == stateOK:
		back(rw, r, "/articulo/"+strconv.FormatInt(id, 10)+"?ok=reintentado")
	default:
		back(rw, r, "/articulo/"+strconv.FormatInt(id, 10)+"?ok=sigue-mal")
	}
}

func (w *web) deletePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/articulo/"+strconv.FormatInt(id, 10)+"?borrar=1#borrar")
		return
	}
	a, err := w.svc.store.get(r.Context(), who.User, id)
	if err == nil {
		err = w.svc.store.delete(r.Context(), who.User, id)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar", err)
	case a.Archived:
		back(rw, r, "/archivo?ok=borrado")
	default:
		back(rw, r, "/?ok=borrado")
	}
}

// ---- Ajustes ----

func (w *web) prefsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	p, err := w.svc.store.prefs(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "ajustes", err)
		return
	}
	w.render(rw, r, "ajustes.html", pageData{Name: who.Name, Tab: "ajustes", Title: "Ajustes", Prefs: p, Size: p.Size, Sizes: sizeOptions(p.Size)})
}

func (w *web) prefsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	p := prefs{Size: r.PostForm.Get("tamano"), Summary: r.PostForm.Get("resumen") == "1", Weekly: r.PostForm.Get("semanal") == "1"}
	if !validSize(p.Size) {
		badRequest(rw)
		return
	}
	if err := w.svc.store.setPrefs(r.Context(), who.User, p); err != nil {
		w.fail(rw, "ajustes", err)
		return
	}
	back(rw, r, "/ajustes?ok=ajustes")
}

// sizePost cambia solo el tamaño de letra, desde el propio lector.
func (w *web) sizePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	size := r.PostForm.Get("tamano")
	if !validSize(size) {
		badRequest(rw)
		return
	}
	p, err := w.svc.store.prefs(r.Context(), who.User)
	if err == nil {
		p.Size = size
		err = w.svc.store.setPrefs(r.Context(), who.User, p)
	}
	if err != nil {
		w.fail(rw, "tamano", err)
		return
	}
	if id, err := strconv.ParseInt(r.PostForm.Get("id"), 10, 64); err == nil && id > 0 {
		back(rw, r, "/articulo/"+strconv.FormatInt(id, 10))
		return
	}
	back(rw, r, "/ajustes")
}

// ---- Para la pasarela y la Agenda ----

// notices entrega a la pasarela los avisos pendientes y los da por
// entregados.
func (w *web) notices(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	list, err := w.svc.store.takeNotices(r.Context(), who.User, w.svc.clock())
	if err != nil {
		w.svc.log.Warn("avisos", "err", err)
		http.Error(rw, "error", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(rw).Encode(list)
}

// internalToday es POST /interno/hoy, solo para la Agenda (por 127.0.0.1,
// sin pasar por la pasarela): la línea del resumen de la mañana. El usuario
// va en el cuerpo, no en la URL.
func (w *web) internalToday(rw http.ResponseWriter, r *http.Request) {
	if !app.Internal(r) {
		http.Error(rw, "solo para las apps del servidor", http.StatusForbidden)
		return
	}
	var in struct {
		User string `json:"usuario"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil || in.User == "" {
		http.Error(rw, "petición no válida", http.StatusBadRequest)
		return
	}
	lines, err := w.svc.todayLines(r.Context(), in.User)
	if err != nil {
		w.svc.log.Warn("resumen de hoy", "err", err)
		http.Error(rw, "error", http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"lineas": lines})
}
