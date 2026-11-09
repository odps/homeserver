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
	"sort"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST y sin JavaScript:
// resumen de cada vehículo, repostajes, mantenimiento, gastos y gasolineras.
// La búsqueda de municipio también va por POST: no queda en ninguna URL.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 16 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /coche detrás de la pasarela
	Name    string
	Tab     string // resumen, repostajes, mantenimiento, gastos, gasolineras
	Title   string
	Flash   string
	Error   string
	Confirm bool

	VID      int64 // el vehículo de la página (0: ninguno)
	Vehicles []vehRow
	Veh      *vehView

	// Resumen
	Sum       *sumView
	ConsChart *lineChart
	CostChart *barChart

	// Repostajes
	Refuels  []refuelRow
	RForm    *refuelForm
	Stations []string

	// Mantenimiento
	Tasks     []taskRow
	TForm     *taskForm
	TaskKinds []option
	Task      *taskRow
	DForm     *doneForm

	// Gastos
	Expenses     []expenseRow
	EForm        *expenseForm
	ExpenseKinds []option
	ByKind       []kindTotal

	// Vehículos
	VForm *vehicleForm
	Fuels []option

	// Gasolineras
	Gas       *gasPage
	Products  []option
	MQuery    string
	MSearched bool
	MResults  []municipio
}

type option struct {
	Key, Label string
	Selected   bool
}

type vehRow struct {
	ID          int64
	Name, Model string
	Current     bool
}

type vehView struct {
	ID                       int64
	Name, Model, Plate, Fuel string
	Unit                     string
	EstKm, LastKm, LastKmDay string
	Estimated                bool
	StartKm                  string
	Refuels, Tasks, Expenses int
}

type sumView struct {
	HasCons                 bool
	Avg, Last               string
	FuelPerKm, AllPerKm     string
	ThisMonth, Year         string
	KmMonth                 string
	Liters, FuelCost, Other string
	Refuels, Segments       int
	Unit                    string
}

type taskRow struct {
	ID          int64
	Name, Kind  string
	Every       string
	Due         string
	Status      string
	StatusLabel string
	Last        string
	Note        string
}

type refuelRow struct {
	ID                   int64
	Day, Km              string
	Liters, Price, Total string
	Full                 bool
	Station, Note        string
	L100                 string
	Talia                bool
}

type refuelForm struct {
	ID                            int64
	Day, Km, Liters, Price, Total string
	Full                          bool
	Station, Note, Unit, KmHint   string
}

type taskForm struct {
	ID                               int64
	Kind, Name, EveryKm, EveryMonths string
	LastKm, LastDay, NextKm, NextDay string
	Note                             string
}

type doneForm struct {
	Day, Km, Cost, NextKm, NextDay, Note, KmHint string
}

type expenseRow struct {
	ID         int64
	Day, Kind  string
	KindKey    string
	Amount, Km string
	Note       string
	TaskID     int64
	Talia      bool
}

type expenseForm struct {
	ID                    int64
	Kind, Day, Amount, Km string
	Note                  string
	FromTask              bool
}

type kindTotal struct {
	Label, Amount string
}

type vehicleForm struct {
	ID                                int64
	Name, Model, Plate, Fuel, StartKm string
	Note                              string
}

type gasPage struct {
	Municipio, Province string
	Product, Label      string
	Rows                []gasRow
	Avg                 string
	Count               int
	Updated, Fetched    string
	Pending             bool
	Stale               bool
	SourceErr, Retry    string
	Summary             bool
}

type gasRow struct {
	Brand, Address, Town, Hours string
	Price, Diff                 string
	Best                        bool
}

type web struct{ svc *service }

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.summaryPage))
	mux.HandleFunc("GET /v/{id}", w.user(w.summaryPage))
	mux.HandleFunc("GET /vehiculos", w.user(w.vehiclesPage))
	mux.HandleFunc("POST /vehiculos", w.user(w.vehicleNewPost))
	mux.HandleFunc("GET /v/{id}/editar", w.user(w.vehicleEditPage))
	mux.HandleFunc("POST /v/{id}/editar", w.user(w.vehicleEditPost))
	mux.HandleFunc("POST /v/{id}/borrar", w.user(w.vehicleDelete))
	for _, tab := range []string{"repostajes", "mantenimiento", "gastos"} {
		mux.HandleFunc("GET /"+tab, w.user(w.firstVehicle(tab)))
	}
	mux.HandleFunc("GET /v/{id}/repostajes", w.user(w.refuelsPage))
	mux.HandleFunc("POST /v/{id}/repostajes", w.user(w.refuelNewPost))
	mux.HandleFunc("GET /repostaje/{id}", w.user(w.refuelPage))
	mux.HandleFunc("POST /repostaje/{id}", w.user(w.refuelPost))
	mux.HandleFunc("POST /repostaje/{id}/borrar", w.user(w.refuelDelete))
	mux.HandleFunc("GET /v/{id}/mantenimiento", w.user(w.tasksPage))
	mux.HandleFunc("POST /v/{id}/mantenimiento", w.user(w.taskNewPost))
	mux.HandleFunc("GET /tarea/{id}", w.user(w.taskPage))
	mux.HandleFunc("POST /tarea/{id}", w.user(w.taskPost))
	mux.HandleFunc("POST /tarea/{id}/hecho", w.user(w.taskDonePost))
	mux.HandleFunc("POST /tarea/{id}/borrar", w.user(w.taskDelete))
	mux.HandleFunc("GET /v/{id}/gastos", w.user(w.expensesPage))
	mux.HandleFunc("POST /v/{id}/gastos", w.user(w.expenseNewPost))
	mux.HandleFunc("GET /gasto/{id}", w.user(w.expensePage))
	mux.HandleFunc("POST /gasto/{id}", w.user(w.expensePost))
	mux.HandleFunc("POST /gasto/{id}/borrar", w.user(w.expenseDelete))
	mux.HandleFunc("GET /gasolineras", w.user(w.gasPage))
	mux.HandleFunc("POST /gasolineras", w.user(w.gasPrefsPost))
	mux.HandleFunc("POST /gasolineras/buscar", w.user(w.gasSearchPost))
	mux.HandleFunc("POST /gasolineras/municipio", w.user(w.gasMunicipioPost))
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
	w.svc.log.Error("coche web", "op", op, "err", err)
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
	"vehiculo": "Vehículo añadido.", "guardado": "Guardado.", "borrado": "Borrado.", "repostaje": "Repostaje apuntado.",
	"tarea": "Tarea añadida.", "hecho": "Hecho: cuenta desde hoy.", "gasto": "Gasto apuntado.", "municipio": "Municipio cambiado.",
	"preferencias": "Preferencias guardadas.",
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func vpath(id int64, tab string) string {
	if tab == "" {
		return "/v/" + idStr(id)
	}
	return "/v/" + idStr(id) + "/" + tab
}

// ---- Vistas comunes ----

func (w *web) vehRows(list []vehicle, cur int64) []vehRow {
	var out []vehRow
	for _, v := range list {
		out = append(out, vehRow{ID: v.ID, Name: v.Name, Model: v.Model, Current: v.ID == cur})
	}
	return out
}

func vehViewOf(st *vstats, today time.Time) *vehView {
	v := st.Vehicle
	vv := &vehView{ID: v.ID, Name: v.Name, Model: v.Model, Plate: v.Plate, Fuel: fuelLabel(v.Fuel), Unit: v.unit(),
		EstKm: kmStr(st.EstKm), LastKm: kmStr(st.LastKm), LastKmDay: shortDate(st.LastKmDay, today), Estimated: st.EstKm != st.LastKm,
		Refuels: len(st.Refuels), Tasks: len(st.Tasks), Expenses: len(st.Expenses)}
	if v.StartKm > 0 {
		vv.StartKm = kmStr(v.StartKm)
	}
	return vv
}

// base de una página de un vehículo: su cabecera y los demás para cambiar.
func (w *web) vehicleData(r *http.Request, who app.Identity, st *vstats, tab, title string) (pageData, error) {
	list, err := w.svc.store.vehicles(r.Context(), who.User)
	if err != nil {
		return pageData{}, err
	}
	return pageData{Name: who.Name, Tab: tab, Title: title, VID: st.Vehicle.ID, Veh: vehViewOf(st, w.svc.today()),
		Vehicles: w.vehRows(list, st.Vehicle.ID), Confirm: r.URL.Query().Get("borrar") == "1"}, nil
}

// loadPath carga el vehículo de la ruta. false si ya se ha contestado.
func (w *web) loadPath(rw http.ResponseWriter, r *http.Request, who app.Identity) (*vstats, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return nil, false
	}
	st, err := w.svc.load(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return nil, false
	case err != nil:
		w.fail(rw, "vehículo", err)
		return nil, false
	}
	return st, true
}

// firstVehicle lleva la pestaña al primer vehículo (o al resumen si no hay).
func (w *web) firstVehicle(tab string) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		list, err := w.svc.store.vehicles(r.Context(), who.User)
		if err != nil {
			w.fail(rw, "vehículos", err)
			return
		}
		if len(list) == 0 {
			back(rw, r, "/")
			return
		}
		back(rw, r, vpath(list[0].ID, tab))
	}
}

func options[T any](list []T, key func(T) (string, string), sel string) []option {
	var out []option
	for _, x := range list {
		k, l := key(x)
		out = append(out, option{Key: k, Label: l, Selected: k == sel})
	}
	return out
}

func fuelOptions(sel string) []option {
	return options(fuels, func(f struct{ Key, Label, Product string }) (string, string) { return f.Key, f.Label }, sel)
}

func productOptions(sel string) []option {
	return options(products, func(p struct{ Key, Label, Field string }) (string, string) { return p.Key, p.Label }, sel)
}

func expenseKindOptions(sel string) []option {
	return options(expenseKinds, func(k struct{ Key, Label string }) (string, string) { return k.Key, k.Label }, sel)
}

func taskKindOptions(sel string) []option {
	var out []option
	for _, k := range taskKinds {
		label := k.Label
		if e := everyText(taskData{EveryKm: k.EveryKm, EveryMonths: k.EveryMonths}); e != "" {
			label += " (" + e + ")"
		}
		out = append(out, option{Key: k.Key, Label: label, Selected: k.Key == sel})
	}
	return out
}

// numField escribe un número para un campo de formulario (vacío si es 0).
func numField(v float64, dec int) string {
	if v == 0 {
		return ""
	}
	return trimNum(v, dec)
}

func kmField(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func dayField(d time.Time) string {
	if d.IsZero() {
		return ""
	}
	return isoDay(d)
}

// ---- Resumen ----

func sumOf(st *vstats) *sumView {
	unit := st.Vehicle.unit()
	s := &sumView{HasCons: st.AvgL100 > 0, ThisMonth: euros(st.thisMonth()), Year: eurosRound(st.year()), Refuels: len(st.Refuels),
		Segments: len(st.Segments), Unit: unit, Liters: num(st.Liters, 0) + " " + unit, FuelCost: eurosRound(st.FuelCost), Other: eurosRound(st.OtherCost)}
	if s.HasCons {
		s.Avg = num(st.AvgL100, 1)
		s.Last = num(st.LastL100, 1)
		s.FuelPerKm = eurPerKm(st.FuelPerKm)
	}
	if st.AllPerKm > 0 {
		s.AllPerKm = eurPerKm(st.AllPerKm)
	}
	if st.KmPerDay > 0 {
		s.KmMonth = kmStr(int(math.Round(st.KmPerDay * 30.44)))
	}
	return s
}

var statusLabels = map[string]string{dueOver: "Vencida", dueSoon: "Pronto", dueOK: "Al día", dueNone: "Sin datos"}

func taskRowOf(t taskState, today time.Time) taskRow {
	row := taskRow{ID: t.ID, Name: t.Name, Kind: t.Kind, Every: everyText(t.taskData), Due: dueText(t, today), Status: t.Status,
		StatusLabel: statusLabels[t.Status], Note: t.Note}
	var last []string
	if !t.LastDay.IsZero() {
		last = append(last, shortDate(t.LastDay, today))
	}
	if t.LastKm > 0 {
		last = append(last, kmStr(t.LastKm))
	}
	row.Last = strings.Join(last, " · ")
	return row
}

func taskRows(st *vstats, today time.Time, n int) []taskRow {
	var out []taskRow
	for _, t := range st.Tasks {
		if n > 0 && len(out) >= n {
			break
		}
		out = append(out, taskRowOf(t, today))
	}
	return out
}

func consChart(st *vstats, today time.Time) *lineChart {
	unit := st.Vehicle.unit()
	var ps []pt
	for _, sg := range st.Segments {
		ps = append(ps, pt{T: sg.To, V: sg.L100, Title: shortDate(sg.To, today) + ": " + num(sg.L100, 1) + " " + unit + "/100 km en " + kmStr(sg.Km)})
	}
	return buildLine(ps, st.AvgL100, "Consumo de cada tramo entre llenados, en "+unit+"/100 km, con la media", today)
}

func (w *web) summaryData(r *http.Request, who app.Identity, st *vstats) (pageData, error) {
	today := w.svc.today()
	d, err := w.vehicleData(r, who, st, "resumen", "")
	if err != nil {
		return d, err
	}
	d.Sum, d.Tasks = sumOf(st), taskRows(st, today, 4)
	d.ConsChart = consChart(st, today)
	d.CostChart = buildBars(st.Months, "Gasto de cada mes en euros: combustible y lo demás")
	return d, nil
}

func (w *web) summaryPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	var st *vstats
	if r.PathValue("id") != "" {
		var ok bool
		if st, ok = w.loadPath(rw, r, who); !ok {
			return
		}
	} else {
		list, err := w.svc.store.vehicles(r.Context(), who.User)
		if err != nil {
			w.fail(rw, "resumen", err)
			return
		}
		if len(list) == 0 {
			w.render(rw, r, "resumen.html", pageData{Name: who.Name, Tab: "resumen", VForm: &vehicleForm{Fuel: "gasolina"}, Fuels: fuelOptions("gasolina")})
			return
		}
		if st, err = w.svc.load(r.Context(), who.User, list[0].ID); err != nil {
			w.fail(rw, "resumen", err)
			return
		}
	}
	d, err := w.summaryData(r, who, st)
	if err != nil {
		w.fail(rw, "resumen", err)
		return
	}
	w.render(rw, r, "resumen.html", d)
}

// ---- Vehículos ----

func readVehicle(r *http.Request) (*vehicleForm, vehicleInput, error) {
	f := r.PostForm
	fm := &vehicleForm{Name: f.Get("nombre"), Model: f.Get("modelo"), Plate: f.Get("matricula"), Fuel: f.Get("combustible"),
		StartKm: f.Get("km"), Note: f.Get("nota")}
	in := vehicleInput{Name: fm.Name, Model: fm.Model, Plate: fm.Plate, Fuel: fm.Fuel, Note: fm.Note}
	km, err := parseOptKm(fm.StartKm)
	if err != nil {
		return fm, in, bad("Los km iniciales no son un número.")
	}
	in.StartKm = km
	return fm, in, nil
}

func (w *web) vehiclesData(r *http.Request, who app.Identity, fm *vehicleForm, msg string) (pageData, error) {
	list, err := w.svc.store.vehicles(r.Context(), who.User)
	if err != nil {
		return pageData{}, err
	}
	if fm == nil {
		fm = &vehicleForm{Fuel: "gasolina"}
	}
	d := pageData{Name: who.Name, Tab: "resumen", Title: "Vehículos", Vehicles: w.vehRows(list, 0), VForm: fm, Fuels: fuelOptions(fm.Fuel), Error: msg}
	if len(list) > 0 {
		d.VID = list[0].ID
	}
	return d, nil
}

func (w *web) vehiclesPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d, err := w.vehiclesData(r, who, nil, "")
	if err != nil {
		w.fail(rw, "vehículos", err)
		return
	}
	w.render(rw, r, "vehiculos.html", d)
}

func (w *web) vehicleNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	fm, in, err := readVehicle(r)
	var id int64
	if err == nil {
		id, err = w.svc.addVehicle(r.Context(), who.User, in)
	}
	switch {
	case isInput(err) || errors.Is(err, errFull):
		msg := err.Error()
		if errors.Is(err, errFull) {
			msg = "Has llegado al máximo de vehículos."
		}
		d, lerr := w.vehiclesData(r, who, fm, msg)
		if lerr != nil {
			w.fail(rw, "vehículo nuevo", lerr)
			return
		}
		w.render(rw, r, "vehiculos.html", d)
		return
	case err != nil:
		w.fail(rw, "vehículo nuevo", err)
		return
	}
	back(rw, r, vpath(id, "")+"?ok=vehiculo")
}

func (w *web) renderVehicleEdit(rw http.ResponseWriter, r *http.Request, who app.Identity, st *vstats, fm *vehicleForm, msg string) {
	d, err := w.vehicleData(r, who, st, "resumen", "Editar "+st.Vehicle.Name)
	if err != nil {
		w.fail(rw, "editar vehículo", err)
		return
	}
	d.VForm, d.Fuels, d.Error = fm, fuelOptions(fm.Fuel), msg
	w.render(rw, r, "vehiculo.html", d)
}

func (w *web) vehicleEditPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	st, ok := w.loadPath(rw, r, who)
	if !ok {
		return
	}
	v := st.Vehicle
	w.renderVehicleEdit(rw, r, who, st, &vehicleForm{ID: v.ID, Name: v.Name, Model: v.Model, Plate: v.Plate, Fuel: v.Fuel, StartKm: kmField(v.StartKm), Note: v.Note}, "")
}

func (w *web) vehicleEditPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	st, ok := w.loadPath(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readVehicle(r)
	fm.ID = st.Vehicle.ID
	if err == nil {
		err = w.svc.updateVehicle(r.Context(), who.User, st.Vehicle.ID, in)
	}
	switch {
	case isInput(err):
		w.renderVehicleEdit(rw, r, who, st, fm, err.Error())
		return
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case err != nil:
		w.fail(rw, "editar vehículo", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "")+"?ok=guardado")
}

func (w *web) vehicleDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if !form(rw, r) {
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, vpath(id, "editar")+"?borrar=1#borrar")
		return
	}
	err := w.svc.store.deleteVehicle(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar vehículo", err)
	default:
		back(rw, r, "/?ok=borrado")
	}
}

// ---- Repostajes ----

func (w *web) stations(st *vstats) []string {
	seen := map[string]bool{}
	var out []string
	for i := len(st.Refuels) - 1; i >= 0 && len(out) < 30; i-- {
		s := st.Refuels[i].Station
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	return out
}

func refuelRows(st *vstats, today time.Time) []refuelRow {
	unit := st.Vehicle.unit()
	cons := map[int64]float64{}
	for _, sg := range st.Segments {
		cons[sg.ToID] = sg.L100
	}
	var out []refuelRow
	for i := len(st.Refuels) - 1; i >= 0; i-- {
		x := st.Refuels[i]
		row := refuelRow{ID: x.ID, Day: shortDate(x.Day, today), Km: kmStr(x.Km), Liters: num(x.Liters, 2) + " " + unit, Price: perLiter(x.Price, unit),
			Total: euros(x.Total), Full: x.Full, Station: x.Station, Note: x.Note, Talia: x.Origin == "asistente"}
		if c, ok := cons[x.ID]; ok {
			row.L100 = num(c, 1) + " " + unit + "/100 km"
		}
		out = append(out, row)
	}
	return out
}

func (w *web) newRefuelForm(st *vstats) *refuelForm {
	return &refuelForm{Day: isoDay(w.svc.today()), Full: true, Unit: st.Vehicle.unit(), KmHint: num(float64(st.EstKm), 0)}
}

func (w *web) renderRefuels(rw http.ResponseWriter, r *http.Request, who app.Identity, st *vstats, fm *refuelForm, msg string) {
	d, err := w.vehicleData(r, who, st, "repostajes", "Repostajes")
	if err != nil {
		w.fail(rw, "repostajes", err)
		return
	}
	if fm == nil {
		fm = w.newRefuelForm(st)
	}
	d.RForm, d.Error, d.Stations = fm, msg, w.stations(st)
	d.Refuels, d.Sum = refuelRows(st, w.svc.today()), sumOf(st)
	w.render(rw, r, "repostajes.html", d)
}

func (w *web) refuelsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if st, ok := w.loadPath(rw, r, who); ok {
		w.renderRefuels(rw, r, who, st, nil, "")
	}
}

func readRefuel(r *http.Request, unit string) (*refuelForm, refuelInput, error) {
	f := r.PostForm
	fm := &refuelForm{Day: f.Get("fecha"), Km: f.Get("km"), Liters: f.Get("litros"), Price: f.Get("precio"), Total: f.Get("total"),
		Full: f.Get("lleno") == "1", Station: f.Get("gasolinera"), Note: f.Get("nota"), Unit: unit}
	in := refuelInput{Full: fm.Full, Station: fm.Station, Note: fm.Note}
	day, err := parseDay(fm.Day)
	if err != nil {
		return fm, in, bad("La fecha no es válida.")
	}
	in.Day = day
	if in.Km, err = parseOptKm(fm.Km); err != nil {
		return fm, in, bad("Los km no son un número.")
	}
	for _, x := range []struct {
		s     string
		dst   *float64
		label string
	}{{fm.Liters, &in.Liters, "Los " + unitWord(unit)}, {fm.Price, &in.Price, "El precio"}, {fm.Total, &in.Total, "El total"}} {
		v, err := parseOpt(x.s)
		if err != nil {
			return fm, in, bad(x.label + " no es un número.")
		}
		*x.dst = v
	}
	return fm, in, nil
}

func (w *web) refuelNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	st, ok := w.loadPath(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readRefuel(r, st.Vehicle.unit())
	fm.KmHint = num(float64(st.EstKm), 0)
	in.VehicleID, in.Origin = st.Vehicle.ID, "web"
	if err == nil {
		_, err = w.svc.addRefuel(r.Context(), who.User, in)
	}
	switch {
	case isInput(err) || errors.Is(err, errFull):
		msg := err.Error()
		if errors.Is(err, errFull) {
			msg = "Has llegado al máximo de repostajes."
		}
		w.renderRefuels(rw, r, who, st, fm, msg)
		return
	case err != nil:
		w.fail(rw, "apuntar repostaje", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "repostajes")+"?ok=repostaje")
}

func (w *web) renderRefuel(rw http.ResponseWriter, r *http.Request, who app.Identity, st *vstats, fm *refuelForm, msg string) {
	d, err := w.vehicleData(r, who, st, "repostajes", "Editar repostaje")
	if err != nil {
		w.fail(rw, "repostaje", err)
		return
	}
	d.RForm, d.Error, d.Stations = fm, msg, w.stations(st)
	w.render(rw, r, "repostaje.html", d)
}

// ownedRefuel carga un repostaje y las cuentas de su vehículo.
func (w *web) ownedRefuel(rw http.ResponseWriter, r *http.Request, who app.Identity) (refuel, *vstats, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return refuel{}, nil, false
	}
	x, err := w.svc.store.refuel(r.Context(), who.User, id)
	var st *vstats
	if err == nil {
		st, err = w.svc.load(r.Context(), who.User, x.VehicleID)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return x, nil, false
	case err != nil:
		w.fail(rw, "repostaje", err)
		return x, nil, false
	}
	return x, st, true
}

func (w *web) refuelPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	x, st, ok := w.ownedRefuel(rw, r, who)
	if !ok {
		return
	}
	w.renderRefuel(rw, r, who, st, &refuelForm{ID: x.ID, Day: isoDay(x.Day), Km: kmField(x.Km), Liters: numField(x.Liters, 3), Price: numField(x.Price, 3),
		Total: numField(x.Total, 2), Full: x.Full, Station: x.Station, Note: x.Note, Unit: st.Vehicle.unit()}, "")
}

func (w *web) refuelPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	x, st, ok := w.ownedRefuel(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readRefuel(r, st.Vehicle.unit())
	fm.ID = x.ID
	if err == nil {
		_, err = w.svc.updateRefuel(r.Context(), who.User, x.ID, in)
	}
	switch {
	case isInput(err):
		w.renderRefuel(rw, r, who, st, fm, err.Error())
		return
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case err != nil:
		w.fail(rw, "editar repostaje", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "repostajes")+"?ok=guardado")
}

func (w *web) refuelDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	x, st, ok := w.ownedRefuel(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/repostaje/"+idStr(x.ID)+"?borrar=1#borrar")
		return
	}
	if err := w.svc.store.deleteRefuel(r.Context(), who.User, x.ID); err != nil {
		w.fail(rw, "borrar repostaje", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "repostajes")+"?ok=borrado")
}

// ---- Mantenimiento ----

func readTask(r *http.Request) (*taskForm, taskInput, error) {
	f := r.PostForm
	fm := &taskForm{Kind: f.Get("tipo"), Name: f.Get("nombre"), EveryKm: f.Get("cada_km"), EveryMonths: f.Get("cada_meses"),
		LastKm: f.Get("ultimo_km"), LastDay: f.Get("ultima_fecha"), NextKm: f.Get("proximo_km"), NextDay: f.Get("proxima_fecha"), Note: f.Get("nota")}
	in := taskInput{Kind: fm.Kind, Name: fm.Name, Note: fm.Note}
	var err error
	for _, x := range []struct {
		s   string
		dst *int
	}{{fm.EveryKm, &in.EveryKm}, {fm.LastKm, &in.LastKm}, {fm.NextKm, &in.NextKm}} {
		if *x.dst, err = parseOptKm(x.s); err != nil {
			return fm, in, bad("Los km no son un número.")
		}
	}
	if m := strings.TrimSpace(fm.EveryMonths); m != "" {
		if in.EveryMonths, err = strconv.Atoi(m); err != nil {
			return fm, in, bad("Los meses no son un número.")
		}
	}
	for _, x := range []struct {
		s   string
		dst *time.Time
	}{{fm.LastDay, &in.LastDay}, {fm.NextDay, &in.NextDay}} {
		if strings.TrimSpace(x.s) == "" {
			continue
		}
		if *x.dst, err = parseDay(x.s); err != nil {
			return fm, in, bad("La fecha no es válida.")
		}
	}
	return fm, in, nil
}

func (w *web) renderTasks(rw http.ResponseWriter, r *http.Request, who app.Identity, st *vstats, fm *taskForm, msg string) {
	d, err := w.vehicleData(r, who, st, "mantenimiento", "Mantenimiento")
	if err != nil {
		w.fail(rw, "mantenimiento", err)
		return
	}
	if fm == nil {
		fm = &taskForm{Kind: "aceite"}
	}
	d.TForm, d.TaskKinds, d.Error = fm, taskKindOptions(fm.Kind), msg
	d.Tasks = taskRows(st, w.svc.today(), 0)
	w.render(rw, r, "mantenimiento.html", d)
}

func (w *web) tasksPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if st, ok := w.loadPath(rw, r, who); ok {
		w.renderTasks(rw, r, who, st, nil, "")
	}
}

func (w *web) taskNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	st, ok := w.loadPath(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readTask(r)
	in.VehicleID = st.Vehicle.ID
	var t task
	if err == nil {
		t, err = w.svc.addTask(r.Context(), who.User, in)
	}
	switch {
	case isInput(err) || errors.Is(err, errFull):
		msg := err.Error()
		if errors.Is(err, errFull) {
			msg = "Has llegado al máximo de tareas de este vehículo."
		}
		w.renderTasks(rw, r, who, st, fm, msg)
		return
	case err != nil:
		w.fail(rw, "tarea nueva", err)
		return
	}
	back(rw, r, "/tarea/"+idStr(t.ID)+"?ok=tarea")
}

// ownedTask carga una tarea y las cuentas de su vehículo.
func (w *web) ownedTask(rw http.ResponseWriter, r *http.Request, who app.Identity) (taskState, *vstats, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return taskState{}, nil, false
	}
	t, err := w.svc.store.task(r.Context(), who.User, id)
	var st *vstats
	if err == nil {
		st, err = w.svc.load(r.Context(), who.User, t.VehicleID)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return taskState{}, nil, false
	case err != nil:
		w.fail(rw, "tarea", err)
		return taskState{}, nil, false
	}
	ts, _ := st.task(id)
	return ts, st, true
}

func taskFormOf(t task) *taskForm {
	return &taskForm{ID: t.ID, Kind: t.Kind, Name: t.Name, EveryKm: kmField(t.EveryKm), EveryMonths: kmField(t.EveryMonths), LastKm: kmField(t.LastKm),
		LastDay: dayField(t.LastDay), NextKm: kmField(t.NextKm), NextDay: dayField(t.NextDay), Note: t.Note}
}

func expenseRows(list []expense, today time.Time) []expenseRow {
	var out []expenseRow
	for i := len(list) - 1; i >= 0; i-- {
		e := list[i]
		row := expenseRow{ID: e.ID, Day: shortDate(e.Day, today), Kind: expenseLabel(e.Kind), KindKey: e.Kind, Amount: euros(e.Amount), Note: e.Note,
			TaskID: e.TaskID, Talia: e.Origin == "asistente"}
		if e.Km > 0 {
			row.Km = kmStr(e.Km)
		}
		out = append(out, row)
	}
	return out
}

func (w *web) renderTask(rw http.ResponseWriter, r *http.Request, who app.Identity, ts taskState, st *vstats, tf *taskForm, df *doneForm, msg string) {
	today := w.svc.today()
	d, err := w.vehicleData(r, who, st, "mantenimiento", ts.Name)
	if err != nil {
		w.fail(rw, "tarea", err)
		return
	}
	row := taskRowOf(ts, today)
	if tf == nil {
		tf = taskFormOf(ts.task)
	}
	if df == nil {
		df = &doneForm{Day: isoDay(today)}
	}
	df.KmHint = num(float64(st.EstKm), 0)
	var mine []expense
	for _, e := range st.Expenses {
		if e.TaskID == ts.ID {
			mine = append(mine, e)
		}
	}
	d.Task, d.TForm, d.DForm, d.TaskKinds, d.Error, d.Expenses = &row, tf, df, taskKindOptions(tf.Kind), msg, expenseRows(mine, today)
	w.render(rw, r, "tarea.html", d)
}

func (w *web) taskPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if ts, st, ok := w.ownedTask(rw, r, who); ok {
		w.renderTask(rw, r, who, ts, st, nil, nil, "")
	}
}

func (w *web) taskPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ts, st, ok := w.ownedTask(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readTask(r)
	fm.ID = ts.ID
	if err == nil {
		_, err = w.svc.updateTask(r.Context(), who.User, ts.ID, in)
	}
	switch {
	case isInput(err):
		w.renderTask(rw, r, who, ts, st, fm, nil, err.Error())
		return
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case err != nil:
		w.fail(rw, "editar tarea", err)
		return
	}
	back(rw, r, "/tarea/"+idStr(ts.ID)+"?ok=guardado")
}

func (w *web) taskDonePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ts, st, ok := w.ownedTask(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	f := r.PostForm
	df := &doneForm{Day: f.Get("fecha"), Km: f.Get("km"), Cost: f.Get("coste"), NextKm: f.Get("proximo_km"), NextDay: f.Get("proxima_fecha"), Note: f.Get("nota")}
	in := doneInput{Note: df.Note, Origin: "web"}
	var err error
	if in.Day, err = parseDay(df.Day); err != nil {
		err = bad("La fecha no es válida.")
	}
	if err == nil {
		if in.Km, err = parseOptKm(df.Km); err != nil {
			err = bad("Los km no son un número.")
		}
	}
	if err == nil {
		if in.NextKm, err = parseOptKm(df.NextKm); err != nil {
			err = bad("Los km de la próxima vez no son un número.")
		}
	}
	if err == nil {
		if in.Cost, err = parseOpt(df.Cost); err != nil {
			err = bad("El coste no es un número.")
		}
	}
	if err == nil && strings.TrimSpace(df.NextDay) != "" {
		if in.NextDay, err = parseDay(df.NextDay); err != nil {
			err = bad("La fecha de la próxima vez no es válida.")
		}
	}
	if err == nil {
		_, _, err = w.svc.taskDone(r.Context(), who.User, ts.ID, in)
	}
	switch {
	case isInput(err):
		w.renderTask(rw, r, who, ts, st, nil, df, err.Error())
		return
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case err != nil:
		w.fail(rw, "tarea hecha", err)
		return
	}
	back(rw, r, "/tarea/"+idStr(ts.ID)+"?ok=hecho")
}

func (w *web) taskDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ts, st, ok := w.ownedTask(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/tarea/"+idStr(ts.ID)+"?borrar=1#borrar")
		return
	}
	if err := w.svc.store.deleteTask(r.Context(), who.User, ts.ID); err != nil {
		w.fail(rw, "borrar tarea", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "mantenimiento")+"?ok=borrado")
}

// ---- Gastos ----

func readExpense(r *http.Request) (*expenseForm, expenseInput, error) {
	f := r.PostForm
	fm := &expenseForm{Kind: f.Get("tipo"), Day: f.Get("fecha"), Amount: f.Get("importe"), Km: f.Get("km"), Note: f.Get("nota")}
	in := expenseInput{Kind: fm.Kind, Note: fm.Note}
	var err error
	if in.Day, err = parseDay(fm.Day); err != nil {
		return fm, in, bad("La fecha no es válida.")
	}
	if in.Amount, err = parseOpt(fm.Amount); err != nil {
		return fm, in, bad("El importe no es un número.")
	}
	if in.Km, err = parseOptKm(fm.Km); err != nil {
		return fm, in, bad("Los km no son un número.")
	}
	return fm, in, nil
}

// byKind es el gasto de los últimos 12 meses por tipo (con el combustible).
func byKind(st *vstats, today time.Time) []kindTotal {
	from := addMonths(time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), -11)
	sums := map[string]float64{}
	fuel := 0.0
	for _, x := range st.Refuels {
		if !x.Day.Before(from) {
			fuel += x.Total
		}
	}
	for _, e := range st.Expenses {
		if !e.Day.Before(from) {
			sums[e.Kind] += e.Amount
		}
	}
	type kv struct {
		k string
		v float64
	}
	var list []kv
	for k, v := range sums {
		if v > 0 {
			list = append(list, kv{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	var out []kindTotal
	if fuel > 0 {
		out = append(out, kindTotal{Label: "Combustible", Amount: euros(fuel)})
	}
	for _, x := range list {
		out = append(out, kindTotal{Label: expenseLabel(x.k), Amount: euros(x.v)})
	}
	return out
}

func (w *web) renderExpenses(rw http.ResponseWriter, r *http.Request, who app.Identity, st *vstats, fm *expenseForm, msg string) {
	today := w.svc.today()
	d, err := w.vehicleData(r, who, st, "gastos", "Gastos")
	if err != nil {
		w.fail(rw, "gastos", err)
		return
	}
	if fm == nil {
		fm = &expenseForm{Kind: "parking", Day: isoDay(today)}
	}
	d.EForm, d.ExpenseKinds, d.Error = fm, expenseKindOptions(fm.Kind), msg
	d.Expenses, d.ByKind, d.Sum = expenseRows(st.Expenses, today), byKind(st, today), sumOf(st)
	w.render(rw, r, "gastos.html", d)
}

func (w *web) expensesPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if st, ok := w.loadPath(rw, r, who); ok {
		w.renderExpenses(rw, r, who, st, nil, "")
	}
}

func (w *web) expenseNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	st, ok := w.loadPath(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readExpense(r)
	in.VehicleID, in.Origin = st.Vehicle.ID, "web"
	if err == nil {
		_, err = w.svc.addExpense(r.Context(), who.User, in)
	}
	switch {
	case isInput(err) || errors.Is(err, errFull):
		msg := err.Error()
		if errors.Is(err, errFull) {
			msg = "Has llegado al máximo de gastos."
		}
		w.renderExpenses(rw, r, who, st, fm, msg)
		return
	case err != nil:
		w.fail(rw, "apuntar gasto", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "gastos")+"?ok=gasto")
}

func (w *web) ownedExpense(rw http.ResponseWriter, r *http.Request, who app.Identity) (expense, *vstats, bool) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return expense{}, nil, false
	}
	e, err := w.svc.store.expense(r.Context(), who.User, id)
	var st *vstats
	if err == nil {
		st, err = w.svc.load(r.Context(), who.User, e.VehicleID)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return e, nil, false
	case err != nil:
		w.fail(rw, "gasto", err)
		return e, nil, false
	}
	return e, st, true
}

func (w *web) renderExpense(rw http.ResponseWriter, r *http.Request, who app.Identity, st *vstats, fm *expenseForm, msg string) {
	d, err := w.vehicleData(r, who, st, "gastos", "Editar gasto")
	if err != nil {
		w.fail(rw, "gasto", err)
		return
	}
	d.EForm, d.ExpenseKinds, d.Error = fm, expenseKindOptions(fm.Kind), msg
	w.render(rw, r, "gasto.html", d)
}

func (w *web) expensePage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	e, st, ok := w.ownedExpense(rw, r, who)
	if !ok {
		return
	}
	w.renderExpense(rw, r, who, st, &expenseForm{ID: e.ID, Kind: e.Kind, Day: isoDay(e.Day), Amount: numField(e.Amount, 2), Km: kmField(e.Km),
		Note: e.Note, FromTask: e.TaskID != 0}, "")
}

func (w *web) expensePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	e, st, ok := w.ownedExpense(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	fm, in, err := readExpense(r)
	fm.ID, fm.FromTask = e.ID, e.TaskID != 0
	if err == nil {
		_, err = w.svc.updateExpense(r.Context(), who.User, e.ID, in)
	}
	switch {
	case isInput(err):
		w.renderExpense(rw, r, who, st, fm, err.Error())
		return
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case err != nil:
		w.fail(rw, "editar gasto", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "gastos")+"?ok=guardado")
}

func (w *web) expenseDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	e, st, ok := w.ownedExpense(rw, r, who)
	if !ok || !form(rw, r) {
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/gasto/"+idStr(e.ID)+"?borrar=1#borrar")
		return
	}
	if err := w.svc.store.deleteExpense(r.Context(), who.User, e.ID); err != nil {
		w.fail(rw, "borrar gasto", err)
		return
	}
	back(rw, r, vpath(st.Vehicle.ID, "gastos")+"?ok=borrado")
}

// ---- Gasolineras ----

func (w *web) gasPageOf(g *gasView) *gasPage {
	now := w.svc.clock()
	p := &gasPage{Municipio: g.Settings.Municipio.Name, Province: g.Settings.Municipio.Province, Product: g.Product, Label: productLabel(g.Product),
		Count: g.Count, Summary: g.Settings.Summary, Pending: g.Prices.Fetched.IsZero(), Stale: !g.Prices.Fetched.IsZero() && now.Sub(g.Prices.Fetched) > 2*priceEvery}
	if !p.Pending {
		p.Fetched = ago(g.Prices.Fetched, now)
		if !g.Prices.Updated.IsZero() {
			p.Updated = ago(g.Prices.Updated, now)
		}
	}
	if g.Avg > 0 {
		p.Avg = perLiter(g.Avg, "l")
	}
	for i, s := range g.Best {
		row := gasRow{Brand: s.Brand, Address: s.Address, Town: s.Town, Hours: s.Hours, Price: perLiter(s.Price, "l"), Best: i == 0}
		if g.sameTown(s.Town) {
			row.Town = ""
		}
		if d := (g.Avg - s.Price) * 100; d >= 0.05 {
			row.Diff = num(d, 1) + " cts menos que la media"
		}
		p.Rows = append(p.Rows, row)
	}
	if st := w.svc.fuel.state(); st.Err != "" {
		p.SourceErr = st.Err
		if st.Until.After(now) {
			p.Retry = "se vuelve a intentar " + strings.Replace(ago(now.Add(-st.Until.Sub(now)), now), "hace", "en", 1)
		}
	}
	return p
}

// sameTown dice si la localidad es la del propio municipio (y no hace falta
// repetirla).
func (g *gasView) sameTown(t string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(t), " "), g.Settings.Municipio.Name)
}

func (w *web) renderGas(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData) {
	g, err := w.svc.gas(r.Context(), who.User, nil, "", 12, false)
	if err != nil {
		w.fail(rw, "gasolineras", err)
		return
	}
	list, err := w.svc.store.vehicles(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "gasolineras", err)
		return
	}
	d.Name, d.Tab, d.Title = who.Name, "gasolineras", "Gasolineras"
	if len(list) > 0 {
		d.VID = list[0].ID
	}
	d.Gas = w.gasPageOf(g)
	d.Products = productOptions(g.Product)
	w.render(rw, r, "gasolineras.html", d)
}

func (w *web) gasPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderGas(rw, r, who, pageData{})
}

func (w *web) gasPrefsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	err := w.svc.setPrefs(r.Context(), who.User, r.PostForm.Get("carburante"), r.PostForm.Get("resumen") == "1")
	switch {
	case isInput(err):
		w.renderGas(rw, r, who, pageData{Error: err.Error()})
		return
	case err != nil:
		w.fail(rw, "preferencias", err)
		return
	}
	back(rw, r, "/gasolineras?ok=preferencias")
}

func (w *web) gasSearchPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	q := clip(r.PostForm.Get("q"), 60)
	d := pageData{MQuery: q, MSearched: q != ""}
	if q != "" {
		all, err := w.svc.municipioList(r.Context())
		if err != nil {
			d.Error = "No se ha podido traer la lista de municipios del Ministerio. Prueba más tarde."
		} else {
			d.MResults = searchMunicipios(all, q, 20)
		}
	}
	w.renderGas(rw, r, who, d)
}

func (w *web) gasMunicipioPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	id, err := strconv.Atoi(r.PostForm.Get("municipio"))
	if err != nil || id <= 0 {
		w.renderGas(rw, r, who, pageData{Error: "Elige un municipio de la lista."})
		return
	}
	_, err = w.svc.setMunicipio(r.Context(), who.User, id)
	switch {
	case isInput(err):
		w.renderGas(rw, r, who, pageData{Error: err.Error()})
		return
	case err != nil:
		w.fail(rw, "municipio", err)
		return
	}
	back(rw, r, "/gasolineras?ok=municipio")
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
