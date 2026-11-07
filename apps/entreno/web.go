package main

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor, sin JavaScript: las últimas sesiones,
// apuntar, cada sesión, el historial de cada ejercicio (con su gráfica en
// SVG), la importación del chat de WhatsApp y las rutinas. La única con
// JavaScript es el modo gimnasio (web_gym.go), y funciona igual sin él.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"muscle": func(m string) string { return strings.ToLower(muscleLabel(m)) },
	"join":   strings.Join,
	"add":    func(a, b int) int { return a + b },
	"lines": func(s string) []string { // un párrafo por línea
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	},
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const (
	maxText    = 20000   // caracteres del texto a apuntar
	maxUpload  = 5 << 20 // fichero subido
	maxUnzip   = 20 << 20
	maxUnknown = 60 // nombres sin reconocer en un formulario
)

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version  string
	Base     string // /entreno detrás de la pasarela
	Name     string
	Tab      string // inicio, gimnasio, rutinas, ejercicios, cuerpo, comida, apuntar, importar
	Title    string // título de la página si no basta con la pestaña (Cardio)
	Script   bool   // carga gimnasio.js (con su CSP)
	Flash    string
	Error    string
	Today    string // AAAA-MM-DD
	Tomorrow string // la fecha más lejana que se deja apuntar

	// Inicio: sesiones y cardio, mezclados por fecha
	Sessions []sessionCard
	Hoy      *todayCard // «Hoy toca»
	BodyCard *bodyCard
	FoodCard *foodCard

	// Cuerpo, el formulario de un día y cada foto
	Body  *bodyView
	Day   *bodyDayForm
	Photo *photoPage

	// Cardio
	Cardio *cardioPage

	// Progreso
	Progress *progressPage

	// Comida: el día, buscar, Open Food Facts, los habituales y los objetivos
	Nutri  *nutriPage
	Search *foodSearch
	OFF    *offPage
	Foods  *foodsPage
	Food   *foodForm
	Goals  *goalsForm

	// Sesión
	Session sessionDetail
	Confirm bool // pide confirmar el borrado

	// Apuntar
	Text      string
	Date      string
	Unknown   []unknownRow
	Partial   bool     // lo reconocido ya está apuntado
	Saved     []string // lo apuntado, «Nombre — series»
	SessionID int64
	Groups    []optGroup

	// Ejercicios
	Stats    []statRow
	Query    string
	Results  []catalogRow
	Searched bool

	// Ejercicio
	Exercise historyView
	Move     *moveView // pasar a otro ejercicio

	// Importar
	Preview *previewView
	Done    *importDone

	// Rutinas
	Routines []routineView
	Suggest  []routineView
	Form     *routineForm

	// Gimnasio
	Gym *gymView
}

type sessionCard struct {
	ID     int64
	Date   string
	Source string
	Lines  []exLine
	Notes  []string
	Cardio string // si no está vacío, es una sesión de cardio: «Correr — 5.2 km en 28:30 · …»

	day string    // AAAA-MM-DD, para ordenar
	at  time.Time //
}

type exLine struct {
	Key, Name, Summary string
}

type sessionDetail struct {
	ID        int64
	Date      string // «martes, 3 de noviembre»
	Time      string
	Source    string
	Notes     []string
	Exercises []exerciseDetail
}

type exerciseDetail struct {
	Key     string
	Name    string
	Summary string
	Unit    string // Reps, Segundos o Metros
	PerSide bool
	HasKg   bool
	HasRIR  bool
	Sets    []setRow
	Note    string
	Best    string // 1RM estimado
}

type setRow struct {
	N             int
	Amount, Kg, R string
}

// unknownRow es un nombre sin reconocer, con lo elegido para él.
type unknownRow struct {
	Index int
	Name  string
	Count int
	Sel   string
}

type optGroup struct {
	Label   string
	Options []option
}

type option struct{ Key, Name string }

type statRow struct {
	Key, Name string
	Times     int
	Last      string
}

type historyView struct {
	Key     string
	Name    string
	Detail  *exDetail // ficha: explicación, imágenes y fuentes
	Muscles string
	Metric  string // qué enseña la gráfica
	Record  string
	Rows    []historyLine
	Chart   *chartView
}

type historyLine struct {
	SessionID int64
	Date      string
	Series    string
	E1RM      string
	Note      string
}

type previewView struct {
	ID       string
	Count    int
	From, To string
	Already  int
	Sample   []sampleRow
	More     int
	Unknown  []unknownRow
}

type sampleRow struct {
	Date  string
	Names []nameTag
}

type nameTag struct {
	Name    string
	Unknown bool
}

type importDone struct{ Saved, Skipped int }

type web struct {
	svc    *service
	images *imageCache
}

func (w *web) routes(mux *http.ServeMux) {
	if w.images == nil {
		w.images = newImageCache()
	}
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.home))
	mux.HandleFunc("GET /progreso", w.user(w.progressPage))
	mux.HandleFunc("GET /apuntar", w.user(w.recordForm))
	mux.HandleFunc("POST /apuntar", w.user(w.recordPost))
	mux.HandleFunc("POST /alias", w.user(w.aliasPost))
	mux.HandleFunc("GET /sesion/{id}", w.user(w.sessionPage))
	mux.HandleFunc("POST /sesion/{id}/borrar", w.user(w.deleteSession))
	mux.HandleFunc("GET /ejercicios", w.user(w.exercisesPage))
	mux.HandleFunc("GET /ejercicio/{key}", w.user(w.exercisePage))
	mux.HandleFunc("GET /ejercicio/{key}/cambiar", w.user(w.movePage))
	mux.HandleFunc("POST /ejercicio/{key}/cambiar", w.user(w.movePost))
	mux.HandleFunc("GET /img/{key}/{n}", w.user(w.imagePage))
	mux.HandleFunc("GET /importar", w.user(w.importPage))
	mux.HandleFunc("POST /importar", w.user(w.importUpload))
	mux.HandleFunc("POST /importar/{id}", w.user(w.importConfirm))
	mux.HandleFunc("GET /api/hoy/texto", w.user(w.todayText))
	mux.HandleFunc("GET /rutinas", w.user(w.routinesPage))
	mux.HandleFunc("GET /rutinas/nueva", w.user(w.routineNew))
	mux.HandleFunc("GET /rutinas/detectar", w.user(w.detectPage))
	mux.HandleFunc("POST /rutinas/detectar", w.user(w.detectSave))
	mux.HandleFunc("GET /rutina/{id}", w.user(w.routineEdit))
	mux.HandleFunc("POST /rutina/{id}", w.user(w.routineSave))
	mux.HandleFunc("POST /rutina/{id}/borrar", w.user(w.routineDelete))
	mux.HandleFunc("POST /rutinas/agenda", w.user(w.agendaPost))
	mux.HandleFunc("POST /interno/hoy", w.internalToday)
	mux.HandleFunc("GET /gimnasio", w.user(w.gymPage))
	mux.HandleFunc("POST /gimnasio/serie", w.user(w.gymSetPost))
	mux.HandleFunc("POST /api/serie", w.user(w.apiSet))
	mux.HandleFunc("GET /cuerpo", w.user(w.bodyPage))
	mux.HandleFunc("POST /cuerpo/peso", w.user(w.weightPost))
	mux.HandleFunc("GET /cuerpo/dia", w.user(w.bodyDayPage))
	mux.HandleFunc("POST /cuerpo/dia", w.user(w.bodyDayPost))
	mux.HandleFunc("POST /cuerpo/dia/borrar", w.user(w.bodyDayDelete))
	mux.HandleFunc("POST /fotos", w.user(w.photoUpload))
	mux.HandleFunc("GET /fotos/{id}", w.user(w.photoView))
	mux.HandleFunc("POST /fotos/{id}/borrar", w.user(w.photoDelete))
	mux.HandleFunc("GET /foto/{id}", w.user(w.photoFile(false)))
	mux.HandleFunc("GET /foto/{id}/mini", w.user(w.photoFile(true)))
	mux.HandleFunc("GET /cardio", w.user(w.cardioPage))
	mux.HandleFunc("POST /cardio", w.user(w.cardioPost))
	mux.HandleFunc("POST /cardio/{id}/borrar", w.user(w.cardioDelete))
	mux.HandleFunc("GET /nutricion", w.user(w.nutriPage))
	mux.HandleFunc("POST /nutricion/apuntar", w.user(w.nutriLogPost))
	mux.HandleFunc("POST /nutricion/rapido", w.user(w.nutriQuickPost))
	mux.HandleFunc("POST /nutricion/apunte/{id}/borrar", w.user(w.nutriDelete))
	mux.HandleFunc("POST /nutricion/guardar-comida", w.user(w.nutriSaveMeal))
	mux.HandleFunc("POST /nutricion/copiar", w.user(w.nutriCopy))
	mux.HandleFunc("GET /nutricion/buscar", w.user(w.foodSearchPage))
	mux.HandleFunc("POST /nutricion/anadir", w.user(w.foodAddPost))
	mux.HandleFunc("GET /nutricion/off", w.user(w.offPage))
	mux.HandleFunc("POST /nutricion/off/anadir", w.user(w.offAddPost))
	mux.HandleFunc("POST /nutricion/off/guardar", w.user(w.offSavePost))
	mux.HandleFunc("GET /nutricion/alimentos", w.user(w.foodsPage))
	mux.HandleFunc("POST /nutricion/alimentos/generico", w.user(w.genericSavePost))
	mux.HandleFunc("GET /nutricion/alimento/nuevo", w.user(w.foodNew))
	mux.HandleFunc("GET /nutricion/alimento/{id}", w.user(w.foodEdit))
	mux.HandleFunc("POST /nutricion/alimento/{id}", w.user(w.foodSave))
	mux.HandleFunc("POST /nutricion/alimento/{id}/borrar", w.user(w.foodDelete))
	mux.HandleFunc("GET /nutricion/objetivos", w.user(w.goalsPage))
	mux.HandleFunc("POST /nutricion/objetivos", w.user(w.goalsPost))
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
	d.Today = w.now().Format("2006-01-02")
	d.Tomorrow = w.now().AddDate(0, 0, 1).Format("2006-01-02")
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if d.Script {
		rw.Header().Set("Content-Security-Policy", cspGym)
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
	w.svc.log.Error("entreno web", "op", op, "err", err)
	http.Error(rw, "Algo ha fallado. Prueba otra vez.", http.StatusInternalServerError)
}

var flashes = map[string]string{
	"apuntado": "Apuntado.", "borrada": "Sesión borrada.", "nada": "No se ha apuntado nada.",
	"guardada": "Rutina guardada.", "rutina-borrada": "Rutina borrada.",
	"detectadas": "Rutinas guardadas: revísalas y ajústalas a tu gusto.",
	"peso":       "Peso apuntado.", "guardado": "Guardado.", "dia-borrado": "Día borrado.",
	"foto": "Foto guardada.", "foto-borrada": "Foto borrada.",
	"cardio": "Cardio apuntado.", "cardio-borrado": "Cardio borrado.",
	"comida-borrada": "Borrado.", "copiado": "Copiado a hoy.", "habitual": "Guardado en tus alimentos.",
	"cambiado": "Hecho: lo apuntado está ahora en este ejercicio, y lo que apuntes con esos nombres vendrá aquí.",
	"agenda":   "Rutinas al día en la Agenda.", "agenda-no": "No usas la Agenda: pide que te la asignen en Usuarios.",
	"agenda-error": "La Agenda no responde ahora; se intentará otra vez al guardar una rutina.",
	"ya-habitual":  "Ya estaba en tus alimentos.", "alimento-borrado": "Alimento borrado.", "objetivos": "Objetivos guardados.",
}

func flash(r *http.Request) string { return flashes[r.URL.Query().Get("ok")] }

// back vuelve a una página de la app con un aviso (la pasarela pone el
// prefijo en la redirección).
func back(rw http.ResponseWriter, r *http.Request, to, ok string) {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(rw, r, to+sep+"ok="+ok, http.StatusSeeOther)
}

func notFound(rw http.ResponseWriter) { http.Error(rw, "No existe.", http.StatusNotFound) }

// ---- Fechas ----

var (
	shortDays = []string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}
	weekdays  = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	months    = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
)

func shortMonth(m time.Month) string {
	if m == time.September {
		return "sept"
	}
	return months[m-1][:3]
}

// shortDate es «mar 3 nov» (con el año si no es el actual). Los días vienen
// de columnas date: se usan tal cual, sin cambiar de zona.
func (w *web) shortDate(t time.Time) string {
	s := fmt.Sprintf("%s %d %s", shortDays[t.Weekday()], t.Day(), shortMonth(t.Month()))
	if t.Year() != w.now().Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s
}

// dayMonth es «3 nov» (con el año si no es el actual).
func (w *web) dayMonth(t time.Time) string {
	s := fmt.Sprintf("%d %s", t.Day(), shortMonth(t.Month()))
	if t.Year() != w.now().Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s
}

func (w *web) longDate(t time.Time) string {
	s := fmt.Sprintf("%s, %d de %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
	if t.Year() != w.now().Year() {
		s += fmt.Sprintf(" de %d", t.Year())
	}
	return s
}

var sources = map[string]string{"web": "Apuntada en la web", "asistente": "Apuntada con el asistente", "whatsapp": "Importada de WhatsApp"}

// ---- Inicio ----

func (w *web) home(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	list, err := w.svc.store.sessions(ctx, who.User, time.Time{}, w.now().AddDate(0, 0, 1), 10)
	if err != nil {
		w.fail(rw, "inicio", err)
		return
	}
	// El cardio de las mismas fechas que las sesiones (si hay 10, desde la
	// más antigua; si hay menos, los 10 últimos).
	from, limit := time.Time{}, 10
	if len(list) == 10 {
		from, limit = list[9].Day, 40
	}
	cardio, err := w.svc.store.cardioList(ctx, who.User, from, w.now().AddDate(0, 0, 1), limit)
	if err != nil {
		w.fail(rw, "inicio", err)
		return
	}
	custom, _ := w.svc.store.customNames(ctx, who.User)
	d := pageData{Name: who.Name, Tab: "inicio", Flash: flash(r)}
	for _, x := range list {
		c := sessionCard{ID: x.ID, Date: w.shortDate(x.Day), Source: x.Source, Notes: x.Notes, day: x.Day.Format("2006-01-02"), at: x.Started}
		for _, e := range x.Exercises {
			c.Lines = append(c.Lines, exLine{Key: e.Key, Name: exerciseName(e.Key, e.Name, custom), Summary: summarize(e)})
		}
		d.Sessions = append(d.Sessions, c)
	}
	d.Sessions = w.withCardio(d.Sessions, cardio)
	if card, err := w.todayCard(ctx, who.User); err != nil {
		w.svc.log.Error("entreno web", "op", "hoy toca", "err", err)
	} else {
		d.Hoy = card
	}
	today := w.now()
	logs, err := w.svc.store.bodyLogs(ctx, who.User, dayOf(today).AddDate(0, 0, -60), dayOf(today).AddDate(0, 0, 1))
	if err != nil {
		w.svc.log.Error("entreno web", "op", "inicio cuerpo", "err", err)
	} else {
		d.BodyCard = w.bodyCardOf(trend(logs, today))
	}
	if card, err := w.homeFoodCard(ctx, who.User); err != nil {
		w.svc.log.Error("entreno web", "op", "inicio comida", "err", err)
	} else {
		d.FoodCard = card
	}
	w.render(rw, r, "inicio.html", d)
}

// withCardio mete las sesiones de cardio entre las del gimnasio, de la más
// reciente a la más antigua.
func (w *web) withCardio(cards []sessionCard, cardio []cardioLog) []sessionCard {
	for _, c := range cardio {
		card := sessionCard{ID: c.ID, Date: w.shortDate(c.Day), Cardio: cardioText(c), day: c.Day.Format("2006-01-02"), at: c.Start}
		if c.Note != "" {
			card.Notes = []string{c.Note}
		}
		cards = append(cards, card)
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].day != cards[j].day {
			return cards[i].day > cards[j].day
		}
		return cards[i].at.After(cards[j].at)
	})
	return cards
}

// todayText es la línea para el resumen de la mañana.
func (w *web) todayText(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	list, err := w.svc.store.sessions(r.Context(), who.User, time.Time{}, w.now().AddDate(0, 0, 1), 1)
	if err != nil {
		w.fail(rw, "hoy", err)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(list) == 0 {
		return
	}
	n := len(list[0].Exercises)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	fmt.Fprintf(rw, "Última sesión: %s — %d ejercicio%s", w.shortDate(list[0].Day), n, plural)
}

// ---- Apuntar ----

func (w *web) recordForm(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d := pageData{Name: who.Name, Tab: "apuntar", Flash: flash(r), Date: w.now().Format("2006-01-02")}
	if f := r.URL.Query().Get("fecha"); f != "" {
		if _, err := w.atFor(f); err == nil {
			d.Date = f
		}
	}
	w.render(rw, r, "apuntar.html", d)
}

// atFor es el momento de la sesión: hoy, ahora; otro día, a mediodía.
func (w *web) atFor(date string) (time.Time, error) {
	now := w.now()
	if date == "" || date == now.Format("2006-01-02") {
		return now, nil
	}
	d, err := time.ParseInLocation("2006-01-02", date, madrid)
	if err != nil || d.Year() < 2000 {
		return time.Time{}, errors.New("La fecha no es válida.")
	}
	if d.After(now) {
		return time.Time{}, errors.New("La fecha no puede ser futura.")
	}
	return d.Add(12 * time.Hour), nil
}

// recordInput lee y valida el texto y la fecha del formulario.
func (w *web) recordInput(rw http.ResponseWriter, r *http.Request, d *pageData) (time.Time, bool) {
	r.Body = http.MaxBytesReader(rw, r.Body, 256<<10)
	if err := r.ParseForm(); err != nil {
		d.Error = "El texto es demasiado largo."
		return time.Time{}, false
	}
	d.Text = strings.TrimSpace(r.PostForm.Get("texto"))
	d.Date = r.PostForm.Get("fecha")
	at, err := w.atFor(d.Date)
	switch {
	case err != nil:
		d.Error = err.Error()
	case d.Text == "":
		d.Error = "Escribe lo que has hecho, un ejercicio por línea."
	case utf8.RuneCountInString(d.Text) > maxText:
		d.Error = "El texto es demasiado largo: apunta cada sesión por separado."
	default:
		if d.Date == "" {
			d.Date = at.Format("2006-01-02")
		}
		return at, true
	}
	return time.Time{}, false
}

func (w *web) recordPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d := pageData{Name: who.Name, Tab: "apuntar"}
	at, ok := w.recordInput(rw, r, &d)
	if !ok {
		w.render(rw, r, "apuntar.html", d)
		return
	}
	res, err := w.svc.record(r.Context(), who.User, d.Text, at, "web")
	unknown := dedupe(res.Unknown)
	switch {
	case err != nil && len(res.Saved) > 0: // al guardar
		w.fail(rw, "apuntar", err)
		return
	case len(res.Saved) > 0 && len(unknown) == 0:
		back(rw, r, fmt.Sprintf("/sesion/%d", res.SessionID), "apuntado")
		return
	case len(unknown) > 0:
		// Lo reconocido (si lo hay) ya está apuntado; queda asignar el resto.
		custom, _ := w.svc.store.customNames(r.Context(), who.User)
		d.Partial, d.SessionID = len(res.Saved) > 0, res.SessionID
		if d.Partial {
			d.Flash = "Apuntado lo que reconozco."
		}
		for _, e := range res.Saved {
			d.Saved = append(d.Saved, exerciseName(e.Key, e.Name, custom)+" — "+summarize(e))
		}
		for i, n := range unknown[:min(len(unknown), maxUnknown)] {
			d.Unknown = append(d.Unknown, unknownRow{Index: i, Name: n})
		}
		d.Groups = append(customGroup(custom), catalogGroups()...)
	default:
		d.Error = err.Error() // «no he encontrado ningún ejercicio…»
	}
	w.render(rw, r, "apuntar.html", d)
}

// aliasPost guarda a qué ejercicio corresponde cada nombre sin reconocer y
// apunta lo que faltaba del mismo texto: si ya se apuntó lo reconocido
// (parcial), solo esos ejercicios; si no se apuntó nada, el texto entero.
func (w *web) aliasPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	d := pageData{Name: who.Name, Tab: "apuntar"}
	at, ok := w.recordInput(rw, r, &d)
	if !ok {
		w.render(rw, r, "apuntar.html", d)
		return
	}
	partial := r.PostForm.Get("parcial") == "1"
	custom, err := w.svc.store.customNames(ctx, who.User)
	if err != nil {
		w.fail(rw, "alias", err)
		return
	}
	type pick struct{ name, to string }
	var picks []pick
	for i := 0; i < maxUnknown; i++ {
		name := strings.TrimSpace(r.PostForm.Get(fmt.Sprintf("n_%d", i)))
		to := r.PostForm.Get(fmt.Sprintf("m_%d", i))
		if name == "" || to == "" {
			continue
		}
		if utf8.RuneCountInString(name) > 120 {
			d.Error = "El nombre del ejercicio es demasiado largo."
			w.render(rw, r, "apuntar.html", d)
			return
		}
		if _, ok := lookup(to); !ok && to != "nuevo" && custom[to] == "" {
			d.Error = "Ese ejercicio no existe: elige uno de la lista."
			w.render(rw, r, "apuntar.html", d)
			return
		}
		picks = append(picks, pick{name, to})
	}
	if len(picks) == 0 {
		if partial && r.PostForm.Get("sesion") != "" {
			if id, err := strconv.ParseInt(r.PostForm.Get("sesion"), 10, 64); err == nil && id > 0 {
				back(rw, r, fmt.Sprintf("/sesion/%d", id), "apuntado")
				return
			}
		}
		back(rw, r, "/apuntar", "nada")
		return
	}
	// Para crear un ejercicio propio con el tipo adecuado (series por
	// tiempo o por distancia).
	nm, err := w.svc.store.matcherFor(ctx, who.User)
	if err != nil {
		w.fail(rw, "alias", err)
		return
	}
	parsed, _ := parseText(d.Text, nm)
	names := map[string]bool{}
	for _, p := range picks {
		key := p.to
		if key == "nuevo" {
			if key, err = w.svc.store.addCustom(ctx, who.User, p.name, kindOf(parsed, p.name)); err != nil {
				w.fail(rw, "alias", err)
				return
			}
		}
		if err := w.svc.store.setAlias(ctx, who.User, p.name, key); err != nil {
			w.fail(rw, "alias", err)
			return
		}
		names[p.name] = true
	}
	if partial {
		id, err := w.recordNames(ctx, who.User, d.Text, at, names)
		if err != nil {
			w.fail(rw, "alias", err)
			return
		}
		if id == 0 {
			back(rw, r, "/apuntar", "nada")
			return
		}
		back(rw, r, fmt.Sprintf("/sesion/%d", id), "apuntado")
		return
	}
	res, err := w.svc.record(ctx, who.User, d.Text, at, "web")
	if len(res.Saved) == 0 { // lo que queda se dejó en «No apuntar»
		if err != nil && !strings.HasPrefix(err.Error(), "no reconozco") {
			d.Error = err.Error()
			w.render(rw, r, "apuntar.html", d)
			return
		}
		back(rw, r, "/apuntar", "nada")
		return
	}
	if err != nil {
		w.fail(rw, "alias", err)
		return
	}
	back(rw, r, fmt.Sprintf("/sesion/%d", res.SessionID), "apuntado")
}

// recordNames apunta del texto solo los ejercicios escritos con esos nombres
// (lo demás ya se apuntó la primera vez, notas incluidas). Devuelve la sesión
// (0 si no había nada que apuntar).
func (w *web) recordNames(ctx context.Context, owner, text string, at time.Time, names map[string]bool) (int64, error) {
	nm, err := w.svc.store.matcherFor(ctx, owner)
	if err != nil {
		return 0, err
	}
	exs, _ := parseText(text, nm)
	var keep []exerciseLog
	for _, e := range exs {
		if e.Key != "" && names[e.Name] {
			keep = append(keep, e)
		}
	}
	if len(keep) == 0 {
		return 0, nil
	}
	if id, ok := w.svc.store.sessionOn(ctx, owner, at, ""); ok {
		return id, w.svc.store.addToSession(ctx, owner, id, nil, keep)
	}
	return w.svc.store.saveSession(ctx, owner, at, "web", nil, keep)
}

// kindOf es el tipo de un ejercicio propio según cómo se apuntó.
func kindOf(exs []exerciseLog, name string) string {
	for _, e := range exs {
		if e.Name != name {
			continue
		}
		for _, st := range e.Sets {
			switch {
			case st.Seconds > 0:
				return "time"
			case st.Meters > 0:
				return "distance"
			}
		}
	}
	return "reps"
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- Catálogo para elegir ----

var muscleLabels = map[string]string{
	"pecho": "Pecho", "espalda": "Espalda", "hombro": "Hombro", "biceps": "Bíceps", "triceps": "Tríceps",
	"antebrazo": "Antebrazo", "cuadriceps": "Cuádriceps", "isquios": "Isquiotibiales", "gluteo": "Glúteo",
	"aductores": "Aductores", "gemelo": "Gemelo", "tibial": "Tibial", "abdomen": "Abdomen", "lumbar": "Lumbar",
	"trapecio": "Trapecio", "cuerpo entero": "Cuerpo entero",
}

func muscleLabel(m string) string {
	if l, ok := muscleLabels[m]; ok {
		return l
	}
	if m == "" {
		return "Otros"
	}
	return strings.ToUpper(m[:1]) + m[1:]
}

// catalogGroups es el catálogo agrupado por el primer músculo, para los
// desplegables.
var catalogGroups = sync.OnceValue(func() []optGroup {
	by := map[string][]option{}
	var order []string
	for _, e := range catalog {
		m := ""
		if len(e.Muscles) > 0 {
			m = e.Muscles[0]
		}
		if _, ok := by[m]; !ok {
			order = append(order, m)
		}
		by[m] = append(by[m], option{Key: e.Key, Name: e.Name})
	}
	rank := map[string]int{}
	for i, m := range muscleGroups {
		rank[m] = i + 1
	}
	sort.SliceStable(order, func(i, j int) bool {
		ri, rj := rank[order[i]], rank[order[j]]
		if ri == 0 {
			ri = len(muscleGroups) + 1
		}
		if rj == 0 {
			rj = len(muscleGroups) + 1
		}
		return ri < rj
	})
	out := make([]optGroup, 0, len(order))
	for _, m := range order {
		opts := by[m]
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].Name < opts[j].Name })
		out = append(out, optGroup{Label: muscleLabel(m), Options: opts})
	}
	return out
})

func customGroup(custom map[string]string) []optGroup {
	if len(custom) == 0 {
		return nil
	}
	g := optGroup{Label: "Tus ejercicios"}
	for k, n := range custom {
		g.Options = append(g.Options, option{Key: k, Name: n})
	}
	sort.Slice(g.Options, func(i, j int) bool { return g.Options[i].Name < g.Options[j].Name })
	return []optGroup{g}
}

// ---- Sesión ----

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (w *web) sessionPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	x, err := w.svc.store.session(ctx, who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "sesion", err)
		return
	}
	custom, _ := w.svc.store.customNames(ctx, who.User)
	d := pageData{Name: who.Name, Tab: "inicio", Flash: flash(r), Confirm: r.URL.Query().Get("borrar") == "1",
		Session: sessionDetail{ID: x.ID, Date: w.longDate(x.Day), Time: x.Started.In(madrid).Format("15:04"), Source: sources[x.Source], Notes: x.Notes}}
	for _, e := range x.Exercises {
		d.Session.Exercises = append(d.Session.Exercises, detail(e, custom))
	}
	w.render(rw, r, "sesion.html", d)
}

func detail(e exerciseLog, custom map[string]string) exerciseDetail {
	ed := exerciseDetail{Key: e.Key, Name: exerciseName(e.Key, e.Name, custom), Summary: summarize(e), PerSide: e.PerSide, Note: e.Note, Unit: "Reps"}
	for _, st := range e.Sets {
		switch {
		case st.Seconds > 0:
			ed.Unit = "Segundos"
		case st.Meters > 0 && ed.Unit != "Segundos":
			ed.Unit = "Metros"
		}
		ed.HasKg = ed.HasKg || st.Kg > 0
		ed.HasRIR = ed.HasRIR || st.RIR != nil
	}
	for i, st := range e.Sets {
		row := setRow{N: i + 1, Amount: "—", Kg: "—", R: "—"}
		switch {
		case st.Seconds > 0:
			row.Amount = strconv.Itoa(st.Seconds)
		case st.Meters > 0:
			row.Amount = trimNum(st.Meters)
		case st.Reps > 0:
			row.Amount = strconv.Itoa(st.Reps)
		}
		if st.Kg > 0 {
			row.Kg = trimNum(st.Kg)
		}
		if st.RIR != nil {
			row.R = strconv.Itoa(*st.RIR)
		}
		ed.Sets = append(ed.Sets, row)
	}
	if _, v := best(e); v > 0 {
		ed.Best = trimNum(v) + " kg"
	}
	return ed
}

func (w *web) deleteSession(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	err := w.svc.store.deleteSession(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "borrar", err)
		return
	}
	back(rw, r, "/", "borrada")
}

// ---- Ejercicios ----

func (w *web) exercisesPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	stats, err := w.svc.store.exerciseStats(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "ejercicios", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "ejercicios", Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	for _, s := range stats {
		d.Stats = append(d.Stats, statRow{Key: s.Key, Name: s.Name, Times: s.Times, Last: w.shortDate(s.Last)})
	}
	if q := []rune(d.Query); len(q) > 80 {
		d.Query = string(q[:80])
	}
	if d.Query != "" {
		d.Searched, d.Results = true, searchCatalog(d.Query, 40)
	}
	w.render(rw, r, "ejercicios.html", d)
}

var reKey = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func (w *web) exercisePage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	key := r.PathValue("key")
	if !reKey.MatchString(key) {
		notFound(rw)
		return
	}
	custom, err := w.svc.store.customNames(ctx, who.User)
	if err != nil {
		w.fail(rw, "ejercicio", err)
		return
	}
	h := historyView{Key: key}
	if e, ok := lookup(key); ok {
		h.Name = e.Name
		if d := details(e); d.Desc != "" || len(d.Images) > 0 {
			h.Detail = &d
		}
		var ms []string
		for _, m := range append(append([]string{}, e.Muscles...), e.Secondary...) {
			ms = append(ms, strings.ToLower(muscleLabel(m)))
		}
		h.Muscles = strings.Join(ms, ", ")
	} else if n, ok := custom[key]; ok {
		h.Name, h.Muscles = n, "Ejercicio propio"
	} else {
		notFound(rw)
		return
	}
	list, err := w.svc.store.history(ctx, who.User, key, 0)
	if err != nil {
		w.fail(rw, "ejercicio", err)
		return
	}
	w.fillHistory(&h, list)
	w.render(rw, r, "ejercicio.html", pageData{Name: who.Name, Tab: "ejercicios", Exercise: h, Flash: flash(r)})
}

// metric es lo que enseña la gráfica de un ejercicio.
type metric struct {
	label, unit string
	value       func(exerciseLog) float64
}

func maxOf(e exerciseLog, f func(setLog) float64) float64 {
	v := 0.0
	for _, st := range e.Sets {
		v = math.Max(v, f(st))
	}
	return v
}

// pickMetric: el 1RM estimado; si no hay, la carga máxima; si es sin carga,
// las repeticiones, los segundos o los metros de la mejor serie.
func pickMetric(list []done) metric {
	has := func(f func(setLog) float64) bool {
		for _, d := range list {
			if maxOf(d.Entry, f) > 0 {
				return true
			}
		}
		return false
	}
	e1 := func(st setLog) float64 { return e1RM(st) }
	kg := func(st setLog) float64 { return st.Kg }
	sec := func(st setLog) float64 { return float64(st.Seconds) }
	m := func(st setLog) float64 { return st.Meters }
	reps := func(st setLog) float64 { return float64(st.Reps) }
	by := func(f func(setLog) float64) func(exerciseLog) float64 {
		return func(e exerciseLog) float64 { return maxOf(e, f) }
	}
	switch {
	case has(e1):
		return metric{"1RM estimado", "kg", by(e1)}
	case has(kg):
		return metric{"Carga máxima", "kg", by(kg)}
	case has(sec):
		return metric{"Segundos de la mejor serie", "s", by(sec)}
	case has(m):
		return metric{"Metros de la mejor serie", "m", by(m)}
	}
	return metric{"Repeticiones de la mejor serie", "reps", by(reps)}
}

func (w *web) fillHistory(h *historyView, list []done) {
	if len(list) == 0 {
		return
	}
	mt := pickMetric(list)
	h.Metric = mt.label
	type point struct {
		day time.Time
		v   float64
	}
	var pts []point // de la más antigua a la más reciente, una por sesión
	at := map[int64]int{}
	rec, recDay := 0.0, time.Time{}
	for i := len(list) - 1; i >= 0; i-- {
		d := list[i]
		v := mt.value(d.Entry)
		if v > rec {
			rec, recDay = v, d.Day
		}
		if v <= 0 {
			continue
		}
		if j, ok := at[d.SessionID]; ok {
			pts[j].v = math.Max(pts[j].v, v)
			continue
		}
		at[d.SessionID] = len(pts)
		pts = append(pts, point{d.Day, v})
	}
	for _, d := range list {
		row := historyLine{SessionID: d.SessionID, Date: w.shortDate(d.Day), Series: summarize(d.Entry), Note: d.Entry.Note}
		if _, v := best(d.Entry); v > 0 {
			row.E1RM = trimNum(v) + " kg"
		}
		h.Rows = append(h.Rows, row)
	}
	if rec > 0 {
		h.Record = fmt.Sprintf("%s %s (%s)", trimNum(rec), mt.unit, w.dayMonth(recDay))
	}
	if len(pts) == 0 {
		return
	}
	days := make([]time.Time, len(pts))
	vals := make([]float64, len(pts))
	for i, p := range pts {
		days[i], vals[i] = p.day, p.v
	}
	h.Chart = w.chart(days, vals, mt.unit)
}

// ---- Gráfica (SVG en el servidor) ----

type chartView struct {
	W, H    int
	Label   string
	Line    string // puntos de la polilínea
	Dots    []chartDot
	YTicks  []chartTick
	XTicks  []chartTick
	X0, X1  int // eje
	YBottom int
}

type chartDot struct {
	X, Y  string
	Title string
}

type chartTick struct {
	X, Y   string
	Label  string
	Anchor string
}

func f1(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

// niceStep es un paso redondo (1, 2, 2,5 o 5 por potencia de 10).
func niceStep(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*p >= raw {
			return m * p
		}
	}
	return 10 * p
}

func (w *web) chart(days []time.Time, vals []float64, unit string) *chartView {
	const W, H, padL, padR, padT, padB = 480, 240, 58, 16, 16, 34
	c := &chartView{W: W, H: H, X0: padL, X1: W - padR, YBottom: H - padB}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi-lo < 1e-9 {
		lo, hi = lo-1, hi+1
		if lo < 0 {
			lo = 0
		}
	}
	step := niceStep((hi - lo) / 4)
	lo, hi = math.Floor(lo/step)*step, math.Ceil(hi/step)*step
	if hi == lo {
		hi = lo + step
	}
	plotW, plotH := float64(W-padL-padR), float64(H-padT-padB)
	y := func(v float64) float64 { return padT + (hi-v)/(hi-lo)*plotH }
	t0, t1 := days[0], days[len(days)-1]
	span := t1.Sub(t0).Hours()
	x := func(t time.Time) float64 {
		if span <= 0 {
			return padL + plotW/2
		}
		return padL + t.Sub(t0).Hours()/span*plotW
	}
	for v := lo; v <= hi+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 8), Y: f1(y(v)), Label: trimNum(v), Anchor: "end"})
	}
	var line []string
	for i, t := range days {
		px, py := f1(x(t)), f1(y(vals[i]))
		line = append(line, px+","+py)
		c.Dots = append(c.Dots, chartDot{X: px, Y: py, Title: fmt.Sprintf("%s: %s %s", w.dayMonth(t), trimNum(vals[i]), unit)})
	}
	c.Line = strings.Join(line, " ")
	ty := strconv.Itoa(H - 8)
	if span <= 0 {
		c.XTicks = []chartTick{{X: f1(padL + plotW/2), Y: ty, Label: w.dayMonth(t0), Anchor: "middle"}}
	} else {
		c.XTicks = []chartTick{{X: strconv.Itoa(padL), Y: ty, Label: w.dayMonth(t0), Anchor: "start"},
			{X: strconv.Itoa(W - padR), Y: ty, Label: w.dayMonth(t1), Anchor: "end"}}
	}
	c.Label = fmt.Sprintf("Evolución de %d sesiones, de %s a %s %s", len(vals), trimNum(vals[0]), trimNum(vals[len(vals)-1]), unit)
	return c
}

// ---- Importar ----

func (w *web) importPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d := pageData{Name: who.Name, Tab: "importar"}
	q := r.URL.Query()
	if q.Has("hechas") {
		saved, err1 := strconv.Atoi(q.Get("hechas"))
		skipped, err2 := strconv.Atoi(q.Get("ya"))
		if err1 == nil && err2 == nil && saved >= 0 && skipped >= 0 {
			d.Done = &importDone{Saved: saved, Skipped: skipped}
		}
	}
	w.render(rw, r, "importar.html", d)
}

// importUpload lee el fichero en memoria (no se guarda ni se registra) y
// enseña la vista previa.
func (w *web) importUpload(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d := pageData{Name: who.Name, Tab: "importar"}
	r.Body = http.MaxBytesReader(rw, r.Body, maxUpload+64<<10)
	text, err := readUpload(r)
	if err != nil {
		d.Error = err.Error()
		w.render(rw, r, "importar.html", d)
		return
	}
	p, err := w.svc.previewImport(r.Context(), who.User, text)
	if err != nil {
		d.Error = err.Error()
		w.render(rw, r, "importar.html", d)
		return
	}
	custom, _ := w.svc.store.customNames(r.Context(), who.User)
	d.Preview, d.Groups = w.previewOf(p, custom), catalogGroups()
	w.render(rw, r, "importar.html", d)
}

// readUpload saca el texto del campo «fichero»: un .txt o .md, o el .zip
// que hace el iPhone (con el .txt dentro). Todo en memoria.
func readUpload(r *http.Request) (string, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", errors.New("Elige el fichero de la exportación.")
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return "", errors.New("Elige el fichero de la exportación.")
		}
		if err != nil {
			return "", errors.New("No se ha podido leer el fichero: ¿pasa de 5 MB?")
		}
		if part.FormName() != "fichero" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, maxUpload+1))
		if err != nil {
			return "", errors.New("No se ha podido leer el fichero: ¿pasa de 5 MB?")
		}
		if len(b) > maxUpload {
			return "", errors.New("El fichero pasa de 5 MB: exporta el chat «Sin archivos».")
		}
		return chatText(b)
	}
}

func chatText(b []byte) (string, error) {
	if bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return "", errors.New("El .zip está dañado.")
		}
		var found bool
		for _, f := range zr.File {
			n := strings.ToLower(f.Name)
			if !strings.HasSuffix(n, ".txt") && !strings.HasSuffix(n, ".md") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return "", errors.New("El .zip está dañado.")
			}
			b, err = io.ReadAll(io.LimitReader(rc, maxUnzip+1))
			rc.Close()
			if err != nil {
				return "", errors.New("El .zip está dañado.")
			}
			if len(b) > maxUnzip {
				return "", errors.New("El chat del .zip es demasiado grande.")
			}
			found = true
			break
		}
		if !found {
			return "", errors.New("El .zip no trae el chat (.txt).")
		}
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(b)) == 0 {
		return "", errors.New("El fichero está vacío.")
	}
	if !utf8.Valid(b) {
		return "", errors.New("El fichero no es texto: sube la exportación del chat (.txt, .md o .zip).")
	}
	return string(b), nil
}

func (w *web) previewOf(p *importPreview, custom map[string]string) *previewView {
	v := &previewView{ID: p.ID, Count: len(p.Sessions), Already: p.Already}
	if v.Count > 0 {
		v.From, v.To = w.dayMonth(p.Sessions[0].Day), w.dayMonth(p.Sessions[v.Count-1].Day)
	}
	for i := v.Count - 1; i >= 0 && len(v.Sample) < 12; i-- {
		x := p.Sessions[i]
		row := sampleRow{Date: w.shortDate(x.Day)}
		for _, e := range x.Exercises {
			if e.Key == "" {
				row.Names = append(row.Names, nameTag{Name: e.Name, Unknown: true})
			} else {
				row.Names = append(row.Names, nameTag{Name: exerciseName(e.Key, e.Name, custom)})
			}
		}
		v.Sample = append(v.Sample, row)
	}
	v.More = v.Count - len(v.Sample)
	for i, n := range sortedKeys(p.Unknown) {
		v.Unknown = append(v.Unknown, unknownRow{Index: i, Name: n, Count: p.Unknown[n]})
	}
	return v
}

var reImportID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (w *web) importConfirm(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	d := pageData{Name: who.Name, Tab: "importar"}
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(rw, r.Body, 256<<10)
	if !reImportID.MatchString(id) || r.ParseForm() != nil {
		notFound(rw)
		return
	}
	p, ok := w.svc.imports.get(who.User, id)
	if !ok {
		d.Error = "La vista previa ha caducado: vuelve a subir el fichero."
		w.render(rw, r, "importar.html", d)
		return
	}
	mapping := map[string]string{}
	for i, name := range sortedKeys(p.Unknown) {
		to := r.PostForm.Get(fmt.Sprintf("m_%d", i))
		if _, ok := lookup(to); !ok && to != "" && to != "nuevo" {
			d.Error = "Ese ejercicio no existe: elige uno de la lista."
			custom, _ := w.svc.store.customNames(ctx, who.User)
			d.Preview, d.Groups = w.previewOf(p, custom), catalogGroups()
			w.render(rw, r, "importar.html", d)
			return
		}
		mapping[name] = to
	}
	saved, skipped, err := w.svc.confirmImport(ctx, who.User, id, mapping)
	if err != nil {
		w.fail(rw, "importar", err)
		return
	}
	http.Redirect(rw, r, fmt.Sprintf("/importar?hechas=%d&ya=%d", saved, skipped), http.StatusSeeOther)
}
