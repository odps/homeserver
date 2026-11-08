package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func TestFormat(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{price(70250.4, "EUR"), "70.250 €"}, {price(70000, "EUR"), "70.000 €"}, {price(123.456, "USD"), "123,46 $"},
		{price(0.123456, "EUR"), "0,1235 €"}, {price(0.00012346, "EUR"), "0,0001235 €"}, {price(1.5, "CHF"), "1,50 CHF"},
		{pct(3.14), "+3,1 %"}, {pct(-2), "−2,0 %"}, {pct(0), "0,0 %"}, {euros(-1234.5), "−1.234,50 €"},
		{eurosRound(12345.6), "12.346 €"}, {eurosRound(999.5), "999,50 €"}, {qty(0.01250000), "0,0125"}, {qty(3), "3"},
		{signed(euros(10), 10), "+10,00 €"}, {num(1234567.891, 2), "1.234.567,89"}, {num(-0.001, 2), "0,00"},
	} {
		if c.got != c.want {
			t.Errorf("%q, quería %q", c.got, c.want)
		}
	}
	for in, want := range map[string]float64{"1.234,56": 1234.56, "1234.56": 1234.56, "0,00012": 0.00012, "70 000": 70000,
		"1.234.567": 1234567, "1,234.5": 1234.5, "12 €": 12, "-3,5": -3.5, "0.05": 0.05} {
		if v, err := parseNum(in); err != nil || !near(v, want) {
			t.Errorf("parseNum(%q) = %v %v", in, v, err)
		}
	}
	// En importes, «70.000» son miles; en cantidades, «1.500» es 1,5.
	for in, want := range map[string]float64{"70.000": 70000, "1.500": 1500, "0.123": 0.123, "12.5": 12.5, "1.234,56": 1234.56, "70.000 €": 70000, "1.2345": 1.2345} {
		if v, err := parseMoney(in); err != nil || !near(v, want) {
			t.Errorf("parseMoney(%q) = %v %v", in, v, err)
		}
	}
	if v, _ := parseNum("1.500"); v != 1.5 {
		t.Errorf("parseNum(1.500) = %v", v)
	}
	for _, in := range []string{"", "abc", "1,2,3", "NaN", "Inf"} {
		if _, err := parseNum(in); err == nil {
			t.Errorf("parseNum(%q) sin error", in)
		}
	}
	if !looksISIN("IE00B4L5Y983") || looksISIN("IE00B4L5Y98") || looksISIN("1E00B4L5Y983") || looksISIN("BTC") {
		t.Error("looksISIN")
	}
}

// fx de prueba: 1 EUR = 1,25 USD hasta el 1 de junio y 1,10 después.
func testFX() fxTable {
	return fxTable{days: []time.Time{day("2026-01-02"), day("2026-06-01")},
		rates: []map[string]float64{{"USD": 1.25}, {"USD": 1.10}}}
}

func TestFIFO(t *testing.T) {
	btc := asset{ID: 1, assetData: assetData{Symbol: "BTC", Name: "BTC", Kind: "cripto", Source: sourceKraken, SourceID: "XBTEUR", Currency: "EUR"}}
	acme := asset{ID: 2, assetData: assetData{Symbol: "ACME", Name: "Acme Corp", Kind: "accion", Source: sourceYahoo, SourceID: "ACME", Currency: "USD"}}
	watch := asset{ID: 3, Watch: true, assetData: assetData{Symbol: "ETH", Kind: "cripto", Source: sourceKraken, SourceID: "ETHEUR", Currency: "EUR"}}
	op := func(id, aid int64, d, kind string, q, p, amount, fee float64, cur, plat, to string) operation {
		return operation{ID: id, AssetID: aid, Day: day(d), opData: opData{Kind: kind, Qty: q, Price: p, Amount: amount, Fee: fee, Currency: cur, Platform: plat, To: to}}
	}
	ops := []operation{
		// Desordenadas a propósito: cuenta la fecha.
		op(3, 1, "2026-03-01", opSell, 1.5, 50000, 0, 15, "EUR", "Kraken", ""),
		op(1, 1, "2026-01-10", opBuy, 1, 30000, 0, 10, "EUR", "Kraken", ""),
		op(2, 1, "2026-02-10", opBuy, 1, 40000, 0, 0, "EUR", "Kraken", ""),
		op(4, 1, "2026-04-01", opTransfer, 0.2, 0, 0, 0, "EUR", "Kraken", "Binance"),
		op(5, 1, "2026-05-01", opReward, 0.01, 55000, 0, 0, "EUR", "Binance", ""),
		op(6, 2, "2026-03-02", opBuy, 10, 100, 0, 0, "USD", "DEGIRO", ""),
		op(7, 2, "2026-07-01", opDividend, 0, 0, 5, 0.6, "USD", "DEGIRO", ""),
		op(8, 0, "2026-07-02", opFee, 0, 0, 2.5, 0, "EUR", "DEGIRO", ""),
	}
	quotes := map[int64]quote{1: {Price: 60000, Prev: 58000, Currency: "EUR", At: testNow()}, 2: {Price: 110, Prev: 100, Currency: "USD", At: testNow()}}
	pf := compute([]asset{btc, acme, watch}, ops, quotes, nil, testFX(), testNow())

	b := pf.pos(1)
	// FIFO: la venta de 1,5 se casa con la primera compra entera (30.010 con
	// comisión) y media de la segunda (20.000).
	if !near(b.Realized, 75000-15-50010) || !near(b.Qty, 0.51) || !near(b.Cost, 20000+550) {
		t.Fatalf("BTC: realizada %v, cantidad %v, coste %v", b.Realized, b.Qty, b.Cost)
	}
	if !near(b.Income, 550) || !near(b.Platforms["Kraken"], 0.3) || !near(b.Platforms["Binance"], 0.21) {
		t.Fatalf("BTC: rendimientos %v, plataformas %v", b.Income, b.Platforms)
	}
	if !near(b.Value, 0.51*60000) || !near(b.Unrealized, 0.51*60000-20550) || !near(b.DayChange, 0.51*2000) {
		t.Fatalf("BTC: valor %v, no realizada %v, hoy %v", b.Value, b.Unrealized, b.DayChange)
	}
	a := pf.pos(2)
	// 1.000 USD con el cambio de su día (1,25) = 800 €; hoy vale 1.100 USD a
	// 1,10 = 1.000 €. El dividendo, neto y con el cambio de julio.
	if !near(a.Cost, 800) || !near(a.Value, 1000) || !near(a.Income, 4.4/1.10) || !near(a.AvgNative(), 100) {
		t.Fatalf("ACME: coste %v, valor %v, dividendo %v, medio %v", a.Cost, a.Value, a.Income, a.AvgNative())
	}
	if !near(pf.Fees, 2.5) || pf.Held != 2 || !near(pf.Value, 0.51*60000+1000) {
		t.Fatalf("cartera: comisiones %v, activos %d, valor %v", pf.Fees, pf.Held, pf.Value)
	}
	if !near(pf.Total, pf.Unrealized+pf.Realized+pf.Income-pf.Fees) {
		t.Fatal("total")
	}
	if pf.Positions[0].Asset.ID != 1 || pf.Positions[2].Asset.ID != 3 {
		t.Fatal("orden: primero lo que vale más, al final lo que no se tiene")
	}
	if len(pf.ByCurrency) != 2 || pf.ByCurrency[0].Key != "EUR" || len(pf.ByPlatform) != 3 {
		t.Fatalf("reparto: %+v %+v", pf.ByCurrency, pf.ByPlatform)
	}
	var sum float64
	for _, s := range pf.ByPlatform {
		sum += s.Pct
	}
	if !near(sum, 100) {
		t.Fatalf("el reparto no suma 100: %v", sum)
	}

	// Vender más de lo que hay: aviso, sin cantidades negativas.
	pf = compute([]asset{btc}, []operation{op(1, 1, "2026-01-10", opSell, 1, 30000, 0, 0, "EUR", "Kraken", "")}, quotes, nil, testFX(), testNow())
	if len(pf.Warnings) != 1 || !strings.Contains(pf.Warnings[0], "se vende más") || pf.pos(1).Qty != 0 {
		t.Fatalf("sobreventa: %+v", pf.Warnings)
	}
	// Sin cambio para una moneda: se avisa.
	pf = compute([]asset{acme}, []operation{op(6, 2, "2026-03-02", opBuy, 1, 100, 0, 0, "USD", "DEGIRO", "")}, quotes, nil, fxTable{}, testNow())
	if len(pf.Warnings) != 1 || !strings.Contains(pf.Warnings[0], "Falta el cambio de USD") {
		t.Fatalf("sin cambio: %+v", pf.Warnings)
	}
	// Comisión en unidades (la red se queda parte al sacar una cripto).
	pf = compute([]asset{btc}, []operation{op(1, 1, "2026-01-10", opBuy, 1, 30000, 0, 0, "EUR", "Kraken", ""),
		op(2, 1, "2026-01-11", opFee, 0.01, 0, 0, 0, "EUR", "Kraken", "")}, quotes, nil, testFX(), testNow())
	if p := pf.pos(1); !near(p.Qty, 0.99) || !near(p.Fees, 300) || !near(p.Cost, 29700) {
		t.Fatalf("comisión en unidades: %+v", p)
	}
}

func TestValueSeries(t *testing.T) {
	btc := asset{ID: 1, assetData: assetData{Symbol: "BTC", Kind: "cripto", Source: sourceKraken, Currency: "EUR"}}
	today := dayOf(testNow())
	var h series
	for i := 10; i >= 1; i-- {
		h.Days = append(h.Days, epochDay(today.Add(-time.Duration(i)*dayUnit)))
		h.Closes = append(h.Closes, 100+float64(10-i)) // de 100 a 109 ayer
	}
	ops := []operation{
		{ID: 1, AssetID: 1, Day: today.Add(-8 * dayUnit), opData: opData{Kind: opBuy, Qty: 2, Price: 101, Currency: "EUR"}},
		{ID: 2, AssetID: 1, Day: today.Add(-3 * dayUnit), opData: opData{Kind: opBuy, Qty: 1, Price: 106, Currency: "EUR"}},
	}
	quotes := map[int64]quote{1: {Price: 112, Prev: 109, Currency: "EUR", At: testNow()}}
	vs := valueSeries([]asset{btc}, ops, quotes, map[int64]series{1: h}, fxTable{}, today.Add(-30*dayUnit), testNow())
	if len(vs) != 9 || !vs[0].Day.Equal(today.Add(-8*dayUnit)) {
		t.Fatalf("empieza en la primera operación: %d %v", len(vs), vs[0].Day)
	}
	if last := vs[len(vs)-1]; !near(last.Value, 3*112) || !near(last.Cost, 202+106) {
		t.Fatalf("hoy: %+v", last)
	}
	// Desde ayer: 3 × (112 − 109) = 9 €.
	if d, p, ok := change(vs, 1); !ok || !near(d, 9) || !near(p, 9.0/327*100) {
		t.Fatalf("desde ayer: %v %v %v", d, p, ok)
	}
	// En 7 días: la compra de por medio no cuenta como ganancia.
	// Hace 7 días: 2 × 103 = 206; hoy 336; metido 106 → 24 €.
	if d, _, ok := change(vs, 7); !ok || !near(d, 24) {
		t.Fatalf("en 7 días: %v %v", d, ok)
	}
	if _, _, ok := change(vs, 30); ok {
		t.Fatal("no hay datos de hace 30 días")
	}
}

func TestSeriesMerge(t *testing.T) {
	a := series{Days: []int32{1, 2, 3}, Closes: []float64{10, 20, 30}}
	b := series{Days: []int32{3, 4}, Closes: []float64{31, 40}}
	m := a.merge(b)
	if len(m.Days) != 4 || m.Closes[2] != 31 || m.Closes[3] != 40 {
		t.Fatalf("%+v", m)
	}
	if v, ok := m.at(fromEpoch(5)); !ok || v != 40 {
		t.Fatal("at después del final")
	}
	if _, ok := m.at(fromEpoch(0)); ok {
		t.Fatal("at antes del principio")
	}
	if s := m.since(fromEpoch(3)); len(s.Days) != 2 {
		t.Fatalf("since: %+v", s)
	}
}

func TestAlertSteps(t *testing.T) {
	now := testNow()
	p := &position{Asset: asset{ID: 1, assetData: assetData{Symbol: "BTC", Kind: "cripto", Currency: "EUR"}},
		Price: 70250, Quote: quote{Price: 70250, Prev: 68138, Currency: "EUR", At: now}}
	a := alert{ID: 1, AssetID: 1, Active: true, alertData: alertData{Kind: alertAbove, Threshold: 70000}}
	st := evalAsset(a, p, series{}, now)
	if !st.cond || st.text != "📈 BTC ha superado 70.000 €: ahora 70.250 € (+3,1 % hoy)" {
		t.Fatalf("texto: %q %v", st.text, st.cond)
	}
	// Una vez: salta y se apaga.
	if ch, fire := step(&a, st, now); !ch || fire == "" || a.Active || !a.Crossed || a.Last != fire {
		t.Fatalf("una vez: %+v", a)
	}
	if ch, fire := step(&a, st, now); ch || fire != "" {
		t.Fatal("apagada, no vuelve a saltar")
	}

	// Cada vez: no repite mientras siga cruzada; se rearma al volver con margen.
	r := alert{ID: 2, AssetID: 1, Active: true, alertData: alertData{Kind: alertAbove, Threshold: 70000, Repeat: true}}
	seq := []struct {
		px   float64
		fire bool
	}{{70250, true}, {71000, false}, {69900, false}, {70100, false}, {69500, false}, {70001, true}}
	for i, s := range seq {
		p.Price = s.px
		_, fire := step(&r, evalAsset(r, p, series{}, now), now)
		if (fire != "") != s.fire || !r.Active {
			t.Fatalf("paso %d (%v): %q", i, s.px, fire)
		}
	}

	// Por debajo, variación del día y caída desde el máximo.
	p.Price, p.Quote.Price = 59000, 59000
	if st := evalAsset(alert{AssetID: 1, alertData: alertData{Kind: alertBelow, Threshold: 60000}}, p, series{}, now); !st.cond ||
		!strings.HasPrefix(st.text, "📉 BTC ha bajado de 60.000 €: ahora 59.000 € (−13,4 % hoy)") {
		t.Fatalf("debajo: %q", st.text)
	}
	if st := evalAsset(alert{AssetID: 1, alertData: alertData{Kind: alertDay, Threshold: 5}}, p, series{}, now); !st.cond ||
		st.text != "📉 BTC cae un 13,4 % hoy: ahora 59.000 €" {
		t.Fatalf("día: %q", st.text)
	}
	h := series{Days: []int32{epochDay(dayOf(now).Add(-40 * dayUnit)), epochDay(dayOf(now).Add(-10 * dayUnit))}, Closes: []float64{90000, 70000}}
	st = evalAsset(alert{AssetID: 1, alertData: alertData{Kind: alertDrop, Threshold: 15, Days: 30}}, p, h, now)
	if !st.cond || st.text != "📉 BTC ha caído un 15,7 % desde su máximo de 30 días (70.000 €): ahora 59.000 €" {
		t.Fatalf("caída: %q", st.text)
	}
	// Una acción con la cotización de ayer: sin variación «de hoy».
	stock := &position{Asset: asset{ID: 2, assetData: assetData{Symbol: "ACME", Kind: "accion", Currency: "USD"}},
		Price: 110, Quote: quote{Price: 110, Prev: 100, Currency: "USD", At: now.Add(-24 * time.Hour)}}
	if st := evalAsset(alert{AssetID: 2, alertData: alertData{Kind: alertDay, Threshold: 5}}, stock, series{}, now); st.ok {
		t.Fatal("variación del día con la bolsa cerrada")
	}

	// La cartera entera.
	pf := &portfolio{Held: 2, Value: 50300, DayChange: 600, DayPct: 1.2}
	if st := evalPortfolio(alert{alertData: alertData{Kind: alertAbove, Threshold: 50000}}, pf); !st.cond ||
		st.text != "💰 Tu cartera ha superado 50.000 €: ahora 50.300 € (+1,2 % hoy)" {
		t.Fatalf("cartera: %q", st.text)
	}
	if st := evalPortfolio(alert{alertData: alertData{Kind: alertAbove, Threshold: 50000}}, &portfolio{}); st.ok {
		t.Fatal("cartera vacía")
	}

	// Validación.
	for _, bad := range []alert{{alertData: alertData{Kind: alertDrop, Threshold: 10}}, {AssetID: 1, alertData: alertData{Kind: "otra", Threshold: 1}},
		{AssetID: 1, alertData: alertData{Kind: alertAbove}}, {AssetID: 1, alertData: alertData{Kind: alertDrop, Threshold: 120}},
		{AssetID: 1, alertData: alertData{Kind: alertDrop, Threshold: 10, Days: 1}}} {
		if checkAlert(&bad) == nil {
			t.Errorf("acepta %+v", bad)
		}
	}
	ok := alert{AssetID: 1, alertData: alertData{Kind: alertDrop, Threshold: 10}}
	if checkAlert(&ok) != nil || ok.Days != 30 {
		t.Fatalf("caída por defecto a 30 días: %+v", ok)
	}
	if d := alertDesc(ok, "BTC", "EUR"); d != "BTC: cae un 10 % o más desde el máximo de 30 días (una vez)" {
		t.Fatalf("descripción: %q", d)
	}
}

func TestChart(t *testing.T) {
	today := dayOf(testNow())
	var ps []pt
	for i := 500; i >= 0; i-- {
		ps = append(ps, pt{today.Add(-time.Duration(i) * dayUnit), 100 + float64(i%17)})
	}
	c := buildChart(ps, nil, []markIn{{T: today.Add(-100 * dayUnit), Buy: true, Title: "Compra"}, {T: today.Add(-1000 * dayUnit), Title: "fuera"}}, "x", today)
	if c == nil || len(strings.Fields(c.Line)) > 401 || len(c.Marks) != 1 || !c.Marks[0].Buy || len(c.YTicks) < 3 || len(c.XTicks) != 4 {
		t.Fatalf("gráfica: %d puntos, %d marcas, %d ticks", len(strings.Fields(c.Line)), len(c.Marks), len(c.YTicks))
	}
	if !strings.Contains(c.XTicks[0].Label, "2025") {
		t.Fatalf("en periodos largos, mes y año: %q", c.XTicks[0].Label)
	}
	if buildChart(ps[:1], nil, nil, "x", today) != nil {
		t.Fatal("un solo punto no es una gráfica")
	}
	flat := buildChart([]pt{{today.Add(-dayUnit), 5}, {today, 5}}, nil, nil, "x", today)
	if flat == nil || strings.Contains(flat.Line, "NaN") {
		t.Fatal("línea plana")
	}
}
