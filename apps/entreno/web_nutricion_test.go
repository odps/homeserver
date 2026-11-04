package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// riceFood es el arroz de la tabla básica, sea cual sea la tabla.
func riceFood(t *testing.T) genericFood {
	t.Helper()
	for _, m := range matchFoods("arroz", nil, 60, 5) {
		if m.Gen != nil {
			return *m.Gen
		}
	}
	t.Fatal("la tabla básica no tiene arroz")
	return genericFood{}
}

// nutriRenderCases son las páginas de la comida para TestWebRender (sin
// base de datos), con lo que tiene que salir en cada una.
func nutriRenderCases(t *testing.T, w *web) ([]renderCase, map[string][]string) {
	now := w.now()
	today := dayOf(now)
	day := func(d int) time.Time { return time.Date(2026, 11, 3+d, 0, 0, 0, 0, time.UTC) }
	rice := riceFood(t)
	own := []food{
		{ID: 7, Name: "Yogur de prueba", Base: baseUd, Unit: "yogur", Per: macros{Kcal: 120, Prot: 10, Carb: 6, Fat: 4}, Portion: 1},
		{ID: 8, Name: "Desayuno de prueba", Base: baseUd, Unit: "ración", Portion: 1, Per: macros{Kcal: 450, Prot: 30},
			Items: []mealItem{{Name: "Avena", Amount: 60, Unit: "g"}, {Name: "Leche", Amount: 250, Unit: "ml"}}},
		{ID: 9, Name: "Pan de prueba", Base: baseG, Per: macros{Kcal: 250, Prot: 9}, Portion: 40, Origin: "off:8400000000001", Note: "Nota de prueba"},
	}
	at := func(d, h int) time.Time { return time.Date(2026, 11, 3+d, h, 0, 0, 0, madrid) }
	entry := func(id int64, d, h int, meal string, e mealEntry, err error) mealEntry {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		e.ID, e.Day, e.At, e.Meal = id, day(d), at(d, h), meal
		e.macros = e.macros.rounded()
		return e
	}
	var week []mealEntry
	for d := -5; d <= 0; d++ {
		r, err := genericEntry(rice, foodReq{Amount: 200, Unit: rice.base()})
		week = append(week, entry(int64(100+d), d, 14, "comida", r, err))
		y, err := ownEntry(own[0], foodReq{Amount: 2})
		week = append(week, entry(int64(200+d), d, 8, "desayuno", y, err))
	}
	week = append(week, entry(300, 0, 21, "cena", mealEntry{Name: "Pizza de prueba", Guess: true, macros: macros{Kcal: 2200, Prot: 70, Carb: 200, Fat: 70}}, nil))
	g := goals{Kcal: 2400, Prot: 150}

	full := w.nutriPageOf(today, week, week, own, g, logForm{})
	if !full.IsToday || full.Next != "" || full.Prev != "2026-11-02" || full.Form.Meal != "cena" || len(full.Groups) != 3 || full.Groups[0].Key != "desayuno" ||
		!full.Totals.Guess || full.Repeat != "" || full.Week == nil || full.Week.Days != 6 || len(full.Week.Chart.Bars) != 7 || full.Week.Chart.GoalY == "" {
		t.Fatalf("día: %+v", full)
	}
	if m := full.Totals.Meters[0]; m.Goal != "2.400 kcal" || !m.Over || m.Pct != "100" || !strings.HasPrefix(m.Left, "te pasas") {
		t.Fatalf("kcal: %+v", m)
	}
	if len(full.Frequents) != 2 || full.Frequents[0].Item == "" {
		t.Fatalf("frecuentes: %+v", full.Frequents)
	}

	// Hoy vacío, ayer no: «Repetir ayer». Sin objetivos y con un error.
	var past []mealEntry
	for _, e := range week {
		if e.Day.Before(day(0)) {
			past = append(past, e)
		}
	}
	empty := w.nutriPageOf(today, past, past, nil, goals{}, logForm{Text: "200 g de arroz, 100 g de blorpitos", Meal: "comida", Missing: []string{"blorpitos"}})
	if empty.Repeat != "2026-11-02" || empty.Totals.HasGoals || len(empty.Frequents) != 1 || empty.Form.Meal != "comida" {
		t.Fatalf("hoy vacío: %+v", empty)
	}
	old := w.nutriPageOf(dayOf(day(-2)), week, week, own, goals{Prot: 150}, logForm{})
	row := entryRowOf(week[0])
	row.Date = w.shortDate(week[0].Day)
	old.Confirm = &row
	if old.IsToday || old.Next != "2026-11-02" || old.Form.Meal != "comida" || old.Repeat != "" {
		t.Fatalf("otro día: %+v", old)
	}

	search := &foodSearch{Query: "arroz", ISO: "2026-11-03", Meal: "cena", MealLabel: "cena", DayText: "hoy", Slots: mealSlots, Searched: true,
		Results: foodResults(append(matchFoods("yogur de prueba", own, 60, 1), matchFoods("arroz", own, 30, 3)...), own)}
	if len(search.Results) < 2 || !search.Results[0].Own || search.Results[0].Unit != baseUd || search.Results[1].Own {
		t.Fatalf("búsqueda: %+v", search.Results)
	}
	prods := []offProduct{
		{Code: "8400000000001", Name: "Pan de prueba", Brand: "Marca", Per100: macros{Kcal: 250, Prot: 9, Carb: 48, Fat: 2}, Serving: 40},
		{Code: "8400000000002", Name: "Bebida de prueba", Per100: macros{Kcal: 45, Prot: 3.2}, Liquid: true},
	}
	off := &offPage{Query: "pan", ISO: "2026-11-02", Meal: "desayuno", DayText: "el lun 2 nov", Slots: mealSlots, Searched: true}
	for i, p := range prods {
		r := offRowOf(p)
		if i == 0 {
			r.SavedID = 9
		}
		off.Results = append(off.Results, r)
	}
	if off.Results[0].Amount != "40" || off.Results[1].Unit != baseML || off.Results[1].Per != "por 100 ml" {
		t.Fatalf("off: %+v", off.Results)
	}
	foods := &foodsPage{}
	for _, f := range own {
		foods.Foods = append(foods.Foods, foodRowOf(f))
	}
	combo := foodFormOf(own[1])
	combo.Confirm = true
	logs := []bodyLog{{Day: day(0), Kg: kg(78)}, {Day: day(-1), Kg: kg(78)}}
	hint := proteinHintText(trend(logs, now))
	if hint != "Con 78 kg, lo habitual para quien entrena son 125–172 g de proteína al día." {
		t.Fatalf("pista: %q", hint)
	}
	card := foodCardOf(week, goals{Kcal: 2400, Prot: 150}, today)
	if card == nil || card.KcalGoal != "2.400" || foodCardOf(nil, goals{}, today) != nil || foodCardOf(nil, goals{Prot: 150}, today) == nil {
		t.Fatalf("tarjeta: %+v", card)
	}

	cases := []renderCase{
		{"nutricion", "nutricion.html", pageData{Tab: "comida", Nutri: full, Flash: "Apuntado."}},
		{"nutricion-vacio", "nutricion.html", pageData{Tab: "comida", Nutri: empty, Error: "No encuentro blorpitos."}},
		{"nutricion-otro-dia", "nutricion.html", pageData{Tab: "comida", Nutri: old}},
		{"nutricion-buscar", "nutricion_buscar.html", pageData{Tab: "comida", Title: "Buscar alimento", Search: search}},
		{"nutricion-buscar-nada", "nutricion_buscar.html", pageData{Tab: "comida", Title: "Buscar alimento", Search: &foodSearch{Query: "blorpitos", ISO: "2026-11-03", Meal: "cena", MealLabel: "cena", DayText: "hoy", Slots: mealSlots, Searched: true}}},
		{"nutricion-off", "nutricion_off.html", pageData{Tab: "comida", Title: "Open Food Facts", OFF: off}},
		{"nutricion-off-error", "nutricion_off.html", pageData{Tab: "comida", Title: "Open Food Facts", Error: offMsg(errOFFBusy), OFF: &offPage{Query: "pan", ISO: "2026-11-03", Meal: "cena", DayText: "hoy", Slots: mealSlots, Searched: true}}},
		{"nutricion-alimentos", "nutricion_alimentos.html", pageData{Tab: "comida", Title: "Tus alimentos", Foods: foods}},
		{"nutricion-alimentos-vacio", "nutricion_alimentos.html", pageData{Tab: "comida", Title: "Tus alimentos", Foods: &foodsPage{}}},
		{"nutricion-alimento", "nutricion_alimento.html", pageData{Tab: "comida", Title: own[1].Name, Food: combo}},
		{"nutricion-alimento-nuevo", "nutricion_alimento.html", pageData{Tab: "comida", Title: "Nuevo alimento", Food: foodFormOf(food{Name: "blorpitos"}), Error: "Faltan las kcal o la proteína."}},
		{"nutricion-objetivos", "nutricion_objetivos.html", pageData{Tab: "comida", Title: "Objetivos", Goals: &goalsForm{Kcal: "2400", Prot: "150", Hint: hint, Set: true}}},
		{"nutricion-objetivos-sin-peso", "nutricion_objetivos.html", pageData{Tab: "comida", Title: "Objetivos", Goals: &goalsForm{}}},
		{"inicio-comida", "inicio.html", pageData{Tab: "inicio", Hoy: &todayCard{None: true}, FoodCard: card}},
	}
	wants := map[string][]string{
		"nutricion": {"<title>Comida · Entreno</title>", "Hoy, martes, 3 de noviembre", "lun 2 nov", "2.400 kcal", "te pasas", `class="meter-row over"`, `preserveAspectRatio="none"`,
			`width="100"`, "faltan", "Hidratos", rice.Name, "2 yogures Yogur de prueba", `<span class="tag">aprox.</span>`, "Pizza de prueba", "/nutricion?dia=2026-11-03&amp;borrar=300",
			`formaction="/entreno/nutricion/rapido"`, `name="item" value="a:7:2:yogures"`, `value="g:` + rice.Key + `:200:` + rice.base() + `"`, `value="cena" selected`,
			"Guardar como habitual", `action="/entreno/nutricion/guardar-comida"`, "Últimos 7 días", "6 de 7", `class="goal"`, "objetivo, 2.400", `class="bar now"`,
			"Proteína cumplida", "Parte de lo apuntado son valores aproximados", `placeholder="200 g de arroz, 2 huevos"`},
		"nutricion-vacio": {"Nada apuntado hoy", "Repetir ayer", `name="desde" value="2026-11-02"`, "Sin objetivos", "/entreno/nutricion/objetivos", "No encuentro:",
			"/entreno/nutricion/buscar?q=blorpitos&amp;dia=2026-11-03&amp;comida=comida", "/entreno/nutricion/alimento/nuevo?nombre=blorpitos", "200 g de arroz, 100 g de blorpitos</textarea>",
			`value="comida" selected`},
		"nutricion-otro-dia": {"domingo, 1 de noviembre", "¿Borrar esto?", `action="/entreno/nutricion/apunte/95/borrar"`, "Copiar a hoy", `name="desde" value="2026-11-01"`,
			">Hoy</a>", "/entreno/nutricion?dia=2026-11-02", `value="comida" selected`},
		"nutricion-buscar": {"<title>Buscar alimento · Entreno</title>", "Yogur de prueba", `<span class="tag accent">habitual</span>`, rice.Name, "por 100 " + rice.base(),
			`name="generico" value="` + rice.Key + `"`, `name="alimento" value="7"`, `action="/entreno/nutricion/anadir"`, `action="/entreno/nutricion/alimentos/generico"`,
			"Se consulta Open Food Facts (datos ODbL); solo se envía lo que buscas.", `name="codigo"`, `action="/entreno/nutricion/off"`, "Se apunta hoy, en cena", `value="cena" selected`},
		"nutricion-buscar-nada": {"No encuentro «blorpitos»", "/entreno/nutricion/alimento/nuevo?nombre=blorpitos"},
		"nutricion-off": {"Pan de prueba (Marca)", "por 100 g: 250 kcal · 9 g prot", "ración: 40 g", `name="cantidad" value="40"`, "Bebida de prueba", "por 100 ml", "Ya está en tus alimentos",
			`action="/entreno/nutricion/off/guardar"`, `name="codigo" value="8400000000002"`, `action="/entreno/nutricion/off/anadir"`, "datos ODbL", `value="desayuno" selected`, "el lun 2 nov"},
		"nutricion-off-error":       {"espera unos segundos", `class="toast error"`},
		"nutricion-alimentos":       {"Yogur de prueba", "por 1 yogur: 120 kcal · 10 g prot", "Ración: 1 yogur", "Avena", "Ración: 40 g", "Open Food Facts", "/entreno/nutricion/alimento/9"},
		"nutricion-alimentos-vacio": {"Aún no tienes alimentos guardados", "/entreno/nutricion/alimento/nuevo"},
		"nutricion-alimento": {"Desayuno de prueba", `action="/entreno/nutricion/alimento/8"`, `value="ud" selected`, `name="unidad" value="ración"`, `name="kcal" value="450"`,
			"60 g Avena", "¿Borrar «Desayuno de prueba»?", `action="/entreno/nutricion/alimento/8/borrar"`},
		"nutricion-alimento-nuevo": {"Nuevo alimento", `action="/entreno/nutricion/alimento/nuevo"`, `name="nombre" value="blorpitos"`, `value="g" selected`, "Faltan las kcal"},
		"nutricion-objetivos": {"<title>Objetivos · Entreno</title>", `name="kcal" value="2400"`, "Con 78 kg, lo habitual para quien entrena son 125–172 g de proteína al día.",
			"Las kcal dependen de tu gasto; si no lo sabes, apunta unas semanas y mira cómo cambia tu peso."},
		"nutricion-objetivos-sin-peso": {"Con tu peso apuntado", "/entreno/cuerpo"},
		"inicio-comida":                {"Comida de hoy", "2.400", "150", "g proteína", `href="/entreno/nutricion"`},
	}
	return cases, wants
}

func TestThousands(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 999: "999", 1450: "1.450", 1449.6: "1.450", 12345: "12.345", 1234567: "1.234.567", -1500: "-1.500"} {
		if got := thousands(v); got != want {
			t.Errorf("%v: %q", v, got)
		}
	}
	if m := meterOf("Proteína", 151, 150, false); !m.Done || m.Left != "objetivo cumplido" {
		t.Errorf("proteína cumplida: %+v", m)
	}
	if m := meterOf("Kcal", 1450, 2400, true); m.Over || m.Left != "quedan 950 kcal" || m.Pct != "60.4" {
		t.Errorf("kcal: %+v", m)
	}
	if m := meterOf("Grasa", 60, 0, false); m.Goal != "" || m.Pct != "" || m.Value != "60 g" {
		t.Errorf("sin objetivo: %+v", m)
	}
}

var (
	reFreq    = regexp.MustCompile(`name="item" value="([^"]+)"`)
	reMealDel = regexp.MustCompile(`borrar=(\d+)`)
	reFoodID  = regexp.MustCompile(`^/nutricion/alimento/(\d+)\?ok=`)
)

func (e *webEnv) mealsOn(user string, d int) []mealEntry {
	e.t.Helper()
	day := time.Date(2026, 11, 3+d, 0, 0, 0, 0, madrid)
	list, err := e.svc.store.meals(e.t.Context(), user, day, day)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

// TestWebNutrition: apuntar con texto (y lo que no se encuentra), con un
// toque, desde la búsqueda; borrar, guardar como habitual, copiar, los
// objetivos, los habituales y que nadie más vea ni toque nada.
func TestWebNutrition(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	rice := riceFood(t)

	code, body := e.get("/nutricion", "ana")
	if code != 200 || !strings.Contains(body, "Nada apuntado hoy") || !strings.Contains(body, "Sin objetivos") || !strings.Contains(body, `href="/nutricion" aria-current="page"`) ||
		!strings.Contains(body, `value="cena" selected`) || !strings.Contains(body, "<title>Comida · Entreno</title>") {
		t.Fatalf("vacío: %d\n%s", code, body)
	}
	noScript(t, "/nutricion", body)
	for _, p := range []string{"/nutricion?dia=2026-11-04", "/nutricion?dia=nope", "/nutricion?borrar=abc", "/nutricion?borrar=999999"} {
		if code, _ = e.get(p, "ana"); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}

	// Apuntar con texto.
	form := url.Values{"dia": {"2026-11-03"}, "comida": {"comida"}, "texto": {"200 g de arroz"}}
	if code, loc, _ := e.post("/nutricion/apuntar", "ana", form); code != http.StatusSeeOther || loc != "/nutricion?ok=apuntado" {
		t.Fatalf("apuntar: %d %q", code, loc)
	}
	list := e.mealsOn("ana", 0)
	if len(list) != 1 || list[0].Generic != rice.Key || list[0].Amount != 200 || list[0].Meal != "comida" || list[0].Source != "web" {
		t.Fatalf("apuntado: %+v", list)
	}
	noPlain(t, e.svc.store.db, "food_logs", rice.Name, `"comida"`)

	// Lo que no se encuentra: no se apunta nada y se ofrece buscarlo.
	form.Set("texto", "200 g de arroz, 100 g de blorpitos")
	code, _, body = e.post("/nutricion/apuntar", "ana", form)
	if code != 200 || !strings.Contains(body, "No encuentro blorpitos") || !strings.Contains(body, "/nutricion/buscar?q=blorpitos&amp;dia=2026-11-03&amp;comida=comida") ||
		!strings.Contains(body, "/nutricion/alimento/nuevo?nombre=blorpitos") || !strings.Contains(body, "100 g de blorpitos</textarea>") || !strings.Contains(body, `class="toast error"`) {
		t.Fatalf("no encontrado: %d\n%s", code, body)
	}
	for _, c := range []struct {
		field, value, want string
	}{
		{"texto", "", "Escribe qué has comido"},
		{"texto", strings.Repeat("a", maxFoodText+1), "demasiado largo"},
		{"comida", "brunch", "Elige la comida."},
	} {
		f := url.Values{"dia": {"2026-11-03"}, "comida": {"comida"}, "texto": {"200 g de arroz"}}
		f.Set(c.field, c.value)
		if code, _, body := e.post("/nutricion/apuntar", "ana", f); code != 200 || !strings.Contains(body, c.want) {
			t.Errorf("%s=%.20q: %d, falta %q", c.field, c.value, code, c.want)
		}
	}
	if code, _, _ := e.post("/nutricion/apuntar", "ana", url.Values{"dia": {"2026-11-05"}, "comida": {"comida"}, "texto": {"200 g de arroz"}}); code != 404 {
		t.Errorf("día futuro: %d", code)
	}
	if list = e.mealsOn("ana", 0); len(list) != 1 {
		t.Fatalf("se ha apuntado algo con errores: %d", len(list))
	}

	// Frecuentes: con un toque, en la comida elegida; los valores se calculan
	// aquí.
	_, body = e.get("/nutricion?ok=apuntado", "ana")
	m := reFreq.FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, "Apuntado.") || !strings.Contains(body, rice.Name) {
		t.Fatalf("frecuentes:\n%s", body)
	}
	item := m[1]
	if want := "g:" + rice.Key + ":200:" + rice.base(); item != want {
		t.Fatalf("frecuente = %q, quiero %q", item, want)
	}
	quick := url.Values{"dia": {"2026-11-03"}, "comida": {"cena"}, "item": {item}, "kcal": {"1"}}
	if code, loc, _ := e.post("/nutricion/rapido", "ana", quick); code != http.StatusSeeOther || loc != "/nutricion?ok=apuntado" {
		t.Fatalf("rápido: %d %q", code, loc)
	}
	if list = e.mealsOn("ana", 0); len(list) != 2 || list[1].Meal != "cena" || list[1].Kcal != list[0].Kcal || list[1].Kcal <= 1 {
		t.Fatalf("rápido apuntado: %+v", list)
	}
	for _, c := range []struct {
		item string
		code int
	}{
		{"g:" + rice.Key + ":0:g", 200}, {"g:" + rice.Key + ":abc:g", 200}, {"g:no-existe:100:g", 404}, {"a:0:1:ud", 404}, {"x:1:1:g", 400}, {"g:" + rice.Key, 400},
		{"g:" + rice.Key + ":2:platos-raros", 200},
	} {
		if code, _, _ := e.post("/nutricion/rapido", "ana", url.Values{"dia": {"2026-11-03"}, "comida": {"cena"}, "item": {c.item}}); code != c.code {
			t.Errorf("rápido %q: %d", c.item, code)
		}
	}

	// Desde la búsqueda: g de un alimento de la tabla, otro día.
	code, body = e.get("/nutricion/buscar?q=arroz&dia=2026-11-02&comida=merienda", "ana")
	if code != 200 || !strings.Contains(body, rice.Name) || !strings.Contains(body, `value="merienda" selected`) || !strings.Contains(body, "Se apunta el lun 2 nov, en merienda") {
		t.Fatalf("buscar: %d\n%s", code, body)
	}
	noScript(t, "/nutricion/buscar", body)
	add := url.Values{"dia": {"2026-11-02"}, "comida": {"merienda"}, "generico": {rice.Key}, "cantidad": {"150"}, "unidad": {rice.base()}, "q": {"arroz"}}
	if code, loc, _ := e.post("/nutricion/anadir", "ana", add); code != http.StatusSeeOther || loc != "/nutricion?dia=2026-11-02&ok=apuntado" {
		t.Fatalf("añadir: %d %q", code, loc)
	}
	if l := e.mealsOn("ana", -1); len(l) != 1 || l[0].Amount != 150 || l[0].Meal != "merienda" {
		t.Fatalf("añadido: %+v", l)
	}
	add.Set("unidad", "kg")
	if code, _, body := e.post("/nutricion/anadir", "ana", add); code != 200 || !strings.Contains(body, "Elige la unidad.") || !strings.Contains(body, rice.Name) {
		t.Errorf("unidad mala: %d", code)
	}
	add.Set("unidad", rice.base())
	add.Set("cantidad", "9000")
	if code, _, body := e.post("/nutricion/anadir", "ana", add); code != 200 || !strings.Contains(body, "La cantidad no es válida") {
		t.Errorf("cantidad mala: %d", code)
	}
	if code, body = e.get("/nutricion/buscar?q=blorpitos", "ana"); code != 200 || !strings.Contains(body, "No encuentro «blorpitos»") {
		t.Errorf("buscar sin resultados: %d", code)
	}

	// Guardar la comida como habitual y apuntarla con un toque.
	if code, loc, _ := e.post("/nutricion/guardar-comida", "ana", url.Values{"dia": {"2026-11-03"}, "comida": {"comida"}, "nombre": {"Plato de prueba"}}); code != http.StatusSeeOther || loc != "/nutricion?ok=habitual" {
		t.Fatalf("guardar comida: %d %q", code, loc)
	}
	own, _ := e.svc.store.foods(ctx, "ana")
	if len(own) != 1 || own[0].Name != "Plato de prueba" || own[0].Base != baseUd || len(own[0].Items) != 1 {
		t.Fatalf("habitual: %+v", own)
	}
	if code, _, body := e.post("/nutricion/guardar-comida", "ana", url.Values{"dia": {"2026-11-03"}, "comida": {"desayuno"}}); code != 200 || !strings.Contains(body, "En esa comida no hay nada apuntado.") {
		t.Errorf("guardar comida vacía: %d", code)
	}
	plate := own[0].ID
	if code, loc, _ := e.post("/nutricion/rapido", "ana", url.Values{"dia": {"2026-11-03"}, "comida": {"merienda"}, "item": {fmt.Sprintf("a:%d:1:ración", plate)}}); code != http.StatusSeeOther {
		t.Fatalf("rápido habitual: %d %q", code, loc)
	}
	if list = e.mealsOn("ana", 0); len(list) != 3 || list[2].FoodID != plate || list[2].Meal != "merienda" {
		t.Fatalf("habitual apuntado: %+v", list)
	}
	if _, body = e.get("/nutricion", "ana"); !strings.Contains(body, fmt.Sprintf(`value="a:%d:1:ración"`, plate)) {
		t.Error("el habitual no sale en frecuentes")
	}

	// Copiar a hoy una comida de otro día.
	if _, body = e.get("/nutricion?dia=2026-11-02", "ana"); !strings.Contains(body, "Copiar a hoy") || strings.Contains(body, "Repetir ayer") {
		t.Fatal("falta «Copiar a hoy»")
	}
	if code, loc, _ := e.post("/nutricion/copiar", "ana", url.Values{"desde": {"2026-11-02"}, "comida": {"merienda"}}); code != http.StatusSeeOther || loc != "/nutricion?ok=copiado" {
		t.Fatalf("copiar: %d %q", code, loc)
	}
	if list = e.mealsOn("ana", 0); len(list) != 4 || list[3].Amount != 150 {
		t.Fatalf("copiado: %+v", list)
	}
	if code, _, body := e.post("/nutricion/copiar", "ana", url.Values{"desde": {"2026-10-01"}}); code != 200 || !strings.Contains(body, "Ese día no hay nada apuntado") {
		t.Errorf("copiar un día vacío: %d", code)
	}
	for _, f := range []url.Values{{"desde": {"2026-11-05"}}, {"desde": {""}}, {"desde": {"2026-11-02"}, "comida": {"brunch"}}} {
		if code, _, _ := e.post("/nutricion/copiar", "ana", f); code != 404 {
			t.Errorf("copiar %v: %d", f, code)
		}
	}

	// «Repetir ayer»: hoy vacío y ayer no.
	if _, _, err := e.svc.logText(ctx, "cris", "200 g de arroz", "comida", time.Date(2026, 11, 2, 14, 0, 0, 0, madrid), "web"); err != nil {
		t.Fatal(err)
	}
	if _, body = e.get("/nutricion", "cris"); !strings.Contains(body, "Repetir ayer") {
		t.Fatal("falta «Repetir ayer»")
	}
	if code, loc, _ := e.post("/nutricion/copiar", "cris", url.Values{"desde": {"2026-11-02"}}); code != http.StatusSeeOther || loc != "/nutricion?ok=copiado" {
		t.Fatalf("repetir ayer: %d %q", code, loc)
	}
	if l := e.mealsOn("cris", 0); len(l) != 1 {
		t.Fatalf("repetido: %+v", l)
	}
	if _, body = e.get("/nutricion", "cris"); strings.Contains(body, "Repetir ayer") {
		t.Error("«Repetir ayer» con algo apuntado hoy")
	}

	// Borrar, con confirmación; bob no puede.
	id := list[0].ID
	if _, body = e.get(fmt.Sprintf("/nutricion?borrar=%d", id), "ana"); !strings.Contains(body, "¿Borrar esto?") || !strings.Contains(body, fmt.Sprintf("/nutricion/apunte/%d/borrar", id)) {
		t.Fatal("falta la confirmación")
	}
	if code, _ = e.get(fmt.Sprintf("/nutricion?borrar=%d", id), "bob"); code != 404 {
		t.Errorf("bob ve lo de ana: %d", code)
	}
	if code, _, _ := e.post(fmt.Sprintf("/nutricion/apunte/%d/borrar", id), "bob", nil); code != 404 {
		t.Errorf("bob borra lo de ana: %d", code)
	}
	if code, loc, _ := e.post(fmt.Sprintf("/nutricion/apunte/%d/borrar", id), "ana", nil); code != http.StatusSeeOther || loc != "/nutricion?ok=comida-borrada" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if l := e.mealsOn("ana", 0); len(l) != 3 {
		t.Fatalf("tras borrar: %d", len(l))
	}
	if code, _, _ := e.post("/nutricion/apunte/abc/borrar", "ana", nil); code != 404 {
		t.Errorf("id no numérico: %d", code)
	}

	// Lo de ana no le sale a bob.
	_, body = e.get("/nutricion", "bob")
	if strings.Contains(body, rice.Name) || strings.Contains(body, "Plato de prueba") || !strings.Contains(body, "Nada apuntado hoy") || reFreq.MatchString(body) {
		t.Fatal("bob ve la comida de ana")
	}
	if code, _, _ := e.post("/nutricion/rapido", "bob", url.Values{"dia": {"2026-11-03"}, "comida": {"cena"}, "item": {fmt.Sprintf("a:%d:1:ración", plate)}}); code != 404 {
		t.Errorf("bob apunta el habitual de ana: %d", code)
	}
	if code, _, body := e.post("/nutricion/guardar-comida", "bob", url.Values{"dia": {"2026-11-03"}, "comida": {"comida"}}); code != 200 || !strings.Contains(body, "no hay nada apuntado") {
		t.Errorf("bob guarda la comida de ana: %d", code)
	}
	if code, _, _ := e.post("/nutricion/copiar", "bob", url.Values{"desde": {"2026-11-03"}}); code != 200 {
		t.Errorf("bob copia el día de ana: %d", code)
	}
	if l := e.mealsOn("bob", 0); len(l) != 0 {
		t.Fatalf("bob tiene lo de ana: %+v", l)
	}
	if code, body = e.get("/nutricion/buscar?q=plato", "bob"); strings.Contains(body, "Plato de prueba") {
		t.Error("bob encuentra el habitual de ana")
	}

	// Objetivos, con la pista del peso.
	if _, body = e.get("/nutricion/objetivos", "ana"); !strings.Contains(body, "Con tu peso apuntado") || !strings.Contains(body, "Las kcal dependen de tu gasto; si no lo sabes, apunta unas semanas y mira cómo cambia tu peso.") {
		t.Fatalf("objetivos sin peso:\n%s", body)
	}
	if _, err := e.svc.store.saveBody(ctx, "ana", time.Date(2026, 11, 3, 12, 0, 0, 0, madrid), bodyLog{Kg: kg(78)}, true); err != nil {
		t.Fatal(err)
	}
	if code, body = e.get("/nutricion/objetivos", "ana"); code != 200 || !strings.Contains(body, "Con 78 kg, lo habitual para quien entrena son 125–172 g de proteína al día.") {
		t.Fatalf("objetivos: %d\n%s", code, body)
	}
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"kcal": {"100"}}, "Las kcal del día tienen que estar entre 800 y 8000."},
		{url.Values{"kcal": {"mucho"}}, "Revisa los números"},
		{url.Values{"proteina": {"-5"}}, "La proteína tiene que estar entre 20 y 500 g."},
	} {
		if code, _, body := e.post("/nutricion/objetivos", "ana", c.form); code != 200 || !strings.Contains(body, c.want) || !strings.Contains(body, "Con 78 kg") {
			t.Errorf("objetivos %v: %d, falta %q", c.form, code, c.want)
		}
	}
	if code, loc, _ := e.post("/nutricion/objetivos", "ana", url.Values{"kcal": {"2400"}, "proteina": {"150,5"}, "hidratos": {""}, "grasa": {""}}); code != http.StatusSeeOther || loc != "/nutricion?ok=objetivos" {
		t.Fatalf("guardar objetivos: %d %q", code, loc)
	}
	if g, _ := e.svc.store.goals(ctx, "ana"); g.Kcal != 2400 || g.Prot != 150.5 || g.Carb != 0 {
		t.Fatalf("objetivos guardados: %+v", g)
	}
	if _, body = e.get("/nutricion?ok=objetivos", "ana"); !strings.Contains(body, "Objetivos guardados.") || !strings.Contains(body, "2.400 kcal") || !strings.Contains(body, "quedan") ||
		strings.Contains(body, "Sin objetivos") {
		t.Fatal("el día sin los objetivos")
	}
	if _, body = e.get("/nutricion/objetivos", "bob"); strings.Contains(body, `value="2400"`) || strings.Contains(body, "78 kg") {
		t.Fatal("bob ve los objetivos o el peso de ana")
	}

	// El inicio: la tarjeta de hoy, solo para ana.
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Comida de hoy") || !strings.Contains(body, " / 2.400") || !strings.Contains(body, `href="/nutricion"`) {
		t.Fatalf("inicio:\n%s", body)
	}
	if _, body = e.get("/", "bob"); strings.Contains(body, "Comida de hoy") {
		t.Fatal("bob ve la tarjeta de ana")
	}
	// Sin objetivos y sin nada en 14 días, no sale.
	if _, body = e.get("/", "dani"); strings.Contains(body, "Comida de hoy") {
		t.Fatal("tarjeta sin nada")
	}
}

// TestWebFoods: crear, cambiar y borrar habituales, guardar uno de la
// tabla sin repetirlo, y que nadie más los vea ni los toque.
func TestWebFoods(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	rice := riceFood(t)

	code, body := e.get("/nutricion/alimentos", "ana")
	if code != 200 || !strings.Contains(body, "Aún no tienes alimentos guardados") {
		t.Fatalf("vacío: %d", code)
	}
	if code, body = e.get("/nutricion/alimento/nuevo?nombre=Batido", "ana"); code != 200 || !strings.Contains(body, `name="nombre" value="Batido"`) {
		t.Fatalf("nuevo: %d", code)
	}
	noScript(t, "/nutricion/alimento/nuevo", body)
	form := url.Values{"nombre": {"Batido de prueba"}, "base": {"ud"}, "unidad": {"batido"}, "kcal": {"180"}, "proteina": {"30,5"}, "hidratos": {""}, "grasa": {"2"},
		"racion": {""}, "nota": {"Nota de prueba"}}
	code, loc, _ := e.post("/nutricion/alimento/nuevo", "ana", form)
	m := reFoodID.FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil || !strings.HasSuffix(loc, "?ok=guardado") {
		t.Fatalf("crear: %d %q", code, loc)
	}
	id := mustID(t, m[1])
	f, err := e.svc.store.foodOne(ctx, "ana", id)
	if err != nil || f.Per.Prot != 30.5 || f.Per.Fat != 2 || f.Portion != 1 || f.Unit != "batido" || f.Note != "Nota de prueba" {
		t.Fatalf("creado: %+v %v", f, err)
	}
	noPlain(t, e.svc.store.db, "foods", "Batido de prueba", "Nota de prueba")
	if code, body = e.get(loc, "ana"); code != 200 || !strings.Contains(body, "Guardado.") || !strings.Contains(body, `name="proteina" value="30.5"`) || !strings.Contains(body, "Borrar…") {
		t.Fatalf("editar: %d\n%s", code, body)
	}

	// Errores: se dicen con lo escrito.
	for _, c := range []struct {
		field, value, want string
	}{
		{"kcal", "mucho", "Revisa los números"},
		{"base", "taza", "La base tiene que ser g, ml o ud."},
		{"nombre", "", "El nombre tiene que tener entre 1 y 80 caracteres."},
		{"kcal", "", "Faltan las kcal o la proteína."},
	} {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set(c.field, c.value)
		if c.want == "Faltan las kcal o la proteína." {
			f.Set("proteina", "")
			f.Set("grasa", "")
		}
		code, _, body := e.post(fmt.Sprintf("/nutricion/alimento/%d", id), "ana", f)
		if code != 200 || !strings.Contains(body, c.want) || !strings.Contains(body, `value="Nota de prueba"`) {
			t.Errorf("%s=%q: %d, falta %q", c.field, c.value, code, c.want)
		}
	}
	form.Set("kcal", "200")
	form.Set("base", "g")
	if code, loc, _ := e.post(fmt.Sprintf("/nutricion/alimento/%d", id), "ana", form); code != http.StatusSeeOther || loc != fmt.Sprintf("/nutricion/alimento/%d?ok=guardado", id) {
		t.Fatalf("cambiar: %d %q", code, loc)
	}
	if f, _ = e.svc.store.foodOne(ctx, "ana", id); f.Per.Kcal != 200 || f.Base != baseG || f.Unit != "" || f.Portion != 100 {
		t.Fatalf("cambiado: %+v", f)
	}
	if _, body = e.get("/nutricion/alimentos", "ana"); !strings.Contains(body, "Batido de prueba") || !strings.Contains(body, "por 100 g: 200 kcal") {
		t.Fatalf("lista:\n%s", body)
	}

	// Uno de la tabla: se guarda una vez y se apunta como habitual.
	code, loc, _ = e.post("/nutricion/alimentos/generico", "ana", url.Values{"generico": {rice.Key}})
	m = reFoodID.FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil || !strings.HasSuffix(loc, "?ok=habitual") {
		t.Fatalf("guardar de la tabla: %d %q", code, loc)
	}
	if _, loc2, _ := e.post("/nutricion/alimentos/generico", "ana", url.Values{"generico": {rice.Key}}); loc2 != "/nutricion/alimento/"+m[1]+"?ok=ya-habitual" {
		t.Fatalf("repetido: %q", loc2)
	}
	if code, _, _ := e.post("/nutricion/alimentos/generico", "ana", url.Values{"generico": {"no-existe"}}); code != 404 {
		t.Errorf("genérico que no existe: %d", code)
	}
	if _, body = e.get("/nutricion/buscar?q=arroz", "ana"); !strings.Contains(body, "Ya está en tus alimentos.") || !strings.Contains(body, `name="alimento" value="`+m[1]+`"`) {
		t.Fatal("la búsqueda no sabe que ya está guardado")
	}
	rid := mustID(t, m[1])
	add := url.Values{"dia": {"2026-11-03"}, "comida": {"comida"}, "alimento": {m[1]}, "cantidad": {"250"}, "unidad": {"g"}}
	if code, loc, _ := e.post("/nutricion/anadir", "ana", add); code != http.StatusSeeOther || loc != "/nutricion?ok=apuntado" {
		t.Fatalf("añadir habitual: %d %q", code, loc)
	}
	if l := e.mealsOn("ana", 0); len(l) != 1 || l[0].FoodID != rid || l[0].Amount != 250 {
		t.Fatalf("habitual apuntado: %+v", l)
	}

	// bob no ve, no cambia y no borra.
	if code, _ = e.get(fmt.Sprintf("/nutricion/alimento/%d", id), "bob"); code != 404 {
		t.Errorf("bob ve el alimento: %d", code)
	}
	if code, _, _ := e.post(fmt.Sprintf("/nutricion/alimento/%d", id), "bob", form); code != 404 {
		t.Errorf("bob cambia el alimento: %d", code)
	}
	if code, _, _ := e.post(fmt.Sprintf("/nutricion/alimento/%d/borrar", id), "bob", nil); code != 404 {
		t.Errorf("bob borra el alimento: %d", code)
	}
	if code, _, _ := e.post("/nutricion/anadir", "bob", add); code != 404 {
		t.Errorf("bob apunta el habitual de ana: %d", code)
	}
	if _, body = e.get("/nutricion/alimentos", "bob"); strings.Contains(body, "Batido de prueba") {
		t.Fatal("bob ve los alimentos de ana")
	}
	if f, _ = e.svc.store.foodOne(ctx, "ana", id); f.Per.Kcal != 200 {
		t.Fatal("bob ha cambiado el alimento")
	}
	for _, p := range []string{"/nutricion/alimento/0", "/nutricion/alimento/abc"} {
		if code, _ = e.get(p, "ana"); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}

	// Borrar, con confirmación.
	if _, body = e.get(fmt.Sprintf("/nutricion/alimento/%d?borrar=1", id), "ana"); !strings.Contains(body, "¿Borrar «Batido de prueba»?") {
		t.Fatal("falta la confirmación")
	}
	if code, loc, _ := e.post(fmt.Sprintf("/nutricion/alimento/%d/borrar", id), "ana", nil); code != http.StatusSeeOther || loc != "/nutricion/alimentos?ok=alimento-borrado" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if _, err := e.svc.store.foodOne(ctx, "ana", id); err != errNotFound {
		t.Fatalf("sigue ahí: %v", err)
	}
	if _, body = e.get("/nutricion/alimentos?ok=alimento-borrado", "ana"); !strings.Contains(body, "Alimento borrado.") || strings.Contains(body, "Batido de prueba") {
		t.Fatal("tras borrar")
	}
}

// TestWebOpenFoodFacts: buscar y apuntar productos con un servidor de
// pruebas (nunca la red). Los valores salen siempre del producto, no del
// formulario.
func TestWebOpenFoodFacts(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/v2/product/8400000000001.json":
			rw.Write([]byte(`{"status":1,"product":{"product_name":"Yogur de prueba","brands":"Marca","quantity":"4 x 125 g","serving_quantity":"125","nutriments":{"energy-kcal_100g":56,"proteins_100g":9.6,"carbohydrates_100g":3.3,"fat_100g":0.5}}}`))
		case "/cgi/search.pl":
			rw.Write([]byte(`{"products":[{"code":"8400000000001","product_name":"Yogur de prueba","brands":"Marca","serving_quantity":125,"nutriments":{"energy-kcal_100g":56,"proteins_100g":9.6}},{"code":"8400000000002","product_name":"Bebida de prueba","quantity":"1 l","nutriments":{"energy_100g":418.4}}]}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()
	e.svc.off = newOFF()
	e.svc.off.base = srv.URL

	code, body := e.get("/nutricion/off?q=yogur&dia=2026-11-02&comida=desayuno", "ana")
	if code != 200 || !strings.Contains(body, "Yogur de prueba (Marca)") || !strings.Contains(body, "Bebida de prueba") || !strings.Contains(body, "datos ODbL") ||
		!strings.Contains(body, `value="desayuno" selected`) || !strings.Contains(body, `name="cantidad" value="125"`) {
		t.Fatalf("buscar: %d\n%s", code, body)
	}
	noScript(t, "/nutricion/off", body)
	if code, body = e.get("/nutricion/off?q=otra", "ana"); code != 200 || !strings.Contains(body, "espera unos segundos") || !strings.Contains(body, `class="toast error"`) {
		t.Fatalf("muy seguido: %d\n%s", code, body)
	}
	if code, body = e.get("/nutricion/off?codigo=8400000000001", "ana"); code != 200 || !strings.Contains(body, "Yogur de prueba (Marca)") || !strings.Contains(body, "ración: 125 g") {
		t.Fatalf("código: %d", code)
	}
	if code, body = e.get("/nutricion/off?codigo=00000000", "ana"); code != 200 || !strings.Contains(body, "Ese código no está en Open Food Facts") {
		t.Fatalf("código que no está: %d\n%s", code, body)
	}
	if _, body = e.get("/nutricion/off?codigo=12ab", "ana"); !strings.Contains(body, "El código de barras son de 8 a 14 cifras") {
		t.Fatal("código malo")
	}

	// Apuntar: se vuelve a pedir el producto; lo que mande el formulario no
	// cuenta.
	add := url.Values{"dia": {"2026-11-03"}, "comida": {"desayuno"}, "codigo": {"8400000000001"}, "cantidad": {"250"}, "kcal": {"9999"}, "nombre": {"Otra cosa"}}
	before := hits.Load()
	if code, loc, _ := e.post("/nutricion/off/anadir", "ana", add); code != http.StatusSeeOther || loc != "/nutricion?ok=apuntado" {
		t.Fatalf("apuntar: %d %q", code, loc)
	}
	if hits.Load() != before+1 {
		t.Error("no se ha vuelto a pedir el producto")
	}
	l := e.mealsOn("ana", 0)
	if len(l) != 1 || l[0].Name != "Yogur de prueba (Marca)" || l[0].Kcal != 140 || l[0].Prot != 24 || l[0].FoodID != 0 || l[0].Meal != "desayuno" {
		t.Fatalf("apuntado: %+v", l)
	}
	if own, _ := e.svc.store.foods(ctx, "ana"); len(own) != 0 {
		t.Fatal("apuntar no guarda el habitual")
	}
	for _, c := range []struct {
		field, value string
		code         int
	}{{"codigo", "abc", 400}, {"comida", "brunch", 400}, {"cantidad", "0", 200}, {"dia", "2026-12-01", 404}} {
		f := url.Values{}
		for k, v := range add {
			f[k] = v
		}
		f.Set(c.field, c.value)
		if code, _, _ := e.post("/nutricion/off/anadir", "ana", f); code != c.code {
			t.Errorf("%s=%q: %d", c.field, c.value, code)
		}
	}

	// Guardar como habitual: una vez; luego se apunta como habitual, sin red.
	code, loc, _ := e.post("/nutricion/off/guardar", "ana", url.Values{"codigo": {"8400000000001"}})
	m := reFoodID.FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil || !strings.HasSuffix(loc, "?ok=habitual") {
		t.Fatalf("guardar: %d %q", code, loc)
	}
	if _, loc2, _ := e.post("/nutricion/off/guardar", "ana", url.Values{"codigo": {"8400000000001"}}); loc2 != "/nutricion/alimento/"+m[1]+"?ok=ya-habitual" {
		t.Fatalf("guardar otra vez: %q", loc2)
	}
	before = hits.Load()
	if code, _, _ := e.post("/nutricion/off/anadir", "ana", add); code != http.StatusSeeOther {
		t.Fatalf("apuntar el habitual: %d", code)
	}
	if hits.Load() != before {
		t.Error("con el habitual no hace falta pedir el producto")
	}
	if l = e.mealsOn("ana", 0); len(l) != 2 || l[1].FoodID != mustID(t, m[1]) || l[1].Kcal != 140 {
		t.Fatalf("apuntado como habitual: %+v", l)
	}
	if _, body = e.get("/nutricion/off?codigo=8400000000001", "ana"); !strings.Contains(body, "Ya está en tus alimentos") {
		t.Error("no sabe que ya está guardado")
	}
	// Para bob no está guardado.
	if _, body = e.get("/nutricion/off?codigo=8400000000001", "bob"); strings.Contains(body, "Ya está en tus alimentos") {
		t.Error("bob ve los habituales de ana")
	}

	// Sin Open Food Facts (o sin red): se dice, sin detalles.
	e.svc.off.base = "http://127.0.0.1:1"
	if _, body = e.get("/nutricion/off?codigo=8400000000003", "ana"); !strings.Contains(body, "Open Food Facts no responde") {
		t.Errorf("sin red:\n%s", body)
	}
	e.svc.off = nil
	if _, body = e.get("/nutricion/off?q=yogur", "ana"); !strings.Contains(body, "Open Food Facts no está disponible") {
		t.Error("sin cliente")
	}
	if code, _, body := e.post("/nutricion/off/guardar", "ana", url.Values{"codigo": {"8400000000009"}}); code != 200 || !strings.Contains(body, "no está disponible") {
		t.Errorf("guardar sin cliente: %d", code)
	}
}
