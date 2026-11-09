package main

import (
	"testing"
)

// Pruebas sin base de datos: reglas, fechas, importes, resúmenes y gráficas.

func TestRuleMatch(t *testing.T) {
	for _, c := range []struct {
		rule, concept string
		want          bool
	}{
		{"farmacia", "Farmacias del centro", true},
		{"mercadona", "COMPRA TARJ. MERCADONA S.A.", true},
		{"nomina", "Nómina de noviembre", true},
		{"Nómina", "nomina", true},
		{"prime video", "Amazon Prime Video anual", true},
		{"gas natural", "Gas Natural Fenosa", true},
		{"gas natural", "gasolinera natural", false}, // las palabras de delante, enteras
		{"netflix", "PAYPAL *NETFLIX.COM", true},
		{"cine", "medicina", false}, // solo al comienzo de una palabra
		{"taxi", "", false},
		{"", "taxi", false},
		{"  ", "taxi", false},
	} {
		if got := ruleMatch(c.rule, c.concept); got != c.want {
			t.Errorf("ruleMatch(%q, %q) = %v", c.rule, c.concept, got)
		}
	}
}

func TestClassify(t *testing.T) {
	rules := []rule{{CatID: 1, ruleData: ruleData{Text: "amazon"}}, {CatID: 2, ruleData: ruleData{Text: "amazon prime"}}, {CatID: 3, ruleData: ruleData{Text: "farmacia"}}}
	for concept, want := range map[string]int64{"Amazon Prime anual": 2, "Amazon pedido": 1, "Farmacia": 3, "Panadería": 0} {
		if got := classify(rules, concept); got != want {
			t.Errorf("classify(%q) = %d, quiero %d", concept, got, want)
		}
	}
	c := newCats([]category{{ID: 7, catData: catData{Name: "Otros", Key: keyOther}}, {ID: 8, catData: catData{Name: "Ingresos", Key: keyIncome}},
		{ID: 9, catData: catData{Name: "Comida para llevar"}}, {ID: 10, catData: catData{Name: "Comida"}}})
	if c.fallback(kindExpense) != 7 || c.fallback(kindIncome) != 8 {
		t.Fatal("categoría por defecto")
	}
	if x, ok := c.find("comida"); !ok || x.ID != 10 {
		t.Fatalf("buscar «comida»: %+v %v", x, ok)
	}
	if x, ok := c.find("COMIDA PARA"); !ok || x.ID != 9 {
		t.Fatalf("buscar por el comienzo: %+v %v", x, ok)
	}
	if _, ok := c.find("ropa"); ok {
		t.Fatal("encuentra lo que no hay")
	}
	if c.name(99) != "Sin categoría" {
		t.Fatal("nombre de una categoría que no existe")
	}
}

func TestDates(t *testing.T) {
	for _, c := range []struct {
		from     string
		n, anch  int
		want     string
		describe string
	}{
		{"2027-01-31", 1, 31, "2027-02-28", "febrero no tiene 31"},
		{"2027-02-28", 1, 31, "2027-03-31", "vuelve al día de siempre"},
		{"2028-01-31", 1, 31, "2028-02-29", "bisiesto"},
		{"2026-11-15", 12, 0, "2027-11-15", "un año"},
		{"2026-12-10", 2, 10, "2027-02-10", "cambia de año"},
	} {
		if got := isoDay(addMonths(day(c.from), c.n, c.anch)); got != c.want {
			t.Errorf("%s: %s + %d meses = %s, quiero %s", c.describe, c.from, c.n, got, c.want)
		}
	}
	sub := subData{Period: "semanal", Amount: 10}
	if got := sub.after(day("2026-11-09")); !got.Equal(day("2026-11-16")) {
		t.Fatalf("semanal: %v", got)
	}
	for _, c := range []struct {
		period string
		amount float64
		month  float64
	}{{"semanal", 7, 30.44}, {"mensual", 12.99, 12.99}, {"bimestral", 30, 15}, {"trimestral", 30, 10}, {"semestral", 60, 10}, {"anual", 120, 10}} {
		if got := (subData{Period: c.period, Amount: c.amount}).monthly(); !near(got, c.month) {
			t.Errorf("%s de %v: %v al mes", c.period, c.amount, got)
		}
	}
	mo, yr := subsCost([]subscription{{subData: subData{Period: "mensual", Amount: 12.99}}, {subData: subData{Period: "anual", Amount: 120}}})
	if !near(mo, 22.99) || !near(yr, 275.88) {
		t.Fatalf("coste: %v %v", mo, yr)
	}
	today := day("2026-11-09") // lunes
	for d, want := range map[string]string{"2026-11-09": "hoy", "2026-11-10": "mañana", "2026-11-12": "el jueves 12", "2026-11-30": "el 30 nov", "2027-01-05": "el 5 ene 2027"} {
		if got := whenDay(day(d), today); got != want {
			t.Errorf("whenDay(%s) = %q", d, got)
		}
	}
	if inDays(day("2026-11-12"), today) != "en 3 días" || inDays(day("2026-11-07"), today) != "hace 2 días" {
		t.Fatal("inDays")
	}
}

func TestNumbers(t *testing.T) {
	for in, want := range map[string]float64{"12,50": 12.5, "12.5": 12.5, "1.200": 1200, "1.234,56": 1234.56, "1234.56": 1234.56, "7 €": 7, "0,99": 0.99} {
		if got, err := parseMoney(in); err != nil || !near(got, want) {
			t.Errorf("parseMoney(%q) = %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "1,2,3"} {
		if _, err := parseMoney(in); err == nil {
			t.Errorf("parseMoney(%q) sin error", in)
		}
	}
	if euros(1234.5) != "1.234,50 €" || signedEuros(3) != "+3,00 €" || signedEuros(-3) != "−3,00 €" || pct(104.4) != "104 %" {
		t.Fatal("formato de importes")
	}
	if moneyInput(1200) != "1200" || moneyInput(12.5) != "12,5" || moneyInput(0) != "" {
		t.Fatalf("moneyInput: %q %q", moneyInput(1200), moneyInput(12.5))
	}
	if clip("  hola   que  tal ", 7) != "hola qu" {
		t.Fatal("clip")
	}
}

func TestSummarize(t *testing.T) {
	c := newCats([]category{{ID: 1, catData: catData{Name: "Comida", Key: "comida", Budget: 100}}, {ID: 2, catData: catData{Name: "Ocio"}},
		{ID: 3, catData: catData{Name: "Ropa", Budget: 50}}, {ID: 4, catData: catData{Name: "Ingresos", Key: keyIncome}}})
	mov := func(d string, kind string, amount float64, cat int64) movement {
		return movement{Day: day(d), CatID: cat, movData: movData{Kind: kind, Amount: amount, Concept: "x"}}
	}
	movs := []movement{
		mov("2026-11-02", kindExpense, 60, 1), mov("2026-11-05", kindExpense, 50.5, 1), mov("2026-11-06", kindExpense, 20, 2),
		mov("2026-11-01", kindIncome, 1500, 4), mov("2026-11-03", kindExpense, 5, 99), // categoría borrada
		mov("2026-10-10", kindExpense, 80, 1), mov("2026-10-12", kindExpense, 40, 2), mov("2026-10-01", kindIncome, 1400, 4),
		mov("2026-06-30", kindExpense, 10, 2), mov("2026-05-31", kindExpense, 999, 2), // fuera de los 6 meses
	}
	sum := summarize(c, movs, day("2026-11-01"))
	if !near(sum.Expenses, 135.5) || !near(sum.Income, 1500) || !near(sum.Balance, 1364.5) || !near(sum.PrevExpenses, 120) || !near(sum.PrevIncome, 1400) {
		t.Fatalf("totales: %+v", sum)
	}
	if !near(sum.Budget, 150) || !near(sum.BudgetSpent, 110.5) || len(sum.Movs) != 5 {
		t.Fatalf("presupuesto: %v %v %d", sum.Budget, sum.BudgetSpent, len(sum.Movs))
	}
	first := sum.ByCat[0]
	if first.Name != "Comida" || !near(first.Spent, 110.5) || !first.over() || !near(first.Prev, 80) || first.Count != 2 {
		t.Fatalf("comida: %+v", first)
	}
	var ropa, sin catSpend
	for _, cs := range sum.ByCat {
		switch cs.Name {
		case "Ropa":
			ropa = cs
		case "Sin categoría":
			sin = cs
		}
	}
	if ropa.Budget != 50 || ropa.Spent != 0 || ropa.over() || !near(sin.Spent, 5) {
		t.Fatalf("ropa %+v, sin categoría %+v", ropa, sin)
	}
	if len(sum.History) != 6 || !sum.History[0].Month.Equal(day("2026-06-01")) || !near(sum.History[0].Expenses, 10) ||
		!near(sum.History[4].Expenses, 120) || !near(sum.History[5].Income, 1500) {
		t.Fatalf("últimos meses: %+v", sum.History)
	}

	ch := monthsChart(sum.History, day("2026-11-01"), day("2026-11-09"))
	if ch == nil || len(ch.Bars) != 5 || ch.Bars[4].Class != "inc now" || len(ch.XTicks) != 6 || ch.XTicks[5].Label != "nov" || ch.XTicks[0].Label != "jun" {
		t.Fatalf("gráfica: %+v", ch)
	}
	if monthsChart([]monthTotal{{Month: day("2026-11-01")}}, day("2026-11-01"), day("2026-11-09")) != nil {
		t.Fatal("gráfica sin nada")
	}
	for _, c := range []struct {
		spent, budget, max float64
		want               meter
	}{
		{50, 100, 0, meter{W: "50", Mark: "100"}},
		{125, 100, 0, meter{W: "100", Mark: "80", Over: true}},
		{30, 0, 60, meter{W: "50", Plain: true}},
		{0, 0, 0, meter{W: "0", Plain: true}},
	} {
		if got := newMeter(c.spent, c.budget, c.max); got != c.want {
			t.Errorf("newMeter(%v, %v, %v) = %+v", c.spent, c.budget, c.max, got)
		}
	}
}
