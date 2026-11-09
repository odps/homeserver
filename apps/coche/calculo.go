package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Las cuentas de un vehículo, hechas con código: consumo entre llenados,
// coste por km, gasto por meses, km estimados de hoy y cuándo toca cada
// tarea de mantenimiento.

// segment es el tramo entre dos llenados: lo echado después del primero
// (incluido el segundo, y los repostajes a medias entre ambos) es lo que se
// ha gastado en esos km.
type segment struct {
	From, To time.Time
	ToID     int64 // el repostaje que lo cierra
	Km       int
	Liters   float64
	Cost     float64
	L100     float64
}

type monthCost struct {
	Month time.Time // día 1
	Fuel  float64
	Other float64
}

func (m monthCost) total() float64 { return m.Fuel + m.Other }

// Estados de una tarea.
const (
	dueOver = "vencida"
	dueSoon = "pronto"
	dueOK   = "ok"
	dueNone = "sin-datos"
)

// Antelación de «pronto».
const (
	soonDays = 30
	soonKm   = 1000
)

// taskState es una tarea con su próximo vencimiento.
type taskState struct {
	task
	DueKm    int
	DueDay   time.Time
	KmLeft   int // con los km estimados de hoy
	DaysLeft int
	EstDay   time.Time // cuándo se llegará a DueKm, al ritmo de siempre
	Status   string
	urgency  float64 // días hasta que toque (para ordenar)
}

func (t taskState) hasKm() bool  { return t.DueKm > 0 }
func (t taskState) hasDay() bool { return !t.DueDay.IsZero() }

// vstats es todo lo calculado de un vehículo.
type vstats struct {
	Vehicle   vehicle
	Refuels   []refuel
	Expenses  []expense
	Segments  []segment
	AvgL100   float64 // 0: aún no se sabe (hacen falta dos llenados)
	LastL100  float64
	FuelPerKm float64
	AllPerKm  float64
	FuelCost  float64
	OtherCost float64
	Liters    float64
	Months    []monthCost // los últimos 12, el último es el actual
	LastKm    int         // los últimos km apuntados
	LastKmDay time.Time
	EstKm     int     // los de hoy, estimados
	KmPerDay  float64 // 0: aún no se sabe
	Tasks     []taskState
}

func (s *vstats) thisMonth() float64 { return s.Months[len(s.Months)-1].total() }

func (s *vstats) year() float64 {
	t := 0.0
	for _, m := range s.Months {
		t += m.total()
	}
	return t
}

// task busca una tarea calculada por su id.
func (s *vstats) task(id int64) (taskState, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return taskState{}, false
}

type reading struct {
	Day time.Time
	Km  int
}

// compute hace las cuentas de un vehículo con lo suyo.
func compute(v vehicle, refuels []refuel, expenses []expense, tasks []task, today time.Time) *vstats {
	s := &vstats{Vehicle: v, Refuels: refuels, Expenses: expenses}

	// Consumo: por km, para encadenar los llenados.
	byKm := append([]refuel(nil), refuels...)
	sort.SliceStable(byKm, func(i, j int) bool {
		if byKm[i].Km != byKm[j].Km {
			return byKm[i].Km < byKm[j].Km
		}
		return byKm[i].Day.Before(byKm[j].Day)
	})
	var last *refuel
	var accL, accC float64
	segKm, segL, segC := 0, 0.0, 0.0
	for i := range byKm {
		r := &byKm[i]
		s.Liters += r.Liters
		s.FuelCost += r.Total
		if last == nil {
			if r.Full {
				last = r
			}
			continue
		}
		accL += r.Liters
		accC += r.Total
		if !r.Full {
			continue
		}
		if d := r.Km - last.Km; d > 0 {
			sg := segment{From: last.Day, To: r.Day, ToID: r.ID, Km: d, Liters: accL, Cost: accC, L100: accL / float64(d) * 100}
			s.Segments = append(s.Segments, sg)
			segKm += d
			segL += accL
			segC += accC
		}
		accL, accC, last = 0, 0, r
	}
	if segKm > 0 {
		s.AvgL100 = segL / float64(segKm) * 100
		s.FuelPerKm = segC / float64(segKm)
		s.LastL100 = s.Segments[len(s.Segments)-1].L100
	}
	for _, e := range expenses {
		s.OtherCost += e.Amount
	}

	// Gasto de los últimos 12 meses.
	first := addMonths(time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), -11)
	for i := 0; i < 12; i++ {
		s.Months = append(s.Months, monthCost{Month: addMonths(first, i)})
	}
	idx := func(d time.Time) int {
		return (d.Year()-first.Year())*12 + int(d.Month()) - int(first.Month())
	}
	for _, r := range refuels {
		if i := idx(r.Day); i >= 0 && i < 12 {
			s.Months[i].Fuel += r.Total
		}
	}
	for _, e := range expenses {
		if i := idx(e.Day); i >= 0 && i < 12 {
			s.Months[i].Other += e.Amount
		}
	}

	// Km: los últimos apuntados y el ritmo, para estimar los de hoy.
	var rs []reading
	for _, r := range refuels {
		rs = append(rs, reading{r.Day, r.Km})
	}
	for _, e := range expenses {
		if e.Km > 0 {
			rs = append(rs, reading{e.Day, e.Km})
		}
	}
	for _, t := range tasks {
		if t.LastKm > 0 && !t.LastDay.IsZero() {
			rs = append(rs, reading{t.LastDay, t.LastKm})
		}
	}
	s.LastKm, s.LastKmDay = v.StartKm, dayOf(v.Created)
	for _, r := range rs {
		if r.Km > s.LastKm || (r.Km == s.LastKm && r.Day.After(s.LastKmDay)) {
			s.LastKm, s.LastKmDay = r.Km, r.Day
		}
	}
	s.KmPerDay = pace(rs, v, s.LastKmDay)
	s.EstKm = s.LastKm
	if s.KmPerDay > 0 && today.After(s.LastKmDay) {
		s.EstKm += int(math.Round(s.KmPerDay * float64(min(daysBetween(s.LastKmDay, today), 365))))
	}
	if driven := s.LastKm - v.StartKm; driven >= 100 && v.StartKm >= 0 {
		s.AllPerKm = (s.FuelCost + s.OtherCost) / float64(driven)
	}

	for _, t := range tasks {
		s.Tasks = append(s.Tasks, dueOf(t, s.EstKm, s.KmPerDay, today))
	}
	rank := map[string]int{dueOver: 0, dueSoon: 1, dueOK: 2, dueNone: 3}
	sort.SliceStable(s.Tasks, func(i, j int) bool {
		a, b := s.Tasks[i], s.Tasks[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.urgency < b.urgency
	})
	return s
}

// pace son los km al día de siempre: los del último año (o de todo, si es
// poco), con al menos dos semanas de diferencia. 0 si no se sabe.
func pace(rs []reading, v vehicle, lastDay time.Time) float64 {
	all := append([]reading(nil), rs...)
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Day.Equal(all[j].Day) {
			return all[i].Day.Before(all[j].Day)
		}
		return all[i].Km < all[j].Km
	})
	// Los km iniciales cuentan desde que se añadió el vehículo, si es antes
	// de todo lo apuntado (si se apuntan repostajes de antes, no se sabe de
	// cuándo son).
	if start := (reading{dayOf(v.Created), v.StartKm}); v.StartKm > 0 && (len(all) == 0 || (!start.Day.After(all[0].Day) && start.Km <= all[0].Km)) {
		all = append([]reading{start}, all...)
	}
	if len(all) < 2 {
		return 0
	}
	rate := func(list []reading) float64 {
		a, b := list[0], list[len(list)-1]
		days := daysBetween(a.Day, b.Day)
		if days < 14 || b.Km <= a.Km {
			return 0
		}
		return float64(b.Km-a.Km) / float64(days)
	}
	from := lastDay.AddDate(-1, 0, 0)
	var recent []reading
	for _, r := range all {
		if !r.Day.Before(from) {
			recent = append(recent, r)
		}
	}
	if len(recent) >= 2 {
		if r := rate(recent); r > 0 {
			return r
		}
	}
	return rate(all)
}

// dueOf calcula el próximo vencimiento de una tarea.
func dueOf(t task, estKm int, perDay float64, today time.Time) taskState {
	st := taskState{task: t, Status: dueNone, urgency: math.Inf(1)}
	switch {
	case t.NextKm > 0:
		st.DueKm = t.NextKm
	case t.EveryKm > 0 && t.LastKm > 0:
		st.DueKm = t.LastKm + t.EveryKm
	}
	switch {
	case !t.NextDay.IsZero():
		st.DueDay = t.NextDay
	case t.EveryMonths > 0 && !t.LastDay.IsZero():
		st.DueDay = addMonths(t.LastDay, t.EveryMonths)
	}
	if !st.hasKm() && !st.hasDay() {
		return st
	}
	st.Status = dueOK
	if st.hasKm() {
		st.KmLeft = st.DueKm - estKm
		days := float64(st.KmLeft) / 40 // sin ritmo conocido, unos 40 km al día
		if perDay > 0 {
			days = float64(st.KmLeft) / perDay
			if st.KmLeft > 0 && days < 3650 {
				st.EstDay = today.Add(time.Duration(math.Round(days)) * dayUnit)
			}
		}
		st.urgency = days
		switch {
		case st.KmLeft <= 0:
			st.Status = dueOver
		case st.KmLeft <= soonKm:
			st.Status = dueSoon
		}
	}
	if st.hasDay() {
		st.DaysLeft = daysBetween(today, st.DueDay)
		st.urgency = math.Min(st.urgency, float64(st.DaysLeft))
		switch {
		case st.DaysLeft < 0:
			st.Status = dueOver
		case st.DaysLeft <= soonDays && st.Status != dueOver:
			st.Status = dueSoon
		}
	}
	return st
}

// dueText dice cuándo toca: «el 12 dic (en 33 días)», «en 2.300 km (hacia
// mar 2027)», «pasada: van ≈150.300 km de 150.000».
func dueText(t taskState, today time.Time) string {
	var parts []string
	if t.hasDay() {
		switch {
		case t.DaysLeft < 0:
			parts = append(parts, "venció el "+shortDate(t.DueDay, today)+" ("+inDays(t.DaysLeft)+")")
		case t.DaysLeft <= 1:
			parts = append(parts, "vence "+inDays(t.DaysLeft))
		default:
			parts = append(parts, "el "+shortDate(t.DueDay, today)+" ("+inDays(t.DaysLeft)+")")
		}
	}
	if t.hasKm() {
		switch {
		case t.KmLeft <= 0:
			parts = append(parts, "a los "+kmStr(t.DueKm)+": ya van ≈"+kmStr(t.DueKm-t.KmLeft))
		case t.EstDay.IsZero():
			parts = append(parts, "a los "+kmStr(t.DueKm)+" (faltan ≈"+kmStr(t.KmLeft)+")")
		default:
			parts = append(parts, "a los "+kmStr(t.DueKm)+" (faltan ≈"+kmStr(t.KmLeft)+", hacia "+monthLabel(t.EstDay)+")")
		}
	}
	if len(parts) == 0 {
		return "Sin fecha ni km: apunta cuándo se hizo o cuándo toca"
	}
	return strings.Join(parts, " o ")
}

// everyText es «cada 15.000 km o 12 meses».
func everyText(t taskData) string {
	var parts []string
	if t.EveryKm > 0 {
		parts = append(parts, kmStr(t.EveryKm))
	}
	if t.EveryMonths > 0 {
		if t.EveryMonths == 1 {
			parts = append(parts, "1 mes")
		} else if t.EveryMonths%12 == 0 {
			y := t.EveryMonths / 12
			if y == 1 {
				parts = append(parts, "1 año")
			} else {
				parts = append(parts, num(float64(y), 0)+" años")
			}
		} else {
			parts = append(parts, num(float64(t.EveryMonths), 0)+" meses")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "cada " + strings.Join(parts, " o ")
}
