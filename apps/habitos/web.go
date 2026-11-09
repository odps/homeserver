package main

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST y sin JavaScript:
// hoy (marcar lo de cada día), hábitos (con su ficha y su calendario), diario
// y datos (porcentajes y relaciones con el diario). Marcar es pulsar un botón
// de un formulario que vuelve a la misma página.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 32 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /habitos detrás de la pasarela
	Name    string
	Tab     string // hoy, habitos, diario, datos
	Title   string
	Flash   string
	Error   string

	// Hoy y lista de hábitos
	Day      *dayNav
	Items    []habitRow
	Others   []habitRow // hoy: los que no tocan ese día
	Archived []habitRow
	Journal  *journalState

	// Ficha y formulario de un hábito
	Habit   *habitRow
	Heat    *heatmap
	Edit    *dayEdit
	Form    *habitForm
	Confirm bool

	// Diario
	Entry     *entryForm
	Chart     *moodChart
	Month     *monthNav
	Entries   []entryRow
	MonthAvg  *monthAvg
	Nudge     settingsData
	NudgeTime string

	// Datos
	Stats     []habitRow
	Corrs     []corrRow
	SleepCorr string
	Range     string
}

type dayNav struct {
	ISO, Label, Prev, Next string
	IsToday                bool
}

// habitRow es un hábito ya calculado para enseñarlo.
type habitRow struct {
	ID                 int64
	Label, Name, Emoji string
	Freq, Goal, Unit   string
	Amount             bool
	Done               bool   // hecho el día que se mira
	Value              string // cantidad del día que se mira
	Step               string // lo que suma el botón rápido
	StepLabel          string
	Progress           string // ancho de la barra (0–100)
	Streak, Best       string
	StreakN            int
	Week               string
	Month, Last30      string
	Remind             string
	Archived           bool
	Weekly             bool
	Scheduled          bool
	Created            string
	Base, DayISO       string // para los formularios de marcar
}

type journalState struct {
	Written bool
	Today   bool
}

type dayEdit struct {
	ISO, Label string
	Done       bool
	Value      string
}

type dayOpt struct {
	Value      int
	Short, Day string
	On         bool
}

type habitForm struct {
	ID                      int64
	Name, Emoji, Kind       string
	Target, Unit            string
	FreqKind, Times, Remind string
	Days                    []dayOpt
}

type option struct {
	Value, Label string
	On           bool
}

type entryForm struct {
	ISO, Label   string
	Prev, Next   string
	IsToday      bool
	Exists       bool
	Mood, Energy []option
	Sleep        string
	Note         string
	Tags         string
	Origin       string
	Updated      string
}

type monthNav struct {
	Key, Label, Prev, Next string
}

type entryRow struct {
	ISO, Label   string
	Mood, Energy string
	MoodIcon     string
	Sleep        string
	Note         string
	Tags         []string
}

type monthAvg struct {
	Days                 int
	Mood, Energy, Sleep  string
	HasMood, HasEnergy   bool
	HasSleep             bool
	BestTag, BestTagMood string
}

type corrRow struct {
	Label         string
	Mood, Energy  string
	Sleep         string
	DoneN, OtherN int
	Up            bool
	Enough        bool
	Missing       string
}

var moodLabels = []string{"", "😣 Muy mal", "🙁 Mal", "😐 Normal", "🙂 Bien", "😄 Muy bien"}
var moodIcons = []string{"", "😣", "🙁", "😐", "🙂", "😄"}
var energyLabels = []string{"", "Agotado", "Baja", "Normal", "Alta", "A tope"}

type web struct{ svc *service }

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.todayPage))
	mux.HandleFunc("POST /marcar", w.user(w.markPost))
	mux.HandleFunc("GET /habitos", w.user(w.habitsPage))
	mux.HandleFunc("GET /habito/nuevo", w.user(w.newHabitPage))
	mux.HandleFunc("POST /habito/nuevo", w.user(w.newHabitPost))
	mux.HandleFunc("GET /habito/{id}", w.user(w.habitPage))
	mux.HandleFunc("POST /habito/{id}", w.user(w.habitPost))
	mux.HandleFunc("POST /habito/{id}/archivar", w.user(w.archivePost))
	mux.HandleFunc("POST /habito/{id}/borrar", w.user(w.deletePost))
	mux.HandleFunc("GET /diario", w.user(w.journalPage))
	mux.HandleFunc("POST /diario", w.user(w.journalPost))
	mux.HandleFunc("POST /diario/borrar", w.user(w.journalDelete))
	mux.HandleFunc("POST /ajustes", w.user(w.settingsPost))
	mux.HandleFunc("GET /datos", w.user(w.dataPage))
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
	w.svc.log.Error("habitos web", "op", op, "err", err)
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

func back(rw http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(rw, r, to, http.StatusSeeOther)
}

var flashes = map[string]string{
	"creado": "Hábito creado.", "guardado": "Guardado.", "borrado": "Borrado.", "archivado": "Archivado: ya no sale en Hoy.",
	"recuperado": "Vuelve a salir en Hoy.", "diario": "Apuntado en el diario.", "diario-borrado": "Día del diario borrado.",
	"ajustes": "Aviso guardado.",
}

// problems son los errores que llegan en ?mal= tras una redirección.
var problems = map[string]string{
	"numero": "Esa cantidad no es válida.", "dia": "Ese día aún no ha llegado.", "hora": "La hora del aviso va como 21:30.",
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// pickDay lee ?dia= (por defecto, hoy). Nunca en el futuro.
func (w *web) pickDay(s string) time.Time {
	today := w.svc.today()
	d, err := parseDay(s)
	if err != nil || d.After(today) || d.Year() < firstYear {
		return today
	}
	return d
}

// capFirst pone en mayúscula la primera letra.
func capFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

func navOf(d, today time.Time) *dayNav {
	n := &dayNav{ISO: isoDay(d), Label: relDay(d, today), Prev: isoDay(d.Add(-dayUnit)), IsToday: d.Equal(today)}
	if n.Label == "hoy" || n.Label == "ayer" {
		n.Label += ", " + longDate(d, today)
	}
	n.Label = capFirst(n.Label)
	if d.Before(today) {
		n.Next = isoDay(d.Add(dayUnit))
	}
	return n
}

// quickStep es lo que suma el botón rápido de un hábito de cantidad.
func quickStep(target float64) float64 {
	switch {
	case target <= 12:
		return 1
	case target <= 60:
		return 5
	case target <= 300:
		return 10
	}
	return math.Max(math.Round(target/10), 1)
}

func pctOr(r ratio) string {
	if p, ok := r.pct(); ok {
		return pctText(p)
	}
	return "—"
}

// rowOf calcula la fila de un hábito, con lo hecho el día day.
func rowOf(h habit, m marks, today, day time.Time) habitRow {
	st := statsOf(h, m, today)
	row := habitRow{ID: h.ID, Label: h.label(), Name: h.Name, Emoji: h.Emoji, Freq: freqText(h.Freq), Goal: h.goalText(), Unit: h.Unit,
		Amount: h.Kind == kindAmount, Done: h.done(m, day), Remind: h.Remind, Archived: h.Archived, Weekly: h.weekly(),
		StreakN: st.Current, Month: pctOr(st.Month), Last30: pctOr(st.Last30), Created: shortDate(dayOf(h.Created), today)}
	if st.Current > 0 {
		row.Streak = h.streakUnit(st.Current)
	}
	if st.Best > 0 {
		row.Best = h.streakUnit(st.Best)
	}
	if h.weekly() {
		row.Week = strconv.Itoa(st.WeekDone) + " de " + strconv.Itoa(st.WeekTotal) + " esta semana"
	} else {
		row.Week = strconv.Itoa(st.WeekDone) + " de " + strconv.Itoa(st.WeekTotal) + " días esta semana"
	}
	if day.Equal(today) {
		row.Scheduled = st.ScheduledToday
	} else {
		row.Scheduled = h.weekly() || h.scheduled(day)
	}
	if h.Kind == kindAmount {
		v := m[isoDay(day)]
		row.Value = trimNum(v, 2)
		step := quickStep(h.target())
		row.Step = trimNum(step, 2)
		row.StepLabel = strings.TrimSpace("+" + row.Step + " " + h.Unit)
		row.Progress = fx1(math.Min(v/h.target(), 1) * 100)
	}
	return row
}

// ---- Hoy ----

func (w *web) todayPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "hoy", err)
		return
	}
	day := w.pickDay(r.URL.Query().Get("dia"))
	d := pageData{Name: who.Name, Tab: "hoy", Day: navOf(day, v.today)}
	if !d.Day.IsToday {
		d.Title = d.Day.Label
	}
	d.Error = problems[r.URL.Query().Get("mal")]
	for _, h := range v.active() {
		if day.Before(dayOf(h.Created)) && len(v.m(h)) == 0 {
			continue
		}
		row := rowOf(h, v.m(h), v.today, day)
		row.Base, row.DayISO = base(r), isoDay(day)
		if row.Scheduled || row.Done {
			d.Items = append(d.Items, row)
		} else {
			d.Others = append(d.Others, row)
		}
	}
	written, err := w.svc.store.hasEntry(r.Context(), who.User, day)
	if err != nil {
		w.fail(rw, "hoy", err)
		return
	}
	d.Journal = &journalState{Written: written, Today: d.Day.IsToday}
	w.render(rw, r, "hoy.html", d)
}

// markPost es pulsar un botón de un hábito: hecho, deshacer, sumar o fijar
// la cantidad.
func (w *web) markPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	id, err := strconv.ParseInt(f.Get("habito"), 10, 64)
	if err != nil || id <= 0 {
		notFound(rw)
		return
	}
	day, err := parseDay(f.Get("dia"))
	if err != nil {
		http.Error(rw, "Fecha no válida.", http.StatusBadRequest)
		return
	}
	h, err := w.svc.store.habit(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "marcar", err)
		return
	}
	m, err := w.svc.store.habitMarks(r.Context(), who.User, id)
	if err != nil {
		w.fail(rw, "marcar", err)
		return
	}
	cur := m[isoDay(day)]
	var amount float64
	switch f.Get("accion") {
	case "hecho":
		amount = h.target()
	case "deshacer":
		amount = 0
	case "sumar":
		step, err := parseNum(f.Get("cantidad"))
		if err != nil {
			step = 1
		}
		amount = cur + step
	case "fijar":
		v := strings.TrimSpace(f.Get("cantidad"))
		if v != "" {
			if amount, err = parseNum(v); err != nil {
				w.markBack(rw, r, h.ID, day, "numero")
				return
			}
		}
	default:
		http.Error(rw, "Acción no válida.", http.StatusBadRequest)
		return
	}
	if err := w.svc.setAmount(r.Context(), who.User, h, day, amount, "web"); err != nil {
		if isInput(err) {
			problem := "numero"
			if day.After(w.svc.today()) {
				problem = "dia"
			}
			w.markBack(rw, r, h.ID, day, problem)
			return
		}
		w.fail(rw, "marcar", err)
		return
	}
	w.markBack(rw, r, h.ID, day, "")
}

// markBack vuelve a donde se pulsó: Hoy o la ficha del hábito.
func (w *web) markBack(rw http.ResponseWriter, r *http.Request, id int64, day time.Time, problem string) {
	fromHabit := r.PostForm.Get("desde") == "ficha"
	v := url.Values{}
	if fromHabit || !day.Equal(w.svc.today()) {
		v.Set("dia", isoDay(day))
	}
	if problem != "" {
		v.Set("mal", problem)
	}
	q := ""
	if len(v) > 0 {
		q = "?" + v.Encode()
	}
	if fromHabit {
		back(rw, r, "/habito/"+idStr(id)+q+"#dia")
		return
	}
	back(rw, r, "/"+q+"#h"+idStr(id))
}

// ---- Hábitos ----

func (w *web) habitsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "habitos", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "habitos", Title: "Hábitos"}
	for _, h := range v.habits {
		row := rowOf(h, v.m(h), v.today, v.today)
		if h.Archived {
			d.Archived = append(d.Archived, row)
		} else {
			d.Items = append(d.Items, row)
		}
	}
	w.render(rw, r, "habitos.html", d)
}

func dayOpts(on []int) []dayOpt {
	var out []dayOpt
	for _, wd := range weekOrder {
		out = append(out, dayOpt{Value: wd, Short: shortDays[wd], Day: weekdays[wd], On: slices.Contains(on, wd)})
	}
	return out
}

func formOf(h habit) *habitForm {
	f := &habitForm{ID: h.ID, Name: h.Name, Emoji: h.Emoji, Kind: h.Kind, Unit: h.Unit, FreqKind: h.Freq.Kind, Remind: h.Remind, Days: dayOpts(h.Freq.Days), Times: "3"}
	if h.Kind == kindAmount {
		f.Target = trimNum(h.Target, 3)
	}
	if h.weekly() {
		f.Times = strconv.Itoa(h.Freq.Times)
	}
	return f
}

// readHabit lee el formulario de un hábito.
func readHabit(r *http.Request) (*habitForm, habitInput, error) {
	p := r.PostForm
	f := &habitForm{Name: p.Get("nombre"), Emoji: p.Get("emoji"), Kind: p.Get("tipo"), Target: p.Get("objetivo"), Unit: p.Get("unidad"),
		FreqKind: p.Get("frecuencia"), Times: p.Get("veces"), Remind: p.Get("recordatorio")}
	in := habitInput{Name: f.Name, Emoji: f.Emoji, Kind: f.Kind, Unit: f.Unit, Remind: f.Remind, Freq: freq{Kind: f.FreqKind}}
	var days []int
	for _, s := range p["dias"] {
		if n, err := strconv.Atoi(s); err == nil {
			days = append(days, n)
		}
	}
	f.Days = dayOpts(days)
	in.Freq.Days = days
	if f.FreqKind == freqWeekly {
		n, err := strconv.Atoi(strings.TrimSpace(f.Times))
		if err != nil {
			return f, in, bad("Las veces por semana van de 1 a 7.")
		}
		in.Freq.Times = n
	}
	if f.Kind == kindAmount {
		t, err := parseNum(f.Target)
		if err != nil {
			return f, in, bad("Pon el objetivo de cada día: un número mayor que cero (8 vasos, 20 min…).")
		}
		in.Target = t
	}
	return f, in, nil
}

func (w *web) newHabitPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	f := &habitForm{Kind: kindYesNo, FreqKind: freqDaily, Times: "3", Days: dayOpts(nil)}
	w.render(rw, r, "habito-form.html", pageData{Name: who.Name, Tab: "habitos", Title: "Nuevo hábito", Form: f})
}

func (w *web) newHabitPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f, in, err := readHabit(r)
	var h habit
	if err == nil {
		h, err = w.svc.createHabit(r.Context(), who.User, in)
	}
	if isInput(err) {
		w.render(rw, r, "habito-form.html", pageData{Name: who.Name, Tab: "habitos", Title: "Nuevo hábito", Form: f, Error: err.Error()})
		return
	} else if err != nil {
		w.fail(rw, "crear hábito", err)
		return
	}
	back(rw, r, "/habito/"+idStr(h.ID)+"?ok=creado")
}

func (w *web) habitPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	w.renderHabit(rw, r, who, id, nil, "")
}

func (w *web) renderHabit(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, f *habitForm, msg string) {
	h, err := w.svc.store.habit(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "hábito", err)
		return
	}
	m, err := w.svc.store.habitMarks(r.Context(), who.User, id)
	if err != nil {
		w.fail(rw, "hábito", err)
		return
	}
	today := w.svc.today()
	day := w.pickDay(r.URL.Query().Get("dia"))
	row := rowOf(h, m, today, day)
	if f == nil {
		f = formOf(h)
	}
	d := pageData{Name: who.Name, Tab: "habitos", Title: h.Name, Habit: &row, Form: f, Error: msg,
		Heat: buildHeatmap(h, m, today, day, 26), Confirm: r.URL.Query().Get("borrar") == "1",
		Edit: &dayEdit{ISO: isoDay(day), Label: relDay(day, today), Done: h.done(m, day), Value: trimNum(m[isoDay(day)], 3)}}
	if d.Error == "" {
		d.Error = problems[r.URL.Query().Get("mal")]
	}
	w.render(rw, r, "habito.html", d)
}

func (w *web) habitPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	f, in, err := readHabit(r)
	f.ID = id
	if err == nil {
		_, err = w.svc.editHabit(r.Context(), who.User, id, in)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderHabit(rw, r, who, id, f, err.Error())
	case err != nil:
		w.fail(rw, "editar hábito", err)
	default:
		back(rw, r, "/habito/"+idStr(id)+"?ok=guardado")
	}
}

func (w *web) archivePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	on := r.PostForm.Get("on") == "1"
	err := w.svc.store.setArchived(r.Context(), who.User, id, on)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "archivar", err)
	case on:
		back(rw, r, "/habito/"+idStr(id)+"?ok=archivado")
	default:
		back(rw, r, "/habito/"+idStr(id)+"?ok=recuperado")
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
		if _, err := w.svc.store.habit(r.Context(), who.User, id); err != nil {
			notFound(rw)
			return
		}
		back(rw, r, "/habito/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	err := w.svc.store.deleteHabit(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar hábito", err)
	default:
		back(rw, r, "/habitos?ok=borrado")
	}
}

// ---- Diario ----

func options(labels []string, sel int) []option {
	out := []option{{Value: "0", Label: "Sin apuntar", On: sel == 0}}
	for i := 1; i < len(labels); i++ {
		out = append(out, option{Value: strconv.Itoa(i), Label: strconv.Itoa(i) + " · " + labels[i], On: sel == i})
	}
	return out
}

func entryFormOf(e entry, exists bool, today time.Time) *entryForm {
	f := &entryForm{ISO: isoDay(e.Day), Label: capFirst(relDay(e.Day, today)), Prev: isoDay(e.Day.Add(-dayUnit)), IsToday: e.Day.Equal(today), Exists: exists,
		Mood: options(moodLabels, e.Mood), Energy: options(energyLabels, e.Energy), Note: e.Note, Tags: strings.Join(e.Tags, ", "), Origin: e.Origin}
	if e.Day.Before(today) {
		f.Next = isoDay(e.Day.Add(dayUnit))
	}
	if e.Sleep != nil {
		f.Sleep = trimNum(*e.Sleep, 2)
	}
	if exists && !e.Updated.IsZero() {
		f.Updated = shortDate(dayOf(e.Updated), today) + " a las " + e.Updated.In(madrid).Format("15:04")
	}
	return f
}

// pickMonth lee ?mes=AAAA-MM (por defecto, el del día elegido).
func pickMonth(s string, day time.Time) time.Time {
	if t, err := time.Parse("2006-01", strings.TrimSpace(s)); err == nil && t.Year() >= firstYear {
		return t
	}
	return monthStart(day)
}

func (w *web) journalPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	day := w.pickDay(r.URL.Query().Get("dia"))
	e, exists, err := w.svc.store.entry(r.Context(), who.User, day)
	if err != nil {
		w.fail(rw, "diario", err)
		return
	}
	w.renderJournal(rw, r, who, entryFormOf(e, exists, w.svc.today()), "")
}

func (w *web) renderJournal(rw http.ResponseWriter, r *http.Request, who app.Identity, f *entryForm, msg string) {
	today := w.svc.today()
	day, _ := parseDay(f.ISO)
	month := pickMonth(r.URL.Query().Get("mes"), day)
	if month.After(monthStart(today)) {
		month = monthStart(today)
	}
	list, err := w.svc.store.entries(r.Context(), who.User, month, month.AddDate(0, 1, -1))
	if err != nil {
		w.fail(rw, "diario", err)
		return
	}
	prefs, err := w.svc.store.settings(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "diario", err)
		return
	}
	if msg == "" {
		msg = problems[r.URL.Query().Get("mal")]
	}
	d := pageData{Name: who.Name, Tab: "diario", Title: "Diario", Entry: f, Error: msg, Chart: buildMoodChart(list, month),
		Nudge: prefs, NudgeTime: prefs.nudgeTime(), MonthAvg: monthAverages(list), Confirm: r.URL.Query().Get("borrar") == "1"}
	nav := &monthNav{Key: month.Format("2006-01"), Label: monthName(month), Prev: month.AddDate(0, -1, 0).Format("2006-01")}
	if month.Before(monthStart(today)) {
		nav.Next = month.AddDate(0, 1, 0).Format("2006-01")
	}
	d.Month = nav
	for i := len(list) - 1; i >= 0; i-- {
		d.Entries = append(d.Entries, entryRowOf(list[i], today))
	}
	w.render(rw, r, "diario.html", d)
}

func entryRowOf(e entry, today time.Time) entryRow {
	row := entryRow{ISO: isoDay(e.Day), Label: longDate(e.Day, today), Note: e.Note, Tags: e.Tags}
	if e.Mood > 0 {
		row.Mood, row.MoodIcon = moodLabels[e.Mood], moodIcons[e.Mood]
	}
	if e.Energy > 0 {
		row.Energy = "energía " + strings.ToLower(energyLabels[e.Energy])
	}
	if e.Sleep != nil {
		row.Sleep = trimNum(*e.Sleep, 2) + " h de sueño"
	}
	return row
}

// monthAverages son las medias del mes y la etiqueta con mejor ánimo.
func monthAverages(list []entry) *monthAvg {
	if len(list) == 0 {
		return nil
	}
	var mood, energy, sleep avg
	tags := map[string]*avg{}
	for _, e := range list {
		if e.Mood > 0 {
			mood.add(float64(e.Mood))
			for _, t := range e.Tags {
				if tags[t] == nil {
					tags[t] = &avg{}
				}
				tags[t].add(float64(e.Mood))
			}
		}
		if e.Energy > 0 {
			energy.add(float64(e.Energy))
		}
		if e.Sleep != nil {
			sleep.add(*e.Sleep)
		}
	}
	a := &monthAvg{Days: len(list)}
	if v, ok := mood.value(); ok {
		a.Mood, a.HasMood = num(v, 1), true
	}
	if v, ok := energy.value(); ok {
		a.Energy, a.HasEnergy = num(v, 1), true
	}
	if v, ok := sleep.value(); ok {
		a.Sleep, a.HasSleep = num(v, 1)+" h", true
	}
	best, bestV := "", 0.0
	for t, x := range tags {
		if v, _ := x.value(); x.N >= 2 && (v > bestV || v == bestV && t < best) {
			best, bestV = t, v
		}
	}
	if best != "" {
		a.BestTag, a.BestTagMood = best, num(bestV, 1)
	}
	return a
}

// readEntry lee el formulario del diario.
func readEntry(r *http.Request) (*entryForm, entryData, error) {
	p := r.PostForm
	var d entryData
	d.Mood, _ = strconv.Atoi(p.Get("animo"))
	d.Energy, _ = strconv.Atoi(p.Get("energia"))
	d.Note = p.Get("nota")
	d.Tags = []string{p.Get("etiquetas")}
	d.Origin = "web"
	f := &entryForm{ISO: p.Get("dia"), Mood: options(moodLabels, d.Mood), Energy: options(energyLabels, d.Energy), Sleep: p.Get("sueno"),
		Note: d.Note, Tags: p.Get("etiquetas"), Exists: true}
	if s := strings.TrimSpace(f.Sleep); s != "" {
		v, err := parseNum(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(s, "h")), " "))
		if err != nil {
			return f, d, bad("Las horas de sueño, con un número: 7 o 7,5.")
		}
		d.Sleep = &v
	}
	return f, d, nil
}

func (w *web) journalPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	day, err := parseDay(r.PostForm.Get("dia"))
	if err != nil {
		http.Error(rw, "Fecha no válida.", http.StatusBadRequest)
		return
	}
	f, d, err := readEntry(r)
	if err == nil {
		err = w.svc.saveEntry(r.Context(), who.User, day, d)
	}
	if isInput(err) {
		today := w.svc.today()
		f.Label, f.IsToday, f.Prev = capFirst(relDay(day, today)), day.Equal(today), isoDay(day.Add(-dayUnit))
		if day.Before(today) {
			f.Next = isoDay(day.Add(dayUnit))
		}
		w.renderJournal(rw, r, who, f, err.Error())
		return
	} else if err != nil {
		w.fail(rw, "diario", err)
		return
	}
	q := "?ok=diario"
	if !day.Equal(w.svc.today()) {
		q = "?dia=" + isoDay(day) + "&ok=diario"
	}
	back(rw, r, "/diario"+q)
}

func (w *web) journalDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	day, err := parseDay(r.PostForm.Get("dia"))
	if err != nil {
		http.Error(rw, "Fecha no válida.", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/diario?dia="+isoDay(day)+"&borrar=1#borrar")
		return
	}
	if err := w.svc.store.deleteEntry(r.Context(), who.User, day); err != nil {
		w.fail(rw, "borrar diario", err)
		return
	}
	back(rw, r, "/diario?dia="+isoDay(day)+"&ok=diario-borrado")
}

func (w *web) settingsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	d := settingsData{Nudge: r.PostForm.Get("aviso") == "1"}
	if t := strings.TrimSpace(r.PostForm.Get("hora")); t != "" {
		m, err := parseHour(t)
		if err != nil {
			back(rw, r, "/diario?mal=hora#aviso")
			return
		}
		d.NudgeTime = hourText(m)
	}
	if err := w.svc.store.saveSettings(r.Context(), who.User, d); err != nil {
		w.fail(rw, "ajustes", err)
		return
	}
	back(rw, r, "/diario?ok=ajustes#aviso")
}

// ---- Datos ----

const corrDays = 90

func corrRowOf(c corr) corrRow {
	row := corrRow{Label: c.Habit.label(), DoneN: c.MoodDone.N, OtherN: c.MoodOther.N, Enough: c.enough()}
	if !row.Enough {
		row.Missing = "Faltan días con el ánimo apuntado (hacen falta " + strconv.Itoa(minCorrDays) + " de cada)."
		return row
	}
	pair := func(a, b avg, dec int, unit string) string {
		x, ok1 := a.value()
		y, ok2 := b.value()
		if !ok1 || !ok2 {
			return ""
		}
		return num(x, dec) + unit + " cuando lo haces y " + num(y, dec) + unit + " cuando no"
	}
	row.Mood = pair(c.MoodDone, c.MoodOther, 1, "")
	row.Energy = pair(c.EnergyDone, c.EnergyOther, 1, "")
	row.Sleep = pair(c.SleepDone, c.SleepOther, 1, " h")
	x, _ := c.MoodDone.value()
	y, _ := c.MoodOther.value()
	row.Up = x > y
	return row
}

func (w *web) dataPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "datos", err)
		return
	}
	from := v.today.Add(-(corrDays - 1) * dayUnit)
	list, err := w.svc.store.entries(r.Context(), who.User, from, v.today)
	if err != nil {
		w.fail(rw, "datos", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "datos", Title: "Datos", Range: strconv.Itoa(corrDays)}
	for _, h := range v.active() {
		d.Stats = append(d.Stats, rowOf(h, v.m(h), v.today, v.today))
	}
	for _, c := range correlations(v.active(), v.marks, list, from, v.today) {
		d.Corrs = append(d.Corrs, corrRowOf(c))
	}
	d.SleepCorr = sleepCorrText(list, from)
	w.render(rw, r, "datos.html", d)
}

// sleepCorrText compara el ánimo de los días tras dormir 7 h o más con el
// de los que menos.
func sleepCorrText(list []entry, from time.Time) string {
	enough, little := sleepMood(list, 7, from)
	x, ok1 := enough.value()
	y, ok2 := little.value()
	if !ok1 || !ok2 || enough.N < minCorrDays || little.N < minCorrDays {
		return ""
	}
	return "Ánimo medio de " + num(x, 1) + " los días que duermes 7 h o más (" + plural(enough.N, "día", "días") + ") y de " +
		num(y, 1) + " los que duermes menos (" + plural(little.N, "día", "días") + ")."
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
