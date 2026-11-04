package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"homeserver/platform/app"
)

// Comida en la web: el día (totales frente a los objetivos, apuntar con
// texto, los frecuentes con un toque, cada comida y la semana), buscar
// alimentos (habituales, tabla básica y Open Food Facts), los habituales y
// los objetivos. Sin JavaScript: cada botón es un formulario POST y los
// valores se calculan siempre en el servidor.

const (
	nutriFreqDays = 30 // días que miran los frecuentes
	nutriFreqN    = 8  // frecuentes en la página
	nutriHomeDays = 14 // la tarjeta del inicio sale si hay algo en estos días
	maxFoodText   = 2000
	maxFoodQuery  = 80
)

// ---- Lo que reciben las plantillas ----

type nutriPage struct {
	ISO, Date          string // día que se ve
	IsToday            bool
	Prev, PrevText     string
	Next, NextText     string // vacío: es hoy
	Totals             nutriTotals
	Form               logForm
	Slots              []mealSlot
	Frequents          []freqButton
	Groups             []groupView
	Repeat             string // AAAA-MM-DD de ayer, si hoy está vacío y ayer no
	Week               *foodWeekView
	Confirm            *entryRow // pide confirmar el borrado
	MealLabel, Default string
}

// logForm es el formulario de apuntar, tal como se escribió.
type logForm struct {
	Text, Meal string
	Missing    []string // lo que no se ha encontrado
}

// meterRow es una cifra del día frente a su objetivo.
type meterRow struct {
	Label, Value, Goal string
	Pct                string // ancho de la barra, de 0 a 100
	Left               string // «quedan 950 kcal»
	Over, Done         bool
}

type nutriTotals struct {
	Meters   []meterRow // kcal y proteína
	Small    []meterRow // hidratos y grasa
	HasGoals bool
	Guess    bool
}

type freqButton struct {
	Item, Name, Amount, Kcal string
}

type groupView struct {
	Key, Label, Total, SaveName string
	Entries                     []entryRow
}

type entryRow struct {
	ID           int64
	Main, Values string
	Guess        bool
	Date         string // en la confirmación
}

type foodWeekView struct {
	Days             int
	AvgKcal, AvgProt string
	ProtDays         int
	HasProt          bool
	Goal             string
	Chart            *kcalChart
}

// kcalChart son las kcal de cada día de la semana, con el objetivo como
// línea.
type kcalChart struct {
	barChart
	GoalY string // vacío: sin objetivo
}

// foodCard es la tarjeta del inicio.
type foodCard struct {
	Kcal, KcalGoal, Prot, ProtGoal string
}

type foodSearch struct {
	Query, ISO, Meal, DayText string
	MealLabel                 string
	Slots                     []mealSlot
	Results                   []foodResult
	Searched                  bool
}

type foodResult struct {
	ID               int64
	Key              string
	Own, Saved       bool
	Name, Per        string
	Values, UnitInfo string
	Units            []option
	Unit, Amount     string
	Items            []string
}

type offPage struct {
	Query, Code, ISO, Meal, DayText string
	Slots                           []mealSlot
	Results                         []offRow
	Searched                        bool
}

type offRow struct {
	Code, Title, Per, Values, Serving, Amount, Unit string
	SavedID                                         int64
}

type foodsPage struct {
	Foods []foodRow
}

type foodRow struct {
	ID                         int64
	Name, Per, Values, Portion string
	Items                      []string
	Origin                     string
}

type foodForm struct {
	ID                             int64
	New, Confirm                   bool
	Name, Base, Unit               string
	Kcal, Prot, Carb, Fat, Portion string
	Note                           string
	Items                          []string
	Origin                         string
	Bases                          []option
}

type goalsForm struct {
	Kcal, Prot, Carb, Fat string
	Hint                  string
	Set                   bool
}

var foodBases = []option{{baseG, "por 100 g"}, {baseML, "por 100 ml"}, {baseUd, "por unidad"}}

// ---- Cifras ----

// thousands es «1.450» (la cifra entera, con punto de miles).
func thousands(v float64) string {
	n := int64(math.Round(v))
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "." + s[i:]
	}
	return sign + s
}

func gramsText(v float64) string { return trimNum(math.Round(v)) + " g" }

// meterOf compara una cifra del día con su objetivo (0: sin objetivo).
// Las kcal avisan al pasarse; la proteína, al llegar.
func meterOf(label string, v, goal float64, kcal bool) meterRow {
	text := gramsText
	if kcal {
		text = func(v float64) string { return thousands(v) + " kcal" }
	}
	m := meterRow{Label: label, Value: text(v)}
	if goal <= 0 {
		return m
	}
	m.Goal = text(goal)
	m.Pct = f1(math.Min(100, math.Max(0, v/goal*100)))
	left := goal - v
	switch {
	case kcal && left >= 0:
		m.Left = "quedan " + text(left)
	case kcal:
		m.Left, m.Over = "te pasas "+text(-left), true
	case left > 0.5:
		m.Left = "faltan " + text(left)
	default:
		m.Left, m.Done = "objetivo cumplido", true
	}
	return m
}

func totalsOfDay(total macros, g goals, guess bool) nutriTotals {
	return nutriTotals{
		Meters:   []meterRow{meterOf("Kcal", total.Kcal, g.Kcal, true), meterOf("Proteína", total.Prot, g.Prot, false)},
		Small:    []meterRow{meterOf("Hidratos", total.Carb, g.Carb, false), meterOf("Grasa", total.Fat, g.Fat, false)},
		HasGoals: g.set(), Guess: guess,
	}
}

func entryRowOf(e mealEntry) entryRow {
	r := entryRow{ID: e.ID, Main: e.Name, Values: macrosText(e.macros), Guess: e.Guess}
	if a := amountText(e.Amount, e.Unit); a != "" {
		r.Main = a + " " + e.Name
	}
	return r
}

// freqItem lleva en un valor lo que se apunta con un toque:
// «a:<id>:<cantidad>:<unidad>» (habitual) o «g:<clave>:<cantidad>:<unidad>».
func freqItem(f frequent) string {
	if f.FoodID != 0 {
		return fmt.Sprintf("a:%d:%s:%s", f.FoodID, trimNum(f.Amount), f.Unit)
	}
	return "g:" + f.Generic + ":" + trimNum(f.Amount) + ":" + f.Unit
}

// ---- Gráfica de la semana ----

func (w *web) kcalBars(totals []float64, end time.Time, goal float64) *kcalChart {
	const W, H, padL, padR, padT, padB = 480, 210, 50, 8, 22, 30
	c := &kcalChart{barChart: barChart{W: W, H: H, X0: padL, X1: W - padR, YBottom: H - padB}}
	top := goal
	for _, v := range totals {
		top = math.Max(top, v)
	}
	if top == 0 {
		top = 2000
	}
	step := niceStep(top / 3)
	hi := math.Ceil(top/step) * step
	plotH := float64(H - padT - padB)
	y := func(v float64) float64 { return padT + (hi-v)/hi*plotH }
	for v := 0.0; v <= hi+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 8), Y: f1(y(v)), Label: thousands(v), Anchor: "end"})
	}
	slot := float64(W-padL-padR) / float64(len(totals))
	bw := slot * 0.62
	var parts []string
	for i, v := range totals {
		day := end.AddDate(0, 0, i-(len(totals)-1))
		h := v / hi * plotH
		if v > 0 && h < 2 {
			h = 2
		}
		x := padL + slot*float64(i) + (slot-bw)/2
		b := bar{X: f1(x), Y: f1(float64(H-padB) - h), W: f1(bw), H: f1(h), VX: f1(x + bw/2), VY: f1(float64(H-padB) - h - 6),
			Label: fmt.Sprintf("%s %d", shortDays[day.Weekday()], day.Day()), LY: strconv.Itoa(H - 8), Now: i == len(totals)-1}
		val := "sin apuntar"
		if v > 0 {
			b.Value = thousands(v)
			val = thousands(v) + " kcal"
		}
		b.Title = w.shortDate(day) + ": " + val
		parts = append(parts, val)
		c.Bars = append(c.Bars, b)
	}
	if goal > 0 {
		c.GoalY = f1(y(goal))
	}
	c.Label = "Kcal de los últimos 7 días, hasta el " + w.dayMonth(end) + ": " + strings.Join(parts, ", ")
	return c
}

// ---- El día ----

// nutriDay es el día de la página (vacío: hoy). No deja días futuros.
func (w *web) nutriDay(s string) (time.Time, bool) {
	today := dayOf(w.now())
	if s == "" {
		return today, true
	}
	d, err := time.ParseInLocation("2006-01-02", s, madrid)
	if err != nil || d.Year() < 2000 || d.After(today) {
		return time.Time{}, false
	}
	return d, true
}

// dayPath es la página de un día (hoy, sin fecha).
func (w *web) dayPath(day time.Time) string {
	iso := day.Format("2006-01-02")
	if iso == w.now().Format("2006-01-02") {
		return "/nutricion"
	}
	return "/nutricion?dia=" + iso
}

// defaultMeal es la comida que sale elegida: hoy, la de la hora; otro día,
// la comida.
func (w *web) defaultMeal(day time.Time) string {
	now := w.now()
	if day.Format("2006-01-02") == now.Format("2006-01-02") {
		return slotFor(now)
	}
	return "comida"
}

func validMeal(m string) bool { return slotLabel(m) != "" }

// nutriPageOf prepara la página de un día: week es lo de los 7 días que
// acaban en él y recent, lo de los últimos 30 días (para los frecuentes).
func (w *web) nutriPageOf(day time.Time, week, recent []mealEntry, own []food, g goals, f logForm) *nutriPage {
	now := w.now()
	today := dayOf(now)
	iso := day.Format("2006-01-02")
	p := &nutriPage{ISO: iso, Date: w.longDate(day), IsToday: iso == today.Format("2006-01-02"), Slots: mealSlots, Form: f}
	if p.IsToday {
		p.Date = "Hoy, " + w.longDate(day)
	}
	prev := day.AddDate(0, 0, -1)
	p.Prev, p.PrevText = prev.Format("2006-01-02"), w.shortDate(prev)
	if !p.IsToday {
		next := day.AddDate(0, 0, 1)
		p.Next, p.NextText = next.Format("2006-01-02"), w.shortDate(next)
	}
	if !validMeal(p.Form.Meal) {
		p.Form.Meal = w.defaultMeal(day)
	}
	p.MealLabel = strings.ToLower(slotLabel(p.Form.Meal))

	var dayList []mealEntry
	yesterday := prev.Format("2006-01-02")
	hadYesterday := false
	for _, e := range week {
		switch e.Day.Format("2006-01-02") {
		case iso:
			dayList = append(dayList, e)
		case yesterday:
			hadYesterday = true
		}
	}
	df := dayOfFood(day, dayList)
	p.Totals = totalsOfDay(df.Total, g, df.Guess)
	for _, grp := range df.Groups {
		gv := groupView{Key: grp.Slot.Key, Label: grp.Slot.Label, Total: macrosText(grp.Total), SaveName: grp.Slot.Label + " de siempre"}
		for _, e := range grp.Entries {
			gv.Entries = append(gv.Entries, entryRowOf(e))
		}
		p.Groups = append(p.Groups, gv)
	}
	if p.IsToday && df.Count == 0 && hadYesterday {
		p.Repeat = yesterday
	}

	byID := map[int64]food{}
	for _, f := range own {
		byID[f.ID] = f
	}
	for _, fr := range frequents(recent, byID, nutriFreqN) {
		p.Frequents = append(p.Frequents, freqButton{Item: freqItem(fr), Name: fr.Name, Amount: amountText(fr.Amount, fr.Unit), Kcal: thousands(fr.Values.Kcal) + " kcal"})
	}

	wk := weekOfFood(week, day, g)
	if wk.Days > 0 {
		v := &foodWeekView{Days: wk.Days, AvgKcal: thousands(wk.Avg.Kcal), AvgProt: trimNum(math.Round(wk.Avg.Prot)), ProtDays: wk.ProtDays, HasProt: g.Prot > 0,
			Chart: w.kcalBars(wk.Totals, day, g.Kcal)}
		if g.Kcal > 0 {
			v.Goal = thousands(g.Kcal)
		}
		p.Week = v
	}
	return p
}

func (w *web) nutriPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	day, ok := w.nutriDay(q.Get("dia"))
	if !ok {
		notFound(rw)
		return
	}
	var confirm *entryRow
	if b := q.Get("borrar"); b != "" {
		id, _ := strconv.ParseInt(b, 10, 64)
		e, err := w.svc.store.mealOne(r.Context(), who.User, id)
		if id <= 0 || errors.Is(err, errNotFound) {
			notFound(rw)
			return
		}
		if err != nil {
			w.fail(rw, "comida", err)
			return
		}
		row := entryRowOf(e)
		row.Date = w.shortDate(e.Day)
		confirm = &row
		day = dayOf(e.Day)
	}
	w.renderNutri(rw, r, who, pageData{Flash: flash(r)}, day, logForm{}, confirm)
}

func (w *web) renderNutri(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, day time.Time, f logForm, confirm *entryRow) {
	ctx := r.Context()
	today := dayOf(w.now())
	week, err := w.svc.store.meals(ctx, who.User, day.AddDate(0, 0, -6), day)
	if err != nil {
		w.fail(rw, "comida", err)
		return
	}
	recent, err := w.svc.store.meals(ctx, who.User, today.AddDate(0, 0, -(nutriFreqDays-1)), today)
	if err != nil {
		w.fail(rw, "comida", err)
		return
	}
	g, err := w.svc.store.goals(ctx, who.User)
	if err != nil {
		w.fail(rw, "comida", err)
		return
	}
	own, err := w.svc.store.foods(ctx, who.User)
	if err != nil {
		w.fail(rw, "comida", err)
		return
	}
	d.Name, d.Tab = who.Name, "comida"
	d.Nutri = w.nutriPageOf(day, week, recent, own, g, f)
	d.Nutri.Confirm = confirm
	w.render(rw, r, "nutricion.html", d)
}

// userMsg separa los errores que se pueden enseñar (validación, en
// español) de los internos (base de datos, red), que solo se registran.
func userMsg(err error) (string, bool) {
	var pg *pgconn.PgError
	var ce *pgconn.ConnectError
	var ne net.Error
	if errors.As(err, &pg) || errors.As(err, &ce) || errors.As(err, &ne) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	return sentence(err), true
}

// readForm lee un formulario pequeño.
func readForm(rw http.ResponseWriter, r *http.Request, limit int64) bool {
	r.Body = http.MaxBytesReader(rw, r.Body, limit)
	if r.ParseForm() != nil {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return false
	}
	return true
}

func formGet(r *http.Request, k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }

// postDay es el momento en que se apunta: hoy, ahora; otro día, a mediodía.
func (w *web) postDay(rw http.ResponseWriter, r *http.Request) (time.Time, bool) {
	at, err := w.atFor(formGet(r, "dia"))
	if err != nil {
		notFound(rw)
		return time.Time{}, false
	}
	return at, true
}

// amountOf lee una cantidad (g, ml o unidades; con coma o punto).
func amountOf(s string) (float64, bool) {
	v, err := parseNumber(s)
	return v, err == nil && v > 0 && v <= 5000
}

const badAmount = "La cantidad no es válida: en g, ml o unidades, por ejemplo 150."

// nutriDone reacciona al error de apuntar algo: si es de la persona, se
// enseña en la página (bad); si no, se registra.
func (w *web) nutriDone(rw http.ResponseWriter, r *http.Request, op string, err error, bad func(string)) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return false
	}
	if msg, ok := userMsg(err); ok {
		bad(msg)
		return false
	}
	w.fail(rw, op, err)
	return false
}

// nutriLogPost apunta un texto («200 g de arroz, 2 huevos»).
func (w *web) nutriLogPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 32<<10) {
		return
	}
	at, ok := w.postDay(rw, r)
	if !ok {
		return
	}
	day := dayOf(at)
	f := logForm{Text: formGet(r, "texto"), Meal: formGet(r, "comida")}
	bad := func(msg string) { w.renderNutri(rw, r, who, pageData{Error: msg}, day, f, nil) }
	switch {
	case !validMeal(f.Meal):
		bad("Elige la comida.")
		return
	case f.Text == "":
		bad("Escribe qué has comido, por ejemplo «200 g de arroz, 2 huevos».")
		return
	case utf8.RuneCountInString(f.Text) > maxFoodText:
		bad("El texto es demasiado largo: apunta menos cosas de una vez.")
		return
	}
	_, missing, err := w.svc.logText(r.Context(), who.User, f.Text, f.Meal, at, "web")
	f.Missing = missing
	if !w.nutriDone(rw, r, "comida apuntar", err, bad) {
		return
	}
	back(rw, r, w.dayPath(day), "apuntado")
}

var reFoodKey = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// quickGeneric apunta un alimento de la tabla con la unidad con la que se
// apuntó la última vez: g, ml, sus unidades («huevos») o una medida casera
// («rebanadas»).
func (w *web) quickGeneric(ctx context.Context, owner string, g genericFood, amount float64, unit, meal string, at time.Time) error {
	u := fold(strings.TrimSpace(unit))
	switch {
	case u == baseG || u == baseML:
		_, err := w.svc.logGeneric(ctx, owner, g.Key, amount, g.base(), meal, at)
		return err
	case u == "" || u == baseUd || (g.Unit != "" && singular(u) == fold(g.Unit)):
		_, err := w.svc.logGeneric(ctx, owner, g.Key, amount, baseUd, meal, at)
		return err
	}
	if _, ok := measures[singular(u)]; !ok {
		return errors.New("esa unidad no vale")
	}
	e, err := genericEntry(g, foodReq{Amount: amount, Unit: singular(u)})
	if err != nil {
		return err
	}
	_, _, err = w.svc.saveEntries(ctx, owner, []mealEntry{e}, meal, at, "web")
	return err
}

// nutriQuickPost apunta un frecuente con un toque. Los valores se vuelven a
// calcular aquí: del formulario solo vale qué es y cuánto.
func (w *web) nutriQuickPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 32<<10) {
		return
	}
	at, ok := w.postDay(rw, r)
	if !ok {
		return
	}
	day := dayOf(at)
	meal := formGet(r, "comida")
	parts := strings.SplitN(formGet(r, "item"), ":", 4)
	if !validMeal(meal) || len(parts) != 4 || utf8.RuneCountInString(parts[3]) > 30 {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	bad := func(msg string) { w.renderNutri(rw, r, who, pageData{Error: msg}, day, logForm{Meal: meal}, nil) }
	amount, ok := amountOf(parts[2])
	if !ok {
		bad(badAmount)
		return
	}
	var err error
	switch parts[0] {
	case "a":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if id <= 0 {
			notFound(rw)
			return
		}
		_, err = w.svc.logFood(r.Context(), who.User, id, amount, meal, at)
	case "g":
		g, ok := genericByKey(parts[1])
		if !reFoodKey.MatchString(parts[1]) || !ok {
			notFound(rw)
			return
		}
		err = w.quickGeneric(r.Context(), who.User, g, amount, parts[3], meal, at)
	default:
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	if !w.nutriDone(rw, r, "comida rapido", err, bad) {
		return
	}
	back(rw, r, w.dayPath(day), "apuntado")
}

func (w *web) nutriDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	ctx := r.Context()
	e, err := w.svc.store.mealOne(ctx, who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "comida borrar", err)
		return
	}
	if err := w.svc.store.deleteMeal(ctx, who.User, id); errors.Is(err, errNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.fail(rw, "comida borrar", err)
		return
	}
	back(rw, r, w.dayPath(dayOf(e.Day)), "comida-borrada")
}

// nutriSaveMeal guarda una comida de un día como habitual.
func (w *web) nutriSaveMeal(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 8<<10) {
		return
	}
	day, ok := w.nutriDay(formGet(r, "dia"))
	meal := formGet(r, "comida")
	if !ok || formGet(r, "dia") == "" || !validMeal(meal) {
		notFound(rw)
		return
	}
	name := strings.Join(strings.Fields(formGet(r, "nombre")), " ")
	bad := func(msg string) { w.renderNutri(rw, r, who, pageData{Error: msg}, day, logForm{}, nil) }
	if utf8.RuneCountInString(name) > 80 {
		bad("El nombre pasa de 80 caracteres.")
		return
	}
	_, err := w.svc.saveMealAsFood(r.Context(), who.User, day, meal, name)
	if !w.nutriDone(rw, r, "comida guardar", err, bad) {
		return
	}
	back(rw, r, w.dayPath(day), "habitual")
}

// nutriCopy copia a hoy una comida de otro día (o el día entero).
func (w *web) nutriCopy(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 4<<10) {
		return
	}
	from, ok := w.nutriDay(formGet(r, "desde"))
	meal := formGet(r, "comida")
	if !ok || formGet(r, "desde") == "" || (meal != "" && !validMeal(meal)) {
		notFound(rw)
		return
	}
	now := w.now()
	bad := func(msg string) { w.renderNutri(rw, r, who, pageData{Error: msg}, from, logForm{}, nil) }
	_, err := w.svc.copyMeals(r.Context(), who.User, from, meal, "", now, "web")
	if !w.nutriDone(rw, r, "comida copiar", err, bad) {
		return
	}
	back(rw, r, "/nutricion", "copiado")
}

// ---- Inicio ----

// foodCardOf es la tarjeta del inicio: sale con objetivos o con algo
// apuntado en los últimos 14 días (list).
func foodCardOf(list []mealEntry, g goals, today time.Time) *foodCard {
	if !g.set() && len(list) == 0 {
		return nil
	}
	iso := today.Format("2006-01-02")
	var day []mealEntry
	for _, e := range list {
		if e.Day.Format("2006-01-02") == iso {
			day = append(day, e)
		}
	}
	t := dayOfFood(today, day).Total
	c := &foodCard{Kcal: thousands(t.Kcal), Prot: trimNum(math.Round(t.Prot))}
	if g.Kcal > 0 {
		c.KcalGoal = thousands(g.Kcal)
	}
	if g.Prot > 0 {
		c.ProtGoal = trimNum(g.Prot)
	}
	return c
}

func (w *web) homeFoodCard(ctx context.Context, owner string) (*foodCard, error) {
	today := dayOf(w.now())
	list, err := w.svc.store.meals(ctx, owner, today.AddDate(0, 0, -(nutriHomeDays-1)), today)
	if err != nil {
		return nil, err
	}
	g, err := w.svc.store.goals(ctx, owner)
	if err != nil {
		return nil, err
	}
	return foodCardOf(list, g, today), nil
}

// ---- Buscar ----

// searchDay es el día y la comida a los que van los resultados.
func (w *web) searchDay(iso, meal string) (string, string, string) {
	day, ok := w.nutriDay(iso)
	if !ok {
		day, _ = w.nutriDay("")
	}
	if !validMeal(meal) {
		meal = w.defaultMeal(day)
	}
	text := "hoy"
	if day.Format("2006-01-02") != w.now().Format("2006-01-02") {
		text = "el " + w.shortDate(day)
	}
	return day.Format("2006-01-02"), meal, text
}

func cutQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > maxFoodQuery {
		q = string(r[:maxFoodQuery])
	}
	return q
}

func perText(base, unit string) string {
	if base == baseUd {
		return "por 1 " + unit
	}
	return "por 100 " + base
}

func itemsText(items []mealItem) []string {
	var out []string
	for _, it := range items {
		s := it.Name
		if a := amountText(it.Amount, it.Unit); a != "" {
			s = a + " " + s
		}
		out = append(out, s)
	}
	return out
}

// foodResults prepara los resultados (own: los habituales, para saber qué
// de la tabla ya está guardado).
func foodResults(list []foodMatch, own []food) []foodResult {
	saved := map[string]bool{}
	for _, f := range own {
		if f.Origin != "" {
			saved[f.Origin] = true
		}
	}
	var out []foodResult
	for _, m := range list {
		if f := m.Own; f != nil {
			res := foodResult{ID: f.ID, Own: true, Name: f.Name, Per: perText(f.Base, f.unitName()), Values: macrosText(f.Per),
				Units: []option{{f.Base, f.unitName()}}, Unit: f.Base, Amount: trimNum(f.Portion), Items: itemsText(f.Items)}
			if f.Base == baseUd {
				res.Units = []option{{baseUd, plural(f.unitName(), 2)}}
			}
			out = append(out, res)
			continue
		}
		g := m.Gen
		res := foodResult{Key: g.Key, Name: g.Name, Per: perText(g.base(), ""), Values: macrosText(g.per100()),
			Units: []option{{g.base(), g.base()}}, Unit: g.base(), Amount: "100", Saved: saved[fmt.Sprintf("usda:%d", g.FDC)]}
		if g.UnitG > 0 {
			res.UnitInfo = fmt.Sprintf("1 %s = %s %s", g.Unit, trimNum(g.UnitG), g.base())
			res.Units = append(res.Units, option{baseUd, plural(g.Unit, 2)})
			res.Unit, res.Amount = baseUd, "1"
		}
		out = append(out, res)
	}
	return out
}

func (w *web) foodSearchPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	s := &foodSearch{Query: cutQuery(q.Get("q")), Slots: mealSlots}
	s.ISO, s.Meal, s.DayText = w.searchDay(q.Get("dia"), q.Get("comida"))
	w.renderSearch(rw, r, who, pageData{Flash: flash(r)}, s)
}

func (w *web) renderSearch(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, s *foodSearch) {
	if s.Query != "" {
		own, err := w.svc.store.foods(r.Context(), who.User)
		if err != nil {
			w.fail(rw, "buscar alimento", err)
			return
		}
		s.Searched, s.Results = true, foodResults(matchFoods(s.Query, own, 30, 20), own)
	}
	s.MealLabel = strings.ToLower(slotLabel(s.Meal))
	d.Name, d.Tab, d.Title, d.Search = who.Name, "comida", "Buscar alimento", s
	w.render(rw, r, "nutricion_buscar.html", d)
}

// foodAddPost apunta un resultado de la búsqueda: un habitual (en su
// unidad) o uno de la tabla (g, ml o unidades).
func (w *web) foodAddPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 8<<10) {
		return
	}
	at, ok := w.postDay(rw, r)
	if !ok {
		return
	}
	day := dayOf(at)
	meal := formGet(r, "comida")
	if !validMeal(meal) {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	s := &foodSearch{Query: cutQuery(formGet(r, "q")), Slots: mealSlots}
	s.ISO, s.Meal, s.DayText = w.searchDay(day.Format("2006-01-02"), meal)
	bad := func(msg string) { w.renderSearch(rw, r, who, pageData{Error: msg}, s) }
	amount, ok := amountOf(formGet(r, "cantidad"))
	if !ok {
		bad(badAmount)
		return
	}
	var err error
	if id := formGet(r, "alimento"); id != "" {
		n, _ := strconv.ParseInt(id, 10, 64)
		if n <= 0 {
			notFound(rw)
			return
		}
		_, err = w.svc.logFood(r.Context(), who.User, n, amount, meal, at)
	} else {
		key, unit := formGet(r, "generico"), formGet(r, "unidad")
		if _, ok := genericByKey(key); !reFoodKey.MatchString(key) || !ok {
			notFound(rw)
			return
		}
		if unit != baseG && unit != baseML && unit != baseUd {
			bad("Elige la unidad.")
			return
		}
		_, err = w.svc.logGeneric(r.Context(), who.User, key, amount, unit, meal, at)
	}
	if !w.nutriDone(rw, r, "comida anadir", err, bad) {
		return
	}
	back(rw, r, w.dayPath(day), "apuntado")
}

// genericSavePost guarda un alimento de la tabla como habitual (si ya lo
// estaba, lleva al que hay).
func (w *web) genericSavePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 4<<10) {
		return
	}
	key := formGet(r, "generico")
	g, ok := genericByKey(key)
	if !reFoodKey.MatchString(key) || !ok {
		notFound(rw)
		return
	}
	w.saveOrigin(rw, r, who, foodFromGeneric(g))
}

// saveOrigin guarda un habitual que sale de la tabla o de Open Food Facts,
// sin repetirlo, y lleva a su ficha.
func (w *web) saveOrigin(rw http.ResponseWriter, r *http.Request, who app.Identity, f food) {
	ctx := r.Context()
	own, err := w.svc.store.foods(ctx, who.User)
	if err != nil {
		w.fail(rw, "guardar alimento", err)
		return
	}
	for _, o := range own {
		if o.Origin == f.Origin {
			back(rw, r, fmt.Sprintf("/nutricion/alimento/%d", o.ID), "ya-habitual")
			return
		}
	}
	id, err := w.svc.store.saveFood(ctx, who.User, f)
	if err != nil {
		if msg, ok := userMsg(err); ok {
			w.renderFoods(rw, r, who, pageData{Error: msg})
			return
		}
		w.fail(rw, "guardar alimento", err)
		return
	}
	back(rw, r, fmt.Sprintf("/nutricion/alimento/%d", id), "habitual")
}

// ---- Open Food Facts ----

// offMsg es lo que se dice cuando Open Food Facts falla: sus avisos tal
// cual; lo demás, sin detalles.
func offMsg(err error) string {
	m := err.Error()
	for _, p := range []string{"Open Food Facts", "ese código", "el código", "escribe"} {
		if strings.HasPrefix(m, p) {
			return sentence(err)
		}
	}
	return "Open Food Facts no responde bien ahora. Prueba en un rato."
}

func offRowOf(p offProduct) offRow {
	f := p.food()
	row := offRow{Code: p.Code, Title: p.Title(), Per: perText(f.Base, ""), Values: macrosText(p.Per100), Amount: trimNum(f.Portion), Unit: f.Base}
	if p.Serving > 0 {
		row.Serving = "ración: " + trimNum(p.Serving) + " " + f.Base
	}
	return row
}

func (w *web) offPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	p := &offPage{Query: cutQuery(q.Get("q")), Code: strings.TrimSpace(q.Get("codigo")), Slots: mealSlots}
	if len(p.Code) > 20 {
		p.Code = p.Code[:20]
	}
	p.ISO, p.Meal, p.DayText = w.searchDay(q.Get("dia"), q.Get("comida"))
	d := pageData{Flash: flash(r)}
	ctx := r.Context()
	if p.Code != "" || p.Query != "" {
		p.Searched = true
		var list []offProduct
		var err error
		switch {
		case w.svc.off == nil:
			err = errors.New("Open Food Facts no está disponible")
		case p.Code != "":
			var prod offProduct
			if prod, err = w.svc.off.barcode(ctx, p.Code); err == nil {
				list = []offProduct{prod}
			}
		default:
			list, err = w.svc.off.search(ctx, p.Query)
		}
		if err != nil {
			d.Error = offMsg(err)
		}
		own, err := w.svc.store.foods(ctx, who.User)
		if err != nil {
			w.fail(rw, "off", err)
			return
		}
		saved := map[string]int64{}
		for _, f := range own {
			saved[f.Origin] = f.ID
		}
		for _, prod := range list {
			row := offRowOf(prod)
			row.SavedID = saved["off:"+prod.Code]
			p.Results = append(p.Results, row)
		}
	}
	w.renderOFF(rw, r, who, d, p)
}

func (w *web) renderOFF(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, p *offPage) {
	d.Name, d.Tab, d.Title, d.OFF = who.Name, "comida", "Open Food Facts", p
	w.render(rw, r, "nutricion_off.html", d)
}

// offProductFor vuelve a pedir el producto por su código: los valores nunca
// salen del formulario.
func (w *web) offProductFor(ctx context.Context, code string) (offProduct, error) {
	if w.svc.off == nil {
		return offProduct{}, errors.New("Open Food Facts no está disponible")
	}
	return w.svc.off.barcode(ctx, code)
}

// offAddPost apunta un producto: si ya es un habitual, como habitual; si
// no, con sus valores por 100 g (sin guardarlo).
func (w *web) offAddPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 4<<10) {
		return
	}
	at, ok := w.postDay(rw, r)
	if !ok {
		return
	}
	ctx := r.Context()
	day := dayOf(at)
	code, meal := formGet(r, "codigo"), formGet(r, "comida")
	if !reBarcode.MatchString(code) || !validMeal(meal) {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	p := &offPage{Code: code, Slots: mealSlots}
	p.ISO, p.Meal, p.DayText = w.searchDay(day.Format("2006-01-02"), meal)
	bad := func(msg string) { w.renderOFF(rw, r, who, pageData{Error: msg}, p) }
	amount, ok := amountOf(formGet(r, "cantidad"))
	if !ok {
		bad(badAmount)
		return
	}
	own, err := w.svc.store.foods(ctx, who.User)
	if err != nil {
		w.fail(rw, "off apuntar", err)
		return
	}
	for _, f := range own {
		if f.Origin == "off:"+code {
			_, err := w.svc.logFood(ctx, who.User, f.ID, amount, meal, at)
			if !w.nutriDone(rw, r, "off apuntar", err, bad) {
				return
			}
			back(rw, r, w.dayPath(day), "apuntado")
			return
		}
	}
	prod, err := w.offProductFor(ctx, code)
	if err != nil {
		bad(offMsg(err))
		return
	}
	f := prod.food()
	e, err := ownEntry(f, foodReq{Amount: amount, Unit: f.Base})
	if err == nil {
		_, _, err = w.svc.saveEntries(ctx, who.User, []mealEntry{e}, meal, at, "web")
	}
	if !w.nutriDone(rw, r, "off apuntar", err, bad) {
		return
	}
	back(rw, r, w.dayPath(day), "apuntado")
}

func (w *web) offSavePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 4<<10) {
		return
	}
	code := formGet(r, "codigo")
	if !reBarcode.MatchString(code) {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	prod, err := w.offProductFor(r.Context(), code)
	if err != nil {
		p := &offPage{Code: code, Slots: mealSlots}
		p.ISO, p.Meal, p.DayText = w.searchDay("", "")
		w.renderOFF(rw, r, who, pageData{Error: offMsg(err)}, p)
		return
	}
	w.saveOrigin(rw, r, who, prod.food())
}

// ---- Habituales ----

func foodRowOf(f food) foodRow {
	row := foodRow{ID: f.ID, Name: f.Name, Per: perText(f.Base, f.unitName()), Values: macrosText(f.Per), Items: itemsText(f.Items)}
	unit := f.unitName()
	if f.Base == baseUd {
		unit = plural(unit, f.Portion)
	}
	row.Portion = "Ración: " + amountText(f.Portion, unit)
	switch {
	case strings.HasPrefix(f.Origin, "usda:"):
		row.Origin = "Tabla básica (USDA)"
	case strings.HasPrefix(f.Origin, "off:"):
		row.Origin = "Open Food Facts"
	}
	return row
}

func (w *web) foodsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderFoods(rw, r, who, pageData{Flash: flash(r)})
}

func (w *web) renderFoods(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData) {
	own, err := w.svc.store.foods(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "alimentos", err)
		return
	}
	p := &foodsPage{}
	for _, f := range own {
		p.Foods = append(p.Foods, foodRowOf(f))
	}
	d.Name, d.Tab, d.Title, d.Foods = who.Name, "comida", "Tus alimentos", p
	w.render(rw, r, "nutricion_alimentos.html", d)
}

func numField(v float64) string {
	if v == 0 {
		return ""
	}
	return trimNum(v)
}

func foodFormOf(f food) *foodForm {
	ff := &foodForm{ID: f.ID, New: f.ID == 0, Name: f.Name, Base: f.Base, Unit: f.Unit, Kcal: numField(f.Per.Kcal), Prot: numField(f.Per.Prot),
		Carb: numField(f.Per.Carb), Fat: numField(f.Per.Fat), Portion: numField(f.Portion), Note: f.Note, Items: itemsText(f.Items), Bases: foodBases}
	if ff.Base == "" {
		ff.Base = baseG
	}
	ff.Origin = foodRowOf(f).Origin
	return ff
}

func (w *web) foodNew(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	name := strings.Join(strings.Fields(r.URL.Query().Get("nombre")), " ")
	if utf8.RuneCountInString(name) > 80 {
		name = ""
	}
	f := foodFormOf(food{Name: name})
	w.render(rw, r, "nutricion_alimento.html", pageData{Name: who.Name, Tab: "comida", Title: "Nuevo alimento", Food: f})
}

func (w *web) foodEdit(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	f, err := w.svc.store.foodOne(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "alimento", err)
		return
	}
	ff := foodFormOf(f)
	ff.Confirm = r.URL.Query().Get("borrar") == "1"
	w.render(rw, r, "nutricion_alimento.html", pageData{Name: who.Name, Tab: "comida", Title: f.Name, Flash: flash(r), Food: ff})
}

// foodSave crea (id «nuevo») o cambia un habitual. Los ingredientes y el
// origen de uno que ya existe se quedan como estaban.
func (w *web) foodSave(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	var f food
	if r.PathValue("id") != "nuevo" {
		id := pathID(r)
		if id <= 0 {
			notFound(rw)
			return
		}
		old, err := w.svc.store.foodOne(ctx, who.User, id)
		if errors.Is(err, errNotFound) {
			notFound(rw)
			return
		}
		if err != nil {
			w.fail(rw, "alimento", err)
			return
		}
		f = old
	}
	if !readForm(rw, r, 8<<10) {
		return
	}
	ff := &foodForm{ID: f.ID, New: f.ID == 0, Name: formGet(r, "nombre"), Base: formGet(r, "base"), Unit: formGet(r, "unidad"),
		Kcal: formGet(r, "kcal"), Prot: formGet(r, "proteina"), Carb: formGet(r, "hidratos"), Fat: formGet(r, "grasa"),
		Portion: formGet(r, "racion"), Note: formGet(r, "nota"), Items: itemsText(f.Items), Bases: foodBases, Origin: foodRowOf(f).Origin}
	bad := func(msg string) {
		title := "Nuevo alimento"
		if !ff.New {
			title = f.Name
		}
		w.render(rw, r, "nutricion_alimento.html", pageData{Name: who.Name, Tab: "comida", Title: title, Error: msg, Food: ff})
	}
	ok := true
	read := func(s string) float64 {
		v, err := parseNumber(s)
		if err != nil {
			ok = false
		}
		return v
	}
	f.Name, f.Base, f.Unit, f.Note = ff.Name, ff.Base, ff.Unit, ff.Note
	f.Per = macros{read(ff.Kcal), read(ff.Prot), read(ff.Carb), read(ff.Fat)}
	f.Portion = read(ff.Portion)
	if !ok {
		bad("Revisa los números: usa cifras, con coma o punto.")
		return
	}
	if err := f.check(); err != nil {
		bad(sentence(err))
		return
	}
	id, err := w.svc.store.saveFood(ctx, who.User, f)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		if msg, ok := userMsg(err); ok {
			bad(msg)
			return
		}
		w.fail(rw, "alimento", err)
		return
	}
	back(rw, r, fmt.Sprintf("/nutricion/alimento/%d", id), "guardado")
}

func (w *web) foodDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	err := w.svc.store.deleteFood(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "alimento borrar", err)
		return
	}
	back(rw, r, "/nutricion/alimentos", "alimento-borrado")
}

// ---- Objetivos ----

// proteinHintText es la proteína habitual para el último peso (vacío sin
// peso): la media de 7 días o, si no hay, el último.
func proteinHintText(t *weightTrend) string {
	if t == nil {
		return ""
	}
	kgv := t.Avg7
	if kgv <= 0 {
		kgv = t.Last
	}
	lo, hi := proteinHint(kgv)
	if lo <= 0 {
		return ""
	}
	return fmt.Sprintf("Con %s kg, lo habitual para quien entrena son %s–%s g de proteína al día.", trimNum(round1(kgv)), trimNum(lo), trimNum(hi))
}

func (w *web) goalsHint(ctx context.Context, owner string) (string, error) {
	today := dayOf(w.now())
	logs, err := w.svc.store.bodyLogs(ctx, owner, today.AddDate(0, 0, -60), today.AddDate(0, 0, 1))
	if err != nil {
		return "", err
	}
	return proteinHintText(trend(logs, w.now())), nil
}

func (w *web) goalsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	g, err := w.svc.store.goals(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "objetivos", err)
		return
	}
	f := &goalsForm{Kcal: numField(g.Kcal), Prot: numField(g.Prot), Carb: numField(g.Carb), Fat: numField(g.Fat), Set: g.set()}
	w.renderGoals(rw, r, who, pageData{Flash: flash(r)}, f)
}

func (w *web) renderGoals(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, f *goalsForm) {
	hint, err := w.goalsHint(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "objetivos", err)
		return
	}
	f.Hint = hint
	d.Name, d.Tab, d.Title, d.Goals = who.Name, "comida", "Objetivos", f
	w.render(rw, r, "nutricion_objetivos.html", d)
}

func (w *web) goalsPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if !readForm(rw, r, 4<<10) {
		return
	}
	f := &goalsForm{Kcal: formGet(r, "kcal"), Prot: formGet(r, "proteina"), Carb: formGet(r, "hidratos"), Fat: formGet(r, "grasa")}
	ok := true
	read := func(s string) float64 {
		v, err := parseNumber(s)
		if err != nil {
			ok = false
		}
		return v
	}
	g := goals{read(f.Kcal), read(f.Prot), read(f.Carb), read(f.Fat)}
	msg := ""
	if !ok {
		msg = "Revisa los números: usa cifras, con coma o punto."
	} else if err := g.check(); err != nil {
		msg = sentence(err)
	}
	if msg != "" {
		w.renderGoals(rw, r, who, pageData{Error: msg}, f)
		return
	}
	if err := w.svc.store.saveGoals(r.Context(), who.User, g); err != nil {
		w.fail(rw, "objetivos", err)
		return
	}
	back(rw, r, "/nutricion", "objetivos")
}
