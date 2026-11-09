package main

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Gráficas en SVG hechas en el servidor (como en Cartera): la plantilla solo
// pinta coordenadas ya calculadas. Sin JavaScript ni librerías.

type chartTick struct {
	X, Y   string
	Label  string
	Anchor string
}

type chartDot struct {
	X, Y  string
	Title string
}

// lineChart es la del consumo de cada tramo entre llenados.
type lineChart struct {
	W, H    int
	Label   string
	Line    string
	Avg     string // línea de la media
	AvgY    string
	Dots    []chartDot
	YTicks  []chartTick
	XTicks  []chartTick
	X0, X1  int
	YBottom int
}

type pt struct {
	T     time.Time
	V     float64
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

// tickLabel escribe un valor del eje con los decimales que pide el paso.
func tickLabel(v, step float64) string {
	dec := 0
	if step < 1 {
		dec = min(int(math.Ceil(-math.Log10(step))), 3)
	}
	return num(v, dec)
}

// buildLine dibuja los puntos (como mucho los últimos 60) con la media.
func buildLine(points []pt, avg float64, label string, today time.Time) *lineChart {
	if len(points) > 60 {
		points = points[len(points)-60:]
	}
	if len(points) < 2 {
		return nil
	}
	const W, H, padL, padR, padT, padB = 480, 200, 44, 14, 14, 30
	c := &lineChart{W: W, H: H, Label: label, X0: padL, X1: W - padR, YBottom: H - padB}
	lo, hi := points[0].V, points[0].V
	for _, p := range points {
		lo, hi = math.Min(lo, p.V), math.Max(hi, p.V)
	}
	if avg > 0 {
		lo, hi = math.Min(lo, avg), math.Max(hi, avg)
	}
	if hi-lo < 0.5 {
		lo, hi = lo-0.5, hi+0.5
	}
	step := niceStep((hi - lo) / 4)
	lo = math.Max(0, math.Floor(lo/step)*step)
	hi = math.Ceil(hi/step) * step
	t0, t1 := points[0].T, points[len(points)-1].T
	span := t1.Sub(t0).Seconds()
	// Por fecha si están repartidos; si no (varios el mismo día), por orden.
	x := func(i int, t time.Time) float64 {
		if span <= 0 {
			return padL + float64(i)/float64(len(points)-1)*float64(W-padL-padR)
		}
		return padL + t.Sub(t0).Seconds()/span*float64(W-padL-padR)
	}
	y := func(v float64) float64 { return padT + (hi-v)/(hi-lo)*float64(H-padT-padB) }
	var b strings.Builder
	for i, p := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		px, py := fx2(x(i, p.T)), fx2(y(p.V))
		b.WriteString(px + "," + py)
		c.Dots = append(c.Dots, chartDot{X: px, Y: py, Title: p.Title})
	}
	c.Line = b.String()
	if avg > 0 {
		c.AvgY = fx2(y(avg))
		c.Avg = "M" + strconv.Itoa(padL) + " " + c.AvgY + "H" + strconv.Itoa(W-padR)
	}
	for v := lo; v <= hi+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 6), Y: fx2(y(v)), Label: tickLabel(v, step), Anchor: "end"})
	}
	n := min(4, len(points))
	for i := 0; i < n; i++ {
		j := i * (len(points) - 1) / max(n-1, 1)
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case n - 1:
			anchor = "end"
		}
		c.XTicks = append(c.XTicks, chartTick{X: fx2(x(j, points[j].T)), Y: strconv.Itoa(H - 8), Label: shortDate(points[j].T, today), Anchor: anchor})
	}
	return c
}

// barChart es la del gasto de cada mes: combustible abajo y lo demás
// encima.
type barChart struct {
	W, H    int
	Label   string
	Bars    []bar
	YTicks  []chartTick
	X0, X1  int
	YBottom int
}

type bar struct {
	X, W           string
	FuelY, FuelH   string
	OtherY, OtherH string
	LabelX, LabelY string
	Label          string
	Title          string
	Now            bool // el mes actual
}

func buildBars(ms []monthCost, label string) *barChart {
	top := 0.0
	for _, m := range ms {
		top = math.Max(top, m.total())
	}
	if top <= 0 {
		return nil
	}
	const W, H, padL, padR, padT, padB = 480, 200, 44, 6, 14, 30
	c := &barChart{W: W, H: H, Label: label, X0: padL, X1: W - padR, YBottom: H - padB}
	step := niceStep(top / 4)
	hi := math.Ceil(top/step) * step
	y := func(v float64) float64 { return padT + (hi-v)/hi*float64(H-padT-padB) }
	for v := 0.0; v <= hi+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 6), Y: fx2(y(v)), Label: num(v, 0), Anchor: "end"})
	}
	slot := float64(W-padL-padR) / float64(len(ms))
	bw := slot * 0.62
	for i, m := range ms {
		x := padL + slot*float64(i) + (slot-bw)/2
		b := bar{X: fx2(x), W: fx2(bw), LabelX: fx2(x + bw/2), LabelY: strconv.Itoa(H - 8), Label: months[m.Month.Month()-1][:3],
			Title: monthLabel(m.Month) + ": " + euros(m.total()) + " (combustible " + euros(m.Fuel) + ", otros " + euros(m.Other) + ")",
			Now:   i == len(ms)-1}
		yb := y(0)
		yf := y(m.Fuel)
		yo := y(m.Fuel + m.Other)
		b.FuelY, b.FuelH = fx2(yf), fx2(yb-yf)
		b.OtherY, b.OtherH = fx2(yo), fx2(yf-yo)
		c.Bars = append(c.Bars, b)
	}
	return c
}
