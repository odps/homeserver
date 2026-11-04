package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
)

// Progreso: cómo van las últimas 12 semanas (de lunes a domingo, en hora de
// Madrid; la actual, al final y marcada). Sesiones por semana, series por
// grupo muscular, peso, kcal y proteína, y los récords recientes. Todo se
// calcula en el servidor con lo de la persona (cada consulta lleva su owner)
// y se dibuja en SVG, sin JavaScript.

const (
	progressWeeks   = 12
	progressRecent  = 28 // días que miran las series por grupo, la proteína y los récords
	progressRecords = 8
	setsLow         = 10 // franja de series por semana y grupo que se suele
	setsHigh        = 20 // recomendar para ganar músculo
	workingShare    = 0.8
)

type progressPage struct {
	From string // la primera semana, «18 ago»

	Gym    *kcalChart // sesiones por semana; nil: ninguna en las 12 semanas
	GymAvg string     // media de las semanas completas
	GymTwo int        // semanas con 2 sesiones o más

	Muscles *muscleChart // nil: ninguna serie en los últimos 28 días

	Weight *weightChart // nil: sin pesajes en las 12 semanas

	Kcal     *kcalChart // nil: nada apuntado en las 12 semanas
	KcalGoal string
	Protein  string // la proteína de los últimos 28 días, en una frase

	Records []recordRow
}

// muscleChart son barras horizontales, una por grupo, con la franja de
// referencia detrás.
type muscleChart struct {
	W, H         int
	Label        string
	Rows         []muscleBar
	Ticks        []chartTick // eje de abajo
	Top, Bottom  int         // zona de las barras, de arriba abajo
	Span         int         // Bottom - Top
	BandX, BandW string      // franja de setsLow a setsHigh
	LowX, HighX  string
}

type muscleBar struct {
	Name       string
	NX, NY     string // nombre, a la izquierda
	X, Y, W, H string
	Value      string
	VX         string
	Title      string
}

// weightChart es la línea del peso medio de cada semana.
type weightChart struct {
	*chartView
	NowX, NowY string // el punto de esta semana, si se ha pesado
	Change     string // «De 80.4 a 79.1 kg (−1.3 kg)»
}

type recordRow struct {
	Key, Name   string
	Date, Value string
	Prev        string
}

// ---- Semanas ----

// weekIndex es la semana (0, la primera) de un día de una columna date, o -1
// si cae fuera de las n semanas que empiezan el lunes first.
func weekIndex(first, day time.Time, n int) int {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, madrid)
	if d.Before(first) {
		return -1
	}
	i := int(math.Round(d.Sub(first).Hours()/24)) / 7
	if i >= n {
		return -1
	}
	return i
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

// gymWeeks cuenta las sesiones de gimnasio de cada semana (el cardio va
// aparte, en su página).
func gymWeeks(list []session, first time.Time, n int) []int {
	out := make([]int, n)
	for _, x := range list {
		if i := weekIndex(first, x.Day, n); i >= 0 {
			out[i]++
		}
	}
	return out
}

// ---- Series por grupo muscular ----

// countedSets son las series de una vez que cuentan para el volumen.
//
// Decisión: ni todas ni solo working(). Todas inflarían el volumen con el
// calentamiento (tres o cuatro series ligeras antes del press cuentan como
// pecho); working() se queda solo con las de la carga más alta y dejaría
// fuera las pirámides y las series de bajada, que también son series duras.
// Cuentan las que llevan al menos el 80 % de la carga más alta de ese
// ejercicio ese día. Sin carga (peso corporal, por tiempo o por distancia) o
// en ejercicios de peso corporal (con lastre en alguna serie), todas.
func countedSets(e exerciseLog, ex exercise) int {
	top := 0.0
	for _, st := range e.Sets {
		top = math.Max(top, st.Kg)
	}
	if top == 0 || ex.Bodyweight {
		return len(e.Sets)
	}
	n := 0
	for _, st := range e.Sets {
		if st.Kg >= top*workingShare {
			n++
		}
	}
	return n
}

// muscleSets suma las series de cada grupo entre dos días (incluidos). Cada
// serie cuenta entera para cada músculo principal del ejercicio (como en
// routineName); los secundarios no cuentan. Los ejercicios sin grupo (los
// propios) van a "" («Otros»).
func muscleSets(list []session, from, to time.Time) map[string]int {
	out := map[string]int{}
	f, t := isoDay(from), isoDay(to)
	for _, x := range list {
		if d := isoDay(x.Day); d < f || d > t {
			continue
		}
		for _, e := range x.Exercises {
			ex, _ := lookup(e.Key)
			n := countedSets(e, ex)
			if n == 0 {
				continue
			}
			seen := map[string]bool{}
			for _, m := range ex.Muscles {
				if !seen[m] {
					seen[m] = true
					out[m] += n
				}
			}
			if len(seen) == 0 {
				out[""] += n
			}
		}
	}
	return out
}

// coreMuscles salen siempre, aunque estén a cero: que falte un grupo grande
// también es información. El resto, solo si hay series.
var coreMuscles = map[string]bool{"pecho": true, "espalda": true, "hombro": true, "biceps": true, "triceps": true,
	"cuadriceps": true, "isquios": true, "gluteo": true, "gemelo": true, "abdomen": true}

func muscleBars(sets map[string]int, weeks float64) *muscleChart {
	type row struct {
		key string
		avg float64
		n   int
	}
	var rows []row
	total := 0
	for _, m := range append(append([]string{}, muscleGroups...), "") {
		n := sets[m]
		total += n
		if n > 0 || coreMuscles[m] {
			rows = append(rows, row{m, float64(n) / weeks, n})
		}
	}
	if total == 0 {
		return nil
	}
	const W, padL, padR, padT, padB, rowH, barH = 480, 150, 44, 6, 26, 28, 14
	H := padT + len(rows)*rowH + padB
	c := &muscleChart{W: W, H: H, Top: padT, Bottom: padT + len(rows)*rowH, Span: len(rows) * rowH}
	hi := 25.0
	for _, r := range rows {
		hi = math.Max(hi, r.avg)
	}
	step := niceStep(hi / 4)
	hi = math.Ceil(hi/step) * step
	plotW := float64(W - padL - padR)
	x := func(v float64) float64 { return padL + v/hi*plotW }
	for v := 0.0; v <= hi+step/2; v += step {
		c.Ticks = append(c.Ticks, chartTick{X: f1(x(v)), Y: strconv.Itoa(H - 6), Label: trimNum(v), Anchor: "middle"})
	}
	c.BandX, c.BandW = f1(x(setsLow)), f1(x(setsHigh)-x(setsLow))
	c.LowX, c.HighX = f1(x(setsLow)), f1(x(setsHigh))
	var parts []string
	for i, r := range rows {
		y0 := float64(padT + i*rowH)
		name := muscleLabel(r.key)
		val := f1(r.avg)
		c.Rows = append(c.Rows, muscleBar{Name: name, NX: strconv.Itoa(padL - 8), NY: f1(y0 + rowH/2 + 4),
			X: strconv.Itoa(padL), Y: f1(y0 + (rowH-barH)/2), W: f1(x(r.avg) - padL), H: strconv.Itoa(barH),
			Value: val, VX: f1(x(r.avg) + 6),
			Title: fmt.Sprintf("%s: %s series por semana (%d en 4 semanas)", name, val, r.n)})
		parts = append(parts, strings.ToLower(name)+" "+val)
	}
	c.Label = fmt.Sprintf("Series por semana de media en las últimas 4 semanas, por grupo muscular: %s. La franja marca de %d a %d series.",
		strings.Join(parts, ", "), setsLow, setsHigh)
	return c
}

// ---- Récords ----

type record struct {
	Key, Name string
	Day       time.Time
	Kg, Prev  float64
}

// recentRecords son los ejercicios cuyo mejor 1RM estimado (el de la
// gráfica de cada ejercicio, e1RM) de los últimos 28 días supera todo lo
// anterior. Hace falta algo anterior con lo que comparar: la primera vez que
// se hace un ejercicio no es un récord. Del más reciente al más antiguo.
func recentRecords(list []session, today time.Time, custom map[string]string) []record {
	from := isoDay(dayOf(today).AddDate(0, 0, -(progressRecent - 1)))
	type acc struct {
		name       string
		best, prev float64
		day        time.Time
	}
	by := map[string]*acc{}
	e1 := func(st setLog) float64 { return e1RM(st) }
	for _, x := range list {
		for _, e := range x.Exercises {
			if e.Key == "" {
				continue
			}
			v := maxOf(e, e1)
			if v <= 0 {
				continue
			}
			a := by[e.Key]
			if a == nil {
				a = &acc{}
				by[e.Key] = a
			}
			if isoDay(x.Day) < from {
				a.prev = math.Max(a.prev, v)
				continue
			}
			// El primer día que se llegó a la mejor marca.
			if v > a.best || (v == a.best && x.Day.Before(a.day)) {
				a.best, a.day, a.name = v, x.Day, e.Name
			}
		}
	}
	var out []record
	for k, a := range by {
		if a.best > a.prev && a.prev > 0 {
			out = append(out, record{Key: k, Name: exerciseName(k, a.name, custom), Day: a.day, Kg: a.best, Prev: a.prev})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Day.Equal(out[j].Day) {
			return out[i].Day.After(out[j].Day)
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > progressRecords {
		out = out[:progressRecords]
	}
	return out
}

// ---- La página ----

// progressOf prepara la página con lo de la persona: list son sus sesiones
// (todas, para los récords), logs su peso y meals su comida de las 12
// semanas.
func (w *web) progressOf(now time.Time, list []session, logs []bodyLog, meals []mealEntry, g goals, custom map[string]string) *progressPage {
	today := dayOf(now)
	first := mondayOf(now).AddDate(0, 0, -7*(progressWeeks-1))
	mondays := make([]time.Time, progressWeeks)
	for i := range mondays {
		mondays[i] = first.AddDate(0, 0, 7*i)
	}
	p := &progressPage{From: weekLabel(first)}

	// Sesiones por semana.
	counts := gymWeeks(list, first, progressWeeks)
	vals := make([]float64, progressWeeks)
	nums := make([]string, progressWeeks)
	sum := 0
	for i, n := range counts {
		vals[i], nums[i] = float64(n), strconv.Itoa(n)
		if n >= 2 {
			p.GymTwo++
		}
		if i < progressWeeks-1 {
			sum += n
		}
	}
	if sum > 0 || counts[progressWeeks-1] > 0 {
		c := weekBars(mondays, vals, weekBarOpts{H: 200, PadL: 40, Min: 4, Whole: true, Tick: trimNum, Value: trimNum})
		for i, n := range counts {
			s := "sesiones"
			if n == 1 {
				s = "sesión"
			}
			c.Bars[i].Title = fmt.Sprintf("Semana del %s: %d %s", weekLabel(mondays[i]), n, s)
		}
		c.Label = fmt.Sprintf("Sesiones de gimnasio por semana, de la semana del %s a esta: %s", p.From, strings.Join(nums, ", "))
		p.Gym = c
		p.GymAvg = f1(float64(sum) / float64(progressWeeks-1))
	}

	// Series por grupo: los últimos 28 días, entre 4.
	p.Muscles = muscleBars(muscleSets(list, today.AddDate(0, 0, -(progressRecent-1)), today), progressRecent/7)

	// Peso: la media de cada semana con algún pesaje.
	var wDays []time.Time
	var wVals []float64
	var wParts []string
	for _, m := range mondays {
		if v, ok := avgIn(logs, m, m.AddDate(0, 0, 6)); ok {
			v = round1(v)
			wDays, wVals = append(wDays, m), append(wVals, v)
			wParts = append(wParts, fmt.Sprintf("semana del %s, %s", weekLabel(m), trimNum(v)))
		}
	}
	if len(wVals) > 0 {
		c := &weightChart{chartView: w.chart(wDays, wVals, "kg")}
		for i := range c.Dots {
			c.Dots[i].Title = fmt.Sprintf("Semana del %s: %s kg de media", weekLabel(wDays[i]), trimNum(wVals[i]))
		}
		c.Label = "Peso medio por semana, en kg: " + strings.Join(wParts, "; ")
		if last := len(wDays) - 1; wDays[last].Equal(mondays[progressWeeks-1]) {
			c.NowX, c.NowY = c.Dots[last].X, c.Dots[last].Y
		}
		if len(wVals) > 1 {
			a, b := wVals[0], wVals[len(wVals)-1]
			c.Change = fmt.Sprintf("De %s a %s kg de media semanal (%s kg).", trimNum(a), trimNum(b), signed(round1(b-a)))
		}
		p.Weight = c
	}

	// Kcal: la media de los días apuntados de cada semana.
	byWeek := make([][]mealEntry, progressWeeks)
	for _, e := range meals {
		if i := weekIndex(first, e.Day, progressWeeks); i >= 0 {
			byWeek[i] = append(byWeek[i], e)
		}
	}
	kcal := make([]float64, progressWeeks)
	var kParts []string
	logged := false
	var titles []string
	for i, m := range mondays {
		wk := weekOfFood(byWeek[i], m.AddDate(0, 0, 6), g)
		t := "sin apuntar"
		if wk.Days > 0 {
			logged = true
			kcal[i] = wk.Avg.Kcal
			dias := "días apuntados"
			if wk.Days == 1 {
				dias = "día apuntado"
			}
			t = fmt.Sprintf("%s kcal al día (%d %s)", thousands(wk.Avg.Kcal), wk.Days, dias)
			kParts = append(kParts, thousands(wk.Avg.Kcal))
		} else {
			kParts = append(kParts, "sin apuntar")
		}
		titles = append(titles, fmt.Sprintf("Semana del %s: %s", weekLabel(m), t))
	}
	if logged {
		c := weekBars(mondays, kcal, weekBarOpts{H: 210, PadL: 50, Empty: 2000, Goal: g.Kcal, Tick: thousands, Value: thousands})
		for i := range c.Bars {
			c.Bars[i].Title = titles[i]
		}
		c.Label = fmt.Sprintf("Media de kcal al día por semana, contando solo los días apuntados, de la semana del %s a esta: %s", p.From, strings.Join(kParts, ", "))
		if g.Kcal > 0 {
			p.KcalGoal = thousands(g.Kcal)
			c.Label += ". Objetivo: " + p.KcalGoal + " kcal"
		}
		p.Kcal = c
	}
	p.Protein = proteinText(meals, today, g)

	for _, r := range recentRecords(list, now, custom) {
		p.Records = append(p.Records, recordRow{Key: r.Key, Name: r.Name, Date: w.dayMonth(r.Day),
			Value: trimNum(r.Kg) + " kg", Prev: trimNum(r.Prev) + " kg"})
	}
	return p
}

// proteinText resume la proteína de los últimos 28 días (solo los días con
// algo apuntado) frente al objetivo. Vacío si no hay nada.
func proteinText(meals []mealEntry, today time.Time, g goals) string {
	from := isoDay(today.AddDate(0, 0, -(progressRecent - 1)))
	byDay := map[string]float64{}
	for _, e := range meals {
		if d := isoDay(e.Day); d >= from && d <= isoDay(today) {
			byDay[d] += e.Prot
		}
	}
	if len(byDay) == 0 {
		return ""
	}
	sum, reached := 0.0, 0
	for _, v := range byDay {
		sum += v
		if g.Prot > 0 && v >= g.Prot {
			reached++
		}
	}
	avg := gramsText(sum / float64(len(byDay)))
	dias := "días apuntados"
	if len(byDay) == 1 {
		dias = "día apuntado"
	}
	if g.Prot <= 0 {
		return fmt.Sprintf("Proteína en las últimas 4 semanas: %s al día de media (%d %s).", avg, len(byDay), dias)
	}
	return fmt.Sprintf("Proteína en las últimas 4 semanas: %s al día de media, con un objetivo de %s. Llegaste en %d de %d %s.",
		avg, gramsText(g.Prot), reached, len(byDay), dias)
}

func (w *web) progressPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	now := w.now()
	today := dayOf(now)
	first := mondayOf(now).AddDate(0, 0, -7*(progressWeeks-1))
	// Todas las sesiones: los récords comparan con todo lo anterior.
	list, err := w.svc.store.sessions(ctx, who.User, time.Time{}, today.AddDate(0, 0, 1), 0)
	if err != nil {
		w.fail(rw, "progreso", err)
		return
	}
	logs, err := w.svc.store.bodyLogs(ctx, who.User, first, today)
	if err != nil {
		w.fail(rw, "progreso", err)
		return
	}
	meals, err := w.svc.store.meals(ctx, who.User, first, today)
	if err != nil {
		w.fail(rw, "progreso", err)
		return
	}
	g, err := w.svc.store.goals(ctx, who.User)
	if err != nil {
		w.fail(rw, "progreso", err)
		return
	}
	custom, err := w.svc.store.customNames(ctx, who.User)
	if err != nil {
		w.fail(rw, "progreso", err)
		return
	}
	w.render(rw, r, "progreso.html", pageData{Name: who.Name, Tab: "inicio", Title: "Progreso",
		Progress: w.progressOf(now, list, logs, meals, g, custom)})
}
