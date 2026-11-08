package main

import (
	"bytes"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// sampleView es una cartera de ejemplo (genérica: el repositorio es
// público) para ver las páginas sin base de datos.
func sampleView() *view {
	now := testNow()
	today := dayOf(now)
	mk := func(id int64, sym, name, kind, src, sid, cur string, watch bool) asset {
		return asset{ID: id, Watch: watch, assetData: assetData{Symbol: sym, Name: name, Kind: kind, Source: src, SourceID: sid, Currency: cur}}
	}
	assets := []asset{
		mk(1, "BTC", "BTC", "cripto", sourceKraken, "XBTEUR", "EUR", true),
		mk(2, "ETH", "ETH", "cripto", sourceKraken, "ETHEUR", "EUR", false),
		mk(3, "ACME", "Acme Corp", "accion", sourceYahoo, "ACME", "USD", false),
		mk(4, "EJEM.AS", "Ejemplo World ETF con un nombre largo para ver cómo se parte", "etf", sourceYahoo, "EJEM.AS", "EUR", false),
		mk(5, "FONDOX", "Fondo de ejemplo", "fondo", sourceManual, "", "EUR", false),
		mk(6, "EJC", "Ejemplo Coin", "cripto", sourceGecko, "ejemplo-coin", "EUR", true),
		mk(7, "VIEJA", "Acción ya vendida", "accion", sourceYahoo, "VIEJA", "EUR", false),
	}
	assets[4].Manual, assets[4].ManualAt = 10.4, now.Add(-72*time.Hour)
	assets[3].ISIN = "IE0000000002"
	hist := map[int64]series{}
	wave := func(id int64, base, amp float64, days int) {
		var h series
		for i := days; i >= 1; i-- {
			h.Days = append(h.Days, epochDay(today.Add(-time.Duration(i)*dayUnit)))
			h.Closes = append(h.Closes, base*(1+amp*math.Sin(float64(i)/23)+0.0006*float64(days-i)))
		}
		hist[id] = h
	}
	wave(1, 42000, 0.18, 720)
	wave(2, 2300, 0.2, 720)
	wave(3, 95, 0.1, 500)
	wave(4, 74, 0.06, 500)
	wave(6, 0.11, 0.3, 365)
	wave(7, 20, 0.1, 500)
	quotes := map[int64]quote{
		1: {Price: 70250, Prev: 68138, Currency: "EUR", At: now.Add(-3 * time.Minute)},
		2: {Price: 2210, Prev: 2290, Currency: "EUR", At: now.Add(-3 * time.Minute)},
		3: {Price: 112.4, Prev: 110.9, Currency: "USD", At: now.Add(-14 * time.Minute)},
		4: {Price: 81.3, Prev: 80.95, Currency: "EUR", At: now.Add(-14 * time.Minute)},
		6: {Price: 0.1234, Prev: 0.1204, Currency: "EUR", At: now.Add(-8 * time.Minute)},
		7: {Price: 22, Prev: 22.1, Currency: "EUR", At: now.Add(-14 * time.Minute)},
	}
	d := func(n int) time.Time { return today.Add(-time.Duration(n) * dayUnit) }
	op := func(id, aid int64, day time.Time, kind string, q, p, amount, fee float64, cur, plat, to, note, origin string) operation {
		return operation{ID: id, AssetID: aid, Day: day, opData: opData{Kind: kind, Qty: q, Price: p, Amount: amount, Fee: fee, Currency: cur, Platform: plat, To: to, Note: note, Origin: origin}}
	}
	ops := []operation{
		op(1, 1, d(400), opBuy, 0.05, 30000, 0, 4.5, "EUR", "Kraken", "", "", "web"),
		op(2, 1, d(250), opBuy, 0.03, 52000, 0, 3, "EUR", "Binance", "", "", "web"),
		op(3, 1, d(120), opSell, 0.02, 61000, 0, 2.4, "EUR", "Binance", "", "", "web"),
		op(4, 1, d(60), opTransfer, 0.01, 0, 0, 0, "EUR", "Binance", "Kraken", "", "web"),
		op(5, 1, d(10), opReward, 0.0004, 66000, 0, 0, "EUR", "Kraken", "", "staking", "asistente"),
		op(6, 2, d(300), opBuy, 1.2, 1800, 0, 2, "EUR", "Kraken", "", "", "web"),
		op(7, 3, d(350), opBuy, 15, 88, 0, 2, "USD", "DEGIRO", "", "", "web"),
		op(8, 3, d(30), opDividend, 0, 0, 3.6, 0.54, "USD", "DEGIRO", "", "", "web"),
		op(9, 4, d(320), opBuy, 40, 71.2, 0, 1, "EUR", "DEGIRO", "", "aportación mensual", "web"),
		op(10, 4, d(200), opBuy, 10, 76, 0, 1, "EUR", "DEGIRO", "", "", "asistente"),
		op(11, 5, d(180), opBuy, 200, 10, 0, 0, "EUR", "Banco de ejemplo", "", "", "web"),
		op(12, 0, d(90), opFee, 0, 0, 2.5, 0, "EUR", "DEGIRO", "", "conexión a bolsa", "web"),
		op(13, 7, d(450), opBuy, 50, 18, 0, 1, "EUR", "DEGIRO", "", "", "web"),
		op(14, 7, d(40), opSell, 50, 21.5, 0, 1, "EUR", "DEGIRO", "", "", "web"),
	}
	fx := fxTable{days: []time.Time{d(800), d(200)}, rates: []map[string]float64{{"USD": 1.08}, {"USD": 1.10}}}
	v := &view{assets: assets, ops: ops, quotes: quotes, hist: hist, fx: fx, now: now}
	v.pf = compute(v.assets, append([]operation(nil), ops...), quotes, hist, fx, now)
	return v
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con CARTERA_PREVIEW_DIR escribe las páginas y
// lo estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	c := &clock{t: testNow()}
	m := newMarket(defaultURLs, false)
	m.now = c.now
	m.status[sourceKraken] = &sourceStatus{OK: testNow().Add(-3 * time.Minute)}
	m.status[sourceYahoo] = &sourceStatus{OK: testNow().Add(-2 * time.Hour), ErrAt: testNow().Add(-10 * time.Minute), Err: "límite de peticiones: se espera antes de volver a pedir", Until: testNow().Add(20 * time.Minute)}
	w := &web{svc: &service{market: m, log: slog.New(slog.DiscardHandler), now: c.now}}
	v := sampleView()
	today := dayOf(v.now)
	if v.pf.Held != 5 || len(v.pf.Warnings) != 0 {
		t.Fatalf("cartera de ejemplo: %d activos, avisos %v", v.pf.Held, v.pf.Warnings)
	}

	sum := func(r string) pageData {
		_, from, opts := pickRange(r, today)
		d := pageData{Name: "Ana", Tab: "resumen", Sum: summaryOf(v.pf), Ranges: opts, Sources: w.sourcesView(v.assets), Chart: valueChart(v.series(from), today)}
		d.Positions, _ = posRows(v.pf)
		return d
	}
	assetsData := func() pageData {
		d := pageData{Name: "Ana", Tab: "activos", Title: "Activos", AssetKinds: kindOptions("otro"), Manual: manualForm{Currency: "EUR", Kind: "otro"}}
		d.Positions, d.Closed = posRows(v.pf)
		return d
	}
	search := assetsData()
	search.Query, search.Searched = "ejemplo", true
	search.Results = []resultRow{
		{Ref: "yahoo:EJEM.AS", Symbol: "EJEM.AS", Name: "Ejemplo World ETF", Kind: "etf", KindLabel: "ETF", Market: "Amsterdam", Source: "Yahoo Finance", Have: true, HaveID: 4},
		{Ref: "coingecko:ejemplo-coin", Symbol: "EJC", Name: "Ejemplo Coin", Kind: "cripto", KindLabel: "Cripto", Market: "CoinGecko", Source: "CoinGecko"},
	}
	search.Failed = "Kraken no responde ahora; prueba más tarde."
	manualErr := assetsData()
	manualErr.Manual, manualErr.Error = manualForm{Symbol: "X", Currency: "EURO", Price: "12,5"}, "La moneda va con su código de tres letras: EUR, USD…"
	noResults := assetsData()
	noResults.Query, noResults.Searched = "nada", true

	ficha := func(id int64, r string, confirm bool) pageData {
		p := v.pf.pos(id)
		_, from, opts := pickRange(r, today)
		d := pageData{Name: "Ana", Tab: "activos", Title: p.Asset.Symbol, Asset: w.assetViewOf(v, p), Ranges: opts, AssetKinds: kindOptions(p.Asset.Kind),
			Confirm: confirm, AlertKinds: alertKindOptions(alertAbove, false), AForm: alertForm{AssetID: id, Kind: alertAbove, Days: "30", Currency: curSymbol(p.Quote.Currency)}}
		var mine []operation
		var marks []markIn
		for _, o := range v.ops {
			if o.AssetID == id {
				mine = append(mine, o)
				marks = append(marks, markIn{T: o.Day, Buy: o.Kind != opSell, Title: opLabel(o.Kind)})
			}
		}
		d.Ops = w.opRows(v, mine)
		d.Alerts = w.alertRows(v, sampleAlerts(), id)
		h := v.hist[id].since(from)
		var ps []pt
		for i, dd := range h.Days {
			ps = append(ps, pt{fromEpoch(dd), h.Closes[i]})
		}
		if p.Price > 0 {
			ps = append(ps, pt{today, p.Price})
		}
		d.Chart = buildChart(ps, nil, marks, "Precio", today)
		return d
	}
	opsData := pageData{Name: "Ana", Tab: "operaciones", Title: "Operaciones", AssetOpts: assetOptions(v.assets, 0, false), Ops: w.opRows(v, v.ops)}
	formPage := func(f *opForm, msg string, confirm bool) pageData {
		d := pageData{Name: "Ana", Tab: "operaciones", Title: "Nueva operación", OpForm: f, OpKinds: opKindOptions(f.Kind), AssetOpts: opAssetOptions(v.assets, f),
			Platforms: platforms(v.ops), Error: msg, Confirm: confirm}
		if f.ID != 0 {
			d.Title = "Editar operación"
		}
		if a, ok := v.asset(f.AssetID); ok {
			f.AssetSymbol = a.Symbol
		}
		return d
	}
	alertsData := pageData{Name: "Ana", Tab: "alertas", Title: "Alertas", Alerts: w.alertRows(v, sampleAlerts(), 0), AForm: alertForm{Kind: alertAbove, Days: "30"},
		AlertKinds: alertKindOptions(alertAbove, false)}
	alertsData.AssetOpts = append([]option{{Key: "0", Label: "Toda la cartera", Selected: true}}, assetOptions(v.assets, 0, false)...)
	alertsEmpty := alertsData
	alertsEmpty.Alerts = nil
	alertsEmpty.AlertKinds = alertKindOptions(alertDrop, false)
	alertsEmpty.Error, alertsEmpty.AForm = "La caída desde el máximo solo vale para un activo.", alertForm{Kind: alertDrop, Threshold: "10", Days: "30"}
	watch := pageData{Name: "Ana", Tab: "seguimiento", Title: "Seguimiento", Watch: w.watchRows(v), Flash: "En seguimiento."}
	watchSearch := watch
	watchSearch.Query, watchSearch.Searched, watchSearch.Results = "ejc", true, search.Results[1:]

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"resumen", "resumen.html", sum("90")},
		{"resumen-todo", "resumen.html", sum("todo")},
		{"resumen-vacio", "resumen.html", pageData{Name: "Ana", Tab: "resumen", Sum: summaryOf(&portfolio{}), Ranges: sum("").Ranges}},
		{"activos", "activos.html", assetsData()},
		{"activos-busqueda", "activos.html", search},
		{"activos-sin-resultados", "activos.html", noResults},
		{"activos-error", "activos.html", manualErr},
		{"activo", "activo.html", ficha(1, "365", false)},
		{"activo-usd", "activo.html", ficha(3, "90", false)},
		{"activo-manual", "activo.html", ficha(5, "90", true)},
		{"activo-seguimiento", "activo.html", ficha(6, "30", false)},
		{"operaciones", "operaciones.html", opsData},
		{"operaciones-vacio", "operaciones.html", pageData{Name: "Ana", Tab: "operaciones", Filter: "6", AssetOpts: assetOptions(v.assets, 6, false)}},
		{"operacion-nueva", "operacion.html", formPage(&opForm{Kind: opBuy, Day: isoDay(today), AssetID: 1, Currency: "EUR", Platform: "Kraken"}, "", false)},
		{"operacion-editar", "operacion.html", formPage(&opForm{ID: 5, AssetID: 1, Kind: opReward, Day: isoDay(today.Add(-10 * dayUnit)), Qty: "0,0004", Price: "66000", Origin: "asistente"}, "", true)},
		{"operacion-error", "operacion.html", formPage(&opForm{Kind: opSell, Day: isoDay(today), AssetID: 3, Qty: "5"}, "Falta el precio (por unidad o el total).", false)},
		{"alertas", "alertas.html", alertsData},
		{"alertas-vacio", "alertas.html", alertsEmpty},
		{"seguimiento", "seguimiento.html", watch},
		{"seguimiento-busqueda", "seguimiento.html", watchSearch},
	}
	wants := map[string][]string{
		"resumen": {"Posiciones", "Reparto", "Por plataforma", "DEGIRO", "Kraken", "Realizada (FIFO)", `class="chart`, `<polyline class="line"`, `<polyline class="cost"`,
			`href="/cartera/activo/1"`, "Fuentes de precios", "Falla: límite de peticiones", "se vuelve a intentar en 20 min", `aria-current="true">90 d`},
		"resumen-vacio":          {"Aún no hay nada en la cartera", `href="/cartera/activos"`},
		"activos":                {"Lo que tienes", "Ya no los tienes", "VIEJA", `action="/cartera/buscar"`, `action="/cartera/activos/manual"`},
		"activos-busqueda":       {"Resultados para «ejemplo»", "Ya lo tienes", `name="ref" value="coingecko:ejemplo-coin"`, "Kraken no responde ahora", `<button class="btn small primary">Añadir</button>`},
		"activos-sin-resultados": {"No ha salido nada"},
		"activos-error":          {"La moneda va con su código", `value="EURO"`, " open"},
		"activo": {"BTC", "70.250 €", "&#43;3,1 % hoy", "Kraken: ", `class="mark buy"`, `class="mark sell"`, "Recompensa", "Talia", "staking",
			`action="/cartera/alertas/nueva"`, `name="desde" value="ficha"`, "BTC por encima de 70.000 € (cada vez)", `href="/cartera/activo/1?borrar=1#borrar"`,
			`href="/cartera/operacion/nueva?activo=1&amp;tipo=venta"`, "Recompensa</a>"},
		"activo-usd":           {"ACME", "112,40 $", "Dividendo</a>", "precio en $"},
		"activo-manual":        {"FONDOX", "puesto a mano", "¿Borrar FONDOX?", `name="confirmar" value="1"`, "Aún no hay histórico: los activos a mano no lo tienen"},
		"activo-seguimiento":   {"EJC", "solo lo sigues", "0,1234 €"},
		"operaciones":          {"Operaciones", "Traspaso entre plataformas", "Binance → Kraken", "General", "conexión a bolsa", `href="/cartera/operacion/12"`, "retención o comisión"},
		"operaciones-vacio":    {"Sin operaciones de ese activo"},
		"operacion-nueva":      {"Nueva operación", `<option value="compra" selected>`, `<option value="1" selected>`, `list="plataformas"`, `<option value="DEGIRO">`, "Banco de ejemplo", "Apuntar"},
		"operacion-editar":     {"Editar operación", "Apuntada por Talia", `value="0,0004"`, "¿Borrar esta operación?", `action="/cartera/operacion/5/borrar"`},
		"operacion-error":      {"Falta el precio", `<option value="venta" selected>`, `<option value="3" selected>`},
		"alertas":              {"La cartera por debajo de 25.000 € (una vez)", "Apagada", "Saltó", "Toda la cartera", `action="/cartera/alerta/1/borrar"`, `name="on" value="0"`},
		"alertas-vacio":        {"Sin alertas", "La caída desde el máximo solo vale", `<option value="caida" selected>`},
		"seguimiento":          {"BTC", "EJC", "lo tienes", "30 d", "Quitar", `name="seguir" value="0"`},
		"seguimiento-busqueda": {"Resultados para «ejc»", `name="seguir" value="1"`, "Seguir", `name="desde" value="seguimiento"`},
	}
	nots := map[string][]string{
		"resumen-vacio":    {"Posiciones", "Reparto"},
		"activo-manual":    {"Recompensa</a>"},
		"activos-busqueda": {`name="seguir"`},
		"operacion-nueva":  {"¿Borrar esta operación?", "Borrar…"},
	}

	dir := os.Getenv("CARTERA_PREVIEW_DIR")
	base := "/cartera"
	if dir != "" {
		base = "."
		os.MkdirAll(dir, 0o755)
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base = "v0.0.0-test", base
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script", "NaN", "Inf"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s contiene %q", p.file, bad)
				}
			}
			if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
				t.Errorf("%s tiene un manejador de eventos", p.file)
			}
			if !strings.Contains(out, `aria-current="page"`) {
				t.Errorf("%s: falta la pestaña activa", p.file)
			}
			if dir == "" {
				for _, want := range wants[p.file] {
					if !strings.Contains(out, want) {
						t.Errorf("%s: falta %q", p.file, want)
					}
				}
				for _, not := range nots[p.file] {
					if strings.Contains(out, not) {
						t.Errorf("%s: sobra %q", p.file, not)
					}
				}
			} else if err := os.WriteFile(filepath.Join(dir, p.file+".html"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
	if dir != "" {
		static, _ := fs.Sub(webFS, "web/static")
		os.MkdirAll(filepath.Join(dir, "static"), 0o755)
		for _, n := range []string{"app.css", "icon.svg"} {
			b, _ := fs.ReadFile(static, n)
			os.WriteFile(filepath.Join(dir, "static", n), b, 0o644)
		}
	}
}

func sampleAlerts() []alert {
	return []alert{
		{ID: 1, AssetID: 1, Active: true, Crossed: true, Fired: testNow().Add(-2 * time.Hour),
			alertData: alertData{Kind: alertAbove, Threshold: 70000, Repeat: true, Last: "📈 BTC ha superado 70.000 €: ahora 70.250 € (+3,1 % hoy)"}},
		{ID: 2, Fired: testNow().Add(-26 * time.Hour), alertData: alertData{Kind: alertBelow, Threshold: 25000, Last: "💰 Tu cartera ha bajado de 25.000 €: ahora 24.870 € (−2,3 % hoy)"}},
		{ID: 3, AssetID: 3, Active: true, alertData: alertData{Kind: alertDay, Threshold: 5, Repeat: true}},
		{ID: 4, AssetID: 4, Active: true, alertData: alertData{Kind: alertDrop, Threshold: 10, Days: 90}},
	}
}
