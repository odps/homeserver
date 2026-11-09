package main

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Gráficas en SVG hechas en el servidor (como en Cartera y Entreno): la
// plantilla solo pinta coordenadas ya calculadas. Sin JavaScript.

func fx1(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

type svgText struct {
	X, Y   string
	Text   string
	Anchor string
}

// ---- Calendario de un hábito ----

type heatCell struct {
	X, Y  int
	Class string // c-done, c-part, c-miss, c-off, c-today, c-none
	Title string
	Day   string // AAAA-MM-DD; vacío en los días futuros (sin enlace)
	On    bool   // el día elegido
}

type heatmap struct {
	W, H   int
	Cells  []heatCell
	Months []svgText
	Days   []svgText
	Label  string
}

const (
	heatCellSize = 13
	heatStep     = 16
	heatLeft     = 20
	heatTop      = 16
)

// buildHeatmap es el calendario de las últimas weeks semanas (columnas de
// lunes a domingo), con cada día enlazado para cambiarlo.
func buildHeatmap(h habit, m marks, today, selected time.Time, weeks int) *heatmap {
	hm := &heatmap{W: heatLeft + weeks*heatStep, H: heatTop + 7*heatStep, Label: "Calendario de las últimas " + strconv.Itoa(weeks) + " semanas"}
	first := weekStart(today).Add(-time.Duration(weeks-1) * 7 * dayUnit)
	start := h.startDay(m)
	lastLabel := -10
	for col := range weeks {
		for row := range 7 {
			d := first.Add(time.Duration(col*7+row) * dayUnit)
			c := heatCell{X: heatLeft + col*heatStep, Y: heatTop + row*heatStep, On: d.Equal(selected)}
			v, marked := m[isoDay(d)]
			when := shortDate(d, today)
			if d.Day() == 1 && col-lastLabel >= 3 {
				hm.Months = append(hm.Months, svgText{X: strconv.Itoa(c.X), Y: "11", Text: months[d.Month()-1][:3], Anchor: "start"})
				lastLabel = col
			}
			switch {
			case d.After(today):
				c.Class = "c-none"
				hm.Cells = append(hm.Cells, c)
				continue
			case h.done(m, d):
				c.Class, c.Title = "c-done", when+": hecho"
				if h.Kind == kindAmount {
					c.Title = when + ": " + trimNum(v, 2) + " de " + h.goalText()
				}
			case marked && v > 0:
				c.Class, c.Title = "c-part", when+": "+trimNum(v, 2)+" de "+h.goalText()
			case d.Before(start):
				c.Class, c.Title = "c-none", when
			case d.Equal(today):
				c.Class, c.Title = "c-today", when+": aún sin hacer"
			case !h.scheduled(d) || h.weekly():
				c.Class, c.Title = "c-off", when+": no tocaba"
				if h.weekly() {
					c.Title = when + ": sin hacer"
				}
			default:
				c.Class, c.Title = "c-miss", when+": sin hacer"
			}
			c.Day = isoDay(d)
			hm.Cells = append(hm.Cells, c)
		}
	}
	for row, wd := range weekOrder {
		if row%2 == 0 && row < 6 {
			hm.Days = append(hm.Days, svgText{X: "0", Y: strconv.Itoa(heatTop + row*heatStep + 10), Text: shortDays[wd], Anchor: "start"})
		}
	}
	return hm
}

// ---- Ánimo, energía y sueño de un mes ----

type dot struct {
	X, Y  string
	Title string
}

type bar struct {
	X, Y, W, H string
	Title      string
}

type moodChart struct {
	W, H                 int
	X0, X1               int
	Mood, Energy         []string // tramos de días seguidos (puntos de polilínea)
	MoodDots, EnergyDots []dot
	YTicks               []svgText
	XTicks               []svgText
	Grid                 []string // y de las líneas de la rejilla
	HasEnergy            bool

	SleepH      int
	Bars        []bar
	SleepTicks  []svgText
	SleepXTicks []svgText
	SleepGrid   []string
	SleepBottom int
	HasSleep    bool
}

// buildMoodChart dibuja el ánimo y la energía (de 1 a 5, en líneas que se
// cortan los días sin dato) y el sueño (en barras) de un mes.
func buildMoodChart(entries []entry, month time.Time) *moodChart {
	n := month.AddDate(0, 1, -1).Day()
	const W, H, padL, padR, padT, padB = 480, 170, 30, 10, 12, 26
	const SH, sPadT, sPadB = 130, 10, 26
	c := &moodChart{W: W, H: H, X0: padL, X1: W - padR, SleepH: SH, SleepBottom: SH - sPadB}
	slot := float64(W-padL-padR) / float64(n)
	x := func(day int) float64 { return padL + (float64(day)-0.5)*slot }
	y := func(v float64) float64 { return padT + (5-v)/4*float64(H-padT-padB) }
	byDay := map[int]entry{}
	maxSleep := 10.0
	for _, e := range entries {
		if e.Day.Year() == month.Year() && e.Day.Month() == month.Month() {
			byDay[e.Day.Day()] = e
			if e.Sleep != nil {
				maxSleep = math.Max(maxSleep, math.Ceil(*e.Sleep))
			}
		}
	}
	if len(byDay) == 0 {
		return nil
	}
	for v := 1; v <= 5; v++ {
		yy := fx1(y(float64(v)))
		c.Grid = append(c.Grid, yy)
		c.YTicks = append(c.YTicks, svgText{X: strconv.Itoa(padL - 8), Y: yy, Text: strconv.Itoa(v), Anchor: "end"})
	}
	for _, d := range []int{1, 8, 15, 22, n} {
		anchor := "middle"
		if d == n && n-22 < 4 {
			continue
		}
		c.XTicks = append(c.XTicks, svgText{X: fx1(x(d)), Y: strconv.Itoa(H - 8), Text: strconv.Itoa(d), Anchor: anchor})
		c.SleepXTicks = append(c.SleepXTicks, svgText{X: fx1(x(d)), Y: strconv.Itoa(SH - 8), Text: strconv.Itoa(d), Anchor: anchor})
	}
	line := func(get func(entry) int, dots *[]dot, what string) []string {
		var segs []string
		var cur []string
		for d := 1; d <= n; d++ {
			e, ok := byDay[d]
			if !ok || get(e) == 0 {
				if len(cur) > 1 {
					segs = append(segs, strings.Join(cur, " "))
				}
				cur = nil
				continue
			}
			px, py := fx1(x(d)), fx1(y(float64(get(e))))
			cur = append(cur, px+","+py)
			*dots = append(*dots, dot{X: px, Y: py, Title: strconv.Itoa(d) + ": " + what + " " + strconv.Itoa(get(e))})
		}
		if len(cur) > 1 {
			segs = append(segs, strings.Join(cur, " "))
		}
		return segs
	}
	c.Mood = line(func(e entry) int { return e.Mood }, &c.MoodDots, "ánimo")
	c.Energy = line(func(e entry) int { return e.Energy }, &c.EnergyDots, "energía")
	c.HasEnergy = len(c.EnergyDots) > 0

	sy := func(v float64) float64 { return sPadT + (maxSleep-v)/maxSleep*float64(SH-sPadT-sPadB) }
	step := 2.0
	if maxSleep > 12 {
		step = 4
	}
	for v := 0.0; v <= maxSleep+0.01; v += step {
		yy := fx1(sy(v))
		c.SleepGrid = append(c.SleepGrid, yy)
		c.SleepTicks = append(c.SleepTicks, svgText{X: strconv.Itoa(padL - 8), Y: yy, Text: strconv.Itoa(int(v)), Anchor: "end"})
	}
	bw := math.Max(slot*0.62, 2)
	for d := 1; d <= n; d++ {
		e, ok := byDay[d]
		if !ok || e.Sleep == nil {
			continue
		}
		top := sy(*e.Sleep)
		c.Bars = append(c.Bars, bar{X: fx1(x(d) - bw/2), Y: fx1(top), W: fx1(bw), H: fx1(math.Max(float64(c.SleepBottom)-top, 0.5)),
			Title: strconv.Itoa(d) + ": " + trimNum(*e.Sleep, 2) + " h"})
	}
	c.HasSleep = len(c.Bars) > 0
	return c
}
