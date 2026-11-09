package main

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST y sin JavaScript:
// Ahora (lo que toca ver y lo que sale esta semana), Series (con su ficha y
// sus capítulos), Pelis y Juegos. La búsqueda va por POST: lo que busca la
// persona no queda en ninguna URL. Los carteles los sirve la propia app: el
// navegador no se conecta a TVmaze.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 16 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /series detrás de la pasarela
	Name    string
	Tab     string // ahora, series, pelis, juegos
	Title   string
	Flash   string
	Error   string
	Source  string // TVmaze no responde, si es el caso

	// Ahora
	Next   []nextRow
	Caught []nextRow
	Days   []dayGroup
	Busy   []itemRow
	Empty  bool

	// Series
	Query    string
	Searched bool
	Results  []resultRow
	Groups   []showGroup

	// Ficha de una serie
	Show    *showPage
	Seasons []seasonRow
	States  []option
	Ratings []option
	Confirm bool

	// Pelis y juegos
	Kind    string // peli o juego
	Path    string // pelis o juegos
	Items   []itemGroup
	Form    itemForm
	Editing bool
}

type option struct {
	Key, Label string
	Selected   bool
}

type nextRow struct {
	ID        int64
	Name      string
	HasPoster bool
	Code      string
	EpName    string
	EpID      int64
	When      string
	Meta      string
	NewSeason bool
}

type dayGroup struct {
	Label string
	Today bool
	Rows  []airRow
}

type airRow struct {
	ID       int64
	Name     string
	Code     string
	EpName   string
	Time     string
	Premiere string
	Watched  bool
	Aired    bool
}

type resultRow struct {
	TVmazeID                    int64
	Name, Year, Status, Network string
	Genres, Language            string
	HaveID                      int64
}

type showGroup struct {
	Label string
	Rows  []showRow
}

type showRow struct {
	ID        int64
	Name      string
	HasPoster bool
	Status    string
	Progress  string
	Next      string
	Stars     string
}

type showPage struct {
	ID                               int64
	Name, URL, Status, Network       string
	Genres, Years, Runtime, Language string
	Summary                          string
	HasPoster                        bool
	State, StateLabel                string
	Rating                           int
	Stars                            string
	Note                             string
	Watched, Aired, Total, Pct       int
	Next                             string
	NextEpID                         int64
	Upcoming                         string
}

type seasonRow struct {
	Number                int
	Open                  bool
	Watched, Aired, Total int
	AllWatched            bool
	Dates                 string
	Episodes              []epRow
}

type epRow struct {
	ID                   int64
	Code, Name, When     string
	Aired, Watched, Next bool
	Premiere             bool
}

type itemGroup struct {
	Label string
	Rows  []itemRow
}

type itemRow struct {
	ID                       int64
	Path                     string
	Title, Year, Platform    string
	State, StateLabel, Stars string
	Day, Note, Meta          string
	Talia                    bool
}

type itemForm struct {
	ID                                        int64
	Title, Year, Platform, State, Rating, Day string
	Note                                      string
	Talia                                     bool
}

type web struct{ svc *service }

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.nowPage))
	mux.HandleFunc("GET /series", w.user(w.showsPage))
	mux.HandleFunc("POST /buscar", w.user(w.searchPost))
	mux.HandleFunc("POST /seguir", w.user(w.followPost))
	mux.HandleFunc("GET /serie/{id}", w.user(w.showPageGet))
	mux.HandleFunc("POST /serie/{id}", w.user(w.showPost))
	mux.HandleFunc("POST /serie/{id}/visto", w.user(w.watchedPost))
	mux.HandleFunc("POST /serie/{id}/borrar", w.user(w.showDelete))
	mux.HandleFunc("GET /cartel/{id}", w.user(w.posterGet))
	for _, k := range []string{kindFilm, kindGame} {
		kind := k
		p := "/" + kindPath(kind)
		mux.HandleFunc("GET "+p, w.user(w.itemsPage(kind)))
		mux.HandleFunc("POST "+p, w.user(w.itemAdd(kind)))
		mux.HandleFunc("GET "+p+"/{id}", w.user(w.itemPage(kind)))
		mux.HandleFunc("POST "+p+"/{id}", w.user(w.itemPost(kind)))
		mux.HandleFunc("POST "+p+"/{id}/borrar", w.user(w.itemDelete(kind)))
	}
	mux.HandleFunc("GET /api/avisos", w.user(w.notices))
	mux.HandleFunc("POST /interno/hoy", w.internalToday)
}

func kindPath(kind string) string {
	if kind == kindGame {
		return "juegos"
	}
	return "pelis"
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
	d.Version, d.Base = version.Version, base(r)
	if d.Flash == "" {
		d.Flash = flashes[r.URL.Query().Get("ok")]
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Security-Policy", csp)
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("Referrer-Policy", "no-referrer")
	if err := pages.ExecuteTemplate(rw, name, d); err != nil {
		w.svc.log.Error("plantilla", "pagina", name, "err", err)
	}
}

// fail registra un error interno (sin datos de la persona) y lo dice.
func (w *web) fail(rw http.ResponseWriter, op string, err error) {
	w.svc.log.Error("series web", "op", op, "err", err)
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

// back vuelve a una página de la app (la pasarela pone el prefijo).
func back(rw http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(rw, r, to, http.StatusSeeOther)
}

var flashes = map[string]string{
	"siguiendo": "Ya la sigues.", "ya-la-seguias": "Ya la seguías: aquí está.", "guardado": "Guardado.", "borrada": "Ya no la sigues.",
	"visto": "Marcado como visto.", "no-visto": "Desmarcado.", "anadido": "Apuntado.", "actualizado": "Ya lo tenías: puesto al día.",
	"borrado": "Borrado.",
}

// sourceNote dice si TVmaze está fallando (sin detalles internos).
func (w *web) sourceNote() string {
	_, msg, until := w.svc.tv.status()
	if msg == "" {
		return ""
	}
	s := "TVmaze no responde ahora (" + msg + ")"
	if now := w.svc.clock(); until.After(now) {
		s += "; se vuelve a intentar " + strings.Replace(ago(now.Add(-until.Sub(now)), now), "hace", "en", 1)
	}
	return s + "."
}

func stars(n int) string {
	if n <= 0 {
		return ""
	}
	n = min(n, 5)
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

func stateOptions(kind, sel string) []option {
	var out []option
	for _, s := range states {
		out = append(out, option{Key: s, Label: stateLabel(kind, s), Selected: s == sel})
	}
	return out
}

func ratingOptions(sel int) []option {
	out := []option{{Key: "0", Label: "Sin valorar", Selected: sel == 0}}
	for i := 1; i <= 5; i++ {
		out = append(out, option{Key: strconv.Itoa(i), Label: stars(i), Selected: sel == i})
	}
	return out
}

// ---- Ahora ----

func nextOf(sv showView, today time.Time) nextRow {
	row := nextRow{ID: sv.ID, Name: sv.Show.Name, HasPoster: sv.HasPoster}
	p := sv.Prog
	if p.Next != nil {
		row.Code, row.EpName, row.EpID, row.NewSeason = epCode(*p.Next), p.Next.Name, p.Next.ID, p.NextIsNew
		row.Meta = "Te quedan " + plural(p.Left, "capítulo", "capítulos")
		if p.Left == 1 {
			row.Meta = "Es el último que ha salido"
		}
		row.When = "salió " + whenText(*p.Next, today)
		return row
	}
	if p.Upcoming != nil {
		row.Code, row.EpName = epCode(*p.Upcoming), p.Upcoming.Name
		row.When = "sale " + whenText(*p.Upcoming, today)
		row.Meta = "Vas al día"
	} else {
		row.Meta = "Vas al día; aún no hay fecha del siguiente"
		if sv.Show.Status == "Ended" {
			row.Meta = "Vista entera"
		}
	}
	return row
}

func (w *web) nowData(v *view, busy []item) pageData {
	d := pageData{Tab: "ahora"}
	var next []showView
	for _, sv := range v.shows {
		if sv.State != stateWatching {
			continue
		}
		if sv.Prog.Next != nil {
			next = append(next, sv)
		} else {
			d.Caught = append(d.Caught, nextOf(sv, v.today))
		}
	}
	// Lo que acaba de salir, arriba.
	sort.SliceStable(next, func(i, j int) bool {
		a, b := next[i].Prog.Next, next[j].Prog.Next
		return airKey(*a).After(airKey(*b))
	})
	for _, sv := range next {
		d.Next = append(d.Next, nextOf(sv, v.today))
	}
	var cur *dayGroup
	for _, a := range v.week(v.today, v.today.Add(6*dayUnit)) {
		day := a.Episode.day()
		label := longDay(day)
		label = strings.ToUpper(label[:1]) + label[1:]
		switch {
		case day.Equal(v.today):
			label = "Hoy · " + longDay(day)
		case day.Equal(v.today.Add(dayUnit)):
			label = "Mañana · " + longDay(day)
		}
		if cur == nil || cur.Label != label {
			d.Days = append(d.Days, dayGroup{Label: label, Today: day.Equal(v.today)})
			cur = &d.Days[len(d.Days)-1]
		}
		row := airRow{ID: a.View.ID, Name: a.View.Show.Name, Code: epCode(a.Episode), EpName: a.Episode.Name, Premiere: premiereNote(a.Episode),
			Watched: a.View.watched[a.Episode.ID], Aired: a.Episode.aired(v.now)}
		if a.Episode.hasTime() {
			row.Time = a.Episode.At.In(madrid).Format("15:04")
		}
		cur.Rows = append(cur.Rows, row)
	}
	for _, it := range busy {
		d.Busy = append(d.Busy, itemRowOf(it, v.today))
	}
	return d
}

func (w *web) nowPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "ahora", err)
		return
	}
	var busy []item
	for _, k := range []string{kindFilm, kindGame} {
		list, err := w.svc.store.items(r.Context(), who.User, k)
		if err != nil {
			w.fail(rw, "ahora", err)
			return
		}
		for _, it := range list {
			if it.State == stateWatching {
				busy = append(busy, it)
			}
		}
	}
	d := w.nowData(v, busy)
	d.Name = who.Name
	d.Empty = len(v.shows) == 0 && len(busy) == 0
	w.render(rw, r, "ahora.html", d)
}

// ---- Series ----

func showRowOf(sv showView, today time.Time) showRow {
	p := sv.Prog
	row := showRow{ID: sv.ID, Name: sv.Show.Name, HasPoster: sv.HasPoster, Status: showStatus(sv.Show.Status), Stars: stars(sv.Rating)}
	switch {
	case p.Total == 0:
		row.Progress = "Aún sin capítulos"
	case p.Watched == 0:
		row.Progress = plural(p.Aired, "capítulo emitido", "capítulos emitidos")
	default:
		row.Progress = strconv.Itoa(p.Watched) + " de " + strconv.Itoa(p.Aired) + " vistos"
	}
	switch {
	case p.Next != nil && p.Watched > 0:
		row.Next = "Te toca el " + epCode(*p.Next)
	case p.Upcoming != nil:
		row.Next = "El " + epCode(*p.Upcoming) + " sale " + whenText(*p.Upcoming, today)
	}
	return row
}

func groupShows(v *view) []showGroup {
	var out []showGroup
	for _, st := range states {
		g := showGroup{Label: stateLabel(kindShow, st)}
		for _, sv := range v.shows {
			if sv.State == st {
				g.Rows = append(g.Rows, showRowOf(sv, v.today))
			}
		}
		if len(g.Rows) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func (w *web) showsData(who app.Identity, v *view) pageData {
	return pageData{Name: who.Name, Tab: "series", Title: "Series", Groups: groupShows(v), Source: w.sourceNote()}
}

func (w *web) showsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "series", err)
		return
	}
	w.render(rw, r, "series.html", w.showsData(who, v))
}

func (w *web) searchPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "buscar", err)
		return
	}
	d := w.showsData(who, v)
	d.Query, d.Searched = clip(r.PostForm.Get("q"), 80), true
	list, err := w.svc.search(r.Context(), d.Query)
	switch {
	case isInput(err):
		d.Error, d.Searched = err.Error(), false
	case errors.Is(err, errSourceWait), errors.Is(err, errSourceBad):
		d.Error = "TVmaze no responde ahora; prueba más tarde."
	case err != nil:
		w.fail(rw, "buscar", err)
		return
	}
	for _, f := range list {
		row := resultRow{TVmazeID: f.TVmazeID, Name: f.Name, Year: f.Year, Status: showStatus(f.Status), Network: f.Network,
			Genres: strings.Join(f.Genres, ", "), Language: f.Language}
		if sv, ok := v.byTVmaze(f.TVmazeID); ok {
			row.HaveID = sv.ID
		}
		d.Results = append(d.Results, row)
	}
	w.render(rw, r, "series.html", d)
}

func (w *web) followPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	tvID, _ := strconv.ParseInt(r.PostForm.Get("tvmaze"), 10, 64)
	state := r.PostForm.Get("estado")
	f, existed, err := w.svc.followShow(r.Context(), who.User, tvID, state)
	if err != nil {
		msg := ""
		switch {
		case isInput(err):
			msg = err.Error()
		case errors.Is(err, errFull):
			msg = "Has llegado al máximo de series."
		case errors.Is(err, errSourceWait), errors.Is(err, errSourceBad):
			msg = "TVmaze no responde ahora; prueba más tarde."
		default:
			w.fail(rw, "seguir", err)
			return
		}
		v, lerr := w.svc.load(r.Context(), who.User)
		if lerr != nil {
			w.fail(rw, "seguir", lerr)
			return
		}
		d := w.showsData(who, v)
		d.Error = msg
		w.render(rw, r, "series.html", d)
		return
	}
	ok := "siguiendo"
	if existed {
		ok = "ya-la-seguias"
	}
	back(rw, r, "/serie/"+idStr(f.ID)+"?ok="+ok)
}

// ---- Ficha de una serie ----

func (w *web) showPageData(who app.Identity, v *view, sv showView) pageData {
	sh, p := sv.Show, sv.Prog
	sp := &showPage{ID: sv.ID, Name: sh.Name, URL: sh.URL, Status: showStatus(sh.Status), Network: sh.Network, Genres: strings.Join(sh.Genres, ", "),
		Language: sh.Language, Summary: sh.Summary, HasPoster: sv.HasPoster, State: sv.State, StateLabel: stateLabel(kindShow, sv.State),
		Rating: sv.Rating, Stars: stars(sv.Rating), Note: sv.Note, Watched: p.Watched, Aired: p.Aired, Total: p.Total}
	if sh.Premiered != "" {
		sp.Years = sh.Premiered[:4]
		switch {
		case sh.Ended != "" && sh.Ended[:4] != sp.Years:
			sp.Years += "–" + sh.Ended[:4]
		case sh.Ended == "" && sh.Status == "Running":
			sp.Years += "–"
		}
	}
	if sh.Runtime > 0 {
		sp.Runtime = strconv.Itoa(sh.Runtime) + " min"
	}
	if p.Aired > 0 {
		sp.Pct = p.Watched * 100 / max(p.Aired, p.Watched)
	}
	if p.Next != nil {
		sp.Next, sp.NextEpID = epCode(*p.Next)+label(p.Next.Name), p.Next.ID
	}
	if p.Upcoming != nil {
		sp.Upcoming = "El " + epCode(*p.Upcoming) + " sale " + whenText(*p.Upcoming, v.today)
		if n := premiereNote(*p.Upcoming); n != "" {
			sp.Upcoming += " (" + n + ")"
		}
	}
	d := pageData{Name: who.Name, Tab: "series", Title: sh.Name, Show: sp, States: stateOptions(kindShow, sv.State), Ratings: ratingOptions(sv.Rating)}
	// Abierta: la temporada del siguiente por ver (o la última).
	openSeason := 0
	if p.Next != nil {
		openSeason = p.Next.Season
	} else if s := sh.seasons(); len(s) > 0 {
		openSeason = s[len(s)-1]
	}
	for _, e := range sh.Episodes {
		if len(d.Seasons) == 0 || d.Seasons[len(d.Seasons)-1].Number != e.Season {
			d.Seasons = append(d.Seasons, seasonRow{Number: e.Season, Open: e.Season == openSeason})
		}
		sr := &d.Seasons[len(d.Seasons)-1]
		aired, seen := e.aired(v.now), sv.watched[e.ID]
		sr.Total++
		if aired {
			sr.Aired++
		}
		if seen {
			sr.Watched++
		}
		row := epRow{ID: e.ID, Code: epCode(e), Name: e.Name, When: whenText(e, v.today), Aired: aired, Watched: seen,
			Next: p.Next != nil && p.Next.ID == e.ID, Premiere: e.premiere()}
		if row.When == "sin fecha" {
			row.When = "sin fecha aún"
		}
		sr.Episodes = append(sr.Episodes, row)
	}
	for i := range d.Seasons {
		sr := &d.Seasons[i]
		sr.AllWatched = sr.Aired > 0 && sr.Watched >= sr.Aired
		var first, last time.Time
		for _, e := range sh.Episodes {
			if e.Season != sr.Number || e.day().IsZero() {
				continue
			}
			if first.IsZero() {
				first = e.day()
			}
			last = e.day()
		}
		if !first.IsZero() {
			sr.Dates = shortDate(first, time.Time{})
			if !last.Equal(first) {
				sr.Dates += " – " + shortDate(last, time.Time{})
			}
		}
	}
	return d
}

func label(name string) string {
	if name == "" {
		return ""
	}
	return " «" + name + "»"
}

func (w *web) renderShow(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, msg string) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "ficha", err)
		return
	}
	sv, ok := v.show(id)
	if !ok {
		notFound(rw)
		return
	}
	d := w.showPageData(who, v, sv)
	d.Error = msg
	d.Confirm = r.URL.Query().Get("borrar") == "1"
	w.render(rw, r, "serie.html", d)
}

func (w *web) showPageGet(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	w.renderShow(rw, r, who, id, "")
}

func (w *web) showPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	f := r.PostForm
	rating, _ := strconv.Atoi(f.Get("valoracion"))
	_, err := w.svc.setMeta(r.Context(), who.User, id, f.Get("estado"), rating, f.Get("nota"))
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderShow(rw, r, who, id, err.Error())
	case err != nil:
		w.fail(rw, "guardar serie", err)
	default:
		back(rw, r, "/serie/"+idStr(id)+"?ok=guardado")
	}
}

// parseAction lee «uno:123», «hasta:123», «quitar:123», «temporada:3» o
// «sin-temporada:3».
func parseAction(s string) (mode string, epID int64, season int, on bool, ok bool) {
	verb, arg, found := strings.Cut(s, ":")
	n, err := strconv.ParseInt(arg, 10, 64)
	if !found || err != nil || n <= 0 {
		return "", 0, 0, false, false
	}
	switch verb {
	case "uno":
		return markOne, n, 0, true, true
	case "hasta":
		return markUpTo, n, 0, true, true
	case "quitar":
		return markOne, n, 0, false, true
	case "temporada", "sin-temporada":
		if n > 1000 {
			return "", 0, 0, false, false
		}
		return markSeason, 0, int(n), verb == "temporada", true
	}
	return "", 0, 0, false, false
}

func (w *web) watchedPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	mode, epID, season, on, ok := parseAction(r.PostForm.Get("accion"))
	if !ok {
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return
	}
	_, _, err := w.svc.markWatched(r.Context(), who.User, id, mode, epID, season, on)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case isInput(err):
		w.renderShow(rw, r, who, id, err.Error())
		return
	case err != nil:
		w.fail(rw, "marcar visto", err)
		return
	}
	okKey := "visto"
	if !on {
		okKey = "no-visto"
	}
	if r.PostForm.Get("desde") == "ahora" {
		back(rw, r, "/?ok="+okKey)
		return
	}
	anchor := ""
	if season > 0 {
		anchor = "#t" + strconv.Itoa(season)
	} else if s, err := w.seasonOf(r, who, id, epID); err == nil && s > 0 {
		anchor = "#t" + strconv.Itoa(s)
	}
	back(rw, r, "/serie/"+idStr(id)+"?ok="+okKey+anchor)
}

// seasonOf es la temporada de un capítulo (para volver a ella).
func (w *web) seasonOf(r *http.Request, who app.Identity, id, epID int64) (int, error) {
	f, err := w.svc.store.follow(r.Context(), who.User, id)
	if err != nil {
		return 0, err
	}
	got, err := w.svc.showsFor(r.Context(), []int64{f.ShowID})
	if err != nil || got[f.ShowID] == nil {
		return 0, err
	}
	e, _ := got[f.ShowID].episode(epID)
	return e.Season, nil
}

func (w *web) showDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		if _, err := w.svc.store.follow(r.Context(), who.User, id); err != nil {
			notFound(rw)
			return
		}
		back(rw, r, "/serie/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	err := w.svc.unfollow(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "dejar de seguir", err)
	default:
		back(rw, r, "/series?ok=borrada")
	}
}

// posterGet sirve el cartel de una serie que sigue la persona (por el id de
// su seguimiento).
func (w *web) posterGet(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	img, err := w.svc.store.poster(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "cartel", err)
		return
	}
	rw.Header().Set("Content-Type", "image/jpeg")
	rw.Header().Set("Cache-Control", "private, no-store")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Content-Security-Policy", "default-src 'none'")
	rw.Write(img)
}

// ---- Pelis y juegos ----

func itemRowOf(it item, today time.Time) itemRow {
	row := itemRow{ID: it.ID, Path: kindPath(it.Kind), Title: it.Title, Platform: it.Platform, State: it.State,
		StateLabel: stateLabel(it.Kind, it.State), Stars: stars(it.Rating), Note: it.Note, Talia: it.Origin == "asistente"}
	if it.Year > 0 {
		row.Year = strconv.Itoa(it.Year)
	}
	if d, err := parseDay(it.Day); err == nil && it.Day != "" {
		row.Day = shortDate(d, today)
	}
	var meta []string
	for _, s := range []string{row.Platform, row.Day, row.Note} {
		if s != "" {
			meta = append(meta, s)
		}
	}
	if row.Talia {
		meta = append(meta, "con Talia")
	}
	row.Meta = strings.Join(meta, " · ")
	return row
}

func (w *web) itemsData(r *http.Request, who app.Identity, kind string) (pageData, error) {
	list, err := w.svc.store.items(r.Context(), who.User, kind)
	if err != nil {
		return pageData{}, err
	}
	return itemsPageData(who, kind, list, dayOf(w.svc.clock())), nil
}

// itemsPageData es la lista de pelis o juegos, por estados.
func itemsPageData(who app.Identity, kind string, list []item, today time.Time) pageData {
	title := "Pelis"
	if kind == kindGame {
		title = "Juegos"
	}
	d := pageData{Name: who.Name, Tab: kindPath(kind), Title: title, Kind: kind, Path: kindPath(kind), States: stateOptions(kind, statePending),
		Ratings: ratingOptions(0), Form: itemForm{State: statePending}}
	sortItems(list)
	for _, st := range states {
		g := itemGroup{Label: stateLabel(kind, st)}
		for _, it := range list {
			if it.State == st {
				g.Rows = append(g.Rows, itemRowOf(it, today))
			}
		}
		if len(g.Rows) > 0 {
			d.Items = append(d.Items, g)
		}
	}
	return d
}

func (w *web) itemsPage(kind string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		d, err := w.itemsData(r, who, kind)
		if err != nil {
			w.fail(rw, "lista", err)
			return
		}
		w.render(rw, r, "items.html", d)
	}
}

// readItem lee el formulario de una peli o un juego.
func readItem(r *http.Request) (itemForm, itemInput, error) {
	f := r.PostForm
	fm := itemForm{Title: f.Get("titulo"), Year: strings.TrimSpace(f.Get("anio")), Platform: f.Get("plataforma"), State: f.Get("estado"),
		Rating: f.Get("valoracion"), Day: f.Get("fecha"), Note: f.Get("nota")}
	in := itemInput{Title: fm.Title, Platform: fm.Platform, State: fm.State, Day: fm.Day, Note: fm.Note, Origin: "web"}
	if fm.Year != "" {
		y, err := strconv.Atoi(fm.Year)
		if err != nil {
			return fm, in, bad("El año va con cuatro cifras.")
		}
		in.Year = y
	}
	in.Rating, _ = strconv.Atoi(fm.Rating)
	if in.State == "" {
		in.State = statePending
	}
	return fm, in, nil
}

func (w *web) itemAdd(kind string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		if !form(rw, r) {
			return
		}
		fm, in, err := readItem(r)
		if err == nil {
			_, _, err = w.svc.addItem(r.Context(), who.User, kind, in, false)
			if err == nil {
				back(rw, r, "/"+kindPath(kind)+"?ok=anadido")
				return
			}
		}
		if !isInput(err) && !errors.Is(err, errFull) {
			w.fail(rw, "apuntar", err)
			return
		}
		d, lerr := w.itemsData(r, who, kind)
		if lerr != nil {
			w.fail(rw, "apuntar", lerr)
			return
		}
		d.Error = err.Error()
		if errors.Is(err, errFull) {
			d.Error = "Has llegado al máximo."
		}
		d.Form = fm
		rating, _ := strconv.Atoi(fm.Rating)
		d.States, d.Ratings = stateOptions(kind, fm.State), ratingOptions(rating)
		w.render(rw, r, "items.html", d)
	}
}

func (w *web) renderItem(rw http.ResponseWriter, r *http.Request, who app.Identity, kind string, it item, fm *itemForm, msg string) {
	d := itemPageData(who, kind, it, fm)
	d.Error, d.Confirm = msg, r.URL.Query().Get("borrar") == "1"
	w.render(rw, r, "item.html", d)
}

// itemPageData es la ficha de una peli o un juego (con lo escrito, si hay
// un error).
func itemPageData(who app.Identity, kind string, it item, fm *itemForm) pageData {
	if fm == nil {
		fm = &itemForm{ID: it.ID, Title: it.Title, Platform: it.Platform, State: it.State, Day: it.Day, Note: it.Note, Talia: it.Origin == "asistente"}
		if it.Year > 0 {
			fm.Year = strconv.Itoa(it.Year)
		}
		fm.Rating = strconv.Itoa(it.Rating)
	}
	rating, _ := strconv.Atoi(fm.Rating)
	title := it.Title
	if title == "" {
		title = fm.Title
	}
	return pageData{Name: who.Name, Tab: kindPath(kind), Title: title, Kind: kind, Path: kindPath(kind), Form: *fm, Editing: true,
		States: stateOptions(kind, fm.State), Ratings: ratingOptions(rating)}
}

// ownItem lee una peli o un juego de la persona, del tipo de la ruta.
func (w *web) ownItem(rw http.ResponseWriter, r *http.Request, who app.Identity, kind string) (item, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return item{}, false
	}
	it, err := w.svc.store.item(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) || (err == nil && it.Kind != kind) {
		notFound(rw)
		return item{}, false
	}
	if err != nil {
		w.fail(rw, "ficha", err)
		return item{}, false
	}
	return it, true
}

func (w *web) itemPage(kind string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		it, ok := w.ownItem(rw, r, who, kind)
		if !ok {
			return
		}
		w.renderItem(rw, r, who, kind, it, nil, "")
	}
}

func (w *web) itemPost(kind string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		it, ok := w.ownItem(rw, r, who, kind)
		if !ok || !form(rw, r) {
			return
		}
		fm, in, err := readItem(r)
		if err == nil {
			_, err = w.svc.updateItem(r.Context(), who.User, it.ID, in)
			if err == nil {
				back(rw, r, "/"+kindPath(kind)+"/"+idStr(it.ID)+"?ok=guardado")
				return
			}
		}
		if errors.Is(err, errNotFound) {
			notFound(rw)
			return
		}
		if !isInput(err) {
			w.fail(rw, "guardar", err)
			return
		}
		fm.ID, fm.Talia = it.ID, it.Origin == "asistente"
		w.renderItem(rw, r, who, kind, it, &fm, err.Error())
	}
}

func (w *web) itemDelete(kind string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		it, ok := w.ownItem(rw, r, who, kind)
		if !ok || !form(rw, r) {
			return
		}
		p := "/" + kindPath(kind) + "/" + idStr(it.ID)
		if r.PostForm.Get("confirmar") != "1" {
			back(rw, r, p+"?borrar=1#borrar")
			return
		}
		if err := w.svc.store.deleteItem(r.Context(), who.User, it.ID); err != nil {
			w.fail(rw, "borrar", err)
			return
		}
		back(rw, r, "/"+kindPath(kind)+"?ok=borrado")
	}
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
// sin pasar por la pasarela): las líneas del resumen de la mañana. El
// usuario va en el cuerpo, no en la URL.
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
