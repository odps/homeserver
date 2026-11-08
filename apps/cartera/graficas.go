package main

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Gráficas en SVG hechas en el servidor (como en Entreno): la plantilla solo
// pinta coordenadas ya calculadas. Sin JavaScript ni librerías.

type chartTick struct {
	X, Y   string
	Label  string
	Anchor string
}

// chartMark es una compra (▲, debajo de la línea) o una venta (▼, encima).
type chartMark struct {
	Path  string
	Buy   bool
	Title string
}

type lineChart struct {
	W, H    int
	Label   string
	Line    string // polilínea del precio o del valor
	Area    string // el área bajo la línea
	Cost    string // la de lo invertido (solo la de la cartera)
	YTicks  []chartTick
	XTicks  []chartTick
	Marks   []chartMark
	X0, X1  int
	YBottom int
	LastX   string
	LastY   string
	Down    bool // acaba por debajo de donde empezó
	From    string
	Change  string // «+12,3 % en el periodo»
}

type pt struct {
	T time.Time
	V float64
}

type markIn struct {
	T     time.Time
	Buy   bool
	Title string
}

func fx2(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

// niceStep es un paso redondo (1, 2, 2,5 o 5 por potencia de 10).
func niceStep(raw float64) float64 {
	if raw <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*p >= raw {
			return m * p
		}
	}
	return 10 * p
}

// thin deja como mucho n puntos (siempre el último), para que el SVG no pese.
func thin(p []pt, n int) []pt {
	if len(p) <= n {
		return p
	}
	k := int(math.Ceil(float64(len(p)) / float64(n)))
	var out []pt
	for i := 0; i < len(p); i += k {
		out = append(out, p[i])
	}
	if last := p[len(p)-1]; !out[len(out)-1].T.Equal(last.T) {
		out = append(out, last)
	}
	return out
}

// tickLabel escribe un valor del eje con los decimales que pide el paso.
func tickLabel(v, step float64) string {
	dec := 0
	if step < 1 {
		dec = min(int(math.Ceil(-math.Log10(step))), 8)
	}
	return num(v, dec)
}

// buildChart dibuja una línea (y, si hay, la del coste) con sus ejes y las
// marcas de las operaciones.
func buildChart(points, cost []pt, marks []markIn, label string, today time.Time) *lineChart {
	points = thin(points, 400)
	if len(points) < 2 {
		return nil
	}
	cost = thin(cost, 400)
	const W, H, padL, padR, padT, padB = 480, 220, 58, 14, 14, 30
	c := &lineChart{W: W, H: H, Label: label, X0: padL, X1: W - padR, YBottom: H - padB}
	lo, hi := points[0].V, points[0].V
	for _, p := range append(append([]pt{}, points...), cost...) {
		lo, hi = math.Min(lo, p.V), math.Max(hi, p.V)
	}
	if hi-lo < math.Abs(hi)*0.002 || hi == lo {
		pad := math.Max(math.Abs(hi)*0.01, 0.01)
		lo, hi = lo-pad, hi+pad
	}
	step := niceStep((hi - lo) / 4)
	lo = math.Floor(lo/step) * step
	hi = math.Ceil(hi/step) * step
	t0, t1 := points[0].T, points[len(points)-1].T
	span := t1.Sub(t0).Seconds()
	if span <= 0 {
		span = 1
	}
	x := func(t time.Time) float64 { return padL + t.Sub(t0).Seconds()/span*float64(W-padL-padR) }
	y := func(v float64) float64 { return padT + (hi-v)/(hi-lo)*float64(H-padT-padB) }
	line := func(ps []pt) string {
		var b strings.Builder
		for i, p := range ps {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(fx2(x(p.T)) + "," + fx2(y(p.V)))
		}
		return b.String()
	}
	c.Line = line(points)
	c.Area = fx2(x(t0)) + "," + strconv.Itoa(c.YBottom) + " " + c.Line + " " + fx2(x(t1)) + "," + strconv.Itoa(c.YBottom)
	if len(cost) >= 2 {
		c.Cost = line(cost)
	}
	last := points[len(points)-1]
	c.LastX, c.LastY = fx2(x(last.T)), fx2(y(last.V))
	c.Down = last.V < points[0].V
	if points[0].V > 0 {
		c.Change = pct((last.V/points[0].V-1)*100) + " en el periodo"
	}
	c.From = shortDate(t0, today)
	for v := lo; v <= hi+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 6), Y: fx2(y(v)), Label: tickLabel(v, step), Anchor: "end"})
	}
	days := t1.Sub(t0).Hours() / 24
	n := 4
	for i := 0; i < n; i++ {
		t := t0.Add(time.Duration(float64(t1.Sub(t0)) * float64(i) / float64(n-1)))
		lbl := shortDate(t, today)
		if days > 200 {
			lbl = months[t.Month()-1][:3] + " " + strconv.Itoa(t.Year())
		}
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case n - 1:
			anchor = "end"
		}
		c.XTicks = append(c.XTicks, chartTick{X: fx2(x(t)), Y: strconv.Itoa(H - 8), Label: lbl, Anchor: anchor})
	}
	// Las marcas, sobre el punto de la línea de ese día (o el más cercano).
	for _, m := range marks {
		if m.T.Before(t0) || m.T.After(t1) {
			continue
		}
		v := points[0].V
		for _, p := range points {
			if p.T.After(m.T) {
				break
			}
			v = p.V
		}
		mx, my := x(m.T), y(v)
		var path string
		if m.Buy {
			path = "M" + fx2(mx) + " " + fx2(my+5) + "l5 9h-10z"
		} else {
			path = "M" + fx2(mx) + " " + fx2(my-5) + "l5 -9h-10z"
		}
		c.Marks = append(c.Marks, chartMark{Path: path, Buy: m.Buy, Title: m.Title})
	}
	return c
}

// ---- Reparto ----

type shareRow struct {
	Name  string
	Pct   string
	Value string
	W     string // ancho de la barra sobre 100
}

func shareRows(list []share) []shareRow {
	var out []shareRow
	for _, s := range list {
		out = append(out, shareRow{Name: s.Key, Pct: num(s.Pct, 1) + " %", Value: euros(s.Value), W: fx2(math.Max(s.Pct, 0.5))})
	}
	return out
}

// ---- Periodos ----

type rangeOpt struct {
	Key, Label string
	On         bool
}

var ranges = []struct {
	Key, Label string
	Days       int
}{{"30", "30 d", 30}, {"90", "90 d", 90}, {"365", "1 año", 365}, {"todo", "Todo", 0}}

// pickRange lee el periodo pedido (por defecto, 90 días) y da desde qué día.
func pickRange(r string, today time.Time) (string, time.Time, []rangeOpt) {
	key, days := "90", 90
	for _, x := range ranges {
		if x.Key == r {
			key, days = x.Key, x.Days
		}
	}
	from := time.Time{}
	if days > 0 {
		from = today.Add(-time.Duration(days) * dayUnit)
	}
	var opts []rangeOpt
	for _, x := range ranges {
		opts = append(opts, rangeOpt{Key: x.Key, Label: x.Label, On: x.Key == key})
	}
	return key, from, opts
}
