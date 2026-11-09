package main

import (
	"math"
	"strconv"
	"time"
)

// Gráficas en SVG hechas en el servidor (como en Cartera): la plantilla solo
// pinta coordenadas ya calculadas. Sin JavaScript ni librerías.

type chartTick struct {
	X, Y   string
	Label  string
	Anchor string
}

type chartBar struct {
	X, Y, W, H string
	Class      string // exp (gastos) o inc (ingresos); now, el mes que se ve
	Title      string
}

// barChart son los gastos e ingresos de varios meses, en barras.
type barChart struct {
	W, H    int
	Label   string
	Bars    []chartBar
	YTicks  []chartTick
	XTicks  []chartTick
	X0, X1  int
	YBottom int
}

func fx1(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

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

// monthsChart dibuja los gastos y los ingresos de cada mes. Sin nada en
// ninguno, nil.
func monthsChart(hist []monthTotal, current, today time.Time) *barChart {
	hi := 0.0
	for _, h := range hist {
		hi = math.Max(hi, math.Max(h.Expenses, h.Income))
	}
	if hi <= 0 || len(hist) == 0 {
		return nil
	}
	const W, H, padL, padR, padT, padB = 480, 200, 52, 8, 12, 28
	c := &barChart{W: W, H: H, Label: "Gastos e ingresos de los últimos meses, en euros", X0: padL, X1: W - padR, YBottom: H - padB}
	step := niceStep(hi / 4)
	top := math.Ceil(hi/step) * step
	y := func(v float64) float64 { return padT + (top-v)/top*float64(H-padT-padB) }
	for v := 0.0; v <= top+step/2; v += step {
		c.YTicks = append(c.YTicks, chartTick{X: strconv.Itoa(padL - 6), Y: fx1(y(v)), Label: tickLabel(v), Anchor: "end"})
	}
	slot := float64(W-padL-padR) / float64(len(hist))
	bw := math.Min(slot*0.34, 26)
	for i, h := range hist {
		mid := padL + slot*(float64(i)+0.5)
		now := ""
		if h.Month.Equal(current) {
			now = " now"
		}
		for j, b := range []struct {
			v     float64
			class string
			what  string
		}{{h.Expenses, "exp", "gastos"}, {h.Income, "inc", "ingresos"}} {
			x := mid - bw - 1 + float64(j)*(bw+2)
			top := y(b.v)
			hgt := float64(c.YBottom) - top
			if b.v > 0 && hgt < 1.5 {
				top, hgt = float64(c.YBottom)-1.5, 1.5
			}
			if b.v <= 0 {
				continue
			}
			c.Bars = append(c.Bars, chartBar{X: fx1(x), Y: fx1(top), W: fx1(bw), H: fx1(hgt), Class: b.class + now,
				Title: monthLabel(h.Month) + ": " + b.what + " " + euros(b.v)})
		}
		c.XTicks = append(c.XTicks, chartTick{X: fx1(mid), Y: strconv.Itoa(H - 8), Label: monthShort(h.Month, today), Anchor: "middle"})
	}
	return c
}

// tickLabel escribe un valor del eje, sin decimales: «1.500», «15 k».
func tickLabel(v float64) string {
	if v >= 10000 {
		return num(v/1000, 0) + " k"
	}
	return num(v, 0)
}

// ---- Barras de presupuesto ----

// meter es una barra de lo gastado frente al presupuesto: W es el ancho de
// lo gastado sobre 100 y Mark, dónde cae el presupuesto (vacío sin él).
type meter struct {
	W, Mark string
	Over    bool
	Plain   bool // sin presupuesto: solo comparado con lo demás
}

// newMeter compara spent con budget; sin presupuesto, con max (lo que más se
// ha gastado en una categoría ese mes).
func newMeter(spent, budget, max float64) meter {
	switch {
	case budget > 0 && spent > budget:
		return meter{W: "100", Mark: fx1(budget / spent * 100), Over: true}
	case budget > 0:
		return meter{W: fx1(math.Max(spent/budget*100, 0)), Mark: "100"}
	case max > 0:
		return meter{W: fx1(math.Max(spent/max*100, 0.5)), Plain: true}
	}
	return meter{W: "0", Plain: true}
}
