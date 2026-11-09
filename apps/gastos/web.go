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
	"homeserver/platform/seal"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor con formularios POST y sin JavaScript:
// movimientos del mes, presupuesto, suscripciones y categorías (con sus
// reglas). En las URL solo van el mes y el id de una categoría: nada de lo
// que escribe la persona.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	// ruleChips prepara las reglas para la plantilla «rulechips»; cat es la
	// categoría a la que volver al borrar una (0: a Categorías).
	"ruleChips": func(base string, cat int64, rules []ruleRow) any {
		return struct {
			Base  string
			CatID int64
			Rules []ruleRow
		}{base, cat, rules}
	},
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 32 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /gastos detrás de la pasarela
	Name    string
	Tab     string // movimientos, presupuesto, suscripciones, categorias
	Title   string
	Flash   string
	Error   string
	Confirm bool // pide confirmar un borrado

	// Mes que se ve
	Month *monthNav

	// Movimientos
	Stats     *statsView
	Days      []dayGroup
	CatOpts   []option // filtro de la lista, o la categoría de una suscripción o regla
	FormCats  []option // la categoría al apuntar o editar un movimiento
	CatFilter string
	Filtered  string // «Comida: 312,40 € en 14 movimientos»
	MovForm   *movForm
	Accounts  []string

	// Presupuesto
	Budget *budgetView
	Chart  *barChart

	// Suscripciones
	Subs       []subRow
	SubsMonth  string
	SubsYear   string
	SubForm    *subForm
	PeriodOpts []option

	// Categorías
	Cats     []catRow
	RuleCats []ruleGroup
	CatForm  *catForm
	RuleForm ruleForm
	NRules   int
}

type option struct {
	Key, Label string
	Selected   bool
}

type monthNav struct {
	Label, Key       string
	Prev, Next       string // vacío: no hay
	Current          bool
	DaysLeft, DaysIn int
}

type statsView struct {
	Expenses, Income, Balance string
	BalanceClass              string
	PrevNote                  string // «−8 % frente a octubre»
	PrevClass                 string
	Count                     int
}

type dayGroup struct {
	Label string
	Total string // lo gastado ese día
	Rows  []movRow
}

type movRow struct {
	ID              int64
	Concept, Cat    string
	Amount, Class   string
	Account, Note   string
	Talia, FromSub  bool
	CatID           int64
	DayISO, DayText string
}

type movForm struct {
	ID                             int64
	Kind, Day, Amount, Concept     string
	CatID                          int64
	Account, Note, Origin, CatName string
	Open                           bool // el formulario de la lista, desplegado
}

type budgetView struct {
	Spent, Income   string
	BSpent, Pct     string // lo gastado en las categorías con presupuesto
	Unbudgeted      string // lo gastado en las demás
	Budget, Left    string // de lo presupuestado
	LeftClass       string
	HasBudget, Over bool
	Meter           meter
	Rows            []budgetRow
	PrevLabel       string
	Compare         []compareRow
}

type budgetRow struct {
	ID              int64
	Name            string
	Spent, Budget   string
	Pct, Left       string
	Over, HasBudget bool
	Meter           meter
	Count           int
}

type compareRow struct {
	Name, Now, Prev, Diff, DiffClass string
}

type subRow struct {
	ID                   int64
	Name, Amount, Period string
	Next, When, Monthly  string
	Cat, Account, Note   string
	Due, Late, Soon      bool
	Notice               string
}

type subForm struct {
	ID                            int64
	Name, Amount, Period, Next    string
	Notice                        string // vacío: sin aviso
	CatID                         int64
	Account, Note, Monthly, After string
}

type catRow struct {
	ID          int64
	Name        string
	Budget      string
	Default     bool
	Rules       int
	SpentNow    string
	BudgetInput string
}

type ruleGroup struct {
	CatID int64
	Name  string
	Rules []ruleRow
}

type ruleRow struct {
	ID   int64
	Text string
}

type catForm struct {
	ID           int64
	Name, Budget string
	Default      bool
	Rules        []ruleRow
	Movs         int
}

type ruleForm struct {
	Text  string
	CatID int64
	Apply bool
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
	mux.HandleFunc("POST /movimiento/nuevo", w.user(w.movNew))
	mux.HandleFunc("GET /movimiento/{id}", w.user(w.movPage))
	mux.HandleFunc("POST /movimiento/{id}", w.user(w.movPost))
	mux.HandleFunc("POST /movimiento/{id}/borrar", w.user(w.movDelete))
	mux.HandleFunc("GET /presupuesto", w.user(w.budgetPage))
	mux.HandleFunc("GET /suscripciones", w.user(w.subsPage))
	mux.HandleFunc("POST /suscripcion/nueva", w.user(w.subNew))
	mux.HandleFunc("GET /suscripcion/{id}", w.user(w.subPage))
	mux.HandleFunc("POST /suscripcion/{id}", w.user(w.subPost))
	mux.HandleFunc("POST /suscripcion/{id}/cobro", w.user(w.subCharge))
	mux.HandleFunc("POST /suscripcion/{id}/borrar", w.user(w.subDelete))
	mux.HandleFunc("GET /categorias", w.user(w.catsPage))
	mux.HandleFunc("POST /categorias/nueva", w.user(w.catNew))
	mux.HandleFunc("POST /categorias/presupuestos", w.user(w.budgetsPost))
	mux.HandleFunc("GET /categoria/{id}", w.user(w.catPage))
	mux.HandleFunc("POST /categoria/{id}", w.user(w.catPost))
	mux.HandleFunc("POST /categoria/{id}/borrar", w.user(w.catDelete))
	mux.HandleFunc("POST /reglas/nueva", w.user(w.ruleNew))
	mux.HandleFunc("POST /regla/{id}/borrar", w.user(w.ruleDelete))
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
		if n, err := strconv.Atoi(r.URL.Query().Get("cambiados")); err == nil && n > 0 && d.Flash != "" {
			d.Flash = strings.TrimSuffix(d.Flash, ".") + " y aplicada a " + strconv.Itoa(n) + " movimiento"
			if n != 1 {
				d.Flash += "s"
			}
			d.Flash += " ya apuntado"
			if n != 1 {
				d.Flash += "s"
			}
			d.Flash += "."
		}
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
	w.svc.log.Error("gastos web", "op", op, "err", err)
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
	"apuntado": "Apuntado.", "guardado": "Guardado.", "borrado": "Borrado.", "cobro": "Cobro registrado: apuntado como gasto.",
	"suscripcion": "Suscripción guardada.", "categoria": "Categoría creada.", "presupuestos": "Presupuestos guardados.",
	"regla": "Regla guardada.", "regla-borrada": "Regla borrada.",
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// postedID lee un id de un campo del formulario (0 si no hay).
func postedID(r *http.Request, field string) int64 {
	id, err := strconv.ParseInt(r.PostForm.Get(field), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// ---- Mes ----

// pickMonth lee ?mes=AAAA-MM (por defecto, el de hoy; nunca futuro).
func pickMonth(r *http.Request, today time.Time) time.Time {
	cur := monthOf(today)
	if m, err := parseMonth(r.URL.Query().Get("mes")); err == nil && !m.After(cur) {
		return m
	}
	return cur
}

func navOf(m, today time.Time) *monthNav {
	cur := monthOf(today)
	n := &monthNav{Label: strings.ToUpper(monthLabel(m)[:1]) + monthLabel(m)[1:], Key: isoMonth(m), Prev: isoMonth(m.AddDate(0, -1, 0)), Current: m.Equal(cur), DaysIn: daysIn(m)}
	if m.Before(cur) {
		n.Next = isoMonth(m.AddDate(0, 1, 0))
	}
	if n.Current {
		n.DaysLeft = daysIn(m) - today.Day()
	}
	return n
}

func monthQuery(m time.Time) string { return "?mes=" + isoMonth(m) }

// ---- Opciones de los formularios ----

func catOptions(c cats, sel int64, auto bool) []option {
	var out []option
	if auto {
		out = append(out, option{Key: "0", Label: "Automática (por las reglas)", Selected: sel == 0})
	}
	for _, x := range c.list {
		out = append(out, option{Key: idStr(x.ID), Label: x.Name, Selected: x.ID == sel})
	}
	return out
}

func periodOptions(sel string) []option {
	var out []option
	for _, p := range periods {
		out = append(out, option{Key: p.Key, Label: p.Label, Selected: p.Key == sel})
	}
	return out
}

// accounts son las cuentas o medios de pago ya usados, para el datalist.
func accounts(movs []movement, subs []subscription) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if k := seal.Fold(a); a != "" && !seen[k] {
			seen[k] = true
			out = append(out, a)
		}
	}
	for _, d := range []string{"Tarjeta", "Efectivo", "Bizum", "Transferencia", "Domiciliación"} {
		add(d)
	}
	for _, m := range movs {
		add(m.Account)
	}
	for _, x := range subs {
		add(x.Account)
	}
	return out
}

// ---- Movimientos ----

// movRows agrupa los movimientos por día (ya vienen del más reciente al
// más antiguo), con lo gastado cada día.
func movRows(c cats, list []movement, today time.Time) []dayGroup {
	var out []dayGroup
	var spent []float64
	for _, m := range list {
		if len(out) == 0 || out[len(out)-1].Rows[0].DayISO != isoDay(m.Day) {
			label := weekdays[m.Day.Weekday()] + " " + shortDate(m.Day, today)
			switch {
			case m.Day.Equal(today):
				label = "hoy"
			case m.Day.Equal(today.Add(-dayUnit)):
				label = "ayer"
			}
			out = append(out, dayGroup{Label: strings.ToUpper(label[:1]) + label[1:]})
			spent = append(spent, 0)
		}
		g := &out[len(out)-1]
		row := movRow{ID: m.ID, Concept: m.Concept, Cat: c.name(m.CatID), CatID: m.CatID, Account: m.Account, Note: m.Note,
			Talia: m.Origin == originAssistant, FromSub: m.Origin == originSub, DayISO: isoDay(m.Day), DayText: shortDate(m.Day, today)}
		row.Amount, row.Class = euros(-m.Amount), "out"
		if m.Kind == kindIncome {
			row.Amount, row.Class = signedEuros(m.Amount), "in"
		} else {
			spent[len(spent)-1] += m.Amount
		}
		g.Rows = append(g.Rows, row)
	}
	for i := range out {
		if spent[i] > 0 {
			out[i].Total = euros(-spent[i])
		}
	}
	return out
}

func statsOf(sum *summary) *statsView {
	st := &statsView{Expenses: euros(sum.Expenses), Income: euros(sum.Income), Balance: signedEuros(sum.Balance), Count: len(sum.Movs)}
	st.BalanceClass = "flat"
	switch {
	case sum.Balance >= 0.005:
		st.BalanceClass = "in"
	case sum.Balance <= -0.005:
		st.BalanceClass = "out"
	}
	if sum.PrevExpenses > 0 && sum.Expenses > 0 {
		d := (sum.Expenses/sum.PrevExpenses - 1) * 100
		prev := months[sum.Month.AddDate(0, -1, 0).Month()-1]
		st.PrevNote = signedPct(d) + " en gastos frente a " + prev
		st.PrevClass = "flat"
		if d >= 0.5 {
			st.PrevClass = "out"
		} else if d <= -0.5 {
			st.PrevClass = "in"
		}
	}
	return st
}

// signedPct es «+12 %», «−8 %».
func signedPct(v float64) string {
	if math.Round(v) > 0 {
		return "+" + pct(v)
	}
	return pct(v)
}

func (w *web) listPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderList(rw, r, who, pickMonth(r, w.svc.today()), nil, "")
}

// renderList pinta los movimientos de un mes, con el formulario para
// apuntar (con lo escrito y el error, si vuelve de uno).
func (w *web) renderList(rw http.ResponseWriter, r *http.Request, who app.Identity, month time.Time, f *movForm, msg string) {
	ctx := r.Context()
	today := w.svc.today()
	sum, c, err := w.svc.monthSummary(ctx, who.User, month)
	if err != nil {
		w.fail(rw, "movimientos", err)
		return
	}
	subs, err := w.svc.store.subscriptions(ctx, who.User)
	if err != nil {
		w.fail(rw, "movimientos", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "movimientos", Month: navOf(month, today), Stats: statsOf(sum), Error: msg}
	if !d.Month.Current {
		d.Title = monthLabel(month)
	}
	list := sum.Movs
	if cat, err := strconv.ParseInt(r.URL.Query().Get("cat"), 10, 64); err == nil && cat >= 0 && r.URL.Query().Get("cat") != "" {
		var mine []movement
		total, n := 0.0, 0
		for _, m := range list {
			if m.CatID == cat || (cat == 0 && c.byID[m.CatID].ID == 0) {
				mine = append(mine, m)
				total += m.signed()
				n++
			}
		}
		list = mine
		d.CatFilter = idStr(cat)
		d.Filtered = c.name(cat) + ": " + signedEuros(total) + " en " + strconv.Itoa(n) + " movimiento"
		if n != 1 {
			d.Filtered += "s"
		}
	}
	d.Days = movRows(c, list, today)
	sel, _ := strconv.ParseInt(d.CatFilter, 10, 64)
	d.CatOpts = catOptions(c, sel, false)
	if f == nil {
		f = &movForm{Kind: kindExpense, Day: isoDay(today), Open: len(sum.Movs) == 0 && d.Month.Current}
	}
	d.MovForm = f
	d.FormCats = catOptions(c, f.CatID, true)
	d.Accounts = accounts(sum.Movs, subs)
	w.render(rw, r, "movimientos.html", d)
}

// readMov lee el formulario de un movimiento.
func readMov(r *http.Request) (*movForm, movInput, error) {
	f := r.PostForm
	fm := &movForm{Kind: f.Get("tipo"), Day: f.Get("fecha"), Amount: f.Get("importe"), Concept: f.Get("concepto"), Account: f.Get("cuenta"),
		Note: f.Get("nota"), CatID: postedID(r, "categoria"), Open: true}
	in := movInput{Kind: fm.Kind, Concept: fm.Concept, CatID: fm.CatID, Account: fm.Account, Note: fm.Note}
	day, err := parseDay(fm.Day)
	if err != nil {
		return fm, in, bad("La fecha no es válida.")
	}
	in.Day = day
	if in.Amount, err = parseMoney(fm.Amount); err != nil {
		if strings.TrimSpace(fm.Amount) == "" {
			return fm, in, bad("Falta el importe.")
		}
		return fm, in, bad("El importe no es un número.")
	}
	in.Amount = math.Abs(in.Amount)
	return fm, in, nil
}

func (w *web) movNew(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	fm, in, err := readMov(r)
	in.Origin = originWeb
	var m movement
	if err == nil {
		m, err = w.svc.addMovement(r.Context(), who.User, in)
	}
	switch {
	case isInput(err):
		month := monthOf(w.svc.today())
		if mm, perr := parseMonth(r.PostForm.Get("mes")); perr == nil && !mm.After(month) {
			month = mm
		}
		w.renderList(rw, r, who, month, fm, err.Error())
		return
	case err != nil:
		w.fail(rw, "apuntar", err)
		return
	}
	back(rw, r, "/"+monthQuery(monthOf(m.Day))+"&ok=apuntado")
}

func (w *web) movPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	m, err := w.svc.store.movement(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "movimiento", err)
		return
	}
	f := &movForm{ID: m.ID, Kind: m.Kind, Day: isoDay(m.Day), Amount: moneyInput(m.Amount), Concept: m.Concept, CatID: m.CatID,
		Account: m.Account, Note: m.Note, Origin: m.Origin}
	w.renderMov(rw, r, who, f, "")
}

func (w *web) renderMov(rw http.ResponseWriter, r *http.Request, who app.Identity, f *movForm, msg string) {
	ctx := r.Context()
	c, err := w.svc.loadCats(ctx, who.User)
	if err != nil {
		w.fail(rw, "movimiento", err)
		return
	}
	today := w.svc.today()
	movs, err := w.svc.store.movements(ctx, who.User, monthOf(today).AddDate(0, -3, 0), time.Time{})
	if err != nil {
		w.fail(rw, "movimiento", err)
		return
	}
	f.CatName = c.name(f.CatID)
	d := pageData{Name: who.Name, Tab: "movimientos", Title: "Editar movimiento", MovForm: f, FormCats: catOptions(c, f.CatID, true),
		Accounts: accounts(movs, nil), Error: msg, Confirm: r.URL.Query().Get("borrar") == "1"}
	if day, err := parseDay(f.Day); err == nil {
		d.Month = navOf(monthOf(day), today)
	}
	w.render(rw, r, "movimiento.html", d)
}

func (w *web) movPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	fm, in, err := readMov(r)
	fm.ID = id
	var m movement
	if err == nil {
		m, err = w.svc.updateMovement(r.Context(), who.User, id, in)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case isInput(err):
		w.renderMov(rw, r, who, fm, err.Error())
		return
	case err != nil:
		w.fail(rw, "editar movimiento", err)
		return
	}
	back(rw, r, "/"+monthQuery(monthOf(m.Day))+"&ok=guardado")
}

func (w *web) movDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/movimiento/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	m, err := w.svc.store.movement(r.Context(), who.User, id)
	if err == nil {
		err = w.svc.store.deleteMovement(r.Context(), who.User, id)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar movimiento", err)
	default:
		back(rw, r, "/"+monthQuery(monthOf(m.Day))+"&ok=borrado")
	}
}

// ---- Presupuesto ----

func budgetOf(sum *summary) *budgetView {
	b := &budgetView{Spent: euros(sum.Expenses), Income: euros(sum.Income), HasBudget: sum.Budget > 0,
		PrevLabel: months[sum.Month.AddDate(0, -1, 0).Month()-1]}
	if b.HasBudget {
		b.Budget, b.BSpent, b.Pct = euros(sum.Budget), euros(sum.BudgetSpent), pct(sum.BudgetSpent/sum.Budget*100)
		if rest := sum.Expenses - sum.BudgetSpent; rest >= 0.005 {
			b.Unbudgeted = euros(rest)
		}
		left := sum.Budget - sum.BudgetSpent
		b.Over = left < -0.005
		b.Meter = newMeter(sum.BudgetSpent, sum.Budget, 0)
		if b.Over {
			b.Left, b.LeftClass = "te has pasado "+euros(-left), "out"
		} else {
			b.Left, b.LeftClass = "quedan "+euros(left), "in"
		}
	}
	top := 0.0
	for _, cs := range sum.ByCat {
		top = math.Max(top, cs.Spent)
	}
	for _, cs := range sum.ByCat {
		if cs.Spent <= 0 && cs.Budget <= 0 {
			continue
		}
		row := budgetRow{ID: cs.ID, Name: cs.Name, Spent: euros(cs.Spent), HasBudget: cs.Budget > 0, Over: cs.over(),
			Meter: newMeter(cs.Spent, cs.Budget, top), Count: cs.Count}
		if row.HasBudget {
			row.Budget, row.Pct = euros(cs.Budget), pct(cs.Spent/cs.Budget*100)
			if row.Over {
				row.Left = "te has pasado " + euros(cs.Spent-cs.Budget)
			} else {
				row.Left = "quedan " + euros(cs.Budget-cs.Spent)
			}
		}
		b.Rows = append(b.Rows, row)
	}
	// Mes contra mes, por categoría (lo que tenga gasto en alguno de los dos).
	for _, cs := range sum.ByCat {
		if cs.Spent <= 0 && cs.Prev <= 0 {
			continue
		}
		row := compareRow{Name: cs.Name, Now: euros(cs.Spent), Prev: euros(cs.Prev), DiffClass: "flat"}
		diff := cs.Spent - cs.Prev
		switch {
		case cs.Prev > 0:
			row.Diff = signedEuros(diff) + " (" + signedPct(diff/cs.Prev*100) + ")"
		default:
			row.Diff = signedEuros(diff)
		}
		if diff >= 0.005 {
			row.DiffClass = "out"
		} else if diff <= -0.005 {
			row.DiffClass = "in"
		}
		b.Compare = append(b.Compare, row)
	}
	return b
}

func (w *web) budgetPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	today := w.svc.today()
	month := pickMonth(r, today)
	sum, _, err := w.svc.monthSummary(r.Context(), who.User, month)
	if err != nil {
		w.fail(rw, "presupuesto", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "presupuesto", Title: "Presupuesto", Month: navOf(month, today), Stats: statsOf(sum), Budget: budgetOf(sum),
		Chart: monthsChart(sum.History, month, today)}
	w.render(rw, r, "presupuesto.html", d)
}

// ---- Suscripciones ----

func subRows(c cats, list []subscription, today time.Time) []subRow {
	var out []subRow
	for _, x := range list {
		row := subRow{ID: x.ID, Name: x.Name, Amount: euros(x.Amount), Period: periodLabel(x.Period), Next: shortDate(x.Next, today),
			When: inDays(x.Next, today), Monthly: euros(x.monthly()), Cat: c.name(x.CatID), Account: x.Account, Note: x.Note}
		row.Late = x.Next.Before(today)
		row.Due = !row.Late && !x.Next.After(today)
		row.Soon = !row.Late && !x.Next.After(today.AddDate(0, 0, 7))
		switch {
		case x.Notice < 0:
			row.Notice = "sin aviso"
		case x.Notice == 0:
			row.Notice = "aviso el mismo día"
		case x.Notice == 1:
			row.Notice = "aviso 1 día antes"
		default:
			row.Notice = "aviso " + strconv.Itoa(x.Notice) + " días antes"
		}
		out = append(out, row)
	}
	return out
}

func (w *web) subsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderSubs(rw, r, who, nil, "")
}

func (w *web) renderSubs(rw http.ResponseWriter, r *http.Request, who app.Identity, f *subForm, msg string) {
	ctx := r.Context()
	c, err := w.svc.loadCats(ctx, who.User)
	if err != nil {
		w.fail(rw, "suscripciones", err)
		return
	}
	list, err := w.svc.store.subscriptions(ctx, who.User)
	if err != nil {
		w.fail(rw, "suscripciones", err)
		return
	}
	today := w.svc.today()
	if f == nil {
		f = &subForm{Period: "mensual", Next: isoDay(today), Notice: "3", CatID: c.byKey[keySubs].ID}
	}
	mo, yr := subsCost(list)
	d := pageData{Name: who.Name, Tab: "suscripciones", Title: "Suscripciones", Subs: subRows(c, list, today), SubsMonth: euros(mo), SubsYear: euros(yr),
		SubForm: f, PeriodOpts: periodOptions(f.Period), CatOpts: catOptions(c, f.CatID, false), Accounts: accounts(nil, list), Error: msg}
	w.render(rw, r, "suscripciones.html", d)
}

// readSub lee el formulario de una suscripción.
func readSub(r *http.Request) (*subForm, subInput, error) {
	f := r.PostForm
	fm := &subForm{Name: f.Get("nombre"), Amount: f.Get("importe"), Period: f.Get("periodicidad"), Next: f.Get("proximo"), Notice: strings.TrimSpace(f.Get("aviso")),
		CatID: postedID(r, "categoria"), Account: f.Get("cuenta"), Note: f.Get("nota")}
	in := subInput{Name: fm.Name, Period: fm.Period, CatID: fm.CatID, Account: fm.Account, Note: fm.Note, Notice: -1}
	day, err := parseDay(fm.Next)
	if err != nil {
		return fm, in, bad("La fecha del próximo cobro no es válida.")
	}
	in.Next = day
	if in.Amount, err = parseMoney(fm.Amount); err != nil {
		return fm, in, bad("El importe no es un número.")
	}
	in.Amount = math.Abs(in.Amount)
	if fm.Notice != "" {
		n, err := strconv.Atoi(fm.Notice)
		if err != nil {
			return fm, in, bad("Los días de aviso son un número (vacío: sin aviso).")
		}
		in.Notice = n
	}
	return fm, in, nil
}

func (w *web) subNew(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	fm, in, err := readSub(r)
	if err == nil {
		_, err = w.svc.addSub(r.Context(), who.User, in)
	}
	switch {
	case isInput(err):
		w.renderSubs(rw, r, who, fm, err.Error())
	case err != nil:
		w.fail(rw, "suscripción", err)
	default:
		back(rw, r, "/suscripciones?ok=suscripcion")
	}
}

func (w *web) subPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	x, err := w.svc.store.subscription(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "suscripción", err)
		return
	}
	f := &subForm{ID: x.ID, Name: x.Name, Amount: moneyInput(x.Amount), Period: x.Period, Next: isoDay(x.Next), CatID: x.CatID,
		Account: x.Account, Note: x.Note, Monthly: euros(x.monthly()), After: shortDate(x.after(x.Next), w.svc.today())}
	if x.Notice >= 0 {
		f.Notice = strconv.Itoa(x.Notice)
	}
	w.renderSub(rw, r, who, f, "")
}

func (w *web) renderSub(rw http.ResponseWriter, r *http.Request, who app.Identity, f *subForm, msg string) {
	ctx := r.Context()
	c, err := w.svc.loadCats(ctx, who.User)
	if err != nil {
		w.fail(rw, "suscripción", err)
		return
	}
	list, err := w.svc.store.subscriptions(ctx, who.User)
	if err != nil {
		w.fail(rw, "suscripción", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "suscripciones", Title: "Editar suscripción", SubForm: f, PeriodOpts: periodOptions(f.Period),
		CatOpts: catOptions(c, f.CatID, false), Accounts: accounts(nil, list), Error: msg, Confirm: r.URL.Query().Get("borrar") == "1"}
	w.render(rw, r, "suscripcion.html", d)
}

func (w *web) subPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	fm, in, err := readSub(r)
	fm.ID = id
	if err == nil {
		_, err = w.svc.updateSub(r.Context(), who.User, id, in)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderSub(rw, r, who, fm, err.Error())
	case err != nil:
		w.fail(rw, "editar suscripción", err)
	default:
		back(rw, r, "/suscripciones?ok=guardado")
	}
}

func (w *web) subCharge(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	_, _, err := w.svc.chargeSub(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderSubs(rw, r, who, nil, err.Error())
	case err != nil:
		w.fail(rw, "registrar cobro", err)
	default:
		back(rw, r, "/suscripciones?ok=cobro")
	}
}

func (w *web) subDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/suscripcion/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	err := w.svc.store.deleteSub(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar suscripción", err)
	default:
		back(rw, r, "/suscripciones?ok=borrado")
	}
}

// ---- Categorías y reglas ----

func (w *web) catsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderCats(rw, r, who, nil, nil, "")
}

func (w *web) renderCats(rw http.ResponseWriter, r *http.Request, who app.Identity, cf *catForm, rf *ruleForm, msg string) {
	ctx := r.Context()
	today := w.svc.today()
	sum, c, err := w.svc.monthSummary(ctx, who.User, today)
	if err != nil {
		w.fail(rw, "categorías", err)
		return
	}
	rules, err := w.svc.store.rules(ctx, who.User)
	if err != nil {
		w.fail(rw, "categorías", err)
		return
	}
	spent := map[int64]float64{}
	for _, cs := range sum.ByCat {
		spent[cs.ID] = cs.Spent
	}
	count := map[int64]int{}
	groups := map[int64]*ruleGroup{}
	for _, x := range rules {
		count[x.CatID]++
		if groups[x.CatID] == nil {
			groups[x.CatID] = &ruleGroup{CatID: x.CatID, Name: c.name(x.CatID)}
		}
		groups[x.CatID].Rules = append(groups[x.CatID].Rules, ruleRow{ID: x.ID, Text: x.Text})
	}
	d := pageData{Name: who.Name, Tab: "categorias", Title: "Categorías", Error: msg, NRules: len(rules), Month: navOf(monthOf(today), today)}
	for _, x := range c.list {
		row := catRow{ID: x.ID, Name: x.Name, Default: x.Key != "", Rules: count[x.ID], BudgetInput: moneyInput(x.Budget)}
		if x.Budget > 0 {
			row.Budget = euros(x.Budget)
		}
		if spent[x.ID] > 0 {
			row.SpentNow = euros(spent[x.ID])
		}
		d.Cats = append(d.Cats, row)
		if g := groups[x.ID]; g != nil {
			sort.SliceStable(g.Rules, func(i, j int) bool { return seal.Fold(g.Rules[i].Text) < seal.Fold(g.Rules[j].Text) })
			d.RuleCats = append(d.RuleCats, *g)
		}
	}
	if cf == nil {
		cf = &catForm{}
	}
	if rf == nil {
		rf = &ruleForm{CatID: c.byKey[keyOther].ID}
	}
	d.CatForm, d.RuleForm = cf, *rf
	d.CatOpts = catOptions(c, rf.CatID, false)
	w.render(rw, r, "categorias.html", d)
}

func (w *web) catNew(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	cf := &catForm{Name: r.PostForm.Get("nombre"), Budget: r.PostForm.Get("presupuesto")}
	budget, err := parseOptMoney(cf.Budget)
	if err != nil {
		err = bad("El presupuesto no es un número.")
	} else {
		_, err = w.svc.addCategory(r.Context(), who.User, cf.Name, math.Abs(budget))
	}
	switch {
	case isInput(err):
		w.renderCats(rw, r, who, cf, nil, err.Error())
	case err != nil:
		w.fail(rw, "nueva categoría", err)
	default:
		back(rw, r, "/categorias?ok=categoria")
	}
}

// budgetsPost guarda los presupuestos de todas las categorías (campos p<id>).
func (w *web) budgetsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	budgets := map[int64]float64{}
	for k, v := range r.PostForm {
		id, err := strconv.ParseInt(strings.TrimPrefix(k, "p"), 10, 64)
		if !strings.HasPrefix(k, "p") || err != nil || id <= 0 || len(v) == 0 {
			continue
		}
		x, err := parseOptMoney(v[0])
		if err != nil {
			w.renderCats(rw, r, who, nil, nil, "Algún presupuesto no es un número.")
			return
		}
		budgets[id] = math.Abs(x)
	}
	err := w.svc.setBudgets(r.Context(), who.User, budgets)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderCats(rw, r, who, nil, nil, err.Error())
	case err != nil:
		w.fail(rw, "presupuestos", err)
	default:
		back(rw, r, "/categorias?ok=presupuestos")
	}
}

func (w *web) catPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	w.renderCat(rw, r, who, id, nil, "")
}

func (w *web) renderCat(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, cf *catForm, msg string) {
	ctx := r.Context()
	c, err := w.svc.loadCats(ctx, who.User)
	if err != nil {
		w.fail(rw, "categoría", err)
		return
	}
	x, ok := c.byID[id]
	if !ok {
		notFound(rw)
		return
	}
	if cf == nil {
		cf = &catForm{Name: x.Name, Budget: moneyInput(x.Budget)}
	}
	cf.ID, cf.Default = id, x.Key != ""
	rules, err := w.svc.store.rules(ctx, who.User)
	if err != nil {
		w.fail(rw, "categoría", err)
		return
	}
	for _, rl := range rules {
		if rl.CatID == id {
			cf.Rules = append(cf.Rules, ruleRow{ID: rl.ID, Text: rl.Text})
		}
	}
	movs, err := w.svc.store.movements(ctx, who.User, time.Time{}, time.Time{})
	if err != nil {
		w.fail(rw, "categoría", err)
		return
	}
	for _, m := range movs {
		if m.CatID == id {
			cf.Movs++
		}
	}
	d := pageData{Name: who.Name, Tab: "categorias", Title: x.Name, CatForm: cf, Error: msg, Confirm: r.URL.Query().Get("borrar") == "1",
		RuleForm: ruleForm{CatID: id}}
	w.render(rw, r, "categoria.html", d)
}

func (w *web) catPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	cf := &catForm{Name: r.PostForm.Get("nombre"), Budget: r.PostForm.Get("presupuesto")}
	budget, err := parseOptMoney(cf.Budget)
	if err != nil {
		err = bad("El presupuesto no es un número.")
	} else {
		err = w.svc.updateCategory(r.Context(), who.User, id, cf.Name, math.Abs(budget))
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case isInput(err):
		w.renderCat(rw, r, who, id, cf, err.Error())
	case err != nil:
		w.fail(rw, "editar categoría", err)
	default:
		back(rw, r, "/categorias?ok=guardado")
	}
}

func (w *web) catDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/categoria/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	err := w.svc.store.deleteCategory(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar categoría", err)
	default:
		back(rw, r, "/categorias?ok=borrado")
	}
}

func (w *web) ruleNew(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	rf := &ruleForm{Text: r.PostForm.Get("texto"), CatID: postedID(r, "categoria"), Apply: r.PostForm.Get("aplicar") == "1"}
	fromCat := r.PostForm.Get("desde") == "categoria"
	n, err := w.svc.addRule(r.Context(), who.User, rf.Text, rf.CatID, rf.Apply)
	switch {
	case isInput(err) && fromCat:
		w.renderCat(rw, r, who, rf.CatID, nil, err.Error())
	case isInput(err):
		w.renderCats(rw, r, who, nil, rf, err.Error())
	case err != nil:
		w.fail(rw, "nueva regla", err)
	default:
		to := "/categorias?ok=regla#reglas"
		if fromCat {
			to = "/categoria/" + idStr(rf.CatID) + "?ok=regla#reglas"
		}
		if n > 0 {
			to = strings.Replace(to, "ok=regla", "ok=regla&cambiados="+strconv.Itoa(n), 1)
		}
		back(rw, r, to)
	}
}

func (w *web) ruleDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	err := w.svc.store.deleteRule(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar regla", err)
	default:
		if cat := postedID(r, "categoria"); cat > 0 {
			back(rw, r, "/categoria/"+idStr(cat)+"?ok=regla-borrada#reglas")
			return
		}
		back(rw, r, "/categorias?ok=regla-borrada#reglas")
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
