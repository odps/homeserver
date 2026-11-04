package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseFoodReq(t *testing.T) {
	cases := []struct {
		in     string
		amount float64
		unit   string
		name   string
	}{
		{"200 g de arroz", 200, "g", "arroz"},
		{"200g arroz", 200, "g", "arroz"},
		{"1,5 kg de patatas", 1500, "g", "patatas"},
		{"250ml leche", 250, "ml", "leche"},
		{"2 huevos", 2, "", "huevos"},
		{"medio aguacate", 0.5, "", "aguacate"},
		{"una lata de atún", 1, "lata", "atún"},
		{"2 rebanadas de pan integral", 2, "rebanada", "pan integral"},
		{"2 cucharadas aceite de oliva", 2, "cucharada", "aceite de oliva"},
		{"arroz 200 g", 200, "g", "arroz"},
		{"arroz 200g", 200, "g", "arroz"},
		{"plátanos 2", 2, "", "plátanos"},
		{"yogur griego", 0, "", "yogur griego"},
		{"3 uds de galletas", 3, "", "galletas"},
	}
	for _, c := range cases {
		r := parseFoodReq(c.in)
		if r.Amount != c.amount || r.Unit != c.unit || r.Name != c.name {
			t.Errorf("%q: %+v", c.in, r)
		}
	}
	if got := splitFoods("200 g de arroz, 2 huevos y un plátano\n1 yogur + café"); len(got) != 5 {
		t.Errorf("partir: %q", got)
	}
}

func TestPlural(t *testing.T) {
	for in, want := range map[string]string{"huevo": "huevos", "ración": "raciones", "yogur": "yogures", "g": "g", "rebanada": "rebanadas"} {
		if got := plural(in, 2); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if plural("huevo", 1) != "huevo" {
		t.Error("singular")
	}
}

// gen es el alimento de la tabla que sale al buscar ese nombre.
func gen(t *testing.T, name string) genericFood {
	t.Helper()
	m := matchFoods(name, nil, 60, 1)
	if len(m) == 0 || m[0].Gen == nil {
		t.Fatalf("la tabla no tiene %q", name)
	}
	return *m[0].Gen
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.6 }

func TestGenericTable(t *testing.T) {
	list := genericFoods()
	if len(list) < 100 {
		t.Fatalf("solo %d alimentos", len(list))
	}
	seen := map[string]bool{}
	for _, g := range list {
		if seen[g.Key] {
			t.Errorf("clave repetida %s", g.Key)
		}
		seen[g.Key] = true
		if g.Name == "" || g.Kcal <= 0 && g.Key != "agua" || g.Kcal > 950 || g.Prot < 0 || g.Prot > 100 {
			t.Errorf("valores raros: %+v", g)
		}
		// Las kcal cuadran más o menos con los macros (4/4/9, la fibra a 2;
		// sin el alcohol ni el cacao, que USDA calcula con otros factores).
		est := 4*g.Prot + 4*(g.Carb-g.Fiber) + 2*g.Fiber + 9*g.Fat
		n := fold(g.Name)
		odd := strings.Contains(n, "cerveza") || strings.Contains(n, "vino") || strings.Contains(n, "cacao")
		if g.Kcal > 40 && math.Abs(est-g.Kcal) > 0.35*g.Kcal && !odd {
			t.Errorf("%s: %v kcal y macros que dan %v", g.Name, g.Kcal, est)
		}
	}
	// Lo más habitual está y va a la variante esperada.
	for _, n := range []string{"arroz", "pollo", "huevo", "leche", "avena", "plátano", "aceite de oliva", "atún", "yogur griego", "pan"} {
		gen(t, n)
	}
	if g := gen(t, "pechuga de pollo"); g.Prot < 20 {
		t.Errorf("pechuga: %+v", g)
	}
}

func TestResolveFoods(t *testing.T) {
	arroz, huevo := gen(t, "arroz"), gen(t, "huevo")
	if huevo.UnitG == 0 {
		t.Fatal("el huevo no tiene unidad")
	}
	list, missing, err := resolveFoods("200 g de arroz, 2 huevos", nil)
	if err != nil || len(missing) > 0 || len(list) != 2 {
		t.Fatalf("%+v %v %v", list, missing, err)
	}
	if list[0].Generic != arroz.Key || !near(list[0].Kcal, 2*arroz.Kcal) || list[0].Amount != 200 || list[0].Unit != "g" {
		t.Errorf("arroz: %+v", list[0])
	}
	if list[1].Generic != huevo.Key || list[1].Amount != 2 || !near(list[1].Prot, huevo.Prot*2*huevo.UnitG/100) {
		t.Errorf("huevos: %+v", list[1])
	}
	// Plurales y orden distinto.
	if l, _, err := resolveFoods("arroz 100g, huevos 3", nil); err != nil || len(l) != 2 || l[1].Amount != 3 {
		t.Errorf("al revés: %+v %v", l, err)
	}
	// Lo que no está no se apunta (nada de la lista).
	_, missing, err = resolveFoods("200 g de arroz, 1 cachopo de la abuela", nil)
	if err == nil || len(missing) != 1 {
		t.Errorf("no está: %v %v", missing, err)
	}
	// Sin gramos ni unidad conocida, pregunta.
	if _, _, err := resolveFoods("arroz", nil); err == nil && arroz.UnitG == 0 {
		t.Error("arroz sin cantidad")
	}
	// Los habituales van por delante de la tabla.
	own := []food{{ID: 7, Name: "Arroz con pollo de casa", Base: baseUd, Per: macros{Kcal: 650, Prot: 45}, Portion: 1, Unit: "táper"}}
	l, _, err := resolveFoods("arroz con pollo", own)
	if err != nil || len(l) != 1 || l[0].FoodID != 7 || l[0].Kcal != 650 || l[0].Unit != "táper" {
		t.Fatalf("habitual: %+v %v", l, err)
	}
	l, _, _ = resolveFoods("2 arroz con pollo", own)
	if len(l) != 1 || l[0].Kcal != 1300 || l[0].Unit != "táperes" {
		t.Errorf("dos táperes: %+v", l)
	}
	if _, _, err := resolveFoods("300 g de arroz con pollo", own); err == nil {
		t.Error("por raciones no se apunta en gramos")
	}
	// Por 100 g: «1 yogur» es una ración.
	own = []food{{ID: 8, Name: "Yogur proteico", Base: baseG, Per: macros{Kcal: 56, Prot: 9.6}, Portion: 125}}
	l, _, _ = resolveFoods("2 yogures proteicos", own)
	if len(l) != 1 || l[0].Amount != 250 || !near(l[0].Prot, 24) {
		t.Errorf("yogures: %+v", l)
	}
}

func TestSlotFor(t *testing.T) {
	for h, want := range map[int]string{8: "desayuno", 12: "almuerzo", 14: "comida", 18: "merienda", 22: "cena"} {
		if got := slotFor(time.Date(2026, 11, 4, h, 0, 0, 0, madrid)); got != want {
			t.Errorf("%d h: %s", h, got)
		}
	}
}

func TestNutritionStoreAndPrivacy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 4, 14, 30, 0, 0, madrid)
	svc.now = func() time.Time { return now }

	if err := svc.store.saveGoals(ctx, "ana", goals{Kcal: 2400, Prot: 150}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.saveGoals(ctx, "ana", goals{Kcal: 100}); err == nil {
		t.Error("objetivo absurdo")
	}
	g, _ := svc.store.goals(ctx, "ana")
	if g.Kcal != 2400 || g.Prot != 150 {
		t.Fatalf("objetivos: %+v", g)
	}

	// Un habitual inventado y lo de hoy.
	id, err := svc.store.saveFood(ctx, "ana", food{Name: "Batido Cuajada Fresquita", Base: baseUd, Per: macros{Kcal: 220, Prot: 30}, Unit: "batido"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.saveFood(ctx, "ana", food{Name: "Malo", Base: baseG, Per: macros{Kcal: 2000}}); err == nil {
		t.Error("más de 950 kcal por 100 g")
	}
	if _, _, err := svc.logText(ctx, "ana", "200 g de arroz, 2 huevos", "comida", now, "web"); err != nil {
		t.Fatal(err)
	}
	e, err := svc.logFood(ctx, "ana", id, 0, "merienda", now)
	if err != nil || e.Kcal != 220 || e.Amount != 1 || e.Unit != "batido" {
		t.Fatalf("un toque: %+v %v", e, err)
	}
	// Un plato estimado (Talia) y ayer.
	if _, _, err := svc.saveEntries(ctx, "ana", []mealEntry{{Name: "Menú del día Tasca Pelayo", Guess: true, macros: macros{Kcal: 900, Prot: 40}}}, "cena", now, "asistente"); err != nil {
		t.Fatal(err)
	}
	yesterday := now.AddDate(0, 0, -1)
	if _, _, err := svc.logText(ctx, "ana", "300 g de arroz", "comida", yesterday, "web"); err != nil {
		t.Fatal(err)
	}
	// Otra persona no ve nada de esto.
	svc.store.saveFood(ctx, "bea", food{Name: "Lo de Bea", Base: baseUd, Per: macros{Kcal: 100}})
	if l, _ := svc.store.meals(ctx, "bea", yesterday, now); len(l) != 0 {
		t.Fatal("bea ve lo de ana")
	}
	if _, err := svc.store.foodOne(ctx, "bea", id); err == nil {
		t.Fatal("bea ve el habitual de ana")
	}
	if err := svc.store.deleteFood(ctx, "bea", id); err == nil {
		t.Fatal("bea borra el habitual de ana")
	}

	list, _ := svc.store.meals(ctx, "ana", now, now)
	d := dayOfFood(now, list)
	if d.Count != 4 || len(d.Groups) != 3 || d.Groups[0].Slot.Key != "comida" || !d.Guess {
		t.Fatalf("día: %+v", d)
	}
	sum := 0.0
	for _, e := range list {
		sum += e.Kcal
	}
	if !near(d.Total.Kcal, sum) {
		t.Errorf("total %v, suma %v", d.Total.Kcal, sum)
	}
	out, err := svc.nutritionSummary(ctx, "ana", now)
	if err != nil || len(out.Apuntado) != 4 || out.Quedan == nil || out.DiasConDat != 2 || out.Media7 == nil || out.Aviso == "" {
		t.Fatalf("resumen: %+v %v", out, err)
	}
	if !near(out.Quedan.Kcal, 2400-d.Total.Kcal) {
		t.Errorf("quedan: %+v", out.Quedan)
	}

	// Frecuentes: el arroz (dos veces) primero.
	week, _ := svc.store.meals(ctx, "ana", now.AddDate(0, 0, -30), now)
	own, _ := svc.store.foods(ctx, "ana")
	byID := map[int64]food{}
	for _, f := range own {
		byID[f.ID] = f
	}
	fr := frequents(week, byID, 5)
	if len(fr) != 3 || fr[0].Generic != gen(t, "arroz").Key || fr[0].Times != 2 || fr[0].Amount != 200 {
		t.Fatalf("frecuentes: %+v", fr)
	}

	// Copiar la comida de ayer a la cena de hoy, y guardar la comida como habitual.
	cp, err := svc.copyMeals(ctx, "ana", yesterday, "comida", "cena", now, "web")
	if err != nil || len(cp) != 1 || cp[0].Meal != "cena" || cp[0].Amount != 300 {
		t.Fatalf("copiar: %+v %v", cp, err)
	}
	fid, err := svc.saveMealAsFood(ctx, "ana", now, "comida", "Arroz con huevo")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := svc.store.foodOne(ctx, "ana", fid)
	if len(f.Items) != 2 || f.Base != baseUd || !near(f.Per.Kcal, d.Groups[0].Total.Kcal) {
		t.Fatalf("comida habitual: %+v", f)
	}

	// Borrar.
	if err := svc.store.deleteMeal(ctx, "bea", list[0].ID); err == nil {
		t.Fatal("bea borra lo de ana")
	}
	if err := svc.store.deleteMeal(ctx, "ana", list[0].ID); err != nil {
		t.Fatal(err)
	}

	pool := svc.store.db
	noPlain(t, pool, "foods", "Cuajada", "Fresquita", "batido", "Arroz con huevo")
	noPlain(t, pool, "food_logs", "Tasca Pelayo", "arroz", "Arroz", "huevo", "merienda", "estimado")
	noPlain(t, pool, "nutrition_goals", "proteina", "kcal")
}

func TestWeekOfFood(t *testing.T) {
	to := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return to.AddDate(0, 0, -n) }
	list := []mealEntry{
		{Day: day(0), macros: macros{Kcal: 2000, Prot: 160}},
		{Day: day(1), macros: macros{Kcal: 1000, Prot: 50}},
		{Day: day(1), macros: macros{Kcal: 1300, Prot: 50}},
		{Day: day(9), macros: macros{Kcal: 5000, Prot: 10}}, // fuera
	}
	w := weekOfFood(list, to, goals{Kcal: 2200, Prot: 150})
	if w.Days != 2 || w.Avg.Kcal != 2150 || w.ProtDays != 1 || w.KcalDays != 2 || len(w.Totals) != 7 || w.Totals[6] != 2000 || w.Totals[5] != 2300 {
		t.Fatalf("%+v", w)
	}
}

func TestOpenFoodFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != offUserAgent {
			t.Errorf("user-agent: %s", r.Header.Get("User-Agent"))
		}
		switch {
		case r.URL.Path == "/api/v2/product/8402001010484.json":
			rw.Write([]byte(`{"status":1,"product":{"product_name":"Yogur natural proteínas","brands":"Marca,Otra","quantity":"4 x 125 g","serving_quantity":"125","nutriments":{"energy-kcal_100g":56,"proteins_100g":9.6,"carbohydrates_100g":3.3,"fat_100g":0.5}}}`))
		case r.URL.Path == "/cgi/search.pl":
			if r.URL.Query().Get("search_terms") != "yogur proteinas" {
				t.Errorf("búsqueda: %s", r.URL.RawQuery)
			}
			rw.Write([]byte(`{"products":[{"code":"8402001010484","product_name":"Yogur","nutriments":{"energy-kcal_100g":56,"proteins_100g":9.6}},{"code":"123","product_name":"Sin código válido","nutriments":{"energy-kcal_100g":10}},{"code":"84020010104","product_name":"Sin valores","nutriments":{}},{"code":"84020010105","product_name":"Bebida","quantity":"1 l","nutriments":{"energy_100g":418.4}}]}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()
	c := newOFF()
	c.base = srv.URL
	ctx := context.Background()
	p, err := c.barcode(ctx, "8402001010484")
	if err != nil || p.Title() != "Yogur natural proteínas (Marca)" || p.Serving != 125 || p.Per100.Prot != 9.6 || p.Liquid {
		t.Fatalf("código: %+v %v", p, err)
	}
	f := p.food()
	if f.Portion != 125 || f.Base != baseG || f.Origin != "off:8402001010484" || f.check() != nil {
		t.Errorf("habitual: %+v", f)
	}
	if _, err := c.barcode(ctx, "12ab"); err == nil {
		t.Error("código malo")
	}
	if _, err := c.barcode(ctx, "00000000"); err == nil {
		t.Error("no existe")
	}
	list, err := c.search(ctx, "  yogur   proteinas ")
	if err != nil || len(list) != 2 || !list[1].Liquid || !near(list[1].Per100.Kcal, 100) {
		t.Fatalf("búsqueda: %+v %v", list, err)
	}
	if _, err := c.search(ctx, "otra"); err != errOFFBusy {
		t.Errorf("sin esperar entre búsquedas: %v", err)
	}
}
