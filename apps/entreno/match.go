package main

import "strings"

// matcher reconoce el ejercicio de un nombre escrito a mano: por los alias
// del catálogo y los de la persona («Pressa» → prensa), sin tildes ni
// mayúsculas, con palabras de más («Incline bench press 30°») y con erratas
// («Front swuat», «Bsrbell row»).
type matcher struct {
	alias map[string]string // nombre plegado → clave
}

func newMatcher(user map[string]string) *matcher {
	m := &matcher{alias: map[string]string{}}
	// Primero los de wger que no están en el catálogo propio: los del propio
	// (y luego los de la persona) mandan si coinciden.
	loadExternal()
	for _, e := range ext.extra {
		m.alias[fold(e.Name)] = e.Key
		for _, a := range e.Aliases {
			m.alias[fold(a)] = e.Key
		}
	}
	for _, e := range catalog {
		m.alias[fold(e.Name)] = e.Key
		for _, a := range e.Aliases {
			m.alias[fold(a)] = e.Key
		}
	}
	for a, key := range user { // los de la persona mandan
		m.alias[fold(a)] = key
	}
	return m
}

func (m *matcher) exact(name string) (string, bool) {
	key, ok := m.alias[fold(name)]
	return key, ok
}

// resolve prueba el nombre entero, luego sus trozos de más a menos palabras
// (antes los del principio) y, por último, con una o dos letras de
// diferencia.
func (m *matcher) resolve(name string) (string, bool) {
	words := strings.Fields(fold(name))
	if len(words) == 0 {
		return "", false
	}
	for n := len(words); n >= 1; n-- {
		for i := 0; i+n <= len(words); i++ {
			if key, ok := m.alias[strings.Join(words[i:i+n], " ")]; ok {
				return key, true
			}
		}
	}
	best, bestD := "", 3
	for n := min(len(words), 4); n >= 1; n-- {
		for i := 0; i+n <= len(words); i++ {
			cand := strings.Join(words[i:i+n], " ")
			if len(cand) < 4 {
				continue
			}
			allowed := 1
			if len(cand) > 7 {
				allowed = 2
			}
			for a, key := range m.alias {
				if abs(len(a)-len(cand)) > allowed {
					continue
				}
				if d := levenshtein(a, cand); d <= allowed && (d < bestD || (d == bestD && key < best)) {
					best, bestD = key, d
				}
			}
		}
		if best != "" {
			return best, true
		}
	}
	return "", false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// exerciseName es el nombre para enseñar de una clave (o el escrito).
func exerciseName(key, written string, custom map[string]string) string {
	if e, ok := lookup(key); ok {
		return e.Name
	}
	if n, ok := custom[key]; ok {
		return n
	}
	if written != "" {
		return written
	}
	return key
}
