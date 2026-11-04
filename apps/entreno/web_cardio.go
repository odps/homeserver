package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
)

// Cardio en la web: lo de esta semana, los minutos de las últimas 8 semanas
// (barras en SVG, con la altura en atributos: la CSP no deja estilos en
// línea), el formulario y las últimas sesiones.

const (
	cardioWeeksShown = 8
	cardioRecent     = 40
)

type cardioPage struct {
	Week    weekTotals
	Chart   *barChart
	Form    cardioForm
	Types   []option
	Efforts []int
	Recent  []cardioRow
	Confirm *cardioRow // pide confirmar el borrado
}

type weekTotals struct {
	Count    int
	Time, Km string
}

// cardioForm es el formulario, tal como se escribió.
type cardioForm struct {
	Type, Date, Time, Dur, Km, HR, Kcal, RPE, Note string
}

type cardioRow struct {
	ID               int64
	Date, Time, Text string
	Note             string
}

// barChart son barras verticales, una por semana.
type barChart struct {
	W, H    int
	Label   string
	Bars    []bar
	YTicks  []chartTick
	X0, X1  int
	YBottom int
}

type bar struct {
	X, Y, W, H string
	Title      string
	Value      string // minutos, encima de la barra
	VX, VY     string
	Label      string // semana del…
	LY         string
	Alt        bool // etiqueta que se esconde en el móvil (una sí, otra no)
	Now        bool // esta semana
}

var efforts = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

func cardioOptions() []option {
	out := make([]option, len(cardioTypes))
	for i, t := range cardioTypes {
		out[i] = option{Key: t.Key, Name: t.Name}
	}
	return out
}

// weekLabel es «27 oct» (sin año: son las últimas semanas).
func weekLabel(t time.Time) string { return fmt.Sprintf("%d %s", t.Day(), shortMonth(t.Month())) }

// cardioBars dibuja los minutos de cada semana (de la más antigua a la actual).
func cardioBars(weeks []cardioWeek) *barChart {
	const W, H, padL, padR, padT, padB = 480, 220, 44, 8, 22, 30
	c := &barChart{W: W, H: H, X0: padL, X1: W - padR, YBottom: H - padB}
	top := 0.0
	var mins []string
	for _, wk := range weeks {
		m := float64(wk.Seconds) / 60
		top = math.Max(top, m)
		mins = append(mins, strconv.Itoa(int(math.Round(m))))
	}
	if top == 0 {
		top = 60
	}
	step := niceStep(top / 3)
	hi := math.Ceil(top/step) * step
	plotH := float64(H - padT - padB)
	y := func(v float64) float64 { return padT + (hi-v)/hi*plotH }
	for v := 0.0; v <= hi+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 8), Y: f1(y(v)), Label: trimNum(v), Anchor: "end"})
	}
	slot := float64(W-padL-padR) / float64(len(weeks))
	bw := slot * 0.62
	for i, wk := range weeks {
		m := float64(wk.Seconds) / 60
		h := m / hi * plotH
		if m > 0 && h < 2 {
			h = 2
		}
		x := padL + slot*float64(i) + (slot-bw)/2
		b := bar{X: f1(x), Y: f1(float64(H-padB) - h), W: f1(bw), H: f1(h), VX: f1(x + bw/2), VY: f1(float64(H-padB) - h - 6),
			Label: weekLabel(wk.Monday), LY: strconv.Itoa(H - 8), Alt: (len(weeks)-1-i)%2 == 1, Now: i == len(weeks)-1}
		if m > 0 {
			b.Value = mins[i]
		}
		sesiones := "sesiones"
		if wk.Count == 1 {
			sesiones = "sesión"
		}
		b.Title = fmt.Sprintf("Semana del %s: %d %s, %s min", weekLabel(wk.Monday), wk.Count, sesiones, mins[i])
		c.Bars = append(c.Bars, b)
	}
	c.Label = fmt.Sprintf("Minutos de cardio por semana, de la semana del %s a esta: %s", weekLabel(weeks[0].Monday), strings.Join(mins, ", "))
	return c
}

func (w *web) cardioRowOf(c cardioLog) cardioRow {
	return cardioRow{ID: c.ID, Date: w.shortDate(c.Day), Time: c.Start.In(madrid).Format("15:04"), Text: cardioText(c), Note: c.Note}
}

// cardioPageOf prepara la página: weekList es el cardio de las últimas
// semanas y recent, las últimas sesiones.
func (w *web) cardioPageOf(weekList, recent []cardioLog, today time.Time, f cardioForm) *cardioPage {
	weeks := cardioWeeks(weekList, today, cardioWeeksShown)
	cur := weeks[len(weeks)-1]
	p := &cardioPage{Week: weekTotals{Count: cur.Count, Time: durText(cur.Seconds), Km: trimNum(math.Round(cur.Meters/100) / 10)},
		Chart: cardioBars(weeks), Form: f, Types: cardioOptions(), Efforts: efforts}
	for _, c := range recent {
		p.Recent = append(p.Recent, w.cardioRowOf(c))
	}
	return p
}

func (w *web) cardioPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d := pageData{Flash: flash(r)}
	var confirm *cardioRow
	if q := r.URL.Query().Get("borrar"); q != "" {
		id, _ := strconv.ParseInt(q, 10, 64)
		c, err := w.svc.store.cardioOne(r.Context(), who.User, id)
		if errors.Is(err, errNotFound) || id <= 0 {
			notFound(rw)
			return
		}
		if err != nil {
			w.fail(rw, "cardio", err)
			return
		}
		row := w.cardioRowOf(c)
		confirm = &row
	}
	w.renderCardio(rw, r, who, d, cardioForm{Type: "correr", Date: w.now().Format("2006-01-02")}, confirm)
}

func (w *web) renderCardio(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, f cardioForm, confirm *cardioRow) {
	ctx := r.Context()
	today := w.now()
	to := dayOf(today).AddDate(0, 0, 1)
	weekList, err := w.svc.store.cardioList(ctx, who.User, mondayOf(today).AddDate(0, 0, -7*(cardioWeeksShown-1)), to, 0)
	if err != nil {
		w.fail(rw, "cardio", err)
		return
	}
	recent, err := w.svc.store.cardioList(ctx, who.User, time.Time{}, to, cardioRecent)
	if err != nil {
		w.fail(rw, "cardio", err)
		return
	}
	d.Name, d.Tab, d.Title = who.Name, "inicio", "Cardio"
	d.Cardio = w.cardioPageOf(weekList, recent, today, f)
	d.Cardio.Confirm = confirm
	w.render(rw, r, "cardio.html", d)
}

func intField(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// cardioFrom valida el formulario. Devuelve el mensaje para la persona si
// algo no vale.
func (w *web) cardioFrom(f cardioForm) (cardioLog, string) {
	t, ok := cardioTypeOf(f.Type)
	if !ok {
		return cardioLog{}, "Elige el tipo de cardio."
	}
	day, err := w.dayFor(f.Date)
	if err != nil {
		return cardioLog{}, err.Error()
	}
	now := w.now()
	start := day
	switch {
	case f.Time != "":
		hm, err := time.Parse("15:04", f.Time)
		if err != nil {
			if hm, err = time.Parse("15:04:05", f.Time); err != nil {
				return cardioLog{}, "La hora no es válida."
			}
		}
		start = time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, madrid)
	case day.Format("2006-01-02") == now.Format("2006-01-02"):
		start = now
	}
	sec, err := parseDuration(f.Dur)
	if err != nil {
		return cardioLog{}, sentence(err)
	}
	km, err := parseNumber(f.Km)
	if err != nil || km < 0 || km > 500 {
		return cardioLog{}, "La distancia no es válida: en km, por ejemplo 5,2."
	}
	hr, ok1 := intField(f.HR)
	kcal, ok2 := intField(f.Kcal)
	rpe, ok3 := intField(f.RPE)
	switch {
	case !ok1:
		return cardioLog{}, "El pulso medio va en pulsaciones por minuto, por ejemplo 145."
	case !ok2:
		return cardioLog{}, "Las kcal van sin decimales, por ejemplo 320."
	case !ok3:
		return cardioLog{}, "El esfuerzo va del 1 al 10."
	}
	c := cardioLog{Start: start, Source: "web", Type: t.Key, Seconds: sec, Meters: math.Round(km * 1000), HR: hr, Kcal: kcal, RPE: rpe, Note: f.Note}
	if err := c.check(); err != nil {
		return cardioLog{}, sentence(err)
	}
	return c, ""
}

func (w *web) cardioPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	r.Body = http.MaxBytesReader(rw, r.Body, 8<<10)
	if r.ParseForm() != nil {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	get := func(k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }
	f := cardioForm{Type: get("tipo"), Date: get("fecha"), Time: get("hora"), Dur: get("duracion"), Km: get("distancia"),
		HR: get("pulso"), Kcal: get("kcal"), RPE: get("esfuerzo"), Note: get("nota")}
	c, msg := w.cardioFrom(f)
	if msg != "" {
		w.renderCardio(rw, r, who, pageData{Error: msg}, f, nil)
		return
	}
	if _, err := w.svc.store.addCardio(r.Context(), who.User, c); err != nil {
		w.fail(rw, "cardio", err)
		return
	}
	back(rw, r, "/cardio", "cardio")
}

func (w *web) cardioDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	err := w.svc.store.deleteCardio(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "cardio borrar", err)
		return
	}
	back(rw, r, "/cardio", "cardio-borrado")
}
