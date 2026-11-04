package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"homeserver/platform/seal"
)

// Añadir rápido: «leche, 2 kg de patatas, huevos y pan» son cuatro cosas.
// Se separa por comas, punto y coma, saltos de línea y la palabra «y» (o «e»
// delante de i/hi), y la cantidad se saca si va delante («2 kg de
// patatas», «media docena de huevos») o detrás («patatas 2kg», «leche x6»,
// «huevos: 12»). Lo que va entre paréntesis al final es una nota.

const (
	maxAddText = 2000 // caracteres del cuadro de añadir
	maxParts   = 50   // cosas de una vez
	maxName    = 80
	maxQty     = 30
	maxNote    = 200
)

// parsed es una cosa leída del texto.
type parsed struct {
	Name string
	Qty  string
	Note string
}

var (
	reSplit     = regexp.MustCompile(`[,;\n\r]+|\s+[yY]\s+`)
	reE         = regexp.MustCompile(`\s+[eE]\s+([iIíÍ]|[hH][iIíÍ])`) // «uvas e higos»
	reLeadY     = regexp.MustCompile(`^[yYeE]\s+`)
	reYMedio    = regexp.MustCompile(`(?i)(\d+)\s*(kilos?|kg|litros?|l)\s+y\s+medi[oa]\b`)
	reHasLetter = regexp.MustCompile(`\pL`)
	reDecimal   = regexp.MustCompile(`(\d),(\d)`) // «1,5 l»: la coma no separa
	reUnYMedio  = regexp.MustCompile(`(?i)\b(?:(?:un|1)\s+)?(kilo|litro)\s+y\s+medio\b`)
	reBullet    = regexp.MustCompile(`^(?:[-*•·]+|\d+[.)])\s+`)
	reNum       = regexp.MustCompile(`^(x)?(\d+(?:[.,]\d+)?)(x)?$`)
	reNumUnit   = regexp.MustCompile(`^(\d+(?:[.,]\d+)?)([a-z]+)$`)
	reNote      = regexp.MustCompile(`^(.*?)\s*\(([^()]*)\)$`)
	reLeadVerb  = regexp.MustCompile(`(?i)^\s*(?:por favor,?\s+)?(?:añade|añadir|apunta|apuntar|pon|poner|compra|comprar|necesito|hay que comprar|falta|faltan)(?:\s+a\s+la\s+lista)?\s*:?\s+`)
	reTrailList = regexp.MustCompile(`(?i)\s+a\s+la\s+lista(?:\s+de\s+la\s+compra)?\s*\.?\s*$`)
)

// unitAlias: unidad escrita (sin tildes) → unidad.
var unitAlias = map[string]string{
	"kg": "kg", "kgs": "kg", "kilo": "kg", "kilos": "kg", "kilogramo": "kg", "kilogramos": "kg",
	"g": "g", "gr": "g", "grs": "g", "gramo": "g", "gramos": "g",
	"l": "l", "lt": "l", "lts": "l", "litro": "l", "litros": "l",
	"ml": "ml", "cl": "cl",
	"ud": "ud", "uds": "ud", "unidad": "ud", "unidades": "ud",
	"docena": "docena", "docenas": "docena",
}

// wordUnits son los envases: singular → plural.
var wordUnits = map[string]string{
	"lata": "latas", "paquete": "paquetes", "bolsa": "bolsas", "bote": "botes", "botella": "botellas", "brik": "briks",
	"carton": "cartones", "caja": "cajas", "barra": "barras", "bandeja": "bandejas", "pack": "packs", "tarro": "tarros",
	"sobre": "sobres", "rollo": "rollos", "pieza": "piezas", "manojo": "manojos", "malla": "mallas", "tableta": "tabletas",
	"tarrina": "tarrinas", "garrafa": "garrafas", "red": "redes", "racimo": "racimos",
}

var wordUnitPlural = func() map[string]string {
	m := map[string]string{}
	for s, p := range wordUnits {
		m[p] = s
	}
	m["brick"], m["bricks"] = "brik", "brik"
	return m
}()

var numberWords = map[string]float64{
	"un": 1, "uno": 1, "una": 1, "dos": 2, "tres": 3, "cuatro": 4, "cinco": 5, "seis": 6, "siete": 7, "ocho": 8,
	"nueve": 9, "diez": 10, "once": 11, "doce": 12, "quince": 15, "veinte": 20, "veinticuatro": 24, "treinta": 30,
	"medio": 0.5, "media": 0.5,
}

// quantity es una cantidad entendida: un número y su unidad (kg, g, l, ml,
// cl, ud, docena, un envase o "" si se cuenta). X dice si se escribió «x6».
type quantity struct {
	N    float64
	Unit string
	X    bool
}

func fold(s string) string { return seal.Fold(s) }

// unitOf reconoce una unidad o un envase.
func unitOf(tok string) (string, bool) {
	f := fold(strings.TrimSuffix(tok, "."))
	if u, ok := unitAlias[f]; ok {
		return u, true
	}
	if _, ok := wordUnits[f]; ok {
		return f, true
	}
	if s, ok := wordUnitPlural[f]; ok {
		return s, true
	}
	return "", false
}

func parseNum(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	return v, err == nil && v > 0 && v < 100000 && !math.IsInf(v, 0)
}

// leadQty lee una cantidad al principio de toks. Devuelve cuántas palabras
// ocupa (con el «de» que la sigue). n == 0: no hay cantidad.
func leadQty(toks []string) (quantity, int) {
	if len(toks) == 0 {
		return quantity{}, 0
	}
	var q quantity
	i := 0
	f0 := fold(toks[0])
	switch {
	case reNum.MatchString(f0):
		m := reNum.FindStringSubmatch(f0)
		v, ok := parseNum(m[2])
		if !ok {
			return quantity{}, 0
		}
		q.N, q.X, i = v, m[1] != "" || m[3] != "", 1
	case reNumUnit.MatchString(f0):
		m := reNumUnit.FindStringSubmatch(f0)
		v, ok := parseNum(m[1])
		u, uok := unitOf(m[2])
		if !ok || !uok {
			return quantity{}, 0
		}
		q.N, q.Unit, i = v, u, 1
	case f0 == "x" && len(toks) > 1 && reNum.MatchString(toks[1]) && !strings.Contains(toks[1], "x"):
		v, ok := parseNum(toks[1])
		if !ok {
			return quantity{}, 0
		}
		q.N, q.X, i = v, true, 2
	default:
		v, ok := numberWords[f0]
		if !ok {
			return quantity{}, 0
		}
		q.N, i = v, 1
	}
	if q.Unit == "" && !q.X && i < len(toks) {
		if u, ok := unitOf(toks[i]); ok {
			q.Unit = u
			i++
		}
	}
	if q.Unit == "" && !q.X {
		// «un melón», «una lechuga»: el artículo no es una cantidad.
		if f0 == "un" || f0 == "una" || f0 == "uno" {
			return quantity{}, 1
		}
	}
	if i < len(toks) && (fold(toks[i]) == "de" || fold(toks[i]) == "del") {
		i++
	}
	return q, i
}

// trailQty lee una cantidad al final de toks: «2 kg», «2kg», «x6», «6x»,
// «x 6», «12». Devuelve cuántas palabras ocupa.
func trailQty(toks []string) (quantity, int) {
	n := len(toks)
	if n < 2 { // tiene que quedar nombre
		return quantity{}, 0
	}
	last := fold(toks[n-1])
	if n >= 3 {
		prev := fold(toks[n-2])
		if u, ok := unitOf(last); ok && reNum.MatchString(prev) && !strings.Contains(prev, "x") {
			if v, ok := parseNum(prev); ok {
				return quantity{N: v, Unit: u}, 2
			}
		}
		if prev == "x" && reNum.MatchString(last) && !strings.Contains(last, "x") {
			if v, ok := parseNum(last); ok {
				return quantity{N: v, X: true}, 2
			}
		}
	}
	if m := reNum.FindStringSubmatch(last); m != nil {
		if v, ok := parseNum(m[2]); ok {
			return quantity{N: v, X: m[1] != "" || m[3] != ""}, 1
		}
	}
	if m := reNumUnit.FindStringSubmatch(last); m != nil {
		v, ok := parseNum(m[1])
		if u, uok := unitOf(m[2]); ok && uok {
			return quantity{N: v, Unit: u}, 1
		}
	}
	return quantity{}, 0
}

// parseQty entiende una cantidad escrita sola («2 kg», «x6», «media
// docena»). ok es false si no es una cantidad (es texto libre).
func parseQty(s string) (quantity, bool) {
	toks := strings.Fields(s)
	if len(toks) == 0 {
		return quantity{}, false
	}
	q, n := leadQty(toks)
	if n == 0 || n != len(toks) || q.N == 0 {
		return quantity{}, false
	}
	return q, true
}

// normalize pasa a unidades base: kg (g), l (ml, cl) y cuenta (ud, docena).
func (q quantity) normalize() quantity {
	switch q.Unit {
	case "g":
		q.N, q.Unit = q.N/1000, "kg"
	case "ml":
		q.N, q.Unit = q.N/1000, "l"
	case "cl":
		q.N, q.Unit = q.N/100, "l"
	case "ud":
		q.Unit = ""
	case "docena":
		q.N, q.Unit = q.N*12, ""
	}
	return q
}

func num(v float64) string {
	v = math.Round(v*1000) / 1000
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", ",")
}

// String es la cantidad como se enseña: «2 kg», «500 g», «1,5 l», «x6»,
// «12», «2 latas».
func (q quantity) String() string {
	q = q.normalize()
	switch q.Unit {
	case "kg":
		if q.N < 1 {
			return num(q.N*1000) + " g"
		}
		return num(q.N) + " kg"
	case "l":
		if q.N < 1 {
			return num(q.N*1000) + " ml"
		}
		return num(q.N) + " l"
	case "":
		if q.N == 0.5 && !q.X {
			return "½"
		}
		if q.X {
			return "x" + num(q.N)
		}
		return num(q.N)
	}
	if q.N == 1 {
		return "1 " + q.Unit
	}
	if q.N < 1 {
		return num(q.N) + " " + q.Unit
	}
	return num(q.N) + " " + wordUnits[q.Unit]
}

// mergeQty junta la cantidad de algo que ya estaba con la nueva. ok es
// false si no se ha podido (se queda la que había).
func mergeQty(had, add string) (string, bool) {
	switch {
	case add == "":
		return had, false
	case had == "":
		return add, true
	}
	a, aok := parseQty(had)
	b, bok := parseQty(add)
	if !aok || !bok {
		return had, false
	}
	a, b = a.normalize(), b.normalize()
	if a.Unit != b.Unit {
		return had, false
	}
	sum := quantity{N: a.N + b.N, Unit: a.Unit, X: a.X}
	s := sum.String()
	if len(s) > maxQty {
		return had, false
	}
	return s, true
}

// clip recorta a n caracteres.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

// splitParts separa el texto en trozos, uno por cosa.
func splitParts(text string) []string {
	if utf8.RuneCountInString(text) > maxAddText {
		text = string([]rune(text)[:maxAddText])
	}
	text = reLeadVerb.ReplaceAllString(text, "")
	text = reTrailList.ReplaceAllString(text, "")
	text = reDecimal.ReplaceAllString(text, "$1.$2")
	text = reYMedio.ReplaceAllString(text, "${1}.5 $2")
	text = reUnYMedio.ReplaceAllString(text, "1.5 $1")
	text = reE.ReplaceAllString(text, ", $1")
	var out []string
	for _, p := range reSplit.Split(text, -1) {
		p = strings.TrimSpace(p)
		p = reBullet.ReplaceAllString(p, "")
		p = reLeadY.ReplaceAllString(p, "")
		p = strings.TrimSpace(strings.TrimRight(p, ".!"))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseItem lee una cosa: nombre, cantidad y nota.
func parseItem(p string) (parsed, bool) {
	var it parsed
	if m := reNote.FindStringSubmatch(p); m != nil && strings.TrimSpace(m[1]) != "" {
		p, it.Note = m[1], clip(m[2], maxNote)
	}
	// «patatas: 2 kg»
	if i := strings.LastIndex(p, ":"); i > 0 {
		name, q := strings.TrimSpace(p[:i]), strings.TrimSpace(p[i+1:])
		if name != "" && q != "" {
			if qq, ok := parseQty(q); ok {
				it.Qty = qq.String()
			} else {
				it.Qty = clip(q, maxQty)
			}
			p = name
		}
	}
	toks := strings.Fields(p)
	if _, ok := parseQty(p); ok { // solo una cantidad, sin nombre
		return parsed{}, false
	}
	if it.Qty == "" {
		if q, n := leadQty(toks); n > 0 && n < len(toks) {
			if q.N > 0 {
				it.Qty = q.String()
			}
			toks = toks[n:]
		} else if q, n := trailQty(toks); n > 0 {
			it.Qty = q.String()
			toks = toks[:len(toks)-n]
			// «patatas de 2 kg»
			if k := len(toks); k > 1 && (fold(toks[k-1]) == "de") {
				toks = toks[:k-1]
			}
		}
	}
	it.Name = clip(strings.Join(toks, " "), maxName)
	if it.Name == "" || nameKey(it.Name) == "" || !reHasLetter.MatchString(it.Name) {
		return parsed{}, false
	}
	return it, true
}

// parseAdd lee el cuadro de añadir: una cosa por trozo, como mucho maxParts.
func parseAdd(text string) []parsed {
	var out []parsed
	for _, p := range splitParts(text) {
		if it, ok := parseItem(p); ok {
			out = append(out, it)
			if len(out) == maxParts {
				break
			}
		}
	}
	return out
}
