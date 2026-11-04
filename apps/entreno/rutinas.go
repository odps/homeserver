package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Rutinas: los días fijos de la persona (empuje, tirón, pierna…). Cada
// ejercicio lleva sus series, el rango de repeticiones (o de segundos o
// metros), el RIR objetivo y el descanso. Se pueden crear a mano o a partir
// del historial (detectRoutines).

const sealRoutine = "entreno/routines"

// routineItem es un ejercicio de una rutina.
type routineItem struct {
	Key     string `json:"key"`
	Sets    int    `json:"sets"`
	Min     int    `json:"min"`  // repeticiones (o segundos, o metros) mínimas del rango
	Max     int    `json:"max"`  // y máximas: al llegar arriba con el RIR objetivo, se sube carga
	RIR     int    `json:"rir"`  // RIR objetivo
	RestSec int    `json:"rest"` // descanso entre series
	Note    string `json:"note,omitempty"`
}

// routine es una rutina.
type routine struct {
	ID       int64         `json:"id"`
	Name     string        `json:"nombre"`
	Days     []int         `json:"dias,omitempty"`       // días de la semana (0 domingo … 6 sábado)
	Hour     string        `json:"hora,omitempty"`       // HH:MM para la agenda; vacío: todo el día
	Minutes  int           `json:"minutos,omitempty"`    // duración, si hay hora
	NoAgenda bool          `json:"sin_agenda,omitempty"` // no ponerla en el calendario
	Items    []routineItem `json:"ejercicios"`
	Position int           `json:"-"`
}

var errRoutine = errors.New("la rutina necesita un nombre y al menos un ejercicio")

// check valida y corrige lo que viene de un formulario o del asistente.
func (r *routine) check() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Items) == 0 || len(r.Items) > 20 {
		return errRoutine
	}
	if len([]rune(r.Name)) > 60 {
		return errors.New("el nombre de la rutina es demasiado largo")
	}
	days := r.Days[:0]
	for _, d := range r.Days {
		if d >= 0 && d <= 6 && !containsInt(days, d) {
			days = append(days, d)
		}
	}
	sort.Ints(days)
	r.Days = days
	r.Hour = strings.TrimSpace(r.Hour)
	if r.Hour == "" {
		r.Minutes = 0
	} else {
		t, err := time.Parse("15:04", r.Hour)
		if err != nil {
			return errors.New("la hora va en formato HH:MM")
		}
		r.Hour = t.Format("15:04")
		if r.Minutes == 0 {
			r.Minutes = 75
		}
		if r.Minutes < 15 || r.Minutes > 300 {
			return errors.New("la duración va de 15 a 300 minutos")
		}
	}
	for i := range r.Items {
		it := &r.Items[i]
		if _, ok := lookup(it.Key); !ok && !strings.HasPrefix(it.Key, "u") {
			return fmt.Errorf("ejercicio desconocido: %s", it.Key)
		}
		it.Sets = clamp(it.Sets, 1, 10)
		it.Min = clamp(it.Min, 1, 600)
		it.Max = clamp(it.Max, it.Min, 600)
		it.RIR = clamp(it.RIR, 0, 5)
		if it.RestSec == 0 {
			it.RestSec = 90
		}
		it.RestSec = clamp(it.RestSec, 15, 600)
		it.Note = strings.TrimSpace(it.Note)
	}
	return nil
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func containsInt(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func (s store) routines(ctx context.Context, owner string) ([]routine, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, position, sealed FROM routines WHERE owner = $1 ORDER BY position, id`, owner)
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (routine, error) {
		var r routine
		var id int64
		var pos int
		var sealed []byte
		if err := row.Scan(&id, &pos, &sealed); err != nil {
			return r, err
		}
		err := s.box.OpenJSON(sealRoutine, sealed, &r)
		r.ID, r.Position = id, pos
		return r, err
	})
}

func (s store) routine(ctx context.Context, owner string, id int64) (routine, error) {
	list, err := s.routines(ctx, owner)
	if err != nil {
		return routine{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return routine{}, errNotFound
}

// saveRoutine crea (ID 0) o cambia una rutina.
func (s store) saveRoutine(ctx context.Context, owner string, r routine) (int64, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	custom, err := s.customNames(ctx, owner)
	if err != nil {
		return 0, err
	}
	for _, it := range r.Items {
		if _, ok := lookup(it.Key); !ok && custom[it.Key] == "" {
			return 0, fmt.Errorf("ejercicio desconocido: %s", it.Key)
		}
	}
	sealed, err := s.box.SealJSON(sealRoutine, r)
	if err != nil {
		return 0, err
	}
	if r.ID == 0 {
		var id int64
		err := s.db.QueryRow(ctx, `INSERT INTO routines (owner, position, sealed)
			VALUES ($1, (SELECT coalesce(max(position) + 1, 0) FROM routines WHERE owner = $1), $2) RETURNING id`, owner, sealed).Scan(&id)
		return id, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE routines SET sealed = $3, updated_at = now() WHERE owner = $1 AND id = $2`, owner, r.ID, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return 0, errNotFound
	}
	return r.ID, err
}

func (s store) deleteRoutine(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM routines WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Qué toca hoy ----

// todayRoutine es la rutina de hoy: la del día de la semana y, si no hay,
// la siguiente a la última que se hizo (rotación).
func (s *service) todayRoutine(ctx context.Context, owner string, now time.Time) (routine, bool, error) {
	list, err := s.store.routines(ctx, owner)
	if err != nil || len(list) == 0 {
		return routine{}, false, err
	}
	wd := int(now.In(madrid).Weekday())
	for _, r := range list {
		if containsInt(r.Days, wd) {
			return r, true, nil
		}
	}
	// Rotación: la que más se parece a la última sesión, y la siguiente.
	last, err := s.store.sessions(ctx, owner, time.Time{}, now, 1)
	if err != nil || len(last) == 0 {
		return list[0], true, err
	}
	best, bestScore := -1, 0.0
	for i, r := range list {
		if sc := overlap(r, last[0]); sc > bestScore {
			best, bestScore = i, sc
		}
	}
	if best < 0 || bestScore < 0.5 {
		return list[0], true, nil
	}
	return list[(best+1)%len(list)], true, nil
}

// overlap mide cuánto se parece una sesión a una rutina (0 a 1, Jaccard).
func overlap(r routine, s session) float64 {
	a, b := map[string]bool{}, map[string]bool{}
	for _, it := range r.Items {
		a[it.Key] = true
	}
	for _, e := range s.Exercises {
		b[e.Key] = true
	}
	return jaccard(a, b)
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// ---- Detectar rutinas en el historial ----

// detectRoutines busca en las sesiones de los últimos 4 meses los grupos que
// se repiten (los mismos ejercicios al menos 3 veces) y propone una rutina por
// grupo: series y repeticiones las más habituales, el rango hasta +2, el RIR
// más habitual, el día de la semana en que más se hace y el nombre por los
// músculos que trabaja. No guarda nada.
func (s *service) detectRoutines(ctx context.Context, owner string, now time.Time) ([]routine, error) {
	list, err := s.store.sessions(ctx, owner, now.AddDate(0, -4, 0), now, 0)
	if err != nil {
		return nil, err
	}
	type cluster struct {
		keys     map[string]bool
		sessions []session
	}
	var clusters []*cluster
	for _, x := range list {
		keys := map[string]bool{}
		for _, e := range x.Exercises {
			if len(e.Sets) > 0 {
				keys[e.Key] = true
			}
		}
		if len(keys) < 2 {
			continue
		}
		var into *cluster
		bestScore := 0.0
		for _, c := range clusters {
			if sc := jaccard(c.keys, keys); sc >= 0.5 && sc > bestScore {
				into, bestScore = c, sc
			}
		}
		if into == nil {
			into = &cluster{keys: keys}
			clusters = append(clusters, into)
		}
		into.sessions = append(into.sessions, x)
	}
	var out []routine
	for _, c := range clusters {
		if len(c.sessions) < 3 {
			continue
		}
		out = append(out, routineFrom(c.sessions))
	}
	// Dos con el mismo nombre (dos días de pierna): se distinguen por el día.
	names := map[string]int{}
	for _, r := range out {
		names[r.Name]++
	}
	dayNames := []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	for i := range out {
		if names[out[i].Name] > 1 && len(out[i].Days) > 0 {
			out[i].Name += " · " + dayNames[out[i].Days[0]]
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Days) == 0 || len(out[j].Days) == 0 {
			return len(out[i].Days) > len(out[j].Days)
		}
		return weekOrder(out[i].Days[0]) < weekOrder(out[j].Days[0])
	})
	return out, nil
}

// weekOrder pone el lunes primero.
func weekOrder(d int) int { return (d + 6) % 7 }

func routineFrom(sessions []session) routine {
	// Ejercicios que salen en al menos la mitad de las sesiones, en su orden
	// habitual.
	count, pos := map[string]int{}, map[string]float64{}
	for _, x := range sessions {
		for i, e := range x.Exercises {
			if len(e.Sets) > 0 {
				count[e.Key]++
				pos[e.Key] += float64(i)
			}
		}
	}
	var keys []string
	for k, n := range count {
		if n*2 >= len(sessions) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return pos[keys[i]]/float64(count[keys[i]]) < pos[keys[j]]/float64(count[keys[j]])
	})
	r := routine{}
	days := map[int]int{}
	for _, x := range sessions {
		days[int(x.Day.Weekday())]++
	}
	if d, n := mode(days); n*2 >= len(sessions) {
		r.Days = []int{d}
	}
	muscles := map[string]int{}
	for _, k := range keys {
		var sets, reps, rirs = map[int]int{}, map[int]int{}, map[int]int{}
		for _, x := range sessions {
			for _, e := range x.Exercises {
				if e.Key != k || len(e.Sets) == 0 {
					continue
				}
				sets[len(e.Sets)]++
				for _, st := range e.Sets {
					reps[amount(st)]++
					if st.RIR != nil {
						rirs[*st.RIR]++
					}
				}
			}
		}
		it := routineItem{Key: k, RestSec: 90}
		it.Sets, _ = mode(sets)
		it.Min, _ = mode(reps)
		it.Max = it.Min + 2
		if e, ok := lookup(k); ok {
			switch e.Kind {
			case "time":
				it.Max = it.Min + 15
				it.RestSec = 60
			case "distance":
				it.Max = it.Min + 10
			}
			for _, m := range e.Muscles {
				muscles[m]++
			}
			if e.Step >= 2.5 && it.Min <= 6 {
				it.RestSec = 150 // básicos pesados
			}
		}
		// El objetivo es RIR 2 (lo habitual para progresar sin fallar); el
		// apuntado no se copia: si siempre se llega a RIR 0, eso es lo que hay
		// que corregir. Se cambia al editar la rutina.
		it.RIR = 2
		r.Items = append(r.Items, it)
	}
	r.Name = routineName(muscles)
	return r
}

// amount es lo que mide la serie: repeticiones, segundos o metros.
func amount(st setLog) int {
	switch {
	case st.Seconds > 0:
		return st.Seconds
	case st.Meters > 0:
		return int(st.Meters)
	}
	return st.Reps
}

func mode(m map[int]int) (int, int) {
	best, bestN := 0, 0
	for v, n := range m {
		if n > bestN || (n == bestN && v < best) {
			best, bestN = v, n
		}
	}
	return best, bestN
}

// routineName pone nombre por los músculos que más trabaja.
func routineName(muscles map[string]int) string {
	sum := func(groups ...string) int {
		n := 0
		for _, g := range groups {
			n += muscles[g]
		}
		return n
	}
	legs := sum("cuadriceps", "isquios", "gluteo", "gemelo", "aductores", "tibial")
	push := sum("pecho", "hombro", "triceps")
	pull := sum("espalda", "biceps", "trapecio", "antebrazo")
	switch {
	case legs > push+pull:
		if muscles["cuadriceps"] >= muscles["isquios"]+muscles["gluteo"] {
			return "Pierna (cuádriceps)"
		}
		return "Pierna (cadena posterior)"
	case push > 2*pull && push > legs:
		return "Empuje"
	case pull > 2*push && pull > legs:
		return "Tirón"
	case legs > 0:
		return "Cuerpo completo"
	}
	return "Torso"
}
