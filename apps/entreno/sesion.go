package main

import (
	"strings"
)

// assemble junta las líneas de una sesión: «Rir 2» va al ejercicio anterior
// (o al nombrado: «Rir lat 2»), las series sin nombre se suman al anterior y
// el texto suelto queda como nota de la sesión.
func assemble(lines []string, nm names) (exs []exerciseLog, notes []string) {
	find := func(name string) int {
		key, ok := nm.resolve(name)
		if !ok {
			return -1
		}
		for i := len(exs) - 1; i >= 0; i-- {
			if exs[i].Key == key {
				return i
			}
		}
		return -1
	}
	for _, line := range lines {
		p := parseLine(line, nm)
		switch p.Kind {
		case lineExercise:
			if len(p.Ex.Sets) == 0 {
				if i := find(p.Ex.Name); i >= 0 {
					exs[i].Note = joinNote([]string{exs[i].Note, p.Ex.Note})
					continue
				}
			}
			exs = append(exs, p.Ex)
		case lineMore:
			if len(exs) == 0 {
				notes = append(notes, strings.TrimSpace(line))
				continue
			}
			last := &exs[len(exs)-1]
			last.Sets = append(last.Sets, p.Ex.Sets...)
			last.PerSide = last.PerSide || p.Ex.PerSide
			last.Note = joinNote([]string{last.Note, p.Ex.Note})
		case lineRIR:
			i := len(exs) - 1
			if p.For != "" {
				i = find(p.For)
			}
			if i < 0 {
				if p.Ex.Name != "" {
					if key, ok := nm.resolve(p.Ex.Name); ok {
						exs = append(exs, exerciseLog{Key: key, Name: p.Ex.Name, Note: p.Ex.Note})
					}
				}
				continue
			}
			for j := range exs[i].Sets {
				if exs[i].Sets[j].RIR == nil && p.RIR != nil {
					v := *p.RIR
					exs[i].Sets[j].RIR = &v
				}
			}
		case lineNote:
			notes = append(notes, p.Text)
		}
	}
	return exs, notes
}

// parseText es assemble de un texto con varias líneas (lo que se pega en la
// web o se le dice al asistente).
func parseText(text string, nm names) ([]exerciseLog, []string) {
	return assemble(strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), nm)
}

// unknownNames son los nombres sin reconocer, con cuántas veces salen.
func unknownNames(exs []exerciseLog) map[string]int {
	out := map[string]int{}
	for _, e := range exs {
		if e.Key == "" && e.Name != "" {
			out[e.Name]++
		}
	}
	return out
}
