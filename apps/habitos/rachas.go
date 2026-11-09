package main

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Las cuentas de los hábitos, sin base de datos: qué días tocan, cuándo un
// día está hecho, las rachas (actual y mejor), el porcentaje cumplido y las
// relaciones con el diario. Las hace el programa; la IA solo las cuenta.

// Tipos de hábito.
const (
	kindYesNo  = "si_no"    // hecho o no
	kindAmount = "cantidad" // con objetivo: 8 vasos, 20 min
)

// Frecuencias.
const (
	freqDaily  = "diaria" // todos los días
	freqDays   = "dias"   // días concretos de la semana
	freqWeekly = "semana" // N veces por semana, cualquier día
)

const eps = 1e-9

type freq struct {
	Kind  string `json:"tipo"`
	Days  []int  `json:"dias,omitempty"`  // freqDays: 0 domingo … 6 sábado
	Times int    `json:"veces,omitempty"` // freqWeekly: veces por semana
}

// habitData es lo cifrado de cada hábito.
type habitData struct {
	Name   string  `json:"nombre"`
	Emoji  string  `json:"emoji,omitempty"`
	Kind   string  `json:"tipo"`
	Target float64 `json:"objetivo,omitempty"` // cantidad: lo que hay que llegar a hacer al día
	Unit   string  `json:"unidad,omitempty"`   // vasos, min, páginas
	Freq   freq    `json:"frecuencia"`
	Remind string  `json:"recordatorio,omitempty"` // HH:MM; vacío, sin recordatorio
}

type habit struct {
	ID         int64
	Archived   bool
	Created    time.Time
	RemindedOn time.Time
	habitData
}

// marks es lo hecho cada día de un hábito: día (AAAA-MM-DD) → cantidad. En
// los de sí o no, 1.
type marks map[string]float64

// target es lo que hay que hacer un día para darlo por hecho.
func (h habit) target() float64 {
	if h.Kind == kindAmount && h.Target > 0 {
		return h.Target
	}
	return 1
}

func (h habit) done(m marks, d time.Time) bool {
	v, ok := m[isoDay(d)]
	return ok && v >= h.target()-eps
}

// scheduled dice si el hábito toca ese día. Los semanales tocan cualquier
// día (hasta llegar a las veces de la semana).
func (h habit) scheduled(d time.Time) bool {
	if h.Freq.Kind == freqDays {
		return slices.Contains(h.Freq.Days, int(d.Weekday()))
	}
	return true
}

func (h habit) weekly() bool { return h.Freq.Kind == freqWeekly }

func (h habit) times() int { return max(h.Freq.Times, 1) }

// startDay es desde cuándo cuenta: el día en que se creó o el de la marca
// más antigua, si se apuntaron días de antes.
func (h habit) startDay(m marks) time.Time {
	start := dayOf(h.Created)
	for k := range m {
		if d, err := parseDay(k); err == nil && d.Before(start) {
			start = d
		}
	}
	return start
}

// streakUnit es en qué se cuenta la racha.
func (h habit) streakUnit(n int) string {
	if h.weekly() {
		return plural(n, "semana", "semanas")
	}
	return plural(n, "día", "días")
}

// weekCount son los días hechos de la semana que empieza en w, hasta today.
func (h habit) weekCount(m marks, w, today time.Time) int {
	n := 0
	for i := range 7 {
		if d := w.Add(time.Duration(i) * dayUnit); !d.After(today) && h.done(m, d) {
			n++
		}
	}
	return n
}

// streaks da la racha actual y la mejor. En los diarios y de días
// concretos cuentan los días que tocan seguidos y hechos (los que no tocan
// ni suman ni cortan); en los semanales, las semanas seguidas en que se llegó
// a las veces. Hoy (o esta semana) sin hacer todavía no corta la racha: aún
// hay tiempo.
func streaks(h habit, m marks, today time.Time) (cur, best int) {
	start := h.startDay(m)
	if start.After(today) {
		return 0, 0
	}
	run := 0
	if h.weekly() {
		last := weekStart(today)
		for w := weekStart(start); !w.After(last); w = w.Add(7 * dayUnit) {
			if h.weekCount(m, w, today) >= h.times() {
				run++
				best = max(best, run)
			} else if !w.Equal(last) {
				run = 0
			}
		}
		return run, best
	}
	for d := start; !d.After(today); d = d.Add(dayUnit) {
		if !h.scheduled(d) {
			continue
		}
		if h.done(m, d) {
			run++
			best = max(best, run)
		} else if !d.Equal(today) {
			run = 0
		}
	}
	return run, best
}

// ratio es lo cumplido de lo previsto en un periodo.
type ratio struct{ Done, Total int }

func (r ratio) pct() (float64, bool) {
	if r.Total == 0 {
		return 0, false
	}
	return float64(r.Done) / float64(r.Total) * 100, true
}

func (r ratio) add(o ratio) ratio { return ratio{r.Done + o.Done, r.Total + o.Total} }

// completion es lo cumplido entre from y to (sin pasar de hoy ni empezar
// antes que el hábito). Diarios y de días concretos: días hechos de los que
// tocaban; hoy solo cuenta si ya está hecho. Semanales: veces hechas (como
// mucho las de la semana) de las previstas, por semanas que tocan el
// periodo; la semana en curso solo cuenta si ya se ha cumplido.
func completion(h habit, m marks, from, to, today time.Time) ratio {
	start := h.startDay(m)
	if from.Before(start) {
		from = start
	}
	if to.After(today) {
		to = today
	}
	var r ratio
	if h.weekly() {
		n := h.times()
		cur := weekStart(today)
		for w := weekStart(from); !w.After(to); w = w.Add(7 * dayUnit) {
			c := h.weekCount(m, w, today)
			// La semana en curso y la de antes de empezar, si no se
			// cumplieron, no cuentan: no había tiempo.
			if c < n && (w.Equal(cur) || w.Before(start)) {
				continue
			}
			r = r.add(ratio{min(c, n), n})
		}
		return r
	}
	for d := from; !d.After(to); d = d.Add(dayUnit) {
		if !h.scheduled(d) {
			continue
		}
		switch {
		case h.done(m, d):
			r = r.add(ratio{1, 1})
		case !d.Equal(today):
			r = r.add(ratio{0, 1})
		}
	}
	return r
}

// habitStats es lo calculado de un hábito para mostrarlo.
type habitStats struct {
	Current, Best  int
	WeekDone       int     // hechos esta semana
	WeekTotal      int     // previstos esta semana (semanales: las veces)
	Month, Last30  ratio   // este mes y los últimos 30 días
	Today          float64 // cantidad apuntada hoy
	DoneToday      bool
	DueToday       bool // toca hoy y no está hecho (semanales: aún faltan veces)
	ScheduledToday bool // toca hoy (semanales: si aún faltan veces o ya se hizo hoy)
}

func statsOf(h habit, m marks, today time.Time) habitStats {
	s := habitStats{Today: m[isoDay(today)], DoneToday: h.done(m, today)}
	s.Current, s.Best = streaks(h, m, today)
	w := weekStart(today)
	if h.weekly() {
		s.WeekDone, s.WeekTotal = h.weekCount(m, w, today), h.times()
		s.ScheduledToday = s.WeekDone < s.WeekTotal || s.DoneToday
	} else {
		for i := range 7 {
			d := w.Add(time.Duration(i) * dayUnit)
			if h.scheduled(d) {
				s.WeekTotal++
				if !d.After(today) && h.done(m, d) {
					s.WeekDone++
				}
			}
		}
		s.ScheduledToday = h.scheduled(today)
	}
	s.DueToday = s.ScheduledToday && !s.DoneToday
	s.Month = completion(h, m, monthStart(today), today, today)
	s.Last30 = completion(h, m, today.Add(-29*dayUnit), today, today)
	return s
}

// freqText describe la frecuencia: «Cada día», «L, X y V», «3 veces por
// semana».
func freqText(f freq) string {
	switch f.Kind {
	case freqDays:
		var names []string
		for _, wd := range weekOrder {
			if slices.Contains(f.Days, wd) {
				names = append(names, shortDays[wd])
			}
		}
		switch len(names) {
		case 0:
			return "Ningún día"
		case 1:
			return "Los " + plWeekday(names[0])
		case 7:
			return "Cada día"
		}
		return strings.Join(names[:len(names)-1], ", ") + " y " + names[len(names)-1]
	case freqWeekly:
		if f.Times == 1 {
			return "1 vez por semana"
		}
		return strconv.Itoa(max(f.Times, 1)) + " veces por semana"
	}
	return "Cada día"
}

// plWeekday es «lunes», «sábados» a partir de la inicial.
func plWeekday(initial string) string {
	for i, s := range shortDays {
		if s == initial {
			name := weekdays[i]
			if i == 0 || i == 6 {
				name += "s"
			}
			return name
		}
	}
	return initial
}

// goalText es «8 vasos» o «hecho».
func (h habit) goalText() string {
	if h.Kind != kindAmount {
		return ""
	}
	return strings.TrimSpace(trimNum(h.target(), 2) + " " + h.Unit)
}

// label es el nombre con su emoji delante.
func (h habit) label() string {
	if h.Emoji != "" {
		return h.Emoji + " " + h.Name
	}
	return h.Name
}

// ---- Diario ----

// entryData es lo cifrado de cada día del diario.
type entryData struct {
	Mood   int      `json:"animo,omitempty"`   // 1–5; 0, sin dato
	Energy int      `json:"energia,omitempty"` // 1–5; 0, sin dato
	Sleep  *float64 `json:"sueno,omitempty"`   // horas
	Note   string   `json:"nota,omitempty"`
	Tags   []string `json:"etiquetas,omitempty"`
	Origin string   `json:"origen,omitempty"` // web o asistente
}

type entry struct {
	Day     time.Time
	Updated time.Time
	entryData
}

func (e entryData) empty() bool {
	return e.Mood == 0 && e.Energy == 0 && e.Sleep == nil && strings.TrimSpace(e.Note) == "" && len(e.Tags) == 0
}

// avg es una media que se va sumando.
type avg struct {
	Sum float64
	N   int
}

func (a *avg) add(v float64) { a.Sum += v; a.N++ }

func (a avg) value() (float64, bool) {
	if a.N == 0 {
		return math.NaN(), false
	}
	return a.Sum / float64(a.N), true
}

// corr compara, de un hábito, los días hechos con los que tocaban y no se
// hicieron: el ánimo, la energía y el sueño medios de cada grupo.
type corr struct {
	Habit                   habit
	MoodDone, MoodOther     avg
	EnergyDone, EnergyOther avg
	SleepDone, SleepOther   avg
}

// minCorrDays son los días que hacen falta en cada grupo para enseñarlo.
const minCorrDays = 3

func (c corr) enough() bool { return c.MoodDone.N >= minCorrDays && c.MoodOther.N >= minCorrDays }

// correlations cruza los hábitos con el diario entre from y today. Solo
// cuentan los días con ánimo apuntado y, en los de días concretos, los que
// tocaban. Sin IA: medias y ya.
func correlations(habits []habit, all map[int64]marks, entries []entry, from, today time.Time) []corr {
	var out []corr
	for _, h := range habits {
		m := all[h.ID]
		start := h.startDay(m)
		c := corr{Habit: h}
		for _, e := range entries {
			if e.Mood == 0 || e.Day.Before(from) || e.Day.Before(start) || e.Day.After(today) || !h.scheduled(e.Day) {
				continue
			}
			if e.Day.Equal(today) && !h.done(m, e.Day) {
				continue // hoy aún puede hacerse
			}
			mood, energy, sleep := &c.MoodOther, &c.EnergyOther, &c.SleepOther
			if h.done(m, e.Day) {
				mood, energy, sleep = &c.MoodDone, &c.EnergyDone, &c.SleepDone
			}
			mood.add(float64(e.Mood))
			if e.Energy > 0 {
				energy.add(float64(e.Energy))
			}
			if e.Sleep != nil {
				sleep.add(*e.Sleep)
			}
		}
		out = append(out, c)
	}
	return out
}

// sleepMood es el ánimo medio los días que se durmió al menos hours y los
// que menos.
func sleepMood(entries []entry, hours float64, from time.Time) (enough, little avg) {
	for _, e := range entries {
		if e.Mood == 0 || e.Sleep == nil || e.Day.Before(from) {
			continue
		}
		if *e.Sleep >= hours {
			enough.add(float64(e.Mood))
		} else {
			little.add(float64(e.Mood))
		}
	}
	return enough, little
}
