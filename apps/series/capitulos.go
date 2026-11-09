package main

import (
	"sort"
	"strings"
	"time"

	"homeserver/platform/seal"
)

// Lo que se calcula con los capítulos: cuándo sale cada uno (en Madrid), cuál
// toca ver, el progreso y qué marcar al decir «visto».

// day es el día de emisión en Madrid (cero si no se sabe). Con hora, el del
// instante convertido; sin ella, la fecha que da TVmaze.
func (e episode) day() time.Time {
	if e.hasTime() {
		return dayOf(e.At)
	}
	if d, err := parseDay(e.Date); err == nil && e.Date != "" {
		return d
	}
	return time.Time{}
}

func (e episode) hasTime() bool { return !e.At.IsZero() }

// aired dice si ya se ha emitido.
func (e episode) aired(now time.Time) bool {
	if e.hasTime() {
		return !e.At.After(now)
	}
	d := e.day()
	return !d.IsZero() && !d.After(dayOf(now))
}

// premiere dice si abre temporada.
func (e episode) premiere() bool { return e.Number == 1 }

func epLess(a, b episode) bool {
	if a.Season != b.Season {
		return a.Season < b.Season
	}
	if a.Number != b.Number {
		return a.Number < b.Number
	}
	return a.ID < b.ID
}

func (d *showData) episode(id int64) (episode, bool) {
	for _, e := range d.Episodes {
		if e.ID == id {
			return e, true
		}
	}
	return episode{}, false
}

// find busca un capítulo por temporada y número.
func (d *showData) find(season, number int) (episode, bool) {
	for _, e := range d.Episodes {
		if e.Season == season && e.Number == number {
			return e, true
		}
	}
	return episode{}, false
}

func (d *showData) seasons() []int {
	var out []int
	for _, e := range d.Episodes {
		if len(out) == 0 || out[len(out)-1] != e.Season {
			out = append(out, e.Season)
		}
	}
	return out
}

// ---- Estados ----

// Estados de una serie, una peli o un juego.
const (
	statePending   = "pendiente" // quiero verla / jugarlo
	stateWatching  = "viendo"
	statePaused    = "pausa"
	stateDone      = "terminado"
	stateAbandoned = "abandonado"
)

var states = []string{stateWatching, statePending, statePaused, stateDone, stateAbandoned}

// Tipos de lo que se apunta a mano.
const (
	kindFilm = "peli"
	kindGame = "juego"
	kindShow = "serie"
)

// stateLabel es cómo se dice cada estado según lo que sea.
func stateLabel(kind, st string) string {
	switch kind {
	case kindGame:
		switch st {
		case statePending:
			return "Quiero jugarlo"
		case stateWatching:
			return "Jugando"
		case statePaused:
			return "En pausa"
		case stateDone:
			return "Terminado"
		case stateAbandoned:
			return "Abandonado"
		}
	case kindFilm:
		switch st {
		case statePending:
			return "Quiero verla"
		case stateWatching:
			return "Viéndola"
		case statePaused:
			return "En pausa"
		case stateDone:
			return "Vista"
		case stateAbandoned:
			return "Abandonada"
		}
	default:
		switch st {
		case statePending:
			return "Quiero verla"
		case stateWatching:
			return "Viendo"
		case statePaused:
			return "En pausa"
		case stateDone:
			return "Terminada"
		case stateAbandoned:
			return "Abandonada"
		}
	}
	return st
}

func validState(st string) bool {
	for _, s := range states {
		if s == st {
			return true
		}
	}
	return false
}

// parseState entiende el estado como lo diría la persona (o Talia):
// «quiero ver», «pendiente», «vista», «jugando», «en pausa»… Vacío si no.
func parseState(s string) string {
	w := strings.Join(seal.Words(s), " ")
	switch w {
	case "pendiente", "quiero", "quiero ver", "quiero verla", "quiero verlo", "quiero jugar", "quiero jugarlo", "por ver", "por jugar", "lista", "watchlist":
		return statePending
	case "viendo", "viendola", "viendolo", "jugando", "en curso", "empezada", "empezado":
		return stateWatching
	case "pausa", "en pausa", "pausada", "pausado", "parada", "parado":
		return statePaused
	case "terminado", "terminada", "visto", "vista", "acabado", "acabada", "completado", "completada", "jugado", "hecho":
		return stateDone
	case "abandonado", "abandonada", "dejada", "dejado", "la deje", "lo deje":
		return stateAbandoned
	}
	return ""
}

// stateStatus es el estado de TVmaze en español.
func showStatus(st string) string {
	switch st {
	case "Running":
		return "En emisión"
	case "Ended":
		return "Terminada"
	case "To Be Determined":
		return "Sin confirmar si sigue"
	case "In Development":
		return "En desarrollo"
	case "":
		return ""
	}
	return st
}

// ---- Progreso ----

type idSet map[int64]bool

func setOf(ids []int64) idSet {
	s := make(idSet, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

func (s idSet) list() []int64 {
	out := make([]int64, 0, len(s))
	for id, on := range s {
		if on {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// progress es lo visto de una serie.
type progress struct {
	Watched   int      // capítulos vistos (de los que hay)
	Aired     int      // emitidos
	Total     int      // todos los que se conocen
	Left      int      // emitidos y sin ver
	Next      *episode // el primero emitido sin ver
	Upcoming  *episode // el próximo por emitirse
	Last      *episode // el último visto (en orden)
	CaughtUp  bool     // lo emitido, todo visto
	AllDone   bool     // todo visto y la serie ha terminado
	NextIsNew bool     // el siguiente abre temporada
}

func progressOf(d *showData, watched idSet, now time.Time) progress {
	var p progress
	for i := range d.Episodes {
		e := &d.Episodes[i]
		p.Total++
		seen := watched[e.ID]
		if seen {
			p.Watched++
			p.Last = e
		}
		if e.aired(now) {
			p.Aired++
			if !seen {
				p.Left++
				if p.Next == nil {
					p.Next = e
				}
			}
		} else if p.Upcoming == nil && !e.day().IsZero() {
			p.Upcoming = e
		}
	}
	p.CaughtUp = p.Left == 0 && p.Aired > 0
	p.AllDone = p.CaughtUp && p.Aired == p.Total && d.Status == "Ended"
	p.NextIsNew = p.Next != nil && p.Next.premiere() && p.Next.Season > 1
	return p
}

// ---- Marcar ----

// Cómo se marca: un capítulo, hasta él (con los anteriores) o la temporada.
const (
	markOne    = "uno"
	markUpTo   = "hasta"
	markSeason = "temporada"
)

// mark marca (o desmarca) capítulos como vistos. Al marcar «hasta aquí» o la
// temporada solo entran los ya emitidos (y el elegido). Devuelve cuántos
// cambian.
func mark(d *showData, watched idSet, mode string, epID int64, season int, on bool, now time.Time) (int, bool) {
	changed := 0
	set := func(e episode) {
		if watched[e.ID] != on {
			changed++
			if on {
				watched[e.ID] = true
			} else {
				delete(watched, e.ID)
			}
		}
	}
	switch mode {
	case markOne, markUpTo:
		target, ok := d.episode(epID)
		if !ok {
			return 0, false
		}
		if mode == markOne || !on {
			set(target)
			return changed, true
		}
		for _, e := range d.Episodes {
			if e.ID == target.ID || (epLess(e, target) && e.aired(now)) {
				set(e)
			}
		}
		return changed, true
	case markSeason:
		found := false
		for _, e := range d.Episodes {
			if e.Season != season {
				continue
			}
			found = true
			if !on || e.aired(now) {
				set(e)
			}
		}
		return changed, found
	}
	return 0, false
}

// autoState pasa de «quiero verla» a «viendo» al marcar algo, y a
// «terminada» al ver todo de una serie que ya ha acabado.
func autoState(st string, d *showData, watched idSet, now time.Time) string {
	p := progressOf(d, watched, now)
	switch {
	case p.AllDone && (st == stateWatching || st == statePending || st == statePaused):
		return stateDone
	case st == statePending && p.Watched > 0:
		return stateWatching
	}
	return st
}

// between son los capítulos que salen entre dos días (incluidos), en orden de
// emisión.
func between(d *showData, from, to time.Time) []episode {
	var out []episode
	for _, e := range d.Episodes {
		day := e.day()
		if !day.IsZero() && !day.Before(from) && !day.After(to) {
			out = append(out, e)
		}
	}
	return out
}

// airOrder ordena por día y hora de emisión (sin hora, al principio del día).
func airKey(e episode) time.Time {
	if e.hasTime() {
		return e.At
	}
	d := e.day()
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, madrid)
}
