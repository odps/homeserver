package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Datos inventados: ejercicios del catálogo, uno propio («u7») y cifras
// redondas.

func utcDay(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestWeekIndex(t *testing.T) {
	first := time.Date(2026, 8, 17, 0, 0, 0, 0, madrid) // lunes
	for _, c := range []struct {
		day  time.Time
		want int
	}{
		{utcDay(2026, 8, 16), -1},
		{utcDay(2026, 8, 17), 0},
		{utcDay(2026, 8, 23), 0},
		{utcDay(2026, 8, 24), 1},
		{utcDay(2026, 10, 25), 9}, // domingo del cambio de hora
		{utcDay(2026, 10, 26), 10},
		{utcDay(2026, 11, 8), 11},
		{utcDay(2026, 11, 9), -1},
	} {
		if got := weekIndex(first, c.day, 12); got != c.want {
			t.Errorf("weekIndex(%s) = %d, quiero %d", c.day.Format("2006-01-02"), got, c.want)
		}
	}
	list := []session{{Day: utcDay(2026, 8, 17)}, {Day: utcDay(2026, 8, 20)}, {Day: utcDay(2026, 11, 3)}, {Day: utcDay(2026, 8, 1)}}
	got := gymWeeks(list, first, 12)
	if got[0] != 2 || got[11] != 1 || got[5] != 0 {
		t.Errorf("gymWeeks = %v", got)
	}
}

func TestCountedSets(t *testing.T) {
	bench, _ := lookup("bench_press")
	pull, _ := lookup("pull_up")
	hollow, _ := lookup("hollow_hold")
	for _, c := range []struct {
		name string
		e    exerciseLog
		ex   exercise
		want int
	}{
		// Pirámide: cuentan 70 y 80 (≥ 64 kg); working() solo daría las de 80.
		{"pirámide", exerciseLog{Sets: []setLog{{Reps: 10, Kg: 60}, {Reps: 8, Kg: 70}, {Reps: 6, Kg: 80}, {Reps: 6, Kg: 80}}}, bench, 3},
		{"calentamiento", exerciseLog{Sets: []setLog{{Reps: 10, Kg: 20}, {Reps: 5, Kg: 40}, {Reps: 5, Kg: 100}, {Reps: 5, Kg: 100}, {Reps: 5, Kg: 100}}}, bench, 3},
		{"peso corporal con lastre", exerciseLog{Sets: []setLog{{Reps: 8}, {Reps: 8, Kg: 10}}}, pull, 2},
		{"por tiempo", exerciseLog{Sets: []setLog{{Seconds: 45}, {Seconds: 45}, {Seconds: 40}}}, hollow, 3},
		{"propio sin carga", exerciseLog{Key: "u7", Sets: []setLog{{Reps: 12}, {Reps: 12}}}, exercise{}, 2},
	} {
		if got := countedSets(c.e, c.ex); got != c.want {
			t.Errorf("%s: %d series, quiero %d", c.name, got, c.want)
		}
		if c.name == "pirámide" && len(working(c.e)) != 2 {
			t.Errorf("working() ha cambiado: %d", len(working(c.e)))
		}
	}
}

func TestMuscleSets(t *testing.T) {
	today := time.Date(2026, 11, 3, 0, 0, 0, 0, madrid)
	from := today.AddDate(0, 0, -27)
	three := []setLog{{Reps: 5, Kg: 100}, {Reps: 5, Kg: 100}, {Reps: 5, Kg: 100}}
	list := []session{
		{Day: utcDay(2026, 11, 3), Exercises: []exerciseLog{{Key: "bench_press", Sets: three}, {Key: "u7", Sets: []setLog{{Reps: 12}, {Reps: 12}}}}},
		{Day: utcDay(2026, 10, 7), Exercises: []exerciseLog{{Key: "back_squat", Sets: three}}},               // el primer día de los 28
		{Day: utcDay(2026, 10, 6), Exercises: []exerciseLog{{Key: "bench_press", Sets: three}}},              // fuera
		{Day: utcDay(2026, 10, 20), Exercises: []exerciseLog{{Key: "", Name: "Sin reconocer", Sets: three}}}, // sin clave: «Otros»
	}
	got := muscleSets(list, from, today)
	want := map[string]int{"pecho": 3, "cuadriceps": 3, "gluteo": 3, "": 5}
	if len(got) != len(want) {
		t.Fatalf("muscleSets = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q: %d series, quiero %d (%v)", k, got[k], v, got)
		}
	}

	c := muscleBars(got, 4)
	if c == nil {
		t.Fatal("sin gráfica")
	}
	var names []string
	for _, r := range c.Rows {
		names = append(names, r.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.HasPrefix(joined, "Pecho,Espalda,Hombro") || !strings.HasSuffix(joined, "Abdomen,Otros") || !strings.Contains(joined, "Cuádriceps") {
		t.Errorf("filas = %s", joined)
	}
	if c.Rows[0].Value != "0.8" || c.Rows[1].Value != "0" || !strings.Contains(c.Label, "pecho 0.8") || !strings.Contains(c.Label, "de 10 a 20") {
		t.Errorf("valores: %+v / %s", c.Rows[:2], c.Label)
	}
	if c.BandX == "" || c.BandW == "" || c.Span != len(c.Rows)*28 {
		t.Errorf("franja: %+v", c)
	}
	if muscleBars(map[string]int{}, 4) != nil {
		t.Error("sin series tiene que salir el texto, no la gráfica")
	}
}

func TestRecentRecords(t *testing.T) {
	now := time.Date(2026, 11, 3, 19, 30, 0, 0, madrid)
	at := func(kg float64) []setLog { return []setLog{{Reps: 5, Kg: kg}} } // 1RM estimado = kg × 7/6
	list := []session{
		// Press: récord el 1 nov (igualado el 3: cuenta el primer día).
		{Day: utcDay(2026, 11, 3), Exercises: []exerciseLog{{Key: "bench_press", Sets: at(90)}, {Key: "lat_pulldown", Sets: at(50)}}},
		{Day: utcDay(2026, 11, 1), Exercises: []exerciseLog{{Key: "bench_press", Sets: at(90)}, {Key: "u7", Name: "Blorpo", Sets: at(30)}}},
		{Day: utcDay(2026, 10, 20), Exercises: []exerciseLog{{Key: "back_squat", Sets: at(100)}, {Key: "barbell_row", Sets: at(60)}}},
		// Antes de los 28 días.
		{Day: utcDay(2026, 10, 1), Exercises: []exerciseLog{{Key: "bench_press", Sets: at(85)}, {Key: "lat_pulldown", Sets: at(55)}, {Key: "u7", Sets: at(25)}, {Key: "barbell_row", Sets: at(60)}}},
	}
	got := recentRecords(list, now, map[string]string{"u7": "Ejercicio propio de prueba"})
	// Sentadilla: primera vez, no es récord. Jalón: peor. Remo: igual.
	if len(got) != 2 {
		t.Fatalf("récords: %+v", got)
	}
	if got[0].Key != "u7" || got[0].Name != "Ejercicio propio de prueba" || got[0].Day.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("primero: %+v", got[0])
	}
	if b := got[1]; b.Key != "bench_press" || b.Kg != 105 || b.Prev != e1RM(setLog{Reps: 5, Kg: 85}) || b.Day.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("press: %+v", b)
	}

	// Como mucho 8, del más reciente al más antiguo.
	var many []session
	for i, k := range []string{"bench_press", "lat_pulldown", "back_squat", "barbell_row", "deadlift", "overhead_press", "leg_press", "dumbbell_bench_press", "romanian_deadlift"} {
		many = append(many, session{Day: utcDay(2026, 10, 10+i), Exercises: []exerciseLog{{Key: k, Sets: at(60)}}},
			session{Day: utcDay(2026, 9, 1), Exercises: []exerciseLog{{Key: k, Sets: at(50)}}})
	}
	got = recentRecords(many, now, nil)
	if len(got) != progressRecords || got[0].Key != "romanian_deadlift" || got[7].Key != "lat_pulldown" {
		t.Errorf("límite y orden: %d %+v", len(got), got)
	}
}

func TestProteinText(t *testing.T) {
	today := time.Date(2026, 11, 3, 0, 0, 0, 0, madrid)
	meals := []mealEntry{
		{Day: utcDay(2026, 11, 3), macros: macros{Prot: 100}},
		{Day: utcDay(2026, 11, 3), macros: macros{Prot: 50}},
		{Day: utcDay(2026, 11, 1), macros: macros{Prot: 90}},
		{Day: utcDay(2026, 10, 1), macros: macros{Prot: 10}}, // fuera
	}
	if got := proteinText(meals, today, goals{Prot: 140}); got != "Proteína en las últimas 4 semanas: 120 g al día de media, con un objetivo de 140 g. Llegaste en 1 de 2 días apuntados." {
		t.Errorf("con objetivo: %q", got)
	}
	if got := proteinText(meals[2:3], today, goals{}); got != "Proteína en las últimas 4 semanas: 90 g al día de media (1 día apuntado)." {
		t.Errorf("sin objetivo: %q", got)
	}
	if got := proteinText(meals[3:], today, goals{Prot: 140}); got != "" {
		t.Errorf("sin nada: %q", got)
	}
}

func TestWeekBarsWhole(t *testing.T) {
	mondays := make([]time.Time, 12)
	vals := make([]float64, 12)
	vals[3], vals[11] = 7, 1
	c := weekBars(mondays, vals, weekBarOpts{H: 200, PadL: 40, Min: 4, Whole: true, Tick: trimNum, Value: trimNum})
	for _, tk := range c.YTicks {
		if strings.Contains(tk.Label, ".") {
			t.Errorf("paso con decimales: %+v", c.YTicks)
		}
	}
	if !c.Bars[11].Now || c.Bars[10].Now || c.Bars[0].Value != "" || c.Bars[3].Value != "7" || c.GoalY != "" {
		t.Errorf("barras: %+v", c.Bars)
	}
}

// progressRenderCases son la página con datos de varias semanas y vacía,
// para TestWebRender (y la vista previa).
func progressRenderCases(t *testing.T, w *web) ([]renderCase, map[string][]string) {
	now := w.now()
	day := func(d int) time.Time { return time.Date(2026, 11, 3+d, 0, 0, 0, 0, time.UTC) }
	custom := map[string]string{"u7": "Ejercicio propio de prueba"}
	var list []session
	var logs []bodyLog
	var meals []mealEntry
	for wk := 0; wk < 12; wk++ {
		base := -7*wk - 1 // lunes de cada semana: 2 nov, 26 oct…
		n := 3 - wk%3
		if wk == 7 {
			n = 0 // una semana sin ir
		}
		for s := 0; s < n && base+s*2 <= 0; s++ {
			kg := 80 - float64(wk)*1.25
			list = append(list, session{ID: int64(100 + wk*3 + s), Day: day(base + s*2), Exercises: []exerciseLog{
				{Key: "bench_press", Sets: []setLog{{Reps: 10, Kg: 40}, {Reps: 5, Kg: kg}, {Reps: 5, Kg: kg}, {Reps: 5, Kg: kg}}},
				{Key: "back_squat", Sets: []setLog{{Reps: 5, Kg: 100}, {Reps: 5, Kg: 100}, {Reps: 5, Kg: 100}}},
				{Key: "lat_pulldown", Sets: []setLog{{Reps: 10, Kg: 50}, {Reps: 10, Kg: 50}, {Reps: 10, Kg: 50}, {Reps: 10, Kg: 50}}},
				{Key: "u7", Name: "Blorpo", Sets: []setLog{{Reps: 12}, {Reps: 12}}},
			}})
		}
		if wk != 4 {
			for d := 0; d < 7 && base+d <= 0; d += 3 {
				logs = append(logs, bodyLog{Day: day(base + d), Kg: kg(78 + float64(wk)*0.15 + float64(d)*0.05)})
			}
		}
		for d := 0; d < 7 && wk < 10; d++ {
			if d == 6 && wk%2 == 0 {
				continue // un domingo sin apuntar
			}
			if day(base + d).After(day(0)) {
				break
			}
			meals = append(meals, mealEntry{Day: day(base + d), Meal: "comida", Name: "Plato de prueba", macros: macros{Kcal: 2000 + float64((wk*7+d)%5)*90, Prot: 120 + float64(d*4)}})
		}
	}
	// Hoy por la mañana se ha pesado.
	logs = append([]bodyLog{{Day: day(0), Kg: kg(77.6)}}, logs...)
	g := goals{Kcal: 2200, Prot: 140}
	full := w.progressOf(now, list, logs, meals, g, custom)
	if full.Gym == nil || full.Muscles == nil || full.Weight == nil || full.Kcal == nil || full.Protein == "" || len(full.Records) == 0 {
		t.Fatalf("progreso con datos: %+v", full)
	}
	if full.From != "17 ago" || full.GymTwo != 6 || full.Weight.NowX == "" || full.Records[0].Key != "bench_press" {
		t.Errorf("progreso: desde %q, %d semanas con 2, ahora %q, récords %+v", full.From, full.GymTwo, full.Weight.NowX, full.Records)
	}
	empty := w.progressOf(now, nil, nil, nil, goals{}, nil)
	cases := []renderCase{
		{"progreso", "progreso.html", pageData{Tab: "inicio", Title: "Progreso", Progress: full}},
		{"progreso-vacio", "progreso.html", pageData{Tab: "inicio", Title: "Progreso", Progress: empty}},
	}
	wants := map[string][]string{
		"progreso": {"<title>Progreso · Entreno</title>", "Sesiones de gimnasio por semana", `role="img" aria-label="Sesiones de gimnasio por semana, de la semana del 17 ago a esta: `,
			`class="bar now"`, "<strong>6 de 12</strong>", `class="band"`, "Cuádriceps", "Otros", "de 10 a 20 series", `class="dot now"`, "kg de media semanal",
			"objetivo, 2.200", `class="goal"`, `class="val alt"`, "Proteína en las últimas 4 semanas", `href="/entreno/ejercicio/bench_press"`, "antes, "},
		"progreso-vacio": {"Aún no hay sesiones de gimnasio", "No hay series apuntadas", "No hay pesajes", "No hay comida apuntada", "Sin récords"},
	}
	return cases, wants
}

// TestWebProgress: la página con datos, enlazada desde el inicio, y que
// nadie más vea lo de otro.
func TestWebProgress(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	if code, _ := e.get("/progreso", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	for _, s := range []struct {
		text string
		at   time.Time
	}{
		{"Bench 3x5 80kg", time.Date(2026, 9, 15, 19, 0, 0, 0, madrid)},
		{"Bench 3x5 85kg\nLat machine 3x10 50kg", time.Date(2026, 10, 27, 19, 0, 0, 0, madrid)},
		{"Bench 3x5 90kg", time.Date(2026, 11, 2, 19, 0, 0, 0, madrid)},
	} {
		if _, err := e.svc.record(ctx, "ana", s.text, s.at, "web"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.store.saveBody(ctx, "ana", time.Date(2026, 11, 3, 8, 0, 0, 0, madrid), bodyLog{Kg: kg(71.3)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.store.addMeals(ctx, "ana", time.Date(2026, 11, 2, 14, 0, 0, 0, madrid), "web",
		[]mealEntry{{Meal: "comida", Name: "Plato de prueba", macros: macros{Kcal: 2345, Prot: 133}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.store.saveGoals(ctx, "ana", goals{Kcal: 2500, Prot: 150}); err != nil {
		t.Fatal(err)
	}

	code, body := e.get("/", "ana")
	if code != 200 || !strings.Contains(body, `href="/progreso"`) || !strings.Contains(body, "Ver progreso") {
		t.Fatalf("inicio sin enlace: %d", code)
	}
	code, body = e.get("/progreso", "ana")
	for _, want := range []string{"<title>Progreso · Entreno</title>", `href="/" aria-current="page"`, "Press de banca con barra", `href="/ejercicio/bench_press"`,
		"105 kg", "Pecho", "Espalda", "71.3", "2.345", "objetivo, 2.500", "133 g al día de media, con un objetivo de 150 g"} {
		if !strings.Contains(body, want) {
			t.Errorf("progreso de ana: falta %q", want)
		}
	}
	if code != 200 || strings.Count(body, `role="img"`) != 4 {
		t.Fatalf("progreso de ana: %d, %d gráficas\n%s", code, strings.Count(body, `role="img"`), body)
	}
	noScript(t, "/progreso", body)

	// Otra persona: nada de lo de ana.
	code, body = e.get("/progreso", "luis")
	if code != 200 {
		t.Fatalf("progreso de luis: %d", code)
	}
	for _, bad := range []string{"Press de banca", "bench_press", "105 kg", "71.3", "2.345", "2.500", "133 g", `role="img"`} {
		if strings.Contains(body, bad) {
			t.Errorf("luis ve %q de ana", bad)
		}
	}
	for _, want := range []string{"Aún no hay sesiones de gimnasio", "No hay series apuntadas", "No hay pesajes", "No hay comida apuntada", "Sin récords"} {
		if !strings.Contains(body, want) {
			t.Errorf("progreso vacío: falta %q", want)
		}
	}
}
