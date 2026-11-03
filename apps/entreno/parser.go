package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"homeserver/platform/seal"
)

// Intérprete del formato de las notas, sin IA:
//
//	Lat machine 3x10 50kg rir 2      3 series × 10 repeticiones, 50 kg, RIR 2
//	Farmer Carry 3x20m 22kg          por distancia
//	Hollow hold 3x45s / Squat hold 2min   por tiempo
//	Scapular push 3x15               sin carga
//	Bulgarian 3x8 12kg/ea            carga por lado
//	Seated calf 20x2 15kg            repeticiones × series (cuando el primero es grande)
//	Military 1x8 3x6 Max 10kg        varios grupos; «Max» = la carga más alta
//	Bench 3x8 50/55/60kg rpe 8       carga por serie; RPE 8 = RIR 2
//	3x8 8kg                          sin nombre: más series del ejercicio anterior
//	Rir 2 / Rir lat 2                el RIR en otra línea, para el anterior o el nombrado
//
// Lo que no es número ni marca queda como nota («partido», «dolor ingle»).

// setLog es una serie.
type setLog struct {
	Reps    int     `json:"reps,omitempty"`
	Seconds int     `json:"seg,omitempty"`
	Meters  float64 `json:"m,omitempty"`
	Kg      float64 `json:"kg,omitempty"`
	RIR     *int    `json:"rir,omitempty"` // nil: no apuntado
}

// exerciseLog es un ejercicio de una sesión.
type exerciseLog struct {
	Key     string   `json:"key,omitempty"` // del catálogo o «u<id>» (propio); "" sin reconocer
	Name    string   `json:"name"`          // como se escribió
	Sets    []setLog `json:"sets,omitempty"`
	PerSide bool     `json:"lado,omitempty"` // carga por lado/mano
	Note    string   `json:"note,omitempty"`
}

// lineKind es lo que resulta de una línea.
type lineKind int

const (
	lineNone     lineKind = iota // nada útil («.», «A», vacía)
	lineExercise                 // un ejercicio (con o sin series)
	lineMore                     // series sin nombre: van al ejercicio anterior
	lineRIR                      // solo el RIR, para el anterior o el nombrado
	lineNote                     // texto: nota
)

type parsedLine struct {
	Kind lineKind
	Ex   exerciseLog
	RIR  *int   // lineRIR
	For  string // lineRIR: nombre del ejercicio al que va («Rir lat 2»)
	Text string // lineNote
}

var (
	reGroup = regexp.MustCompile(`(?i)(\d+)\s*[x×]\s*(\d+(?:[.,]\d+)?)\s*(min|seg|s|m)?\b`)
	reLoad  = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?(?:\s*/\s*\d+(?:[.,]\d+)?)*)\s*kg(\s*/\s*ea|\s+ea|\s+c/u|\s+por\s+lado)?\b`)
	reRIR   = regexp.MustCompile(`(?i)\br[ií]r\s*(?:(n\s*/\s*a)|([a-zñ]+\s+)?(\d+))?`)
	reRPE   = regexp.MustCompile(`(?i)\brpe\s*(\d+(?:[.,]5)?)`)
	reHold  = regexp.MustCompile(`(?i)\b(\d+(?:[.,]\d+)?)\s*(min|seg|s)\b`)
	reWords = regexp.MustCompile(`(?i)\b(max|máx|progresivo|ea|kg)\b`)
)

// sensible para los números: «12,5» = 12.5
func num(s string) float64 {
	f, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	return f
}

// group es «AxB»: series y repeticiones (o segundos o metros).
type group struct {
	sets  int
	value float64
	unit  string // "", "s", "min", "m"
}

// orient decide qué número son las series: lo normal es series×reps
// (3x10), pero «20x2» o «12x4» son reps×series. Con unidad (3x30s, 3x20m),
// siempre series×cantidad.
func orient(a, b float64, unit string) (sets int, value float64) {
	if unit != "" || a <= 6 || b > 6 {
		return int(a), b
	}
	return int(b), a
}

func parseRIR(s string) *int {
	s = strings.ToLower(strings.ReplaceAll(s, " ", ""))
	if s == "" || strings.HasPrefix(s, "n") {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 10 {
		return nil
	}
	return &n
}

// names reconoce nombres de ejercicio: resolve con tolerancia (erratas,
// palabras de más al final) y exact solo si el nombre es tal cual uno
// conocido.
type names interface {
	resolve(name string) (key string, ok bool)
	exact(name string) (key string, ok bool)
}

type span struct{ a, b int }

// parseLine interpreta una línea.
func parseLine(line string, nm names) parsedLine {
	line = strings.TrimSpace(strings.Trim(line, "*_ "))
	letters := strings.IndexFunc(line, unicode.IsLetter) >= 0
	if line == "" || (!letters && !strings.ContainsAny(line, "0123456789")) {
		return parsedLine{}
	}
	if len([]rune(line)) <= 2 && !strings.ContainsAny(line, "0123456789") {
		return parsedLine{} // «A», «B»: marca de rutina
	}

	// Dónde está cada cosa en la línea, para separar nombre, números y nota.
	var used []span
	mark := func(loc []int) { used = append(used, span{loc[0], loc[1]}) }

	var groups []group
	for _, m := range reGroup.FindAllStringSubmatchIndex(line, -1) {
		g := group{unit: strings.ToLower(subm(line, m, 3))}
		if g.unit == "seg" {
			g.unit = "s"
		}
		g.sets, g.value = orient(num(subm(line, m, 1)), num(subm(line, m, 2)), g.unit)
		if g.sets <= 0 || g.sets > 20 || g.value <= 0 {
			continue
		}
		groups = append(groups, g)
		mark(m)
	}
	var loads []float64
	perSide, isMax := false, false
	for _, m := range reLoad.FindAllStringSubmatchIndex(line, -1) {
		if overlaps(used, m[0], m[1]) {
			continue
		}
		for _, p := range strings.Split(subm(line, m, 1), "/") {
			loads = append(loads, num(p))
		}
		perSide = perSide || subm(line, m, 2) != ""
		mark(m)
	}
	var rir *int
	rirFor, hasRIR := "", false
	if m := reRIR.FindStringSubmatchIndex(line); m != nil {
		hasRIR = true
		rirFor = strings.TrimSpace(subm(line, m, 2))
		rir = parseRIR(subm(line, m, 3))
		mark(m)
	}
	if m := reRPE.FindStringSubmatchIndex(line); m != nil && rir == nil {
		hasRIR = true
		if r := 10 - int(math.Round(num(subm(line, m, 1)))); r >= 0 {
			rir = &r
		}
		mark(m)
	}
	seconds := 0
	if len(groups) == 0 {
		if m := reHold.FindStringSubmatchIndex(line); m != nil && !overlaps(used, m[0], m[1]) {
			v := num(subm(line, m, 1))
			if strings.EqualFold(subm(line, m, 2), "min") {
				v *= 60
			}
			seconds = int(v)
			mark(m)
		}
	}
	for _, m := range reWords.FindAllStringIndex(line, -1) {
		if !overlaps(used, m[0], m[1]) {
			if strings.EqualFold(line[m[0]:m[1]], "max") || strings.EqualFold(line[m[0]:m[1]], "máx") {
				isMax = true
			}
			if strings.EqualFold(line[m[0]:m[1]], "ea") {
				perSide = true
			}
			mark(m)
		}
	}
	_ = isMax // «Max 30kg»: se guarda como la carga de las series (la más alta)

	// Lo que no son números ni marcas: el nombre (al principio) y la nota.
	var rest strings.Builder
	first := -1
	for i, r := range line {
		if overlaps(used, i, i+1) {
			if first < 0 {
				first = i
			}
			rest.WriteRune('\x00')
			continue
		}
		rest.WriteRune(r)
	}
	parts := strings.Split(rest.String(), "\x00")
	var name, note string
	numbersFirst := first == 0 || (first > 0 && strings.TrimSpace(line[:first]) == "")
	if !numbersFirst {
		name = cleanName(parts[0])
		note = joinNote(parts[1:])
	} else {
		// «3x6 14kg bench press rir 2», «4x15 15kg semitese»: el nombre va
		// detrás; se toma el trozo más largo que se reconozca.
		note = joinNote(parts)
		if n, rem, ok := leadingName(note, nm); ok {
			name, note = n, rem
		}
	}

	numbers := len(groups) > 0 || len(loads) > 0 || seconds > 0
	switch {
	case !numbers && hasRIR && name == "" && note == "":
		return parsedLine{Kind: lineRIR, RIR: rir, For: rirFor}
	case !numbers && hasRIR && name != "":
		// «Lumbar rir 4»: el RIR de un ejercicio ya apuntado (lo decide quien
		// junta la sesión); si no estaba, es un ejercicio sin series.
		return parsedLine{Kind: lineRIR, RIR: rir, For: name, Ex: exerciseLog{Name: name, Note: note}}
	case !numbers && name != "":
		// Sin números, solo si el nombre es tal cual un ejercicio («Leg
		// press averiada» es una nota, no un ejercicio).
		if key, ok := nm.exact(name); ok {
			return parsedLine{Kind: lineExercise, Ex: exerciseLog{Key: key, Name: name, Note: note}}
		}
		return parsedLine{Kind: lineNote, Text: strings.TrimSpace(line)}
	case !numbers:
		return parsedLine{Kind: lineNote, Text: strings.TrimSpace(line)}
	}

	ex := exerciseLog{Name: name, Note: note, PerSide: perSide}
	if name != "" {
		ex.Key, _ = nm.resolve(name)
	}
	ex.Sets = expand(groups, loads, seconds, rir)
	if name == "" {
		return parsedLine{Kind: lineMore, Ex: ex}
	}
	return parsedLine{Kind: lineExercise, Ex: ex}
}

// expand convierte grupos y cargas en series.
func expand(groups []group, loads []float64, seconds int, rir *int) []setLog {
	// «4x6 4x6 50kg»: el mismo grupo repetido es una errata.
	var gs []group
	for i, g := range groups {
		if i > 0 && g == groups[i-1] {
			continue
		}
		gs = append(gs, g)
	}
	if len(gs) == 0 {
		gs = []group{{sets: 1}}
		if seconds > 0 {
			gs[0].value, gs[0].unit = float64(seconds), "s"
		}
	}
	var out []setLog
	for _, g := range gs {
		for i := 0; i < g.sets; i++ {
			s := setLog{}
			switch g.unit {
			case "s":
				s.Seconds = int(g.value)
			case "min":
				s.Seconds = int(g.value * 60)
			case "m":
				s.Meters = g.value
			default:
				s.Reps = int(g.value)
			}
			out = append(out, s)
		}
	}
	for i := range out {
		switch {
		case len(loads) == len(out) && len(loads) > 1:
			out[i].Kg = loads[i]
		case len(loads) > 0:
			out[i].Kg = loads[len(loads)-1]
		}
		if rir != nil {
			v := *rir
			out[i].RIR = &v
		}
	}
	return out
}

func subm(s string, m []int, i int) string {
	if m[2*i] < 0 {
		return ""
	}
	return s[m[2*i]:m[2*i+1]]
}

func overlaps(spans []span, a, b int) bool {
	for _, s := range spans {
		if a < s.b && b > s.a {
			return true
		}
	}
	return false
}

// cleanName quita al nombre la puntuación del final y lo que va entre
// paréntesis («Military dumbbell (estabilidad).»).
func cleanName(s string) string {
	if i := strings.Index(s, "("); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ".,;:-|"))
}

func joinNote(parts []string) string {
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), ".,;:"))
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// leadingName busca al principio del texto el nombre de un ejercicio (de
// más a menos palabras) y devuelve el resto como nota.
func leadingName(text string, nm names) (name, rest string, ok bool) {
	words := strings.Fields(strings.ReplaceAll(text, ",", " , "))
	for n := min(len(words), 5); n >= 1; n-- {
		cand := strings.Join(words[:n], " ")
		if strings.Contains(cand, ",") {
			continue
		}
		if _, found := nm.exact(cand); found {
			return cand, joinNote([]string{strings.Join(words[n:], " ")}), true
		}
	}
	return "", text, false
}

// fold deja un nombre listo para comparar: minúsculas, sin tildes ni signos.
func fold(s string) string {
	return strings.Join(seal.Words(s), " ")
}
