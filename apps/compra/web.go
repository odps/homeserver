package main

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST: la lista, editar
// una cosa y el orden de las secciones. La única con JavaScript es la lista
// (lista.js, para tachar sin recargar y sin cobertura), y funciona igual sin
// él.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// cspLista es la de la lista: deja cargar su script (de la propia app) y
// hacer fetch solo al mismo origen. Nada en línea.
const cspLista = "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 16 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /compra detrás de la pasarela
	Name    string
	Tab     string // lista, secciones
	Title   string
	Script  bool // carga lista.js (con su CSP)
	Flash   string
	Error   string

	// Lista
	Sections     []sectionView
	Total        int // cosas en la lista
	Done         int // tachadas
	Left         int // por comprar
	Text         string
	Running      []suggView // se te estará acabando
	Usual        []suggView // lo de siempre
	ConfirmClear bool

	// Editar una cosa
	Item    *itemForm
	Options []sectionOpt

	// Orden de las secciones
	Order []sectionOpt
}

type sectionView struct {
	Key, Label string
	Items      []itemView
	Left       int // sin tachar
}

type itemView struct {
	ID              int64
	Name, Qty, Note string
	Done            bool
	By              string // «Talia» si la añadió el asistente; en la fase 2, la receta
}

type suggView struct {
	Name, Meta string
}

type itemForm struct {
	ID                        int64
	Name, Qty, Note, Section  string
	Added, Source, SectionTxt string
	Done                      bool
}

type sectionOpt struct {
	Key, Label  string
	Selected    bool
	N           int
	First, Last bool
}

type web struct{ svc *service }

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.listPage))
	mux.HandleFunc("POST /anadir", w.user(w.addPost))
	mux.HandleFunc("POST /marcar", w.user(w.markPost))
	mux.HandleFunc("GET /cosa/{id}", w.user(w.itemPage))
	mux.HandleFunc("POST /cosa/{id}", w.user(w.itemPost))
	mux.HandleFunc("POST /cosa/{id}/borrar", w.user(w.itemDelete))
	mux.HandleFunc("POST /terminar", w.user(w.finishPost))
	mux.HandleFunc("POST /vaciar", w.user(w.clearPost))
	mux.HandleFunc("GET /secciones", w.user(w.sectionsPage))
	mux.HandleFunc("POST /secciones", w.user(w.sectionsPost))
	mux.HandleFunc("POST /api/marcar", w.user(w.apiMark))
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

func (w *web) now() time.Time { return w.svc.clock().In(madrid) }

func (w *web) render(rw http.ResponseWriter, r *http.Request, name string, d pageData) {
	d.Version, d.Base = version.Version, base(r)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if d.Script {
		rw.Header().Set("Content-Security-Policy", cspLista)
	} else {
		rw.Header().Set("Content-Security-Policy", csp)
	}
	rw.Header().Set("Cache-Control", "no-store")
	if err := pages.ExecuteTemplate(rw, name, d); err != nil {
		w.svc.log.Error("plantilla", "pagina", name, "err", err)
	}
}

// fail registra un error interno (sin datos de la persona) y lo dice.
func (w *web) fail(rw http.ResponseWriter, op string, err error) {
	w.svc.log.Error("compra web", "op", op, "err", err)
	http.Error(rw, "Algo ha fallado. Prueba otra vez.", http.StatusInternalServerError)
}

func notFound(rw http.ResponseWriter) { http.Error(rw, "No existe.", http.StatusNotFound) }

// form lee un formulario pequeño.
func form(rw http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(rw, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ---- Avisos ----

var flashes = map[string]string{
	"guardado": "Guardado.", "borrada": "Quitado de la lista.", "vaciada": "Lista vaciada.",
	"orden": "Orden guardado.", "nada-tachado": "No hay nada tachado todavía.",
}

var errorsText = map[string]string{
	"vacio": "No he entendido nada que añadir.",
	"llena": "La lista está llena: termina o vacía lo que sobra.",
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// flash es el aviso tras volver a la lista. Tras añadir solo se cuentan las
// cosas: los nombres no van en la URL.
func flash(r *http.Request) string {
	q := r.URL.Query()
	n := func(k string) int { v, _ := strconv.Atoi(q.Get(k)); return max(0, min(v, maxParts)) }
	switch q.Get("ok") {
	case "anadido":
		var parts []string
		if a := n("n"); a > 0 {
			parts = append(parts, "Añadido: "+plural(a, "cosa", "cosas")+".")
		}
		if j := n("j"); j > 0 {
			parts = append(parts, plural(j, "ya estaba", "ya estaban")+" y se ha juntado la cantidad.")
		}
		if d := n("d"); d > 0 {
			parts = append(parts, plural(d, "ya estaba", "ya estaban")+" en la lista.")
		}
		return strings.Join(parts, " ")
	case "terminado":
		return "Hecho: " + plural(n("n"), "cosa pasa", "cosas pasan") + " al historial."
	}
	return flashes[q.Get("ok")]
}

// back vuelve a una página de la app (la pasarela pone el prefijo).
func back(rw http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(rw, r, to, http.StatusSeeOther)
}

// ---- Fechas ----

var (
	weekdays = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	months   = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
)

func (w *web) longDate(t time.Time) string {
	t = t.In(madrid)
	s := fmt.Sprintf("%s %d de %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
	if t.Year() != w.now().Year() {
		s += fmt.Sprintf(" de %d", t.Year())
	}
	return s
}

// ---- La lista ----

func (w *web) listPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	list, err := w.svc.store.items(ctx, who.User)
	if err != nil {
		w.fail(rw, "lista", err)
		return
	}
	order, err := w.svc.store.order(ctx, who.User)
	if err != nil {
		w.fail(rw, "lista", err)
		return
	}
	running, usual, err := w.svc.suggestions(ctx, who.User, list)
	if err != nil {
		w.fail(rw, "sugerencias", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "lista", Script: true, Flash: flash(r), Error: errorsText[r.URL.Query().Get("error")],
		ConfirmClear: r.URL.Query().Get("vaciar") == "1"}
	d.Sections, d.Total, d.Done = groupItems(list, order)
	d.Left = d.Total - d.Done
	d.Running, d.Usual = suggViews(running, usual)
	w.render(rw, r, "lista.html", d)
}

// groupItems reparte la lista por secciones, en el orden de la persona; en
// cada una, lo tachado al final.
func groupItems(list []item, order []string) ([]sectionView, int, int) {
	by := map[string]*sectionView{}
	done := 0
	for _, it := range list {
		sv := by[it.Section]
		if sv == nil {
			sv = &sectionView{Key: it.Section, Label: sectionLabel(it.Section)}
			by[it.Section] = sv
		}
		v := itemView{ID: it.ID, Name: it.Name, Qty: it.Qty, Note: it.Note, Done: it.Done}
		switch it.Source {
		case sourceAssistant:
			v.By = "Talia"
		case sourceRecipe:
			v.By = it.Recipe
		}
		sv.Items = append(sv.Items, v)
		if it.Done {
			done++
		} else {
			sv.Left++
		}
	}
	var out []sectionView
	for _, k := range order {
		sv := by[k]
		if sv == nil {
			continue
		}
		var open, closed []itemView
		for _, v := range sv.Items {
			if v.Done {
				closed = append(closed, v)
			} else {
				open = append(open, v)
			}
		}
		sv.Items = append(open, closed...)
		out = append(out, *sv)
	}
	return out, len(list), done
}

func suggViews(running, usual []suggestion) ([]suggView, []suggView) {
	var r, u []suggView
	for _, s := range running {
		every := int(math.Round(s.AvgDays))
		r = append(r, suggView{Name: s.Name, Meta: "Cada " + plural(every, "día", "días") + " · la última, hace " + plural(s.Since, "día", "días")})
	}
	for _, s := range usual {
		u = append(u, suggView{Name: s.Name, Meta: plural(s.Times, "vez", "veces")})
	}
	return r, u
}

func (w *web) addPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	var res []addResult
	var err error
	if name := r.PostForm.Get("nombre"); name != "" {
		res, err = w.svc.addName(r.Context(), who.User, name, sourceWeb)
	} else {
		res, err = w.svc.addText(r.Context(), who.User, r.PostForm.Get("texto"), sourceWeb)
	}
	switch {
	case errors.Is(err, errNothing):
		back(rw, r, "/?error=vacio")
		return
	case errors.Is(err, errFull):
		back(rw, r, "/?error=llena")
		return
	case err != nil:
		w.fail(rw, "añadir", err)
		return
	}
	count := map[string]int{}
	for _, x := range res {
		count[x.Result]++
	}
	back(rw, r, fmt.Sprintf("/?ok=anadido&n=%d&j=%d&d=%d", count[addNew], count[addMerged], count[addDup]))
}

// markPost tacha o destacha sin JavaScript. hecho dice el estado final, así
// que repetirlo no cambia nada.
func (w *web) markPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	id, err := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return
	}
	err = w.svc.store.setDone(r.Context(), who.User, id, r.PostForm.Get("hecho") == "1")
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "tachar", err)
	default:
		back(rw, r, "/#i-"+strconv.FormatInt(id, 10))
	}
}

func (w *web) finishPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	n, err := w.svc.store.finish(r.Context(), who.User, w.now())
	if err != nil {
		w.fail(rw, "terminar", err)
		return
	}
	if n == 0 {
		back(rw, r, "/?ok=nada-tachado")
		return
	}
	back(rw, r, "/?ok=terminado&n="+strconv.Itoa(n))
}

// clearPost vacía la lista; sin «confirmar» primero lo pregunta.
func (w *web) clearPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/?vaciar=1#vaciar")
		return
	}
	if _, err := w.svc.store.clear(r.Context(), who.User); err != nil {
		w.fail(rw, "vaciar", err)
		return
	}
	back(rw, r, "/?ok=vaciada")
}

// ---- Editar una cosa ----

func (w *web) sectionOptions(sel string) []sectionOpt {
	out := make([]sectionOpt, len(sections))
	for i, s := range sections {
		out[i] = sectionOpt{Key: s.Key, Label: s.Label, Selected: s.Key == sel}
	}
	return out
}

func (w *web) itemPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	it, err := w.svc.store.item(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "cosa", err)
		return
	}
	w.renderItem(rw, r, who, w.itemFormOf(it), "")
}

func (w *web) itemFormOf(it item) *itemForm {
	f := &itemForm{ID: it.ID, Name: it.Name, Qty: it.Qty, Note: it.Note, Section: it.Section, Done: it.Done}
	if !it.Added.IsZero() {
		f.Added = w.longDate(it.Added)
	}
	switch it.Source {
	case sourceAssistant:
		f.Source = "Talia"
	case sourceRecipe:
		f.Source = "la receta"
		if it.Recipe != "" {
			f.Source += " «" + it.Recipe + "»"
		}
	}
	return f
}

func (w *web) renderItem(rw http.ResponseWriter, r *http.Request, who app.Identity, f *itemForm, msg string) {
	f.SectionTxt = sectionLabel(f.Section)
	w.render(rw, r, "cosa.html", pageData{Name: who.Name, Tab: "lista", Title: "Editar", Item: f, Options: w.sectionOptions(f.Section), Error: msg})
}

func (w *web) itemPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	in := &itemForm{ID: id, Name: clip(f.Get("nombre"), maxName), Qty: clip(f.Get("cantidad"), maxQty), Note: clip(f.Get("nota"), maxNote), Section: f.Get("seccion")}
	if !validSection(in.Section) {
		http.Error(rw, "Esa sección no existe.", http.StatusBadRequest)
		return
	}
	if q, ok := parseQty(in.Qty); ok {
		in.Qty = q.String()
	}
	if in.Name == "" || nameKey(in.Name) == "" {
		w.renderItem(rw, r, who, in, "Ponle un nombre.")
		return
	}
	err := w.svc.store.update(r.Context(), who.User, id, in.Name, in.Qty, in.Note, in.Section, w.now())
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "editar", err)
	default:
		back(rw, r, "/?ok=guardado#i-"+strconv.FormatInt(id, 10))
	}
}

func (w *web) itemDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	err := w.svc.store.remove(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar", err)
	default:
		back(rw, r, "/?ok=borrada")
	}
}

// ---- Orden de las secciones ----

func orderOptions(order []string) []sectionOpt {
	out := make([]sectionOpt, len(order))
	for i, k := range order {
		out[i] = sectionOpt{Key: k, Label: sectionLabel(k), N: i + 1, First: i == 0, Last: i == len(order)-1}
	}
	return out
}

func (w *web) sectionsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	order, err := w.svc.store.order(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "secciones", err)
		return
	}
	w.render(rw, r, "secciones.html", pageData{Name: who.Name, Tab: "secciones", Order: orderOptions(order), Flash: flash(r)})
}

func (w *web) sectionsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	order, err := w.svc.store.order(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "secciones", err)
		return
	}
	f := r.PostForm
	key := ""
	switch {
	case f.Get("restablecer") != "":
		order = defaultOrder()
	case validSection(f.Get("subir")):
		key = f.Get("subir")
		order = move(order, key, -1)
	case validSection(f.Get("bajar")):
		key = f.Get("bajar")
		order = move(order, key, 1)
	default:
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return
	}
	if err := w.svc.store.setOrder(r.Context(), who.User, order); err != nil {
		w.fail(rw, "secciones", err)
		return
	}
	to := "/secciones?ok=orden"
	if key != "" {
		to += "#sec-" + key
	}
	back(rw, r, to)
}

// move sube (-1) o baja (+1) una sección un puesto.
func move(order []string, key string, delta int) []string {
	out := append([]string(nil), order...)
	for i, k := range out {
		if k != key {
			continue
		}
		j := i + delta
		if j >= 0 && j < len(out) {
			out[i], out[j] = out[j], out[i]
		}
		break
	}
	return out
}
