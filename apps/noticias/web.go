package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor, sin JavaScript: el digest de hoy, los
// días anteriores y los ajustes (intereses, feeds propios y medios).

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"ago":       ago,
	"longDay":   longDay,
	"plus":      func(n int) string { return fmt.Sprintf("y %d más", n) },
	"join":      strings.Join,
	"has":       func(list []string, s string) bool { return slices.Contains(list, s) },
	"inc":       func(n int) int { return n + 1 },
	"twodigits": func(n int) string { return fmt.Sprintf("%02d", n) },
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type pageData struct {
	Version string
	Base    string // /noticias detrás de la pasarela
	Name    string
	Tab     string
	Flash   string
	Error   string
	Digest  digest
	Days    []string
	Today   bool
	Prefs   prefs
	Topics  []topic
	General []generalView
	Feeds   map[string]feedState
	Themes  []presetView // categorías
	Social  []presetView // redes y tendencias
	Reddit  string
}

type generalView struct {
	generalSource
	Enabled bool
	State   feedState
}

type presetView struct {
	Key, Name, Hint string
	Enabled         bool
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	}
	return t.In(madrid).Format("02/01")
}

var weekdays = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
var months = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}

func longDay(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return fmt.Sprintf("%s, %d de %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
}

type web struct {
	svc *service
}

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.home))
	mux.HandleFunc("GET /dia/{dia}", w.user(w.day))
	mux.HandleFunc("GET /dias", w.user(w.daysPage))
	mux.HandleFunc("GET /ajustes", w.user(w.settings))
	mux.HandleFunc("POST /ajustes", w.user(w.savePrefs))
	mux.HandleFunc("POST /temas", w.user(w.addTopic))
	mux.HandleFunc("POST /temas/{id}", w.user(w.editTopic))
	mux.HandleFunc("POST /temas/{id}/feeds", w.user(w.addFeed))
	mux.HandleFunc("POST /feeds/{id}/borrar", w.user(w.deleteFeed))
	mux.HandleFunc("POST /rehacer", w.user(w.rebuild))
	mux.HandleFunc("GET /api/digest/texto", w.user(w.digestText))
}

// user exige la identidad que pone la pasarela.
func (w *web) user(h func(http.ResponseWriter, *http.Request, app.Identity)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		who := app.User(r)
		if !who.IsUser() {
			http.Error(rw, "entra desde la pasarela", http.StatusUnauthorized)
			return
		}
		if err := w.svc.store.ensureUser(r.Context(), who.User); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		h(rw, r, who)
	}
}

func base(r *http.Request) string { return strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/") }

func (w *web) render(rw http.ResponseWriter, r *http.Request, name string, d pageData) {
	d.Version, d.Base = version.Version, base(r)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Security-Policy", csp)
	rw.Header().Set("Cache-Control", "no-store")
	if err := pages.ExecuteTemplate(rw, name, d); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}
}

func flash(r *http.Request) string {
	return map[string]string{"ajustes": "Ajustes guardados.", "tema": "Interés guardado.", "feed": "Feed añadido.",
		"borrado": "Borrado.", "rehecho": "Digest de hoy preparado de nuevo con lo último."}[r.URL.Query().Get("ok")]
}

func (w *web) home(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	d, err := w.svc.today(ctx, who.User, time.Now())
	data := pageData{Name: who.Name, Tab: "hoy", Digest: d, Today: true, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "digest.html", data)
}

func (w *web) day(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	day := r.PathValue("dia")
	if _, err := time.Parse("2006-01-02", day); err != nil {
		http.NotFound(rw, r)
		return
	}
	d, err := w.svc.store.digest(r.Context(), who.User, day)
	if errors.Is(err, errNotFound) {
		http.NotFound(rw, r)
		return
	}
	data := pageData{Name: who.Name, Tab: "dias", Digest: d}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "digest.html", data)
}

func (w *web) daysPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	days, err := w.svc.store.days(r.Context(), who.User, 60)
	data := pageData{Name: who.Name, Tab: "dias", Days: days}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "days.html", data)
}

func (w *web) settings(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderSettings(rw, r, who, "")
}

func (w *web) renderSettings(rw http.ResponseWriter, r *http.Request, who app.Identity, errMsg string) {
	ctx := r.Context()
	p, err1 := w.svc.store.prefs(ctx, who.User)
	topics, err2 := w.svc.store.topics(ctx, who.User)
	states, err3 := w.svc.store.feedStates(ctx, knownFeeds(topics))
	data := pageData{Name: who.Name, Tab: "ajustes", Prefs: p, Topics: topics, Feeds: states, Flash: flash(r), Error: errMsg}
	if err := errors.Join(err1, err2, err3); err != nil && errMsg == "" {
		data.Error = err.Error()
	}
	for _, g := range generalSources {
		data.General = append(data.General, generalView{generalSource: g, Enabled: !slices.Contains(p.Disabled, g.Key), State: states[g.URL]})
	}
	for _, pr := range presets {
		v := presetView{Key: pr.Key, Name: pr.Name, Hint: pr.Hint, Enabled: slices.Contains(p.Presets, pr.Key)}
		if pr.Group == "redes" {
			data.Social = append(data.Social, v)
		} else {
			data.Themes = append(data.Themes, v)
		}
	}
	for _, r := range p.Reddit {
		data.Reddit += "r/" + r + " "
	}
	data.Reddit = strings.TrimSpace(data.Reddit)
	if errMsg != "" {
		rw.WriteHeader(http.StatusBadRequest)
	}
	w.render(rw, r, "settings.html", data)
}

// back redirige dentro de la app. La pasarela ya antepone /noticias a las
// redirecciones que empiezan por «/»: aquí no se añade.
func back(rw http.ResponseWriter, r *http.Request, to, ok string) {
	http.Redirect(rw, r, to+"?ok="+ok, http.StatusSeeOther)
}

func (w *web) savePrefs(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	p, err := w.svc.store.prefs(r.Context(), who.User)
	if err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	// El formulario de ajustes va por partes (secciones, medios, digest):
	// cada una manda solo sus campos y «part» dice cuál es.
	switch r.FormValue("part") {
	case "secciones":
		p.Presets = []string{}
		for _, pr := range presets {
			if r.FormValue("preset_"+pr.Key) != "" {
				p.Presets = append(p.Presets, pr.Key)
			}
		}
		subs, bad := subreddits(strings.FieldsFunc(r.FormValue("reddit"), func(c rune) bool { return c == ',' || unicode.IsSpace(c) }))
		if len(bad) > 0 {
			w.renderSettings(rw, r, who, "Estos subforos no tienen un nombre válido: "+strings.Join(bad, ", "))
			return
		}
		p.Reddit = subs[:min(len(subs), maxSubreddits)]
		p.World = r.FormValue("world") != ""
	case "medios":
		p.Disabled = []string{}
		for _, g := range generalSources {
			if r.FormValue("src_"+g.Key) == "" {
				p.Disabled = append(p.Disabled, g.Key)
			}
		}
	default:
		var err1, err2, err3 error
		p.DigestAt = r.FormValue("digest_at")
		p.TopCount, err1 = strconv.Atoi(r.FormValue("top_count"))
		p.WorldCount, err2 = strconv.Atoi(r.FormValue("world_count"))
		p.TopicCount, err3 = strconv.Atoi(r.FormValue("topic_count"))
		p.AISummary = r.FormValue("ai_summary") != ""
		if errors.Join(err1, err2, err3) != nil {
			w.renderSettings(rw, r, who, errPrefs.Error())
			return
		}
	}
	if err := p.check(); err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	if err := w.svc.store.savePrefs(r.Context(), who.User, p); err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "ajustes")
}

func (w *web) addTopic(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if _, err := w.svc.addTopic(r.Context(), who.User, r.FormValue("name"), r.FormValue("query")); err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "tema")
}

func (w *web) editTopic(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if r.FormValue("action") == "delete" {
		w.svc.store.deleteTopic(r.Context(), who.User, id)
		back(rw, r, "/ajustes", "borrado")
		return
	}
	if err := w.svc.updateTopic(r.Context(), who.User, id, r.FormValue("name"), r.FormValue("query")); err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "tema")
}

func (w *web) addFeed(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := w.svc.addFeed(r.Context(), who.User, id, r.FormValue("url")); err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "feed")
}

func (w *web) deleteFeed(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	w.svc.store.deleteOwnFeed(r.Context(), who.User, id)
	back(rw, r, "/ajustes", "borrado")
}

func (w *web) rebuild(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	w.svc.collect(ctx)
	if _, err := w.svc.prepare(ctx, who.User, time.Now()); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	back(rw, r, "/", "rehecho")
}

// digestText es el digest de hoy en texto (para los avisos de Telegram).
func (w *web) digestText(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	per, _ := strconv.Atoi(r.URL.Query().Get("por_seccion"))
	if per <= 0 || per > 20 {
		per = 5
	}
	d, err := w.svc.today(r.Context(), who.User, time.Now())
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(rw, plainDigest(d, per))
}
