package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// Progresión, sin IA: doble progresión con RIR.
//
//   - Si en la última sesión todas las series llegaron arriba del rango con
//     el RIR objetivo (o más), se sube la carga un escalón y se vuelve abajo
//     del rango.
//   - Si no, misma carga y una repetición más por serie, hasta arriba.
//   - Si fue más duro de la cuenta (RIR por debajo del objetivo o series por
//     debajo del rango), se mantiene.
//   - Estancado: tres sesiones seguidas sin mejorar el 1RM estimado (ni las
//     repeticiones) con el RIR ya en el objetivo o por debajo. Se propone una
//     semana de descarga (−10 % y una serie menos) o cambiar de variante.
//
// En tiempo y distancia se progresa en segundos o metros (+5).

// planItem es un ejercicio de la rutina de hoy, con lo que se propone.
type planItem struct {
	Item      routineItem `json:"-"`
	Key       string      `json:"key"`
	Name      string      `json:"ejercicio"`
	Kind      string      `json:"tipo"` // reps, time, distance
	Last      *done       `json:"-"`
	LastText  string      `json:"ultima_vez,omitempty"`
	Target    setLog      `json:"-"`
	Sets      int         `json:"series"`
	Goal      string      `json:"objetivo"` // «4×6 · 20 kg · RIR 2»
	Advice    string      `json:"consejo"`  // por qué
	Stalled   bool        `json:"estancado,omitempty"`
	DoneToday []setLog    `json:"-"`
	RestSec   int         `json:"descanso_s"`
}

// plan prepara la rutina para hoy: por ejercicio, lo de la última vez, lo
// propuesto y lo ya hecho hoy.
func (s *service) plan(ctx context.Context, owner string, r routine, now time.Time) ([]planItem, error) {
	custom, err := s.store.customNames(ctx, owner)
	if err != nil {
		return nil, err
	}
	today := dayOf(now)
	var todaySession *session
	if list, err := s.store.sessions(ctx, owner, today, today, 0); err == nil && len(list) > 0 {
		todaySession = &list[0]
	}
	// Las sesiones de esta rutina: un ejercicio que se hace en dos rutinas
	// (jalón 3x10 el jueves y 3x8 el sábado) progresa con lo de la suya.
	mine := map[int64]bool{}
	if recent, err := s.store.sessions(ctx, owner, today.AddDate(0, -6, 0), today.AddDate(0, 0, -1), 0); err == nil {
		for _, x := range recent {
			if overlap(r, x) >= 0.5 {
				mine[x.ID] = true
			}
		}
	}
	var out []planItem
	for _, it := range r.Items {
		p := planItem{Item: it, Key: it.Key, Name: exerciseName(it.Key, "", custom), Kind: "reps", Sets: it.Sets, RestSec: it.RestSec}
		e, ok := lookup(it.Key)
		if ok {
			p.Kind = e.Kind
		}
		hist, err := s.store.history(ctx, owner, it.Key, 0)
		if err != nil {
			return nil, err
		}
		// Lo de hoy no cuenta como «última vez»; mejor lo de esta rutina.
		var past, ofRoutine []done
		for _, d := range hist {
			if d.Day.Before(today) {
				past = append(past, d)
				if mine[d.SessionID] {
					ofRoutine = append(ofRoutine, d)
				}
			}
		}
		if len(ofRoutine) > 0 {
			past = ofRoutine
		}
		if len(past) > 6 {
			past = past[:6]
		}
		if todaySession != nil {
			for _, x := range todaySession.Exercises {
				if x.Key == it.Key {
					p.DoneToday = append(p.DoneToday, x.Sets...)
				}
			}
		}
		p.Target, p.Advice, p.Stalled = progress(it, e, past)
		if len(past) > 0 {
			p.Last = &past[0]
			p.LastText = past[0].Day.Format("2006-01-02") + ": " + summarize(past[0].Entry)
		}
		if p.Stalled {
			// Descarga: una serie menos.
			p.Sets = max(1, it.Sets-1)
		}
		p.Goal = setText(p.Sets, p.Target, false)
		out = append(out, p)
	}
	return out, nil
}

// working son las series de trabajo de una vez: las de la carga más alta
// (las de calentamiento o las bajadas no cuentan para decidir).
func working(e exerciseLog) []setLog {
	top := 0.0
	for _, st := range e.Sets {
		top = math.Max(top, st.Kg)
	}
	var out []setLog
	for _, st := range e.Sets {
		if st.Kg == top {
			out = append(out, st)
		}
	}
	return out
}

// progress decide la propuesta de hoy a partir de las últimas veces (de la
// más reciente a la más antigua).
func progress(it routineItem, e exercise, past []done) (setLog, string, bool) {
	rir := it.RIR
	target := setLog{RIR: &rir}
	setAmount := func(st *setLog, v int) {
		switch e.Kind {
		case "time":
			st.Seconds = v
		case "distance":
			st.Meters = float64(v)
		default:
			st.Reps = v
		}
	}
	if len(past) == 0 {
		setAmount(&target, it.Min)
		return target, "Primera vez: elige una carga con la que llegues a " + rangeText(it, e) + " dejando " + fmt.Sprint(it.RIR) + " en reserva.", false
	}
	last := working(past[0].Entry)
	if len(last) == 0 {
		setAmount(&target, it.Min)
		return target, "La última vez no tiene series apuntadas.", false
	}
	kg := last[0].Kg
	target.Kg = kg
	minAmt, allTop, harder, anyRIR := math.MaxInt, true, false, false
	for _, st := range last {
		a := amount(st)
		minAmt = min(minAmt, a)
		if a < it.Max {
			allTop = false
		}
		if st.RIR != nil {
			anyRIR = true
			if *st.RIR < it.RIR {
				harder = true
			}
		}
		if a < it.Min {
			harder = true
		}
	}
	step := e.Step
	if step == 0 {
		step = 2.5
	}

	// Estancado: tres veces sin mejorar, ya en el RIR objetivo o por debajo.
	if stalled(past, it) {
		target.Kg = roundTo(kg*0.9, step)
		setAmount(&target, it.Min)
		return target, fmt.Sprintf("Llevas %d sesiones sin progresar con %s y costándote. Toca una semana de descarga (−10 %%, una serie menos) o cambiar de variante; luego vuelve a intentarlo.", stallRun(past, it), loadText(kg)), true
	}
	switch {
	case allTop && !harder:
		setAmount(&target, it.Min)
		if e.Kind == "time" || e.Kind == "distance" || kg == 0 {
			setAmount(&target, minAmt+5)
			if e.Kind == "reps" || e.Kind == "" {
				setAmount(&target, minAmt+1)
			}
			target.Kg = kg
			return target, "Llegaste arriba del rango: hoy un poco más.", false
		}
		target.Kg = kg + step
		msg := fmt.Sprintf("Llegaste a %d con RIR %d o más en todas las series: sube a %s y vuelve a %d.", it.Max, it.RIR, loadText(target.Kg), it.Min)
		if !anyRIR {
			msg = fmt.Sprintf("Llegaste a %d en todas las series: sube a %s y vuelve a %d (apunta el RIR para afinar).", it.Max, loadText(target.Kg), it.Min)
		}
		return target, msg, false
	case harder:
		setAmount(&target, max(it.Min, minAmt))
		return target, "La última vez fue más duro de la cuenta: mantén la carga y busca el RIR objetivo.", false
	default:
		setAmount(&target, min(it.Max, minAmt+1))
		if e.Kind == "time" || e.Kind == "distance" {
			setAmount(&target, min(it.Max, minAmt+5))
		}
		return target, "Misma carga y un poco más que la última vez.", false
	}
}

// stalled: las tres últimas veces sin mejorar el 1RM estimado (o lo hecho,
// sin carga) y con el RIR en el objetivo o por debajo.
func stalled(past []done, it routineItem) bool { return stallRun(past, it) >= 3 }

// hard: la vez costó de verdad (RIR por debajo del objetivo o series por
// debajo del rango). Repetir la misma carga cómodo no es estancarse: es no
// haber intentado subir.
func hard(e exerciseLog, it routineItem) bool {
	for _, st := range working(e) {
		if (st.RIR != nil && *st.RIR < it.RIR) || amount(st) < it.Min {
			return true
		}
	}
	return false
}

func stallRun(past []done, it routineItem) int {
	if len(past) < 3 {
		return 0
	}
	score := func(d done) float64 {
		st, v := best(d.Entry)
		if v == 0 {
			v = float64(amount(st)) + st.Kg
		}
		return v
	}
	if !hard(past[0].Entry, it) {
		return 0
	}
	run := 1
	ref := score(past[0])
	for _, d := range past[1:] {
		if score(d) < ref*0.99 || !hard(d.Entry, it) {
			break // la vez anterior era peor (sí progresaba) o fue cómoda
		}
		run++
	}
	if run < 3 {
		return 0
	}
	return run
}

func roundTo(v, step float64) float64 { return math.Round(v/step) * step }

func loadText(kg float64) string {
	if kg == 0 {
		return "sin carga"
	}
	return trimNum(kg) + " kg"
}

func rangeText(it routineItem, e exercise) string {
	unit := " repeticiones"
	switch e.Kind {
	case "time":
		unit = " s"
	case "distance":
		unit = " m"
	}
	return fmt.Sprintf("%d–%d%s", it.Min, it.Max, unit)
}

// ---- Registro serie a serie (modo gimnasio) ----

// logSet apunta una serie en la sesión de hoy (la crea si no hay) y en el
// ejercicio que toca. id evita duplicarla si la web la reenvía (sin
// cobertura, al recuperar la conexión).
func (s *service) logSet(ctx context.Context, owner, key string, st setLog, at time.Time) (int64, error) {
	if _, ok := lookup(key); !ok {
		custom, err := s.store.customNames(ctx, owner)
		if err != nil {
			return 0, err
		}
		if _, ok := custom[key]; !ok {
			return 0, fmt.Errorf("ejercicio desconocido: %s", key)
		}
	}
	if st.Reps < 0 || st.Reps > 1000 || st.Seconds < 0 || st.Seconds > 7200 || st.Meters < 0 || st.Meters > 100000 || st.Kg < 0 || st.Kg > 1000 {
		return 0, fmt.Errorf("serie no válida")
	}
	if st.RIR != nil && (*st.RIR < 0 || *st.RIR > 10) {
		return 0, fmt.Errorf("RIR no válido")
	}
	st.ID = strings.TrimSpace(st.ID)
	if len(st.ID) > 64 {
		return 0, fmt.Errorf("id de serie no válido")
	}
	return s.store.appendSet(ctx, owner, at, key, st)
}
