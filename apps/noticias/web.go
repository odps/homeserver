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

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor, sin JavaScript: el digest de hoy, los
// días anteriores y los ajustes (intereses, feeds propios y medios).

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"ago":     ago,
	"longDay": longDay,
	"plus":    func(n int) string { return fmt.Sprintf("y %d más", n) },
	"join":    strings.Join,
	"has":     func(list []string, s string) bool { return slices.Contains(list, s) },
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
}

type generalView struct {
	generalSource
	Enabled bool
	State   feedState
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
	states, err3 := w.svc.store.feedStates(ctx)
	data := pageData{Name: who.Name, Tab: "ajustes", Prefs: p, Topics: topics, Feeds: states, Flash: flash(r), Error: errMsg}
	if err := errors.Join(err1, err2, err3); err != nil && errMsg == "" {
		data.Error = err.Error()
	}
	for _, g := range generalSources {
		data.General = append(data.General, generalView{generalSource: g, Enabled: !slices.Contains(p.Disabled, g.Key), State: states[g.URL]})
	}
	if errMsg != "" {
		rw.WriteHeader(http.StatusBadRequest)
	}
	w.render(rw, r, "settings.html", data)
}

func back(rw http.ResponseWriter, r *http.Request, to, ok string) {
	http.Redirect(rw, r, base(r)+to+"?ok="+ok, http.StatusSeeOther)
}

func (w *web) savePrefs(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	p, err := w.svc.store.prefs(r.Context(), who.User)
	if err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	at := strings.TrimSpace(r.FormValue("digest_at"))
	top, err1 := strconv.Atoi(r.FormValue("top_count"))
	per, err2 := strconv.Atoi(r.FormValue("topic_count"))
	if t, err := time.Parse("15:04", at); err != nil || errors.Join(err1, err2) != nil || top < 3 || top > 30 || per < 1 || per > 15 {
		w.renderSettings(rw, r, who, "Revisa los ajustes: la hora como 07:00, de 3 a 30 noticias generales y de 1 a 15 por interés.")
		return
	} else {
		at = t.Format("15:04")
	}
	p.DigestAt, p.TopCount, p.TopicCount, p.AISummary = at, top, per, r.FormValue("ai_summary") != ""
	p.Disabled = []string{}
	for _, g := range generalSources {
		if r.FormValue("src_"+g.Key) == "" {
			p.Disabled = append(p.Disabled, g.Key)
		}
	}
	if err := w.svc.store.savePrefs(r.Context(), who.User, p); err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "ajustes")
}

func topicInput(r *http.Request) (string, string, error) {
	name := cleanText(r.FormValue("name"), 60)
	query := cleanText(r.FormValue("query"), 120)
	if name == "" {
		return "", "", errors.New("ponle un nombre al interés")
	}
	return name, query, nil
}

func (w *web) addTopic(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	name, query, err := topicInput(r)
	if err == nil {
		err = w.svc.store.addTopic(r.Context(), who.User, name, query)
		if err != nil && strings.Contains(err.Error(), "duplicate") {
			err = errors.New("ya tienes un interés con ese nombre")
		}
	}
	if err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	go w.svc.collect(context.WithoutCancel(r.Context())) // para tener ya sus noticias
	back(rw, r, "/ajustes", "tema")
}

func (w *web) editTopic(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if r.FormValue("action") == "delete" {
		w.svc.store.deleteTopic(r.Context(), who.User, id)
		back(rw, r, "/ajustes", "borrado")
		return
	}
	name, query, err := topicInput(r)
	if err == nil {
		err = w.svc.store.updateTopic(r.Context(), who.User, id, name, query)
	}
	if err != nil {
		w.renderSettings(rw, r, who, err.Error())
		return
	}
	go w.svc.collect(context.WithoutCancel(r.Context()))
	back(rw, r, "/ajustes", "tema")
}

func (w *web) addFeed(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := validFeedURL(r.FormValue("url"), w.svc.fetch.allowHTTP)
	if err == nil {
		// Se comprueba antes de guardarlo: mejor saber ya que no es un feed.
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		var f parsedFeed
		f, err = w.svc.fetch.fetch(ctx, u)
		cancel()
		if err != nil {
			err = fmt.Errorf("no se ha podido leer el feed: %v", err)
		} else {
			w.svc.store.feedChecked(r.Context(), u, f.Title, nil)
			for i := range f.Items {
				if f.Items[i].Source == "" {
					f.Items[i].Source = f.Title
				}
			}
			w.svc.store.saveItems(r.Context(), u, f.Items)
			err = w.svc.store.addOwnFeed(r.Context(), who.User, id, u)
		}
	}
	if err != nil {
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
