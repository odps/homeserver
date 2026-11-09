package main

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST y sin JavaScript:
// luz (precios de hoy y mañana), mantenimiento (con la ficha de cada
// tarea), lecturas de los contadores y ajustes de los avisos.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 16 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /casa detrás de la pasarela
	Name    string
	Tab     string // luz, mantenimiento, lecturas, ajustes
	Title   string
	Flash   string
	Error   string

	// Luz
	Today, Tomorrow *luzView
	Source          sourceView

	// Mantenimiento
	Tasks   []taskRow
	Late    int
	Due     int
	TForm   taskForm
	Units   []option
	Suggest []suggestion

	// Ficha de una tarea
	Task    *taskView
	Confirm bool

	// Lecturas
	MeterTabs []meterTab
	Meter     *meterSummary
	RForm     readingForm
	MeterOpts []option

	// Ajustes
	Settings settingsForm
}

type option struct {
	Key, Label string
	Selected   bool
}

type sourceView struct {
	OK      bool
	Pending bool
	Last    string
	Error   string
	Retry   string
}

type hourRow struct {
	Label, Price, Level string
	Now                 bool
}

type luzView struct {
	Title, Date        string
	IsToday            bool
	Chart              *barChart
	Lo, Hi, Avg        string
	LoAt, HiAt         string
	Now, NowLevel      string
	NowAt              string
	Block2, Block2Avg  string
	Block3, Block3Avg  string
	Rest, RestAvg      string
	RestDiffers        bool
	RestPast           bool // el bloque más barato del día ya ha pasado
	Cheap, Dear, Hours []hourRow
}

type taskRow struct {
	ID                     int64
	Name, Every, Last, Due string
	When, State, StateName string
	Note                   string
}

type taskForm struct {
	Name, Every, Unit, Last, Due, Note string
}

type taskView struct {
	taskRow
	Done     []string
	Template bool
}

type meterTab struct {
	Key, Label, Emoji string
	On                bool
}

type readingRow struct {
	ID               int64
	Day, Value, Note string
	Used, PerDay     string
	Reset, First     bool
}

type meterSummary struct {
	Key, Label, Unit, Emoji string
	HasLast                 bool
	Last, LastDay           string
	HasUse                  bool
	LastUse, LastPerDay     string
	LastSpan                string
	HasAvg                  bool
	AvgDay, AvgDays         string
	YearUsed                string
	Chart                   *barChart
	Rows                    []readingRow
	More                    int
}

type readingForm struct {
	Meter, Day, Value, Note string
}

type settingsForm struct {
	Maint, Low, High             bool
	LowBelow, HighAbove, GasUnit string
}

type web struct{ svc *service }

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.luzPage))
	mux.HandleFunc("GET /mantenimiento", w.user(w.tasksPage))
	mux.HandleFunc("POST /mantenimiento/nueva", w.user(w.taskNewPost))
	mux.HandleFunc("POST /mantenimiento/sugerencia", w.user(w.templatePost))
	mux.HandleFunc("GET /tarea/{id}", w.user(w.taskPage))
	mux.HandleFunc("POST /tarea/{id}", w.user(w.taskPost))
	mux.HandleFunc("POST /tarea/{id}/hecho", w.user(w.taskDonePost))
	mux.HandleFunc("POST /tarea/{id}/borrar", w.user(w.taskDelete))
	mux.HandleFunc("GET /lecturas", w.user(w.readingsPage))
	mux.HandleFunc("POST /lecturas/nueva", w.user(w.readingNewPost))
	mux.HandleFunc("POST /lectura/{id}/borrar", w.user(w.readingDelete))
	mux.HandleFunc("GET /ajustes", w.user(w.settingsPage))
	mux.HandleFunc("POST /ajustes", w.user(w.settingsPost))
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
	w.svc.log.Error("casa web", "op", op, "err", err)
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
	"anadida": "Tarea añadida.", "ya-estaba": "Ya la tenías: aquí está.", "guardado": "Guardado.", "borrada": "Borrada.",
	"hecho": "Hecha. La próxima ya está calculada.", "apuntada": "Lectura apuntada.", "sustituida": "Lectura apuntada (sustituye a la de ese día).",
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func (w *web) today() time.Time { return dayOf(w.svc.clock()) }

// optDay lee una fecha que puede ir vacía.
func optDay(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	d, err := parseDay(s)
	if err != nil {
		return d, bad("La fecha va como AAAA-MM-DD.")
	}
	return d, nil
}

func dayValue(d time.Time) string {
	if d.IsZero() {
		return ""
	}
	return isoDay(d)
}

// ---- Luz ----

func (w *web) sourceView() sourceView {
	st := w.svc.ree.status()
	now := w.svc.clock()
	v := sourceView{OK: st.Err == "", Pending: st.OK.IsZero() && st.Err == "", Last: ago(st.OK, now), Error: st.Err}
	if st.Err != "" && st.Until.After(now) {
		v.Retry = "se vuelve a intentar en " + strconv.Itoa(int(st.Until.Sub(now).Minutes()+1)) + " min"
	}
	return v
}

func hourRows(hs []hourPrice, l *luzDay) []hourRow {
	var out []hourRow
	for _, h := range hs {
		out = append(out, hourRow{Label: spanLabel(h.T, h.T.Add(time.Hour)), Price: kwh(h.P), Level: level(h.P, l.CheapMax, l.DearMin),
			Now: l.HasNow && h.T.Equal(l.Now.T)})
	}
	return out
}

func luzViewOf(l *luzDay, title string, now time.Time) *luzView {
	if l == nil {
		return nil
	}
	v := &luzView{Title: title, Date: longDate(l.Day), IsToday: dayOf(now).Equal(l.Day), Chart: priceChart(l, now),
		Lo: kwh(l.Lo), Hi: kwh(l.Hi), Avg: kwh(l.Avg)}
	v.LoAt, v.HiAt = hoursList(l.Cheap[:1]), hoursList(l.Dear[:1])
	if l.HasNow {
		v.Now, v.NowLevel, v.NowAt = kwh(l.Now.P), level(l.Now.P, l.CheapMax, l.DearMin), spanLabel(l.Now.T, l.Now.T.Add(time.Hour))
	}
	if l.Has2 {
		v.Block2, v.Block2Avg = l.Block2.label(), kwh(l.Block2.Avg)
	}
	if l.Has3 {
		v.Block3, v.Block3Avg = l.Block3.label(), kwh(l.Block3.Avg)
	}
	if v.IsToday && l.HasRest3 && (!l.Has3 || !l.Rest3.Start.Equal(l.Block3.Start)) {
		v.Rest, v.RestAvg, v.RestDiffers = l.Rest3.label(), kwh(l.Rest3.Avg), true
		v.RestPast = l.Has3 && !l.Block3.End.After(now)
	}
	v.Cheap, v.Dear, v.Hours = hourRows(l.Cheap, l), hourRows(l.Dear, l), hourRows(l.Hours, l)
	return v
}

func (w *web) luzPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	today, tomorrow, err := w.svc.luz(r.Context())
	if err != nil {
		w.fail(rw, "luz", err)
		return
	}
	now := w.svc.clock()
	d := pageData{Name: who.Name, Tab: "luz", Today: luzViewOf(today, "Hoy", now), Tomorrow: luzViewOf(tomorrow, "Mañana", now), Source: w.sourceView()}
	w.render(rw, r, "luz.html", d)
}

// ---- Mantenimiento ----

var stateNames = map[string]string{stLate: "Atrasada", stToday: "Toca hoy", stSoon: "Pronto", stOK: "Al día"}

func taskRowOf(t task, today time.Time) taskRow {
	st, n := t.state(today)
	row := taskRow{ID: t.ID, Name: t.Name, Every: t.every(), Due: shortDate(t.Due, today), State: st, StateName: stateNames[st], Note: t.Note}
	if !t.Last.IsZero() {
		row.Last = shortDate(t.Last, today)
	}
	switch st {
	case stLate:
		row.When = "tocaba " + relDay(t.Due, today) + " (" + plural(-n, "día", "días") + " de retraso)"
	case stToday:
		row.When = "toca hoy"
	default:
		row.When = "toca " + inDays(n) + " (" + shortDate(t.Due, today) + ")"
	}
	return row
}

func unitOptions(sel string) []option {
	var out []option
	for _, u := range units {
		out = append(out, option{Key: u.Key, Label: u.Many, Selected: u.Key == sel})
	}
	return out
}

func (w *web) renderTasks(rw http.ResponseWriter, r *http.Request, who app.Identity, f *taskForm, msg string) {
	list, err := w.svc.store.tasks(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "mantenimiento", err)
		return
	}
	today := w.today()
	if f == nil {
		f = &taskForm{Every: "3", Unit: unitMonths}
	}
	d := pageData{Name: who.Name, Tab: "mantenimiento", Title: "Mantenimiento", TForm: *f, Units: unitOptions(f.Unit), Error: msg}
	have := map[string]bool{}
	for _, t := range list {
		row := taskRowOf(t, today)
		d.Tasks = append(d.Tasks, row)
		switch row.State {
		case stLate:
			d.Late++
		case stToday:
			d.Due++
		}
		have[t.Template] = true
	}
	for _, tp := range suggestions {
		if !have[tp.Key] {
			d.Suggest = append(d.Suggest, tp)
		}
	}
	w.render(rw, r, "mantenimiento.html", d)
}

func (w *web) tasksPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderTasks(rw, r, who, nil, "")
}

// readTask lee el formulario de una tarea.
func readTask(r *http.Request) (*taskForm, taskInput, error) {
	f := r.PostForm
	tf := &taskForm{Name: f.Get("nombre"), Every: f.Get("cada"), Unit: f.Get("unidad"), Last: f.Get("ultima"), Due: f.Get("proxima"), Note: f.Get("nota")}
	in := taskInput{Name: tf.Name, Unit: tf.Unit, Note: tf.Note}
	n, err := strconv.Atoi(strings.TrimSpace(tf.Every))
	if err != nil {
		return tf, in, bad("Cada cuánto va con un número entero: cada 3 meses.")
	}
	in.Every = n
	if in.Last, err = optDay(tf.Last); err != nil {
		return tf, in, err
	}
	if in.Due, err = optDay(tf.Due); err != nil {
		return tf, in, err
	}
	return tf, in, nil
}

func (w *web) taskNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	tf, in, err := readTask(r)
	if err == nil {
		_, err = w.svc.addTask(r.Context(), who.User, in)
	}
	switch {
	case isInput(err):
		w.renderTasks(rw, r, who, tf, err.Error())
	case errors.Is(err, errFull):
		w.renderTasks(rw, r, who, tf, "Has llegado al máximo de tareas.")
	case err != nil:
		w.fail(rw, "nueva tarea", err)
	default:
		back(rw, r, "/mantenimiento?ok=anadida")
	}
}

func (w *web) templatePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	t, existed, err := w.svc.addTemplate(r.Context(), who.User, r.PostForm.Get("clave"))
	switch {
	case isInput(err):
		http.Error(rw, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errFull):
		w.renderTasks(rw, r, who, nil, "Has llegado al máximo de tareas.")
	case err != nil:
		w.fail(rw, "sugerencia", err)
	case existed:
		back(rw, r, "/tarea/"+idStr(t.ID)+"?ok=ya-estaba")
	default:
		back(rw, r, "/mantenimiento?ok=anadida")
	}
}

func (w *web) renderTask(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, f *taskForm, msg string) {
	t, err := w.svc.store.task(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "tarea", err)
		return
	}
	today := w.today()
	v := &taskView{taskRow: taskRowOf(t, today), Template: t.Template != ""}
	for _, d := range t.Done {
		v.Done = append(v.Done, longDate(d)+" de "+strconv.Itoa(d.Year()))
	}
	if f == nil {
		f = &taskForm{Name: t.Name, Every: strconv.Itoa(t.Every), Unit: t.Unit, Last: dayValue(t.Last), Due: dayValue(t.Due), Note: t.Note}
	}
	d := pageData{Name: who.Name, Tab: "mantenimiento", Title: t.Name, Task: v, TForm: *f, Units: unitOptions(f.Unit), Error: msg,
		Confirm: r.URL.Query().Get("borrar") == "1"}
	w.render(rw, r, "tarea.html", d)
}

func (w *web) taskPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	w.renderTask(rw, r, who, id, nil, "")
}

func (w *web) taskPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if !form(rw, r) {
		return
	}
	tf, in, err := readTask(r)
	if err == nil {
		_, err = w.svc.updateTask(r.Context(), who.User, id, in)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderTask(rw, r, who, id, tf, err.Error())
	case err != nil:
		w.fail(rw, "guardar tarea", err)
	default:
		back(rw, r, "/tarea/"+idStr(id)+"?ok=guardado")
	}
}

func (w *web) taskDonePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if !form(rw, r) {
		return
	}
	day, err := optDay(r.PostForm.Get("fecha"))
	if err == nil {
		_, err = w.svc.markDone(r.Context(), who.User, id, day)
	}
	fromList := r.PostForm.Get("desde") == "lista"
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err) && fromList:
		w.renderTasks(rw, r, who, nil, err.Error())
	case isInput(err):
		w.renderTask(rw, r, who, id, nil, err.Error())
	case err != nil:
		w.fail(rw, "hecho", err)
	case fromList:
		back(rw, r, "/mantenimiento?ok=hecho")
	default:
		back(rw, r, "/tarea/"+idStr(id)+"?ok=hecho")
	}
}

func (w *web) taskDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if !form(rw, r) {
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		if _, err := w.svc.store.task(r.Context(), who.User, id); err != nil {
			notFound(rw)
			return
		}
		back(rw, r, "/tarea/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	err := w.svc.store.deleteTask(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar tarea", err)
	default:
		back(rw, r, "/mantenimiento?ok=borrada")
	}
}

// ---- Lecturas ----

// value escribe una lectura o un consumo con los decimales justos.
func value(v float64) string { return trimNum(v, 3) }

func meterSummaryOf(m meterView, today time.Time) *meterSummary {
	s := &meterSummary{Key: m.Meter, Label: meterLabel(m.Meter), Unit: m.Unit}
	for _, x := range meters {
		if x.Key == m.Meter {
			s.Emoji = x.Emoji
		}
	}
	if r, ok := m.last(); ok {
		s.HasLast, s.Last, s.LastDay = true, value(r.Value)+" "+m.Unit, relDay(r.Day, today)
	}
	if p, ok := m.lastPeriod(); ok {
		s.HasUse, s.LastUse, s.LastPerDay = true, value(p.Used)+" "+m.Unit, value(p.PerDay)+" "+m.Unit
		s.LastSpan = plural(p.Days, "día", "días") + ", del " + shortDate(p.From, today) + " al " + shortDate(p.To, today)
	}
	if m.AvgDays > 0 {
		s.HasAvg, s.AvgDay, s.AvgDays, s.YearUsed = true, value(m.AvgDay)+" "+m.Unit, plural(m.AvgDays, "día", "días"), value(m.YearUsed)+" "+m.Unit
	}
	s.Chart = usageChart(m, today)
	// Las lecturas, de la más nueva a la más antigua, con lo gastado desde
	// la anterior.
	byTo := map[time.Time]usage{}
	for _, p := range m.Periods {
		byTo[p.To] = p
	}
	for i := len(m.Readings) - 1; i >= 0; i-- {
		r := m.Readings[i]
		row := readingRow{ID: r.ID, Day: shortDate(r.Day, today), Value: value(r.Value), Note: r.Note, First: i == 0}
		if p, ok := byTo[r.Day]; ok && i > 0 {
			row.Reset = p.Reset
			if !p.Reset {
				row.Used, row.PerDay = "+"+value(p.Used), value(p.PerDay)
			}
		}
		if len(s.Rows) == 40 {
			s.More = i + 1
			break
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}

func meterOptions(sel string) []option {
	var out []option
	for _, m := range meters {
		out = append(out, option{Key: m.Key, Label: m.Label, Selected: m.Key == sel})
	}
	return out
}

func (w *web) renderReadings(rw http.ResponseWriter, r *http.Request, who app.Identity, meter string, f *readingForm, msg string) {
	views, _, err := w.svc.meterViews(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "lecturas", err)
		return
	}
	if !validMeter(meter) {
		meter = meterPower
	}
	today := w.today()
	if f == nil {
		f = &readingForm{Meter: meter, Day: isoDay(today)}
	}
	d := pageData{Name: who.Name, Tab: "lecturas", Title: "Lecturas", RForm: *f, MeterOpts: meterOptions(f.Meter), Error: msg}
	for _, m := range meters {
		d.MeterTabs = append(d.MeterTabs, meterTab{Key: m.Key, Label: m.Label, Emoji: m.Emoji, On: m.Key == meter})
	}
	for _, v := range views {
		if v.Meter == meter {
			d.Meter = meterSummaryOf(v, today)
		}
	}
	w.render(rw, r, "lecturas.html", d)
}

func (w *web) readingsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderReadings(rw, r, who, r.URL.Query().Get("c"), nil, "")
}

func (w *web) readingNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	rf := &readingForm{Meter: f.Get("contador"), Day: f.Get("fecha"), Value: f.Get("valor"), Note: f.Get("nota")}
	in := readingInput{Meter: rf.Meter, Note: rf.Note}
	var err error
	in.Day, err = optDay(rf.Day)
	if err == nil {
		if in.Value, err = parseNum(rf.Value); err != nil {
			err = bad("La lectura es un número: lo que marca el contador.")
		}
	}
	var replaced bool
	if err == nil {
		_, replaced, err = w.svc.addReading(r.Context(), who.User, in)
	}
	switch {
	case isInput(err):
		w.renderReadings(rw, r, who, rf.Meter, rf, err.Error())
	case errors.Is(err, errFull):
		w.renderReadings(rw, r, who, rf.Meter, rf, "Has llegado al máximo de lecturas.")
	case err != nil:
		w.fail(rw, "nueva lectura", err)
	case replaced:
		back(rw, r, "/lecturas?c="+rf.Meter+"&ok=sustituida")
	default:
		back(rw, r, "/lecturas?c="+rf.Meter+"&ok=apuntada")
	}
}

func (w *web) readingDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if !form(rw, r) {
		return
	}
	err := w.svc.store.deleteReading(r.Context(), who.User, id)
	meter := r.PostForm.Get("contador")
	if !validMeter(meter) {
		meter = meterPower
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar lectura", err)
	default:
		back(rw, r, "/lecturas?c="+meter+"&ok=borrada")
	}
}

// ---- Ajustes ----

func settingsFormOf(st settingsData) settingsForm {
	return settingsForm{Maint: !st.MaintOff, Low: st.PriceLow, High: st.PriceHigh,
		LowBelow: num(st.low(), 3), HighAbove: num(st.high(), 3), GasUnit: st.gasUnit()}
}

func (w *web) settingsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	st, err := w.svc.store.settings(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "ajustes", err)
		return
	}
	w.render(rw, r, "ajustes.html", pageData{Name: who.Name, Tab: "ajustes", Title: "Ajustes", Settings: settingsFormOf(st)})
}

// gasUnits son las unidades del contador de gas que se pueden elegir.
var gasUnits = []string{"m³", "kWh"}

func (w *web) settingsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	sf := settingsForm{Maint: f.Get("mantenimiento") == "1", Low: f.Get("barata") == "1", High: f.Get("cara") == "1",
		LowBelow: f.Get("barata_hasta"), HighAbove: f.Get("cara_desde"), GasUnit: f.Get("unidad_gas")}
	again := func(msg string) {
		w.render(rw, r, "ajustes.html", pageData{Name: who.Name, Tab: "ajustes", Title: "Ajustes", Settings: sf, Error: msg})
	}
	st := settingsData{MaintOff: !sf.Maint, PriceLow: sf.Low, PriceHigh: sf.High}
	lo, err1 := parseNum(sf.LowBelow)
	hi, err2 := parseNum(sf.HighAbove)
	switch {
	case err1 != nil || err2 != nil || lo <= 0 || hi <= 0 || lo > 2 || hi > 2:
		again("Los umbrales van en €/kWh, entre 0 y 2: por ejemplo 0,10.")
		return
	case lo >= hi:
		again("El umbral de «muy barata» tiene que ser menor que el de «muy cara».")
		return
	}
	st.LowBelow, st.HighAbove = lo, hi
	for _, u := range gasUnits {
		if u == sf.GasUnit {
			st.GasUnit = u
		}
	}
	if st.GasUnit == "" {
		again("Elige la unidad del gas.")
		return
	}
	if err := w.svc.store.saveSettings(r.Context(), who.User, st); err != nil {
		w.fail(rw, "ajustes", err)
		return
	}
	w.svc.kick() // por si ya están los precios de mañana
	back(rw, r, "/ajustes?ok=guardado")
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
