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
// resumen, activos (con su ficha), operaciones, alertas y seguimiento. Las
// búsquedas también van por POST: lo que busca la persona no queda en
// ninguna URL.

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
	Base    string // /cartera detrás de la pasarela
	Name    string
	Tab     string // resumen, activos, operaciones, alertas, seguimiento
	Title   string
	Flash   string
	Error   string

	// Resumen
	Sum      *summaryView
	Chart    *lineChart
	Ranges   []rangeOpt
	Sources  []sourceView
	Warnings []string

	// Activos
	Positions []posRow
	Closed    []posRow
	Query     string
	Searched  bool
	Results   []resultRow
	Failed    string
	Manual    manualForm

	// Ficha de un activo
	Asset   *assetView
	Ops     []opRow
	Alerts  []alertRow
	AForm   alertForm
	Confirm bool

	// Operaciones
	OpForm    *opForm
	OpKinds   []option
	AssetOpts []option
	Platforms []string
	Filter    string

	// Alertas
	AlertKinds []option

	// Seguimiento
	Watch []watchRow

	// Tipos de activo
	AssetKinds []option
}

type option struct {
	Key, Label string
	Selected   bool
}

type summaryView struct {
	Value, Cost                    string
	DayChange, DayPct, DayClass    string
	HasDay                         bool
	Unrealized, UnrealPct, UClass  string
	Realized, RClass               string
	Income, Fees                   string
	Total, TotalClass              string
	Held                           int
	ByKind, ByPlatform, ByCurrency []shareRow
}

type posRow struct {
	ID                   int64
	Symbol, Name, Kind   string
	Qty, Price, Value    string
	DayPct, DayClass     string
	GP, GPPct, GPClass   string
	Avg, Cost, Platforms string
	NoPrice              bool
	Realized, Income     string
}

type resultRow struct {
	Ref, Symbol, Name, Kind, KindLabel, Market, Source, ISIN string
	Have                                                     bool
	HaveID                                                   int64
}

type manualForm struct {
	Symbol, Name, Kind, Currency, Price string
}

type sourceView struct {
	Name    string
	OK      bool
	Pending bool // aún no se le ha pedido nada
	Last    string
	Error   string
	Retry   string
}

type assetView struct {
	ID                                    int64
	Symbol, Name, Kind, KindKey           string
	Source, SourceKey, SourceID, Currency string
	ISIN, Note, Manual                    string
	Manualable                            bool
	Watch                                 bool
	Price, PriceAt, PriceFrom             string
	DayPct, DayClass                      string
	Held                                  bool
	Qty, Value, Avg, AvgNative, Cost      string
	GP, GPPct, GPClass                    string
	Realized, Income, Fees                string
	Platforms                             []string
}

type opRow struct {
	ID                    int64
	Day, Kind, KindKey    string
	AssetID               int64
	Symbol                string
	Qty, Price, Amount    string
	Platform, Note, Extra string
	Talia                 bool
}

type opForm struct {
	ID                                        int64
	AssetID                                   int64
	Kind, Day, Time, Qty, Price, Total, Fee   string
	Currency, Platform, To, Note, AssetSymbol string
	Origin                                    string
}

type alertRow struct {
	ID      int64
	AssetID int64
	Symbol  string
	Desc    string
	Active  bool
	Crossed bool
	Fired   string
	Last    string
}

type alertForm struct {
	AssetID   int64
	Kind      string
	Threshold string
	Days      string
	Repeat    bool
	Currency  string
}

type watchRow struct {
	ID                  int64
	Symbol, Name, Price string
	DayPct, DayClass    string
	Month, MonthClass   string
	Held                bool
	Source              string
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
	mux.HandleFunc("GET /activos", w.user(w.assetsPage))
	mux.HandleFunc("POST /buscar", w.user(w.searchPost))
	mux.HandleFunc("POST /activos/nuevo", w.user(w.addAssetPost))
	mux.HandleFunc("POST /activos/manual", w.user(w.addManualPost))
	mux.HandleFunc("GET /activo/{id}", w.user(w.assetPage))
	mux.HandleFunc("POST /activo/{id}", w.user(w.assetPost))
	mux.HandleFunc("POST /activo/{id}/borrar", w.user(w.assetDelete))
	mux.HandleFunc("GET /operaciones", w.user(w.opsPage))
	mux.HandleFunc("GET /operacion/nueva", w.user(w.opNewPage))
	mux.HandleFunc("POST /operacion/nueva", w.user(w.opNewPost))
	mux.HandleFunc("GET /operacion/{id}", w.user(w.opPage))
	mux.HandleFunc("POST /operacion/{id}", w.user(w.opPost))
	mux.HandleFunc("POST /operacion/{id}/borrar", w.user(w.opDelete))
	mux.HandleFunc("GET /alertas", w.user(w.alertsPage))
	mux.HandleFunc("POST /alertas/nueva", w.user(w.alertNewPost))
	mux.HandleFunc("POST /alerta/{id}/borrar", w.user(w.alertDelete))
	mux.HandleFunc("POST /alerta/{id}/activar", w.user(w.alertToggle))
	mux.HandleFunc("GET /seguimiento", w.user(w.watchPage))
	mux.HandleFunc("POST /seguimiento", w.user(w.watchPost))
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
	w.svc.log.Error("cartera web", "op", op, "err", err)
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
	"anadido": "Añadido.", "ya-estaba": "Ya lo tenías: aquí está.", "guardado": "Guardado.", "borrado": "Borrado.",
	"apuntada": "Operación apuntada.", "alerta": "Alerta creada.", "alerta-borrada": "Alerta borrada.",
	"siguiendo": "En seguimiento.", "sin-seguir": "Ya no lo sigues.", "activada": "Alerta activada.", "desactivada": "Alerta apagada.",
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// cls es la clase de color de una variación.
func cls(v float64) string {
	switch {
	case v > 0.00001:
		return "up"
	case v < -0.00001:
		return "down"
	}
	return "flat"
}

func (w *web) today() time.Time { return dayOf(w.svc.clock()) }

// ---- Resumen ----

func summaryOf(pf *portfolio) *summaryView {
	s := &summaryView{
		Value: euros(pf.Value), Cost: euros(pf.Cost),
		DayChange: signed(euros(pf.DayChange), pf.DayChange), DayPct: pct(pf.DayPct), DayClass: cls(pf.DayChange), HasDay: pf.DayChange != 0,
		Unrealized: signed(euros(pf.Unrealized), pf.Unrealized), UnrealPct: pct(pf.UnrealPct), UClass: cls(pf.Unrealized),
		Realized: signed(euros(pf.Realized), pf.Realized), RClass: cls(pf.Realized),
		Income: euros(pf.Income), Fees: euros(pf.Fees),
		Total: signed(euros(pf.Total), pf.Total), TotalClass: cls(pf.Total), Held: pf.Held,
		ByKind: shareRows(pf.ByKind), ByPlatform: shareRows(pf.ByPlatform), ByCurrency: shareRows(pf.ByCurrency),
	}
	return s
}

// sourcesView es el estado de las fuentes que usa la persona (y de las que
// fallen). Sin detalles internos.
func (w *web) sourcesView(assets []asset) []sourceView {
	used := map[string]bool{sourceFX: true}
	for _, a := range assets {
		used[a.Source] = true
	}
	st := w.svc.market.statuses()
	now := w.svc.clock()
	var out []sourceView
	for _, src := range []string{sourceKraken, sourceGecko, sourceYahoo, sourceFX} {
		x, ok := st[src]
		if !used[src] && (!ok || x.Err == "") {
			continue
		}
		v := sourceView{Name: sourceName(src), OK: x.Err == "", Last: ago(x.OK, now)}
		if !ok {
			v.Last, v.Pending = "aún no se ha consultado", true
			v.OK = true
		}
		if x.Err != "" {
			v.Error = x.Err
			if x.Until.After(now) {
				v.Retry = "se vuelve a intentar " + strings.Replace(ago(now.Add(-x.Until.Sub(now)), now), "hace", "en", 1)
			}
		}
		out = append(out, v)
	}
	return out
}

func (w *web) summaryPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "resumen", err)
		return
	}
	_, from, opts := pickRange(r.URL.Query().Get("r"), dayOf(v.now))
	d := pageData{Name: who.Name, Tab: "resumen", Sum: summaryOf(v.pf), Ranges: opts, Sources: w.sourcesView(v.assets), Warnings: v.pf.Warnings}
	d.Chart = valueChart(v.series(from), dayOf(v.now))
	d.Positions, _ = posRows(v.pf)
	w.render(rw, r, "resumen.html", d)
}

func valueChart(vs []valuePoint, today time.Time) *lineChart {
	var val, cost []pt
	for _, p := range vs {
		val = append(val, pt{p.Day, p.Value})
		cost = append(cost, pt{p.Day, p.Cost})
	}
	c := buildChart(val, cost, nil, "Valor de la cartera en euros, y lo invertido", today)
	if c == nil {
		return nil
	}
	// Lo comprado o vendido en el periodo no es ganancia ni pérdida: la
	// variación descuenta el dinero metido o sacado.
	first, last := vs[0], vs[len(vs)-1]
	diff := last.Value - first.Value - (last.Flow - first.Flow)
	c.Down, c.Change = diff < 0, ""
	if first.Value > eps {
		c.Change = pct(diff/first.Value*100) + " en el periodo (sin contar compras ni ventas)"
	}
	return c
}

// ---- Activos ----

func posRows(pf *portfolio) (held, closed []posRow) {
	for _, p := range pf.Positions {
		if p.Ops == 0 {
			continue // solo en seguimiento
		}
		row := posRow{ID: p.Asset.ID, Symbol: p.Asset.Symbol, Name: p.Asset.Name, Kind: kindLabel(p.Asset.Kind), Qty: qty(p.Qty),
			Realized: signed(euros(p.Realized), p.Realized), Income: euros(p.Income)}
		if p.Qty <= eps {
			closed = append(closed, row)
			continue
		}
		row.Avg, row.Cost = euros(p.Avg()), euros(p.Cost)
		if p.Price > 0 {
			row.Price = price(p.Price, p.Quote.Currency)
		}
		if !p.Priced {
			row.NoPrice = true
		} else {
			row.Value = euros(p.Value)
			row.GP, row.GPPct, row.GPClass = signed(euros(p.Unrealized), p.Unrealized), pct(p.UnrealPct), cls(p.Unrealized)
			if p.DayPct != 0 {
				row.DayPct, row.DayClass = pct(p.DayPct), cls(p.DayPct)
			}
		}
		var names []string
		for n := range p.Platforms {
			if n == "" {
				n = "sin plataforma"
			}
			names = append(names, n)
		}
		sort.Strings(names)
		row.Platforms = strings.Join(names, ", ")
		held = append(held, row)
	}
	return held, closed
}

func kindOptions(sel string) []option {
	var out []option
	for _, k := range kinds {
		out = append(out, option{Key: k.Key, Label: k.Label, Selected: k.Key == sel})
	}
	return out
}

func (w *web) assetsData(r *http.Request, who app.Identity, v *view) pageData {
	d := pageData{Name: who.Name, Tab: "activos", Title: "Activos", AssetKinds: kindOptions("otro"), Manual: manualForm{Currency: "EUR", Kind: "otro"}}
	d.Positions, d.Closed = posRows(v.pf)
	return d
}

func (w *web) assetsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "activos", err)
		return
	}
	w.render(rw, r, "activos.html", w.assetsData(r, who, v))
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
	d := w.assetsData(r, who, v)
	d.Query, d.Searched = clip(r.PostForm.Get("q"), 60), true
	if r.PostForm.Get("desde") == "seguimiento" {
		d.Tab, d.Title = "seguimiento", "Seguimiento"
	}
	have := map[string]int64{}
	for _, a := range v.assets {
		have[a.key()] = a.ID
	}
	list, failed := w.svc.search(r.Context(), d.Query)
	for _, f := range list {
		row := resultRow{Ref: f.ref(), Symbol: f.Symbol, Name: f.Name, Kind: f.Kind, KindLabel: kindLabel(f.Kind), Market: f.Market,
			Source: sourceName(f.Source), ISIN: f.ISIN}
		row.HaveID = have[assetData{Source: f.Source, SourceID: f.SourceID}.key()]
		row.Have = row.HaveID != 0
		d.Results = append(d.Results, row)
	}
	if len(failed) > 0 {
		d.Failed = strings.Join(failed, ", ") + " no responde ahora; prueba más tarde."
	}
	w.render(rw, r, "activos.html", d)
}

func (w *web) addAssetPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	watch := f.Get("seguir") == "1"
	a, existed, err := w.svc.addFound(r.Context(), who.User, f.Get("ref"), f.Get("tipo"), f.Get("isin"), watch)
	switch {
	case isInput(err), errors.Is(err, errFull):
		v, lerr := w.svc.load(r.Context(), who.User)
		if lerr != nil {
			w.fail(rw, "añadir activo", lerr)
			return
		}
		d := w.assetsData(r, who, v)
		d.Error = err.Error()
		if errors.Is(err, errFull) {
			d.Error = "Has llegado al máximo de activos."
		}
		w.render(rw, r, "activos.html", d)
		return
	case errors.Is(err, errSourceWait), errors.Is(err, errSourceBad):
		v, lerr := w.svc.load(r.Context(), who.User)
		if lerr != nil {
			w.fail(rw, "añadir activo", lerr)
			return
		}
		d := w.assetsData(r, who, v)
		d.Error = "La fuente de precios no responde ahora; prueba más tarde."
		w.render(rw, r, "activos.html", d)
		return
	case err != nil:
		w.fail(rw, "añadir activo", err)
		return
	}
	ok := "anadido"
	if existed {
		ok = "ya-estaba"
	}
	if watch {
		back(rw, r, "/seguimiento?ok=siguiendo")
		return
	}
	back(rw, r, "/activo/"+idStr(a.ID)+"?ok="+ok)
}

func (w *web) addManualPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	in := manualForm{Symbol: f.Get("simbolo"), Name: f.Get("nombre"), Kind: f.Get("tipo"), Currency: f.Get("moneda"), Price: f.Get("precio")}
	px, err := parseOpt(in.Price, true)
	if err == nil {
		var a asset
		var existed bool
		a, existed, err = w.svc.addManual(r.Context(), who.User, in.Symbol, in.Name, in.Kind, in.Currency, px, f.Get("seguir") == "1")
		if err == nil {
			ok := "anadido"
			if existed {
				ok = "ya-estaba"
			}
			back(rw, r, "/activo/"+idStr(a.ID)+"?ok="+ok)
			return
		}
	} else {
		err = bad("El precio no es un número.")
	}
	if !isInput(err) && !errors.Is(err, errFull) {
		w.fail(rw, "activo manual", err)
		return
	}
	v, lerr := w.svc.load(r.Context(), who.User)
	if lerr != nil {
		w.fail(rw, "activo manual", lerr)
		return
	}
	d := w.assetsData(r, who, v)
	d.Manual, d.AssetKinds, d.Error = in, kindOptions(in.Kind), err.Error()
	w.render(rw, r, "activos.html", d)
}

// ---- Ficha de un activo ----

func (w *web) assetViewOf(v *view, p *position) *assetView {
	a := p.Asset
	av := &assetView{ID: a.ID, Symbol: a.Symbol, Name: a.Name, Kind: kindLabel(a.Kind), KindKey: a.Kind, Source: sourceName(a.Source),
		SourceKey: a.Source, SourceID: a.SourceID, Currency: a.Currency, ISIN: a.ISIN, Note: a.Note, Watch: a.Watch,
		Manualable: true}
	if a.Manual > 0 {
		av.Manual = trimNum(a.Manual, 10)
	}
	if p.Price > 0 {
		av.Price = price(p.Price, p.Quote.Currency)
		if !p.Quote.At.IsZero() {
			av.PriceAt = ago(p.Quote.At, v.now)
		}
		switch p.PriceFrom {
		case "manual":
			av.PriceFrom = "puesto a mano"
		case "historico":
			av.PriceFrom = "último cierre guardado"
		case "operacion":
			av.PriceFrom = "el de tu última operación"
		default:
			av.PriceFrom = sourceName(p.PriceFrom)
		}
		if ch := p.Quote.change(); p.Quote.Prev > 0 && ch != 0 {
			av.DayPct, av.DayClass = pct(ch), cls(ch)
		}
	}
	av.Realized, av.Income, av.Fees = signed(euros(p.Realized), p.Realized), euros(p.Income), euros(p.Fees)
	if p.Qty > eps {
		av.Held = true
		av.Qty, av.Cost, av.Avg = qty(p.Qty), euros(p.Cost), euros(p.Avg())
		if a.Currency != "EUR" && p.AvgNative() > 0 {
			av.AvgNative = price(p.AvgNative(), a.Currency)
		}
		if p.Priced {
			av.Value = euros(p.Value)
			av.GP, av.GPPct, av.GPClass = signed(euros(p.Unrealized), p.Unrealized), pct(p.UnrealPct), cls(p.Unrealized)
		}
		var names []string
		for n, q := range p.Platforms {
			if q <= eps {
				continue
			}
			if n == "" {
				n = "Sin plataforma"
			}
			names = append(names, n+": "+qty(q))
		}
		sort.Strings(names)
		av.Platforms = names
	}
	return av
}

func (w *web) opRows(v *view, ops []operation) []opRow {
	today := dayOf(v.now)
	var out []opRow
	for i := len(ops) - 1; i >= 0; i-- { // la más reciente arriba
		o := ops[i]
		row := opRow{ID: o.ID, Day: shortDate(o.Day, today), Kind: opLabel(o.Kind), KindKey: o.Kind, AssetID: o.AssetID, Platform: o.Platform,
			Note: o.Note, Talia: o.Origin == originAssistant}
		if a, ok := v.asset(o.AssetID); ok {
			row.Symbol = a.Symbol
		} else if o.AssetID == 0 {
			row.Symbol = "General"
		}
		if o.Qty > 0 {
			row.Qty = qty(o.Qty)
		}
		cur := o.Currency
		switch o.Kind {
		case opBuy, opSell, opReward:
			row.Price = price(o.Price, cur)
			row.Amount = price(o.Qty*o.Price, cur)
			if o.Fee > 0 {
				row.Extra = "comisión " + price(o.Fee, cur)
			}
		case opDividend:
			row.Amount = price(o.Amount, cur)
			if o.Fee > 0 {
				row.Extra = "retención o comisión " + price(o.Fee, cur)
			}
		case opFee:
			if o.Amount > 0 {
				row.Amount = price(o.Amount, cur)
			}
		case opTransfer:
			row.Platform = o.Platform + " → " + o.To
			if o.Fee > 0 {
				row.Extra = "comisión " + price(o.Fee, cur)
			}
		}
		out = append(out, row)
	}
	return out
}

func (w *web) alertRows(v *view, list []alert, onlyAsset int64) []alertRow {
	var out []alertRow
	for _, a := range list {
		if onlyAsset != 0 && a.AssetID != onlyAsset {
			continue
		}
		sym, cur := "", "EUR"
		if x, ok := v.asset(a.AssetID); ok {
			sym, cur = x.Symbol, x.Currency
			if q, ok := v.quotes[x.ID]; ok && q.Currency != "" {
				cur = q.Currency
			}
		}
		row := alertRow{ID: a.ID, AssetID: a.AssetID, Symbol: sym, Desc: alertDesc(a, sym, cur), Active: a.Active, Crossed: a.Crossed, Last: a.Last}
		if !a.Fired.IsZero() {
			row.Fired = ago(a.Fired, v.now)
		}
		out = append(out, row)
	}
	return out
}

func alertKindOptions(sel string, portfolio bool) []option {
	list := []option{{Key: alertAbove, Label: "Precio por encima de"}, {Key: alertBelow, Label: "Precio por debajo de"},
		{Key: alertDay, Label: "Variación del día de ± X % o más"}, {Key: alertDrop, Label: "Caída de X % desde el máximo de N días"}}
	if portfolio {
		list = []option{{Key: alertAbove, Label: "Valor por encima de (€)"}, {Key: alertBelow, Label: "Valor por debajo de (€)"},
			{Key: alertDay, Label: "Variación del día de ± X % o más"}}
	}
	for i := range list {
		list[i].Selected = list[i].Key == sel
	}
	return list
}

func (w *web) assetPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	w.renderAsset(rw, r, who, id, "", nil)
}

func (w *web) renderAsset(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, msg string, af *alertForm) {
	ctx := r.Context()
	v, err := w.svc.load(ctx, who.User)
	if err != nil {
		w.fail(rw, "ficha", err)
		return
	}
	p := v.pf.pos(id)
	if p == nil {
		notFound(rw)
		return
	}
	alerts, err := w.svc.store.alerts(ctx, who.User)
	if err != nil {
		w.fail(rw, "ficha", err)
		return
	}
	today := dayOf(v.now)
	_, from, opts := pickRange(r.URL.Query().Get("r"), today)
	d := pageData{Name: who.Name, Tab: "activos", Title: p.Asset.Symbol, Asset: w.assetViewOf(v, p), Ranges: opts, Error: msg,
		AssetKinds: kindOptions(p.Asset.Kind), Confirm: r.URL.Query().Get("borrar") == "1", AlertKinds: alertKindOptions("", false)}
	var mine []operation
	var marks []markIn
	for _, o := range v.ops {
		if o.AssetID != id {
			continue
		}
		mine = append(mine, o)
		switch o.Kind {
		case opBuy, opReward:
			marks = append(marks, markIn{T: o.Day, Buy: true, Title: opLabel(o.Kind) + " · " + shortDate(o.Day, today) + " · " + qty(o.Qty) + " a " + price(o.Price, o.Currency)})
		case opSell:
			marks = append(marks, markIn{T: o.Day, Title: "Venta · " + shortDate(o.Day, today) + " · " + qty(o.Qty) + " a " + price(o.Price, o.Currency)})
		}
	}
	d.Ops = w.opRows(v, mine)
	d.Alerts = w.alertRows(v, alerts, id)
	if af == nil {
		af = &alertForm{AssetID: id, Kind: alertAbove, Days: "30", Repeat: false}
	}
	af.Currency = curSymbol(p.Quote.Currency)
	if af.Currency == "" {
		af.Currency = curSymbol(p.Asset.Currency)
	}
	d.AForm = *af
	d.AlertKinds = alertKindOptions(af.Kind, false)
	h := v.hist[id]
	if !from.IsZero() {
		h = h.since(from)
	}
	var points []pt
	for i, day := range h.Days {
		points = append(points, pt{fromEpoch(day), h.Closes[i]})
	}
	if p.Price > 0 && p.PriceFrom != "historico" {
		if n := len(points); n > 0 && !points[n-1].T.Before(today) {
			points = points[:n-1]
		}
		points = append(points, pt{today, p.Price})
	}
	d.Chart = buildChart(points, nil, marks, "Precio de "+p.Asset.Symbol+" en "+p.Asset.Currency, today)
	w.render(rw, r, "activo.html", d)
}

func (w *web) assetPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	ctx := r.Context()
	a, err := w.svc.store.asset(ctx, who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "editar activo", err)
		return
	}
	f := r.PostForm
	if name := clip(f.Get("nombre"), 80); name != "" {
		a.Name = name
	}
	if validKind(f.Get("tipo")) {
		a.Kind = f.Get("tipo")
	}
	a.Note = clip(f.Get("nota"), 300)
	if isin := strings.ToUpper(strings.TrimSpace(f.Get("isin"))); isin == "" || looksISIN(isin) {
		a.ISIN = isin
	} else {
		w.renderAsset(rw, r, who, id, "Ese ISIN no es válido (12 caracteres, como ES0000000000).", nil)
		return
	}
	px, err := parseOpt(f.Get("precio_manual"), true)
	if err != nil || px < 0 {
		w.renderAsset(rw, r, who, id, "El precio a mano no es un número.", nil)
		return
	}
	if px != a.Manual {
		a.Manual, a.ManualAt = px, w.svc.clock()
		if px == 0 {
			a.ManualAt = time.Time{}
		}
	}
	a.Watch = f.Get("seguir") == "1"
	if err := w.svc.store.updateAsset(ctx, who.User, a); err != nil {
		w.fail(rw, "editar activo", err)
		return
	}
	if a.Manual > 0 {
		w.svc.checkAlerts(ctx, who.User) // con el precio nuevo
	}
	back(rw, r, "/activo/"+idStr(id)+"?ok=guardado")
}

func (w *web) assetDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/activo/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	err := w.svc.store.deleteAsset(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar activo", err)
	default:
		back(rw, r, "/activos?ok=borrado")
	}
}

// ---- Operaciones ----

const originAssistant = "asistente"

func (w *web) opsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "operaciones", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "operaciones", Title: "Operaciones", AssetOpts: assetOptions(v.assets, 0, false)}
	ops := v.ops
	if f, err := strconv.ParseInt(r.URL.Query().Get("activo"), 10, 64); err == nil && f > 0 {
		var mine []operation
		for _, o := range ops {
			if o.AssetID == f {
				mine = append(mine, o)
			}
		}
		ops = mine
		d.Filter = idStr(f)
		d.AssetOpts = assetOptions(v.assets, f, false)
	}
	d.Ops = w.opRows(v, ops)
	w.render(rw, r, "operaciones.html", d)
}

// opAssetOptions son los activos para el formulario de una operación: sin
// activo elegido, «Elige…» (para no apuntar en uno por error); al final, la
// comisión general sin activo.
func opAssetOptions(list []asset, f *opForm) []option {
	general := f.AssetID == 0 && f.Kind == opFee && f.ID != 0
	var out []option
	if f.AssetID == 0 && !general {
		out = append(out, option{Key: "", Label: "Elige un activo…", Selected: true})
	}
	out = append(out, assetOptions(list, f.AssetID, false)...)
	return append(out, option{Key: "0", Label: "Ninguno (comisión general)", Selected: general})
}

func assetOptions(list []asset, sel int64, general bool) []option {
	var out []option
	if general {
		out = append(out, option{Key: "0", Label: "Ninguno (comisión general)", Selected: sel == 0})
	}
	for _, a := range list {
		lbl := a.Symbol
		if a.Name != "" && !strings.EqualFold(a.Name, a.Symbol) {
			lbl += " · " + a.Name
		}
		out = append(out, option{Key: idStr(a.ID), Label: clip(lbl, 60), Selected: a.ID == sel})
	}
	return out
}

func opKindOptions(sel string) []option {
	var out []option
	for _, k := range opKinds {
		out = append(out, option{Key: k.Key, Label: k.Label, Selected: k.Key == sel})
	}
	return out
}

func (w *web) renderOpForm(rw http.ResponseWriter, r *http.Request, who app.Identity, v *view, f *opForm, msg string) {
	d := pageData{Name: who.Name, Tab: "operaciones", Title: "Nueva operación", OpForm: f, OpKinds: opKindOptions(f.Kind),
		AssetOpts: opAssetOptions(v.assets, f), Platforms: platforms(v.ops), Error: msg,
		Confirm: r.URL.Query().Get("borrar") == "1"}
	if f.ID != 0 {
		d.Title = "Editar operación"
	}
	if a, ok := v.asset(f.AssetID); ok {
		f.AssetSymbol = a.Symbol
	}
	w.render(rw, r, "operacion.html", d)
}

func (w *web) opNewPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "nueva operación", err)
		return
	}
	f := &opForm{Kind: opBuy, Day: isoDay(dayOf(v.now))}
	if id, err := strconv.ParseInt(r.URL.Query().Get("activo"), 10, 64); err == nil {
		if a, ok := v.asset(id); ok {
			f.AssetID, f.Currency = a.ID, a.Currency
			if a.Source == sourceKraken {
				f.Platform = "Kraken"
			}
		}
	}
	for _, k := range opKinds {
		if k.Key == r.URL.Query().Get("tipo") {
			f.Kind = k.Key
		}
	}
	if len(v.assets) == 0 {
		back(rw, r, "/activos?ok=")
		return
	}
	w.renderOpForm(rw, r, who, v, f, "")
}

// readOp lee el formulario de una operación.
func readOp(r *http.Request) (*opForm, opInput, error) {
	f := r.PostForm
	fm := &opForm{Kind: f.Get("tipo"), Day: f.Get("fecha"), Time: f.Get("hora"), Qty: f.Get("cantidad"), Price: f.Get("precio"),
		Total: f.Get("total"), Fee: f.Get("comision"), Currency: f.Get("moneda"), Platform: f.Get("plataforma"), To: f.Get("destino"),
		Note: f.Get("nota")}
	fm.AssetID, _ = strconv.ParseInt(f.Get("activo"), 10, 64)
	in := opInput{AssetID: fm.AssetID, Kind: fm.Kind, Time: fm.Time, Currency: fm.Currency, Platform: fm.Platform, To: fm.To, Note: fm.Note}
	day, err := parseDay(fm.Day)
	if err != nil {
		return fm, in, bad("La fecha no es válida.")
	}
	in.Day = day
	for _, x := range []struct {
		s     string
		dst   *float64
		label string
		money bool
	}{{fm.Qty, &in.Qty, "La cantidad", false}, {fm.Price, &in.Price, "El precio", true}, {fm.Total, &in.Total, "El importe", true}, {fm.Fee, &in.Fee, "La comisión", true}} {
		v, err := parseOpt(x.s, x.money)
		if err != nil {
			return fm, in, bad(x.label + " no es un número.")
		}
		*x.dst = v
	}
	return fm, in, nil
}

func (w *web) opNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	fm, in, err := readOp(r)
	in.Origin = "web"
	var o operation
	if err == nil {
		o, err = w.svc.addOp(r.Context(), who.User, in)
	}
	switch {
	case isInput(err) || errors.Is(err, errFull) || errors.Is(err, errNotFound):
		v, lerr := w.svc.load(r.Context(), who.User)
		if lerr != nil {
			w.fail(rw, "apuntar", lerr)
			return
		}
		msg := err.Error()
		if errors.Is(err, errFull) {
			msg = "Has llegado al máximo de operaciones."
		} else if errors.Is(err, errNotFound) {
			msg = "Elige un activo de la lista."
		}
		w.renderOpForm(rw, r, who, v, fm, msg)
		return
	case err != nil:
		w.fail(rw, "apuntar", err)
		return
	}
	if o.AssetID != 0 {
		back(rw, r, "/activo/"+idStr(o.AssetID)+"?ok=apuntada#operaciones")
		return
	}
	back(rw, r, "/operaciones?ok=apuntada")
}

func (w *web) opPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "operación", err)
		return
	}
	var o *operation
	for i := range v.ops {
		if v.ops[i].ID == id {
			o = &v.ops[i]
		}
	}
	if o == nil {
		notFound(rw)
		return
	}
	f := &opForm{ID: o.ID, AssetID: o.AssetID, Kind: o.Kind, Day: isoDay(o.Day), Time: o.Time, Currency: o.Currency,
		Platform: o.Platform, To: o.To, Note: o.Note, Origin: o.Origin}
	num := func(x float64) string {
		if x == 0 {
			return ""
		}
		return trimNum(x, 10)
	}
	f.Qty, f.Price, f.Fee = num(o.Qty), num(o.Price), num(o.Fee)
	if o.Kind == opDividend || o.Kind == opFee {
		f.Total = num(o.Amount)
		if o.Kind == opFee {
			f.Fee = ""
		}
	}
	w.renderOpForm(rw, r, who, v, f, "")
}

func (w *web) opPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	fm, in, err := readOp(r)
	fm.ID = id
	var o operation
	if err == nil {
		o, err = w.svc.updateOp(r.Context(), who.User, id, in)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case isInput(err):
		v, lerr := w.svc.load(r.Context(), who.User)
		if lerr != nil {
			w.fail(rw, "editar operación", lerr)
			return
		}
		w.renderOpForm(rw, r, who, v, fm, err.Error())
		return
	case err != nil:
		w.fail(rw, "editar operación", err)
		return
	}
	if o.AssetID != 0 {
		back(rw, r, "/activo/"+idStr(o.AssetID)+"?ok=guardado#operaciones")
		return
	}
	back(rw, r, "/operaciones?ok=guardado")
}

func (w *web) opDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	if r.PostForm.Get("confirmar") != "1" {
		back(rw, r, "/operacion/"+idStr(id)+"?borrar=1#borrar")
		return
	}
	o, err := w.svc.store.op(r.Context(), who.User, id)
	if err == nil {
		err = w.svc.store.deleteOp(r.Context(), who.User, id)
	}
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar operación", err)
	case o.AssetID != 0:
		back(rw, r, "/activo/"+idStr(o.AssetID)+"?ok=borrado#operaciones")
	default:
		back(rw, r, "/operaciones?ok=borrado")
	}
}

// ---- Alertas ----

func (w *web) alertsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderAlerts(rw, r, who, nil, "")
}

func (w *web) renderAlerts(rw http.ResponseWriter, r *http.Request, who app.Identity, af *alertForm, msg string) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "alertas", err)
		return
	}
	list, err := w.svc.store.alerts(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "alertas", err)
		return
	}
	if af == nil {
		af = &alertForm{Kind: alertAbove, Days: "30"}
	}
	d := pageData{Name: who.Name, Tab: "alertas", Title: "Alertas", Alerts: w.alertRows(v, list, 0), AForm: *af, Error: msg,
		AlertKinds: alertKindOptions(af.Kind, false)}
	d.AssetOpts = append([]option{{Key: "0", Label: "Toda la cartera", Selected: af.AssetID == 0}}, assetOptions(v.assets, af.AssetID, false)...)
	w.render(rw, r, "alertas.html", d)
}

func (w *web) alertNewPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	f := r.PostForm
	af := &alertForm{Kind: f.Get("tipo"), Threshold: f.Get("umbral"), Days: f.Get("dias"), Repeat: f.Get("repetir") == "1"}
	af.AssetID, _ = strconv.ParseInt(f.Get("activo"), 10, 64)
	fromAsset := f.Get("desde") == "ficha" && af.AssetID > 0
	again := func(msg string) {
		if fromAsset {
			w.renderAsset(rw, r, who, af.AssetID, msg, af)
		} else {
			w.renderAlerts(rw, r, who, af, msg)
		}
	}
	th, err := parseMoney(af.Threshold)
	if err != nil {
		again("El umbral no es un número.")
		return
	}
	a := alert{AssetID: af.AssetID, alertData: alertData{Kind: af.Kind, Threshold: math.Abs(th), Repeat: af.Repeat}}
	if af.Kind == alertDrop {
		a.Days, _ = strconv.Atoi(strings.TrimSpace(af.Days))
		if a.Days == 0 {
			a.Days = -1 // que lo rechace checkAlert
		}
	}
	if err := checkAlert(&a); err != nil {
		again(strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ".")
		return
	}
	_, err = w.svc.store.addAlert(r.Context(), who.User, a)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
		return
	case errors.Is(err, errFull):
		again("Has llegado al máximo de alertas.")
		return
	case err != nil:
		w.fail(rw, "alerta", err)
		return
	}
	w.svc.checkAlerts(r.Context(), who.User) // por si ya se cumple
	if fromAsset {
		back(rw, r, "/activo/"+idStr(af.AssetID)+"?ok=alerta#alertas")
		return
	}
	back(rw, r, "/alertas?ok=alerta")
}

// alertBack vuelve a la ficha si se vino de ella.
func alertBack(r *http.Request, ok string) string {
	if id, err := strconv.ParseInt(r.PostForm.Get("activo"), 10, 64); err == nil && id > 0 && r.PostForm.Get("desde") == "ficha" {
		return "/activo/" + idStr(id) + "?ok=" + ok + "#alertas"
	}
	return "/alertas?ok=" + ok
}

func (w *web) alertDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	err := w.svc.store.deleteAlert(r.Context(), who.User, id)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "borrar alerta", err)
	default:
		back(rw, r, alertBack(r, "alerta-borrada"))
	}
}

func (w *web) alertToggle(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id, ok := pathID(r)
	if !ok || !form(rw, r) {
		if !ok {
			notFound(rw)
		}
		return
	}
	on := r.PostForm.Get("on") == "1"
	err := w.svc.store.setAlertActive(r.Context(), who.User, id, on)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "alerta", err)
	default:
		if on {
			w.svc.checkAlerts(r.Context(), who.User)
			back(rw, r, alertBack(r, "activada"))
			return
		}
		back(rw, r, alertBack(r, "desactivada"))
	}
}

// ---- Seguimiento ----

func (w *web) watchRows(v *view) []watchRow {
	today := dayOf(v.now)
	var out []watchRow
	for _, p := range v.pf.Positions {
		if !p.Asset.Watch {
			continue
		}
		row := watchRow{ID: p.Asset.ID, Symbol: p.Asset.Symbol, Name: p.Asset.Name, Held: p.Qty > eps, Source: sourceName(p.Asset.Source)}
		if p.Price > 0 {
			row.Price = price(p.Price, p.Quote.Currency)
			if ch := p.Quote.change(); p.Quote.Prev > 0 && ch != 0 {
				row.DayPct, row.DayClass = pct(ch), cls(ch)
			}
			if old, ok := v.hist[p.Asset.ID].at(today.Add(-30 * dayUnit)); ok && old > 0 {
				ch := (p.Price/old - 1) * 100
				row.Month, row.MonthClass = pct(ch), cls(ch)
			}
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Symbol) < strings.ToLower(out[j].Symbol) })
	return out
}

func (w *web) watchPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	v, err := w.svc.load(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "seguimiento", err)
		return
	}
	w.render(rw, r, "seguimiento.html", pageData{Name: who.Name, Tab: "seguimiento", Title: "Seguimiento", Watch: w.watchRows(v)})
}

func (w *web) watchPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !form(rw, r) {
		return
	}
	id, err := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return
	}
	on := r.PostForm.Get("seguir") == "1"
	err = w.svc.store.setWatch(r.Context(), who.User, id, on)
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "seguimiento", err)
	case r.PostForm.Get("desde") == "ficha":
		back(rw, r, "/activo/"+idStr(id)+"?ok=guardado")
	case on:
		back(rw, r, "/seguimiento?ok=siguiendo")
	default:
		back(rw, r, "/seguimiento?ok=sin-seguir")
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
