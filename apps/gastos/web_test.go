package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sampleCats son las categorías de serie con algún presupuesto, más una
// propia (datos genéricos: el repositorio es público).
func sampleCats() cats {
	var list []category
	for i, d := range defaultCats {
		list = append(list, category{ID: int64(i + 1), catData: catData{Name: d.Name, Key: d.Key}})
	}
	list = append(list, category{ID: 20, catData: catData{Name: "Mascotas", Budget: 40}})
	for i := range list {
		switch list[i].Key {
		case "comida":
			list[i].Budget = 350
		case "ocio":
			list[i].Budget = 120
		case "transporte":
			list[i].Budget = 90
		}
	}
	sortCats(list)
	return newCats(list)
}

// sampleMovs son unos meses de movimientos de ejemplo.
func sampleMovs(c cats, today time.Time) []movement {
	id := int64(0)
	mk := func(d time.Time, kind string, amount float64, concept, key string, extra ...string) movement {
		id++
		m := movement{ID: id, Day: d, CatID: c.byKey[key].ID, movData: movData{Kind: kind, Amount: amount, Concept: concept, Origin: originWeb}}
		if key == "mascotas" {
			m.CatID = 20
		}
		if len(extra) > 0 {
			m.Account = extra[0]
		}
		if len(extra) > 1 {
			m.Note = extra[1]
		}
		if len(extra) > 2 {
			m.Origin = extra[2]
		}
		return m
	}
	d := func(n int) time.Time { return today.Add(-time.Duration(n) * dayUnit) }
	var out []movement
	out = append(out,
		mk(d(0), kindExpense, 23.4, "Gasolinera de ejemplo", "transporte", "Tarjeta"),
		mk(d(0), kindExpense, 3.2, "Café", "ocio", "Efectivo", "", originAssistant),
		mk(d(1), kindExpense, 87.65, "Supermercado del barrio", "comida", "Tarjeta", "compra de la semana"),
		mk(d(2), kindExpense, 12.99, "Plataforma de ejemplo", keySubs, "Tarjeta", "", originSub),
		mk(d(3), kindExpense, 64, "Cena con amigos en un restaurante con un nombre muy largo para ver cómo se parte", "ocio", "Tarjeta"),
		mk(d(4), kindExpense, 210, "Supermercado grande", "comida", "Tarjeta"),
		mk(d(5), kindExpense, 42, "Pienso y veterinario", "mascotas"),
		mk(d(6), kindExpense, 18, "Regalo", keyOther),
		mk(d(8), kindIncome, 1650, "Nómina", keyIncome, "Cuenta de ejemplo"),
		mk(d(8), kindIncome, 25, "Devolución de una compra", "ropa"),
	)
	// Meses anteriores.
	for i := 1; i <= 5; i++ {
		m := monthOf(today).AddDate(0, -i, 0)
		out = append(out,
			mk(m.AddDate(0, 0, 2), kindIncome, 1650, "Nómina", keyIncome),
			mk(m.AddDate(0, 0, 4), kindExpense, 300+float64(i)*20, "Supermercado", "comida"),
			mk(m.AddDate(0, 0, 9), kindExpense, 650, "Alquiler", "vivienda"),
			mk(m.AddDate(0, 0, 14), kindExpense, 80-float64(i)*5, "Cine y cena", "ocio"),
		)
	}
	// Del más reciente al más antiguo, como los da el store.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	sortMovs(out)
	return out
}

func sortMovs(list []movement) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && (list[j].Day.After(list[j-1].Day) || (list[j].Day.Equal(list[j-1].Day) && list[j].ID > list[j-1].ID)); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func sampleSubs(c cats) []subscription {
	sub := func(id int64, name string, amount float64, period string, next string, notice int, key string) subscription {
		return subscription{ID: id, CatID: c.byKey[key].ID, subData: subData{Name: name, Amount: amount, Period: period, Next: day(next), Notice: notice}}
	}
	return []subscription{
		sub(1, "Gimnasio de ejemplo", 35, "mensual", "2026-11-05", 1, keySubs),
		sub(2, "Plataforma de ejemplo", 12.99, "mensual", "2026-11-09", 3, keySubs),
		sub(3, "Música de ejemplo", 10.99, "mensual", "2026-11-12", 0, keySubs),
		sub(4, "Seguro del coche de ejemplo", 420, "anual", "2027-03-01", 15, "transporte"),
		sub(5, "Clases semanales", 15, "semanal", "2026-11-16", -1, "educacion"),
	}
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con GASTOS_PREVIEW_DIR escribe las páginas y lo
// estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	today := dayOf(testNow())
	month := monthOf(today)
	c := sampleCats()
	movs := sampleMovs(c, today)
	subs := sampleSubs(c)
	sum := summarize(c, movs, month)
	if sum.Expenses <= 0 || len(sum.Movs) != 10 {
		t.Fatalf("datos de ejemplo: %+v", sum)
	}

	list := func(f *movForm, msg, cat string) pageData {
		d := pageData{Name: "Ana", Tab: "movimientos", Month: navOf(month, today), Stats: statsOf(sum), Error: msg, CatFilter: cat}
		rows := sum.Movs
		if cat != "" {
			var mine []movement
			for _, m := range rows {
				if idStr(m.CatID) == cat {
					mine = append(mine, m)
				}
			}
			rows = mine
			d.Filtered = "Comida: −297,65 € en 2 movimientos"
		}
		d.Days = movRows(c, rows, today)
		sel, _ := strconv.ParseInt(cat, 10, 64)
		d.CatOpts = catOptions(c, sel, false)
		if f == nil {
			f = &movForm{Kind: kindExpense, Day: isoDay(today)}
		}
		d.MovForm, d.FormCats, d.Accounts = f, catOptions(c, f.CatID, true), accounts(sum.Movs, subs)
		return d
	}
	emptySum := summarize(c, nil, month)
	emptyList := pageData{Name: "Ana", Tab: "movimientos", Month: navOf(month, today), Stats: statsOf(emptySum),
		MovForm: &movForm{Kind: kindExpense, Day: isoDay(today), Open: true}, FormCats: catOptions(c, 0, true), CatOpts: catOptions(c, 0, false)}
	past := monthOf(today).AddDate(0, -2, 0)
	pastSum := summarize(c, movs, past)
	pastList := pageData{Name: "Ana", Tab: "movimientos", Title: monthLabel(past), Month: navOf(past, today), Stats: statsOf(pastSum),
		Days: movRows(c, pastSum.Movs, today), MovForm: &movForm{Kind: kindExpense, Day: isoDay(today)}, FormCats: catOptions(c, 0, true), CatOpts: catOptions(c, 0, false)}

	movPage := func(m movement, confirm bool, msg string) pageData {
		f := &movForm{ID: m.ID, Kind: m.Kind, Day: isoDay(m.Day), Amount: moneyInput(m.Amount), Concept: m.Concept, CatID: m.CatID, Account: m.Account,
			Note: m.Note, Origin: m.Origin, CatName: c.name(m.CatID)}
		return pageData{Name: "Ana", Tab: "movimientos", Title: "Editar movimiento", MovForm: f, FormCats: catOptions(c, f.CatID, true),
			Accounts: accounts(movs, nil), Confirm: confirm, Month: navOf(monthOf(m.Day), today), Error: msg}
	}
	budget := func(s *summary, m time.Time) pageData {
		return pageData{Name: "Ana", Tab: "presupuesto", Title: "Presupuesto", Month: navOf(m, today), Stats: statsOf(s), Budget: budgetOf(s),
			Chart: monthsChart(s.History, m, today)}
	}
	noBudgetCats := newCats(nil)
	for _, x := range c.list {
		x.Budget = 0
		noBudgetCats.list = append(noBudgetCats.list, x)
	}
	noBudgetCats = newCats(noBudgetCats.list)
	subsPage := func(list []subscription, f *subForm, msg string) pageData {
		if f == nil {
			f = &subForm{Period: "mensual", Next: isoDay(today), Notice: "3", CatID: c.byKey[keySubs].ID}
		}
		mo, yr := subsCost(list)
		return pageData{Name: "Ana", Tab: "suscripciones", Title: "Suscripciones", Subs: subRows(c, list, today), SubsMonth: euros(mo), SubsYear: euros(yr),
			SubForm: f, PeriodOpts: periodOptions(f.Period), CatOpts: catOptions(c, f.CatID, false), Accounts: accounts(nil, list), Error: msg}
	}
	subPage := func(x subscription, confirm bool) pageData {
		f := &subForm{ID: x.ID, Name: x.Name, Amount: moneyInput(x.Amount), Period: x.Period, Next: isoDay(x.Next), CatID: x.CatID,
			Monthly: euros(x.monthly()), After: shortDate(x.after(x.Next), today), Notice: "3"}
		return pageData{Name: "Ana", Tab: "suscripciones", Title: "Editar suscripción", SubForm: f, PeriodOpts: periodOptions(f.Period),
			CatOpts: catOptions(c, f.CatID, false), Confirm: confirm}
	}
	rules := []rule{{ID: 1, CatID: c.byKey["comida"].ID, ruleData: ruleData{Text: "mercadona"}}, {ID: 2, CatID: c.byKey["comida"].ID, ruleData: ruleData{Text: "supermercado"}},
		{ID: 3, CatID: c.byKey[keySubs].ID, ruleData: ruleData{Text: "netflix"}}, {ID: 4, CatID: 20, ruleData: ruleData{Text: "veterinario"}}}
	catsPage := func(msg string, cf *catForm) pageData {
		d := pageData{Name: "Ana", Tab: "categorias", Title: "Categorías", Error: msg, NRules: len(rules), Month: navOf(month, today)}
		spent := map[int64]float64{}
		for _, cs := range sum.ByCat {
			spent[cs.ID] = cs.Spent
		}
		for _, x := range c.list {
			row := catRow{ID: x.ID, Name: x.Name, Default: x.Key != "", BudgetInput: moneyInput(x.Budget)}
			if spent[x.ID] > 0 {
				row.SpentNow = euros(spent[x.ID])
			}
			var g ruleGroup
			for _, r := range rules {
				if r.CatID == x.ID {
					row.Rules++
					g.CatID, g.Name = x.ID, x.Name
					g.Rules = append(g.Rules, ruleRow{ID: r.ID, Text: r.Text})
				}
			}
			d.Cats = append(d.Cats, row)
			if g.CatID != 0 {
				d.RuleCats = append(d.RuleCats, g)
			}
		}
		if cf == nil {
			cf = &catForm{}
		}
		d.CatForm, d.RuleForm, d.CatOpts = cf, ruleForm{CatID: c.byKey[keyOther].ID}, catOptions(c, c.byKey[keyOther].ID, false)
		return d
	}
	catPage := func(id int64, confirm bool) pageData {
		x := c.byID[id]
		cf := &catForm{ID: id, Name: x.Name, Budget: moneyInput(x.Budget), Default: x.Key != "", Movs: 7}
		for _, r := range rules {
			if r.CatID == id {
				cf.Rules = append(cf.Rules, ruleRow{ID: r.ID, Text: r.Text})
			}
		}
		return pageData{Name: "Ana", Tab: "categorias", Title: x.Name, CatForm: cf, Confirm: confirm, RuleForm: ruleForm{CatID: id}}
	}
	var gym, food movement
	for _, m := range movs {
		if m.Origin == originAssistant && gym.ID == 0 {
			gym = m
		}
		if m.Note != "" && food.ID == 0 {
			food = m
		}
	}
	comidaID := idStr(c.byKey["comida"].ID)

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"movimientos", "movimientos.html", list(nil, "", "")},
		{"movimientos-filtro", "movimientos.html", list(nil, "", comidaID)},
		{"movimientos-error", "movimientos.html", list(&movForm{Kind: kindIncome, Day: isoDay(today), Amount: "12,5x", Concept: "Algo", Account: "Bizum", Open: true}, "El importe no es un número.", "")},
		{"movimientos-vacio", "movimientos.html", emptyList},
		{"movimientos-pasado", "movimientos.html", pastList},
		{"movimiento", "movimiento.html", movPage(food, false, "")},
		{"movimiento-talia", "movimiento.html", movPage(gym, true, "")},
		{"presupuesto", "presupuesto.html", budget(sum, month)},
		{"presupuesto-sin", "presupuesto.html", budget(summarize(noBudgetCats, movs, month), month)},
		{"presupuesto-vacio", "presupuesto.html", budget(summarize(noBudgetCats, nil, past), past)},
		{"suscripciones", "suscripciones.html", subsPage(subs, nil, "")},
		{"suscripciones-vacio", "suscripciones.html", subsPage(nil, nil, "")},
		{"suscripciones-error", "suscripciones.html", subsPage(subs, &subForm{Name: "Algo", Period: "anual", Next: "", Notice: ""}, "Falta la fecha del próximo cobro.")},
		{"suscripcion", "suscripcion.html", subPage(subs[3], false)},
		{"suscripcion-borrar", "suscripcion.html", subPage(subs[0], true)},
		{"categorias", "categorias.html", catsPage("", nil)},
		{"categorias-error", "categorias.html", catsPage("Ya tienes una categoría con ese nombre.", &catForm{Name: "Comida", Budget: "100"})},
		{"categoria", "categoria.html", catPage(c.byKey["comida"].ID, false)},
		{"categoria-borrar", "categoria.html", catPage(20, true)},
	}
	wants := map[string][]string{
		"movimientos": {"Noviembre 2026", "Gastos", "Balance", "Hoy", "Ayer", "Gasolinera de ejemplo", "−23,40 €", "&#43;1.650,00 €", "Talia", "suscripción",
			`href="/gastos/movimiento/`, `action="/gastos/movimiento/nuevo"`, `name="mes" value="2026-11"`, "Automática (por las reglas)", `<option value="Bizum">`,
			`href="?mes=2026-10"`, `class="mnav off"`, "compra de la semana", "en gastos frente a octubre"},
		"movimientos-filtro":  {"Comida: −297,65 € en 2 movimientos", `<option value="` + comidaID + `" selected>`},
		"movimientos-error":   {"El importe no es un número.", `value="12,5x"`, `value="ingreso" checked`, " open"},
		"movimientos-vacio":   {"Sin movimientos en Noviembre 2026", " open", `href="/gastos/categorias"`},
		"movimientos-pasado":  {"Septiembre 2026", `href="?mes=2026-10"`, "Alquiler"},
		"movimiento":          {"Editar movimiento", "compra de la semana", `value="87,65"`, "¿Está mal o sobra?", "Noviembre 2026</a>"},
		"movimiento-talia":    {"Apuntado por Talia", "¿Borrar este movimiento?", `name="confirmar" value="1"`},
		"presupuesto":         {"de 600,00 €", "quedan", "Por categoría", "Comida", "pasado", `class="meter over"`, `class="limit"`, "Mes a mes", `class="b exp now"`, "Frente a octubre", "quedan 21 días del mes"},
		"presupuesto-sin":     {"Aún no tienes presupuestos", `class="meter plain"`, "sin presupuesto"},
		"presupuesto-vacio":   {"Nada gastado en Septiembre 2026"},
		"suscripciones":       {"Al mes, unos", "Al año, unos", "Gimnasio de ejemplo", "Sin registrar", "Registrar cobro", "<strong>hoy</strong>", "sin aviso", "aviso el mismo día", "65,22 €/mes", `action="/gastos/suscripcion/nueva"`},
		"suscripciones-vacio": {"Sin suscripciones", " open"},
		"suscripciones-error": {"Falta la fecha del próximo cobro", `<option value="anual" selected>`, `value="Algo"`},
		"suscripcion":         {"Seguro del coche de ejemplo", "Unos 35,00 € al mes", "Tras el próximo, el 1 mar 2028", `action="/gastos/suscripcion/4/cobro"`, "¿Te has dado de baja?"},
		"suscripcion-borrar":  {"¿Borrar esta suscripción?", `action="/gastos/suscripcion/1/borrar"`},
		"categorias":          {"Presupuesto al mes", `name="p` + comidaID + `" value="350"`, "Mascotas", "Reglas", "mercadona", `action="/gastos/regla/1/borrar"`, `action="/gastos/reglas/nueva"`, "este mes"},
		"categorias-error":    {"Ya tienes una categoría con ese nombre.", `value="Comida"`},
		"categoria":           {"Comida", "de serie", "mercadona", `name="desde" value="categoria"`, `name="categoria" value="` + comidaID + `"`, "7 movimientos"},
		"categoria-borrar":    {"¿Borrar Mascotas?", "veterinario", `action="/gastos/categoria/20/borrar"`},
	}
	nots := map[string][]string{
		"movimientos":        {"Sin movimientos"},
		"movimientos-filtro": {"Gasolinera"},
		"movimiento":         {"¿Borrar este movimiento?", "Apuntado por Talia"},
		"categoria":          {"¿Borrar"},
	}

	dir := os.Getenv("GASTOS_PREVIEW_DIR")
	base := "/gastos"
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
