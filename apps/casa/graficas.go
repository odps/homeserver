package main

import (
	"math"
	"strconv"
	"time"
)

// Gráficas en SVG hechas en el servidor (como en Cartera y Entreno): la
// plantilla solo pinta coordenadas ya calculadas. Sin JavaScript ni
// librerías.

type chartTick struct {
	X, Y   string
	Label  string
	Anchor string
}

// chartBar es una barra: una hora del precio de la luz o un periodo entre
// dos lecturas.
type chartBar struct {
	X, Y, W, H string
	Class      string
	Title      string
}

type chartRect struct {
	X, Y, W, H string
	Title      string
}

type barChart struct {
	W, H   int
	Label  string
	Bars   []chartBar
	YTicks []chartTick
	XTicks []chartTick
	X0, X1 int
	Base   int        // y de la línea de cero
	Band   *chartRect // el bloque más barato, de fondo
	NowX   string     // la hora en curso ("" si no es hoy)
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

const (
	chartW    = 720
	chartH    = 260
	padLeft   = 46
	padRight  = 8
	padTop    = 14
	padBottom = 30
)

// yScale da la escala de 0 a un máximo redondo con 4 o 5 marcas.
func yScale(hi float64, label func(float64) string) (top float64, ticks []chartTick, y func(float64) float64) {
	if hi <= 0 {
		hi = 1
	}
	step := niceStep(hi / 4)
	top = math.Ceil(hi/step-1e-9) * step
	h := float64(chartH - padTop - padBottom)
	y = func(v float64) float64 { return float64(chartH-padBottom) - v/top*h }
	for v := 0.0; v <= top+step/2; v += step {
		ticks = append(ticks, chartTick{X: strconv.Itoa(padLeft - 6), Y: fx1(y(v)), Label: label(v), Anchor: "end"})
	}
	return top, ticks, y
}

// priceChart son las barras de los precios de un día, coloreadas por tramo
// (barata, media, cara), con la hora en curso y el bloque más barato de 3 h
// marcados.
func priceChart(l *luzDay, now time.Time) *barChart {
	if l == nil || len(l.Hours) == 0 {
		return nil
	}
	c := &barChart{W: chartW, H: chartH, X0: padLeft, X1: chartW - padRight, Base: chartH - padBottom,
		Label: "Precio de la luz por horas, " + longDate(l.Day) + ", en euros por kWh"}
	hi := math.Max(l.Hi, 0.01)
	_, ticks, y := yScale(hi, kwhShort)
	c.YTicks = ticks
	n := len(l.Hours)
	slot := float64(c.X1-c.X0) / float64(n)
	bw := slot * 0.74
	xOf := func(i int) float64 { return float64(c.X0) + float64(i)*slot }
	index := map[time.Time]int{}
	for i, h := range l.Hours {
		index[h.T] = i
		v := math.Max(h.P, 0)
		top := y(v)
		cls := level(h.P, l.CheapMax, l.DearMin)
		if l.HasNow && h.T.Equal(l.Now.T) {
			cls += " now"
			c.NowX = fx1(xOf(i) + slot/2)
		}
		c.Bars = append(c.Bars, chartBar{X: fx1(xOf(i) + (slot-bw)/2), Y: fx1(top), W: fx1(bw), H: fx1(math.Max(float64(c.Base)-top, 1)),
			Class: cls, Title: hourLabel(h.T) + ": " + kwh(h.P)})
		if hourOf(h.T)%3 == 0 && (i == 0 || hourOf(l.Hours[i-1].T) != hourOf(h.T)) {
			c.XTicks = append(c.XTicks, chartTick{X: fx1(xOf(i) + slot/2), Y: strconv.Itoa(chartH - 10), Label: strconv.Itoa(hourOf(h.T)), Anchor: "middle"})
		}
	}
	if l.Has3 {
		i, j := index[l.Block3.Start], index[l.Block3.End.Add(-time.Hour)]
		c.Band = &chartRect{X: fx1(xOf(i)), Y: strconv.Itoa(padTop - 6), W: fx1(float64(j-i+1) * slot), H: strconv.Itoa(c.Base - padTop + 6),
			Title: "Bloque más barato de 3 h: de " + l.Block3.label()}
	}
	return c
}

// usageChart son las barras de lo gastado al día entre lecturas del último
// año: cada barra ocupa el tiempo entre dos lecturas y su altura es la media
// diaria.
func usageChart(m meterView, today time.Time) *barChart {
	since := today.AddDate(-1, 0, 0)
	var ps []usage
	for _, p := range m.Periods {
		if !p.Reset && p.To.After(since) {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return nil
	}
	c := &barChart{W: chartW, H: chartH, X0: padLeft + 8, X1: chartW - padRight, Base: chartH - padBottom,
		Label: "Consumo medio al día de " + meterLabel(m.Meter) + " entre lecturas, en " + m.Unit}
	start, end := ps[0].From, ps[len(ps)-1].To
	if start.Before(since) {
		start = since
	}
	span := end.Sub(start).Hours()
	if span <= 0 {
		return nil
	}
	hi := 0.0
	for _, p := range ps {
		hi = math.Max(hi, p.PerDay)
	}
	dec := 1
	if hi < 1 {
		dec = 3
	} else if hi < 10 {
		dec = 2
	}
	_, ticks, y := yScale(hi, func(v float64) string { return trimNum(v, dec) })
	c.YTicks = ticks
	w := float64(c.X1 - c.X0)
	xOf := func(t time.Time) float64 {
		if t.Before(start) {
			t = start
		}
		return float64(c.X0) + t.Sub(start).Hours()/span*w
	}
	for _, p := range ps {
		x0, x1 := xOf(p.From), xOf(p.To)
		top := y(p.PerDay)
		c.Bars = append(c.Bars, chartBar{X: fx1(x0 + 0.6), Y: fx1(top), W: fx1(math.Max(x1-x0-1.2, 1.5)), H: fx1(math.Max(float64(c.Base)-top, 1)),
			Class: "use", Title: shortDate(p.From, today) + " – " + shortDate(p.To, today) + ": " + trimNum(p.PerDay, dec) + " " + m.Unit + " al día"})
	}
	// Marcas en el día 1 de cada mes, como mucho 6.
	var monthsIn []time.Time
	for d := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0); !d.After(end); d = d.AddDate(0, 1, 0) {
		monthsIn = append(monthsIn, d)
	}
	step := max(1, (len(monthsIn)+5)/6)
	for i := 0; i < len(monthsIn); i += step {
		d := monthsIn[i]
		c.XTicks = append(c.XTicks, chartTick{X: fx1(xOf(d)), Y: strconv.Itoa(chartH - 10), Label: months[d.Month()-1][:3], Anchor: "middle"})
	}
	if len(c.XTicks) == 0 {
		c.XTicks = []chartTick{{X: fx1(xOf(start)), Y: strconv.Itoa(chartH - 10), Label: shortDate(start, today), Anchor: "start"},
			{X: fx1(xOf(end)), Y: strconv.Itoa(chartH - 10), Label: shortDate(end, today), Anchor: "end"}}
	}
	return c
}
