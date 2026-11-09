package main

import (
	"math"
	"slices"
	"strings"
	"testing"
)

// Hoy es el jueves 12 de noviembre de 2026: ago(1) es el miércoles 11,
// ago(3) el lunes 9 (empieza la semana) y ago(10) el lunes 2.

func TestWeekdays(t *testing.T) {
	if d := dayOf(testNow()); d.Weekday().String() != "Thursday" || weekStart(d) != ago(3) || weekStart(ago(4)) != ago(10) {
		t.Fatalf("calendario de la prueba: %v %v", d.Weekday(), weekStart(d))
	}
}

func TestStreakDaily(t *testing.T) {
	today := dayOf(testNow())
	h := mk(1, "Leer", kindYesNo, 0, freq{Kind: freqDaily}, 30)
	m := marked(1, 2, 3, 4, 5, 8, 9, 10, 20)
	if cur, best := streaks(h, m, today); cur != 5 || best != 5 {
		t.Fatalf("hoy aún sin hacer no corta la racha: %d %d", cur, best)
	}
	m[isoDay(today)] = 1
	if cur, best := streaks(h, m, today); cur != 6 || best != 6 {
		t.Fatalf("con hoy: %d %d", cur, best)
	}
	if cur, best := streaks(h, marked(2, 3), today); cur != 0 || best != 2 {
		t.Fatalf("ayer sin hacer corta la racha: %d %d", cur, best)
	}
	if cur, best := streaks(h, marks{}, today); cur != 0 || best != 0 {
		t.Fatalf("sin nada: %d %d", cur, best)
	}

	st := statsOf(h, marked(1, 2, 3, 4, 5, 8, 9, 10, 20), today)
	// Este mes: del 1 al 11 de noviembre (hoy, sin hacer, no cuenta), 8 hechos.
	if st.Month != (ratio{8, 11}) || st.Last30 != (ratio{9, 29}) || st.WeekDone != 3 || st.WeekTotal != 7 || !st.DueToday || !st.ScheduledToday {
		t.Fatalf("estadísticas: %+v", st)
	}
	if p, ok := st.Month.pct(); !ok || math.Abs(p-72.727) > 0.01 {
		t.Fatalf("porcentaje: %v", p)
	}
}

func TestStreakDays(t *testing.T) {
	today := dayOf(testNow())
	// Lunes, miércoles y viernes: hoy (jueves) no toca.
	h := mk(1, "Estirar", kindYesNo, 0, freq{Kind: freqDays, Days: []int{1, 3, 5}}, 40)
	m := marked(1, 3, 6, 10, 2) // el martes (2) no toca: ni suma ni corta
	if cur, best := streaks(h, m, today); cur != 3 || best != 3 {
		t.Fatalf("días concretos: %d %d", cur, best)
	}
	st := statsOf(h, m, today)
	if st.ScheduledToday || st.DueToday || st.WeekTotal != 3 || st.WeekDone != 2 {
		t.Fatalf("estadísticas: %+v", st)
	}
	// Lunes y jueves: hoy toca y aún no está hecho.
	h2 := mk(2, "Llamar a casa", kindYesNo, 0, freq{Kind: freqDays, Days: []int{1, 4}}, 40)
	m2 := marked(3, 7, 10)
	if cur, _ := streaks(h2, m2, today); cur != 3 {
		t.Fatalf("hoy pendiente: %d", cur)
	}
	if st := statsOf(h2, m2, today); !st.DueToday {
		t.Fatal("hoy toca")
	}
	m2[isoDay(today)] = 1
	if cur, _ := streaks(h2, m2, today); cur != 4 {
		t.Fatalf("hecho hoy: %d", cur)
	}
	// El jueves pasado sin hacer corta la racha.
	if cur, best := streaks(h2, marked(3, 10, 14), today); cur != 1 || best != 2 {
		t.Fatalf("corte: %d %d", cur, best)
	}
}

func TestStreakWeekly(t *testing.T) {
	today := dayOf(testNow())
	h := mk(1, "Correr", kindYesNo, 0, freq{Kind: freqWeekly, Times: 3}, 50)
	m := marked(
		45, 43, 41, // 28 sep – 4 oct: cumplida
		38, 36, 34, // 5 – 11 oct: cumplida
		31, 29, 27, // 12 – 18 oct: cumplida
		24, 20, // 19 – 25 oct: no
		17, 15, 13, 11, // 26 oct – 1 nov: cumplida (4)
		10, 8, 6, // 2 – 8 nov: cumplida
		2, // esta semana: 1 de 3, aún en curso
	)
	if cur, best := streaks(h, m, today); cur != 2 || best != 3 {
		t.Fatalf("semanal: %d %d", cur, best)
	}
	st := statsOf(h, m, today)
	if st.WeekDone != 1 || st.WeekTotal != 3 || !st.ScheduledToday || !st.DueToday {
		t.Fatalf("esta semana: %+v", st)
	}
	// Este mes: semanas del 26 oct y del 2 nov (la en curso no cuenta).
	if st.Month != (ratio{6, 6}) || st.Last30 != (ratio{11, 12}) {
		t.Fatalf("cumplimiento: mes %+v, 30 días %+v", st.Month, st.Last30)
	}
	m[isoDay(ago(1))] = 1
	m[isoDay(today)] = 1
	if cur, best := streaks(h, m, today); cur != 3 || best != 3 {
		t.Fatalf("semana cumplida: %d %d", cur, best)
	}
	st = statsOf(h, m, today)
	if st.DueToday || !st.ScheduledToday || st.Month != (ratio{9, 9}) {
		t.Fatalf("cumplida: %+v", st)
	}
	// Cumplida sin hacer hoy: ya no toca.
	delete(m, isoDay(today))
	m[isoDay(ago(3))] = 1
	if st := statsOf(h, m, today); st.ScheduledToday || st.DueToday {
		t.Fatalf("ya cumplida esta semana: %+v", st)
	}
}

func TestStreakAmount(t *testing.T) {
	today := dayOf(testNow())
	h := mk(1, "Agua", kindAmount, 8, freq{Kind: freqDaily}, 10)
	m := marks{isoDay(ago(1)): 8, isoDay(ago(2)): 10, isoDay(ago(3)): 5, isoDay(ago(4)): 8, isoDay(today): 3}
	if cur, best := streaks(h, m, today); cur != 2 || best != 2 {
		t.Fatalf("cantidad: %d %d", cur, best)
	}
	if st := statsOf(h, m, today); st.Today != 3 || st.DoneToday || !st.DueToday {
		t.Fatalf("hoy a medias: %+v", st)
	}
	if h.goalText() != "8" {
		t.Fatalf("objetivo sin unidad: %q", h.goalText())
	}
	h.Unit = "vasos"
	if h.goalText() != "8 vasos" {
		t.Fatalf("objetivo: %q", h.goalText())
	}
}

func TestStartDayBackfill(t *testing.T) {
	today := dayOf(testNow())
	// Creado hace 5 días, pero con días de antes apuntados.
	h := mk(1, "Meditar", kindYesNo, 0, freq{Kind: freqDaily}, 5)
	m := marked(9, 8, 7, 6, 4, 3, 2, 1)
	if h.startDay(m) != ago(9) {
		t.Fatalf("empieza: %v", h.startDay(m))
	}
	if cur, best := streaks(h, m, today); cur != 4 || best != 4 {
		t.Fatalf("con días de antes: %d %d", cur, best)
	}
	m[isoDay(ago(5))] = 1
	if cur, _ := streaks(h, m, today); cur != 9 {
		t.Fatalf("seguidos: %d", cur)
	}
	// Antes de crearlo y sin marcas, no cuenta como fallo.
	if r := completion(mk(2, "Nuevo", kindYesNo, 0, freq{Kind: freqDaily}, 2), marked(1), ago(20), today, today); r != (ratio{1, 2}) {
		t.Fatalf("desde que se creó: %+v", r)
	}
}

func TestFreqText(t *testing.T) {
	for f, want := range map[*freq]string{
		{Kind: freqDaily}:                      "Cada día",
		{Kind: freqDays, Days: []int{1, 3, 5}}: "L, X y V",
		{Kind: freqDays, Days: []int{6}}:       "Los sábados",
		{Kind: freqDays, Days: []int{2}}:       "Los martes",
		{Kind: freqDays, Days: []int{0, 6}}:    "S y D",
		{Kind: freqWeekly, Times: 3}:           "3 veces por semana",
		{Kind: freqWeekly, Times: 1}:           "1 vez por semana",
	} {
		if got := freqText(*f); got != want {
			t.Errorf("%+v: %q, quería %q", *f, got, want)
		}
	}
}

func TestCorrelations(t *testing.T) {
	today := dayOf(testNow())
	h := mk(1, "Pasear", kindYesNo, 0, freq{Kind: freqDaily}, 20)
	m := map[int64]marks{1: marked(1, 2, 3)}
	f := func(v float64) *float64 { return &v }
	var entries []entry
	for _, n := range []int{1, 2, 3} {
		entries = append(entries, entry{Day: ago(n), entryData: entryData{Mood: 4, Energy: 5, Sleep: f(8)}})
	}
	for _, n := range []int{4, 5, 6} {
		entries = append(entries, entry{Day: ago(n), entryData: entryData{Mood: 2, Energy: 3, Sleep: f(6)}})
	}
	entries = append(entries, entry{Day: today, entryData: entryData{Mood: 1}})    // hoy aún puede hacerse
	entries = append(entries, entry{Day: ago(7), entryData: entryData{Note: "x"}}) // sin ánimo
	cs := correlations([]habit{h}, m, entries, ago(89), today)
	if len(cs) != 1 || !cs[0].enough() {
		t.Fatalf("correlaciones: %+v", cs)
	}
	c := cs[0]
	md, _ := c.MoodDone.value()
	mo, _ := c.MoodOther.value()
	sd, _ := c.SleepDone.value()
	if md != 4 || mo != 2 || c.MoodDone.N != 3 || c.MoodOther.N != 3 || sd != 8 {
		t.Fatalf("medias: %v %v %v %+v", md, mo, sd, c)
	}
	row := corrRowOf(c)
	if !row.Up || row.Mood != "4,0 cuando lo haces y 2,0 cuando no" || row.Sleep != "8,0 h cuando lo haces y 6,0 h cuando no" {
		t.Fatalf("fila: %+v", row)
	}
	if text := sleepCorrText(entries, ago(89)); !strings.Contains(text, "4,0 los días que duermes 7 h o más (3 días)") {
		t.Fatalf("sueño y ánimo: %q", text)
	}
	// Con pocos días, no se enseña.
	few := correlations([]habit{h}, m, entries[:2], ago(89), today)
	if few[0].enough() || corrRowOf(few[0]).Missing == "" {
		t.Fatal("con dos días no hay medias")
	}
}

func TestCheckHabit(t *testing.T) {
	ok := []habitInput{
		{Name: "Beber agua", Emoji: "💧", Kind: kindAmount, Target: 8, Unit: "vasos", Remind: "9:30"},
		{Name: "Meditar"},
		{Name: "Correr", Freq: freq{Kind: freqWeekly, Times: 3}},
		{Name: "Estirar", Freq: freq{Kind: freqDays, Days: []int{5, 1, 3, 3, 9}}},
	}
	for _, in := range ok {
		if _, err := checkHabit(in); err != nil {
			t.Errorf("rechaza %+v: %v", in, err)
		}
	}
	d, _ := checkHabit(ok[0])
	if d.Remind != "09:30" || d.Target != 8 || d.Kind != kindAmount {
		t.Fatalf("limpio: %+v", d)
	}
	d, _ = checkHabit(ok[3])
	if !slices.Equal(d.Freq.Days, []int{1, 3, 5}) {
		t.Fatalf("días: %v", d.Freq.Days)
	}
	if d, _ := checkHabit(habitInput{Name: "Todos", Freq: freq{Kind: freqDays, Days: []int{0, 1, 2, 3, 4, 5, 6}}}); d.Freq.Kind != freqDaily {
		t.Fatalf("todos los días es diaria: %+v", d.Freq)
	}
	if d, _ := checkHabit(habitInput{Name: "Sí o no", Unit: "vasos"}); d.Unit != "" || d.Kind != kindYesNo {
		t.Fatalf("sí o no sin unidad: %+v", d)
	}
	bad := []habitInput{
		{Name: "  "},
		{Name: "¡¡!!"},
		{Name: "Agua", Kind: kindAmount},
		{Name: "Agua", Kind: kindAmount, Target: -1},
		{Name: "Agua", Kind: "raro"},
		{Name: "Correr", Freq: freq{Kind: freqWeekly, Times: 9}},
		{Name: "Estirar", Freq: freq{Kind: freqDays}},
		{Name: "Leer", Freq: freq{Kind: "mensual"}},
		{Name: "Leer", Remind: "25:00"},
		{Name: "Leer", Emoji: "abc"},
	}
	for _, in := range bad {
		if _, err := checkHabit(in); !isInput(err) {
			t.Errorf("acepta %+v: %v", in, err)
		}
	}
}

func TestFindHabit(t *testing.T) {
	list := []habit{
		{ID: 1, habitData: habitData{Name: "Beber agua"}},
		{ID: 2, habitData: habitData{Name: "Leer"}},
		{ID: 3, habitData: habitData{Name: "Leer en inglés"}},
		{ID: 4, Archived: true, habitData: habitData{Name: "Correr"}},
	}
	for q, want := range map[string]int64{"agua": 1, "BEBER": 1, "leer": 2, "ingles": 3, "Correr": 4} {
		if h, err := findHabit(list, q); err != nil || h.ID != want {
			t.Errorf("%q: %d %v", q, h.ID, err)
		}
	}
	for _, q := range []string{"le", "corr", "nadar", ""} {
		if _, err := findHabit(list, q); !isInput(err) {
			t.Errorf("%q: %v", q, err)
		}
	}
	if _, err := findHabit(list, "le"); !strings.Contains(err.Error(), "varios") {
		t.Errorf("ambiguo: %v", err)
	}
}

func TestParsers(t *testing.T) {
	for s, want := range map[string]int{"9:30": 570, "09:30": 570, "21": 1260, "7.05": 425, "00:00": 0} {
		if got, err := parseHour(s); err != nil || got != want {
			t.Errorf("hora %q: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"24:00", "9:60", "a", "9:5:1", "9:300"} {
		if _, err := parseHour(s); err == nil {
			t.Errorf("hora %q aceptada", s)
		}
	}
	for s, want := range map[string]float64{"7,5": 7.5, "8": 8, " 2.25 ": 2.25} {
		if got, err := parseNum(s); err != nil || got != want {
			t.Errorf("número %q: %v %v", s, got, err)
		}
	}
	for _, s := range []string{"", "1,2,3", "x", "1.000,5"} {
		if _, err := parseNum(s); err == nil {
			t.Errorf("número %q aceptado", s)
		}
	}
	if got := cleanTags([]string{"trabajo, #Familia,, trabajo", "TRABAJO"}); !slices.Equal(got, []string{"trabajo", "Familia"}) {
		t.Errorf("etiquetas: %q", got)
	}
	if got, err := parseWeekdays([]string{"lunes", "miércoles", "V", "sábados"}); err != nil || !slices.Equal(got, []int{1, 3, 5, 6}) {
		t.Errorf("días: %v %v", got, err)
	}
	if got, err := parseWeekdays([]string{"mie, X"}); err != nil || !slices.Equal(got, []int{3, 3}) {
		t.Errorf("días cortos: %v %v", got, err)
	}
	if _, err := parseWeekdays([]string{"festivos"}); !isInput(err) {
		t.Errorf("día raro: %v", err)
	}
	today := dayOf(testNow())
	for s, want := range map[string]int{"": 0, "hoy": 0, "Ayer": 1, "anteayer": 2, "2026-11-01": 11} {
		if d, err := parseWhen(s, today); err != nil || !d.Equal(ago(want)) {
			t.Errorf("fecha %q: %v %v", s, d, err)
		}
	}
	if _, err := parseWhen("mañana", today); !isInput(err) {
		t.Error("acepta «mañana»")
	}
	if num(-0.04, 1) != "0,0" || trimNum(2.50, 2) != "2,5" || pctText(72.7) != "73 %" {
		t.Errorf("formato: %q %q %q", num(-0.04, 1), trimNum(2.5, 2), pctText(72.7))
	}
	if relDay(today, today) != "hoy" || relDay(ago(1), today) != "ayer" || relDay(ago(3), today) != "lunes 9 de noviembre" {
		t.Errorf("días: %q %q", relDay(ago(1), today), relDay(ago(3), today))
	}
}

func TestReminderText(t *testing.T) {
	today := dayOf(testNow())
	water := mk(1, "Beber agua", kindAmount, 8, freq{Kind: freqDaily}, 10)
	water.Emoji, water.Unit = "💧", "vasos"
	m := marks{isoDay(ago(1)): 8, isoDay(ago(2)): 8, isoDay(today): 3}
	if got := reminderText(water, statsOf(water, m, today)); got != "⏰ 💧 Beber agua: llevas 3 de 8 vasos hoy. Racha: 2 días." {
		t.Errorf("cantidad: %q", got)
	}
	run := mk(2, "Correr", kindYesNo, 0, freq{Kind: freqWeekly, Times: 3}, 10)
	if got := reminderText(run, statsOf(run, marked(1), today)); got != "⏰ Correr: llevas 1 de 3 esta semana." {
		t.Errorf("semanal: %q", got)
	}
	med := mk(3, "Meditar", kindYesNo, 0, freq{Kind: freqDaily}, 10)
	if got := reminderText(med, statsOf(med, marked(1), today)); got != "⏰ Meditar: aún no lo has marcado hoy. Racha: 1 día." {
		t.Errorf("sí o no: %q", got)
	}
}

func TestCharts(t *testing.T) {
	today := dayOf(testNow())
	h := mk(1, "Agua", kindAmount, 8, freq{Kind: freqDays, Days: []int{1, 2, 3, 4, 5}}, 60)
	m := marks{isoDay(ago(1)): 8, isoDay(ago(2)): 4}
	hm := buildHeatmap(h, m, today, ago(2), 26)
	if len(hm.Cells) != 26*7 || hm.W != heatLeft+26*heatStep {
		t.Fatalf("calendario: %d celdas", len(hm.Cells))
	}
	classes := map[string]int{}
	links := 0
	for _, c := range hm.Cells {
		classes[c.Class]++
		if c.Day != "" {
			links++
		}
		if c.On && c.Day != isoDay(ago(2)) {
			t.Errorf("día elegido: %+v", c)
		}
	}
	// Jueves 12: quedan viernes, sábado y domingo por llegar.
	if classes["c-done"] != 1 || classes["c-part"] != 1 || classes["c-today"] != 1 || links != 26*7-3 || classes["c-miss"] == 0 || classes["c-off"] == 0 {
		t.Fatalf("clases: %v, enlaces %d", classes, links)
	}
	if len(hm.Months) < 5 {
		t.Fatalf("meses: %+v", hm.Months)
	}

	f := func(v float64) *float64 { return &v }
	month := monthStart(today)
	entries := []entry{
		{Day: month, entryData: entryData{Mood: 3, Energy: 2, Sleep: f(6.5)}},
		{Day: month.Add(dayUnit), entryData: entryData{Mood: 4, Sleep: f(7)}},
		{Day: month.Add(2 * dayUnit), entryData: entryData{Mood: 5, Energy: 4, Sleep: f(13)}},
		{Day: month.Add(5 * dayUnit), entryData: entryData{Mood: 2}},
	}
	c := buildMoodChart(entries, month)
	if c == nil || len(c.Mood) != 1 || len(c.MoodDots) != 4 || len(c.Bars) != 3 || !c.HasEnergy || !c.HasSleep {
		t.Fatalf("gráfica: %+v", c)
	}
	for _, s := range append(append([]string{}, c.Mood...), c.Energy...) {
		if strings.Contains(s, "NaN") || strings.Contains(s, "Inf") {
			t.Fatalf("coordenadas: %q", s)
		}
	}
	if buildMoodChart(nil, month) != nil {
		t.Fatal("sin datos, sin gráfica")
	}
}
