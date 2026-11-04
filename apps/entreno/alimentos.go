package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Alimentos: la tabla básica (datos/alimentos.json, de USDA FoodData
// Central, dominio público; ver datos/LEEME-alimentos.md), los habituales de
// cada uno y cómo se entiende «200 g de arroz, 2 huevos» sin IA.

//go:embed datos/alimentos.json
var alimentosJSON []byte

// genericFood es un alimento de la tabla básica; los valores, por 100 g.
type genericFood struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	FDC     int      `json:"fdc"`
	Kcal    float64  `json:"kcal"`
	Prot    float64  `json:"prot"`
	Carb    float64  `json:"carb"`
	Fat     float64  `json:"fat"`
	Fiber   float64  `json:"fiber"`
	Unit    string   `json:"unit,omitempty"`   // «huevo», «rebanada»…
	UnitG   float64  `json:"unit_g,omitempty"` // gramos de una unidad
	Liquid  bool     `json:"liquid,omitempty"` // se mide en ml
	Default bool     `json:"default,omitempty"`
}

func (g genericFood) per100() macros { return macros{g.Kcal, g.Prot, g.Carb, g.Fat} }

func (g genericFood) base() string {
	if g.Liquid {
		return baseML
	}
	return baseG
}

var generic struct {
	once  sync.Once
	list  []genericFood
	byKey map[string]int
}

func genericFoods() []genericFood {
	generic.once.Do(func() {
		if err := json.Unmarshal(alimentosJSON, &generic.list); err != nil {
			panic("datos/alimentos.json: " + err.Error())
		}
		generic.byKey = map[string]int{}
		for i, g := range generic.list {
			generic.byKey[g.Key] = i
		}
	})
	return generic.list
}

func genericByKey(key string) (genericFood, bool) {
	genericFoods()
	i, ok := generic.byKey[key]
	if !ok {
		return genericFood{}, false
	}
	return generic.list[i], true
}

// ---- Buscar por nombre ----

var foodStop = map[string]bool{"de": true, "del": true, "con": true, "la": true, "el": true, "los": true, "las": true, "en": true, "al": true, "a": true, "y": true, "un": true, "una": true, "unos": true, "unas": true}

// stem quita el plural de forma tosca: «tomates» y «tomate» quedan igual,
// como «melocotones» y «melocotón».
func stem(w string) string {
	if len(w) <= 3 {
		return w
	}
	w = strings.TrimSuffix(w, "s")
	if len(w) > 3 {
		w = strings.TrimSuffix(w, "e")
	}
	return w
}

func foodWords(s string) []string {
	var out []string
	for _, w := range strings.Fields(fold(s)) {
		if !foodStop[w] {
			out = append(out, stem(w))
		}
	}
	return out
}

// foodMatch es un alimento encontrado: uno habitual o uno de la tabla.
type foodMatch struct {
	Own   *food
	Gen   *genericFood
	Score int
}

func (m foodMatch) Name() string {
	if m.Own != nil {
		return m.Own.Name
	}
	return m.Gen.Name
}

// nameScore puntúa un nombre frente a lo buscado: 100 si es igual, 60–80 si
// lo contiene todo (más cuantas menos palabras sobren) y menos de 60 si solo
// coincide en parte.
func nameScore(q []string, name string) int {
	c := foodWords(name)
	if len(q) == 0 || len(c) == 0 {
		return 0
	}
	if strings.Join(q, " ") == strings.Join(c, " ") {
		return 100
	}
	have := map[string]bool{}
	for _, w := range c {
		have[w] = true
	}
	hit := 0
	for _, w := range q {
		if have[w] {
			hit++
		}
	}
	if hit == len(q) {
		return 60 + 20*len(q)/len(c)
	}
	return 40 * hit / len(q)
}

// matchFoods busca por nombre entre los habituales y la tabla básica, de
// más a menos parecido. Los habituales van por delante.
func matchFoods(query string, own []food, min, n int) []foodMatch {
	q := foodWords(query)
	var out []foodMatch
	for i := range own {
		if s := nameScore(q, own[i].Name); s >= min {
			out = append(out, foodMatch{Own: &own[i], Score: s + 15})
		}
	}
	list := genericFoods()
	for i := range list {
		g := &list[i]
		best := nameScore(q, g.Name)
		for _, a := range g.Aliases {
			best = max(best, nameScore(q, a))
		}
		if best >= min {
			if g.Default {
				best += 5
			}
			out = append(out, foodMatch{Gen: g, Score: best})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return len(out[i].Name()) < len(out[j].Name())
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// ---- «200 g de arroz, 2 huevos» ----

// foodReq es una cosa pedida: cantidad, unidad y nombre.
type foodReq struct {
	Amount float64 // 0: sin cantidad
	Unit   string  // g, ml, o una palabra («rebanada», «lata»); vacío: unidades del alimento
	Name   string
	Whole  string // con Unit palabra, el nombre entero («claras de huevo»)
}

var numberWords = map[string]float64{
	"un": 1, "una": 1, "uno": 1, "medio": 0.5, "media": 0.5, "dos": 2, "tres": 3, "cuatro": 4,
	"cinco": 5, "seis": 6, "siete": 7, "ocho": 8, "nueve": 9, "diez": 10, "doce": 12,
}

// unitWords son las unidades de medida: a qué se pasan y cuánto valen.
var unitWords = map[string]struct {
	unit string
	mult float64
}{
	"g": {baseG, 1}, "gr": {baseG, 1}, "grs": {baseG, 1}, "gramo": {baseG, 1}, "gramos": {baseG, 1},
	"kg": {baseG, 1000}, "kilo": {baseG, 1000}, "kilos": {baseG, 1000},
	"ml": {baseML, 1}, "mililitro": {baseML, 1}, "mililitros": {baseML, 1},
	"cl": {baseML, 10}, "l": {baseML, 1000}, "litro": {baseML, 1000}, "litros": {baseML, 1000},
	"ud": {baseUd, 1}, "uds": {baseUd, 1}, "unidad": {baseUd, 1}, "unidades": {baseUd, 1},
}

// measures son medidas caseras con su peso aproximado en gramos (o ml),
// para cuando el alimento no tiene la suya.
var measures = map[string]float64{
	"cucharada": 15, "cucharadita": 5, "vaso": 250, "taza": 250, "tazon": 350, "bol": 300,
	"punado": 30, "scoop": 30, "cazo": 30, "loncha": 20, "rebanada": 35, "lata": 80, "plato": 300,
}

// splitFoods parte un texto en cosas: por líneas, comas, «y» o «+».
func splitFoods(text string) []string {
	text = strings.NewReplacer("\n", ",", ";", ",", "+", ",").Replace(text)
	var out []string
	for _, part := range strings.Split(text, ",") {
		for _, p := range strings.Split(" "+part+" ", " y ") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func parseAmount(w string) (float64, bool) {
	if v, ok := numberWords[w]; ok {
		return v, true
	}
	v, err := strconv.ParseFloat(strings.Replace(w, ",", ".", 1), 64)
	return v, err == nil && v > 0 && !math.IsInf(v, 0)
}

// parseFoodReq entiende «200 g de arroz», «200g arroz», «2 huevos», «una
// lata de atún», «medio aguacate», «arroz 200 g» o solo «arroz».
func parseFoodReq(text string) foodReq {
	words := strings.Fields(strings.ToLower(text))
	// «200g» → «200 g»
	var ws []string
	for _, w := range words {
		i := strings.IndexFunc(w, func(r rune) bool { return (r < '0' || r > '9') && r != ',' && r != '.' })
		if i > 0 {
			if _, ok := unitWords[w[i:]]; ok {
				ws = append(ws, w[:i], w[i:])
				continue
			}
		}
		ws = append(ws, w)
	}
	var r foodReq
	take := func(ws []string) []string {
		if len(ws) == 0 {
			return ws
		}
		v, ok := parseAmount(ws[0])
		if !ok {
			return ws
		}
		r.Amount, ws = v, ws[1:]
		if len(ws) == 0 {
			return ws
		}
		if u, ok := unitWords[ws[0]]; ok {
			r.Amount *= u.mult
			r.Unit, ws = u.unit, ws[1:]
		} else if len(ws) > 2 && ws[1] == "de" {
			r.Whole = strings.Join(ws, " ")
			r.Unit, ws = singular(fold(ws[0])), ws[1:]
		} else if _, ok := measures[singular(fold(ws[0]))]; ok && len(ws) > 1 {
			r.Whole = strings.Join(ws, " ")
			r.Unit, ws = singular(fold(ws[0])), ws[1:]
		}
		if r.Unit == baseUd {
			r.Unit = ""
		}
		if len(ws) > 0 && ws[0] == "de" {
			ws = ws[1:]
		}
		return ws
	}
	rest := take(ws)
	if r.Amount == 0 && len(ws) >= 2 {
		// «arroz 200 g» o «arroz 200g»
		for i := len(ws) - 2; i >= 1; i-- {
			if _, ok := parseAmount(ws[i]); ok {
				saved := r
				tail := take(ws[i:])
				if len(tail) == 0 {
					rest = ws[:i]
					break
				}
				r = saved
			}
		}
		if r.Amount == 0 {
			if v, ok := parseAmount(ws[len(ws)-1]); ok {
				r.Amount, rest = v, ws[:len(ws)-1]
			}
		}
	}
	r.Name = strings.Join(rest, " ")
	return r
}

// singular quita el plural a una palabra de medida: «rebanadas» → «rebanada».
func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ones"):
		return strings.TrimSuffix(w, "es")
	case strings.HasSuffix(w, "s") && len(w) > 3:
		return strings.TrimSuffix(w, "s")
	}
	return w
}

// plural pone una unidad en plural si hace falta: «2 huevos», «2 raciones».
func plural(unit string, n float64) string {
	if n <= 1 || unit == "" || unit == baseG || unit == baseML || strings.HasSuffix(unit, "s") {
		return unit
	}
	if strings.HasSuffix(unit, "ón") {
		return strings.TrimSuffix(unit, "ón") + "ones"
	}
	last := unit[len(unit)-1]
	if strings.ContainsRune("aeiou", rune(last)) {
		return unit + "s"
	}
	return unit + "es"
}

// entryFor calcula lo que se apunta de un alimento encontrado.
func entryFor(m foodMatch, r foodReq) (mealEntry, error) {
	if m.Own != nil {
		return ownEntry(*m.Own, r)
	}
	return genericEntry(*m.Gen, r)
}

func ownEntry(f food, r foodReq) (mealEntry, error) {
	e := mealEntry{Name: f.Name, FoodID: f.ID}
	switch {
	case r.Unit == baseG || r.Unit == baseML:
		if f.Base == baseUd {
			return e, fmt.Errorf("«%s» se apunta por %s, no por %s", f.Name, plural(f.unitName(), 2), r.Unit)
		}
		e.Amount, e.Unit = r.Amount, f.unitName()
	case f.Base == baseUd:
		e.Amount = r.Amount
		if e.Amount == 0 {
			e.Amount = f.Portion
		}
		e.Unit = plural(f.unitName(), e.Amount)
	default: // por 100 g/ml, sin gramos: raciones
		n := r.Amount
		if n == 0 {
			n = 1
		}
		e.Amount, e.Unit = n*f.Portion, f.unitName()
	}
	if e.Amount <= 0 {
		return e, fmt.Errorf("falta la cantidad de «%s»", f.Name)
	}
	e.macros = f.valuesFor(e.Amount)
	return e, nil
}

func genericEntry(g genericFood, r foodReq) (mealEntry, error) {
	e := mealEntry{Name: g.Name, Generic: g.Key}
	grams := 0.0
	switch {
	case r.Unit == baseG || r.Unit == baseML:
		grams = r.Amount
		e.Amount, e.Unit = r.Amount, g.base()
	case r.Unit == "" || (g.Unit != "" && fold(r.Unit) == fold(g.Unit)):
		if g.UnitG <= 0 {
			return e, fmt.Errorf("¿cuántos gramos de %s?", strings.ToLower(g.Name))
		}
		n := r.Amount
		if n == 0 {
			n = 1
		}
		grams = n * g.UnitG
		e.Amount, e.Unit = n, plural(g.Unit, n)
	default:
		per, ok := measures[fold(r.Unit)]
		if !ok {
			return e, fmt.Errorf("no sé cuánto es «%s» de %s: dilo en gramos", r.Unit, strings.ToLower(g.Name))
		}
		n := r.Amount
		if n == 0 {
			n = 1
		}
		grams = n * per
		e.Amount, e.Unit = n, plural(r.Unit, n)
	}
	if grams <= 0 {
		return e, fmt.Errorf("falta la cantidad de %s", strings.ToLower(g.Name))
	}
	e.macros = g.per100().scale(grams / 100)
	return e, nil
}

// resolveFoods entiende un texto entero. Si algo no se encuentra o no se
// entiende, no se apunta nada: se devuelve qué falló.
func resolveFoods(text string, own []food) ([]mealEntry, []string, error) {
	parts := splitFoods(text)
	if len(parts) == 0 {
		return nil, nil, errors.New("escribe qué has comido (por ejemplo: «200 g de arroz, 2 huevos»)")
	}
	if len(parts) > 30 {
		return nil, nil, errors.New("demasiadas cosas de una vez")
	}
	var out []mealEntry
	var missing, problems []string
	for _, p := range parts {
		r := parseFoodReq(p)
		if r.Name == "" {
			problems = append(problems, fmt.Sprintf("falta qué es «%s»", p))
			continue
		}
		found := matchFoods(r.Name, own, 60, 1)
		if r.Whole != "" {
			// «3 claras de huevo»: si el nombre entero es un alimento, va antes
			// que «clara» como medida de «huevo».
			if alt := matchFoods(r.Whole, own, 80, 1); len(alt) > 0 && (len(found) == 0 || alt[0].Score >= found[0].Score) {
				found, r = alt, foodReq{Amount: r.Amount, Name: r.Whole}
			}
		}
		if len(found) == 0 {
			if r.Whole != "" {
				r.Name = r.Whole
			}
			missing = append(missing, r.Name)
			continue
		}
		e, err := entryFor(found[0], r)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, e)
	}
	if len(missing) > 0 || len(problems) > 0 {
		msg := ""
		if len(missing) > 0 {
			msg = "no encuentro " + strings.Join(missing, ", ")
		}
		if len(problems) > 0 {
			if msg != "" {
				msg += "; "
			}
			msg += strings.Join(problems, "; ")
		}
		return nil, missing, errors.New(msg)
	}
	return out, nil, nil
}

// ---- Apuntar ----

// logText apunta lo que diga un texto («200 g de arroz, 2 huevos») en una
// comida de ese momento (meal vacío: la que toque por la hora).
func (s *service) logText(ctx context.Context, owner, text, meal string, at time.Time, source string) ([]mealEntry, []string, error) {
	own, err := s.store.foods(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	list, missing, err := resolveFoods(text, own)
	if err != nil {
		return nil, missing, err
	}
	return s.saveEntries(ctx, owner, list, meal, at, source)
}

func (s *service) saveEntries(ctx context.Context, owner string, list []mealEntry, meal string, at time.Time, source string) ([]mealEntry, []string, error) {
	if meal == "" {
		meal = slotFor(at)
	}
	for i := range list {
		list[i].Meal = meal
	}
	ids, err := s.store.addMeals(ctx, owner, at, source, list)
	if err != nil {
		return nil, nil, err
	}
	for i := range list {
		list[i].ID, list[i].Day, list[i].At, list[i].Source = ids[i], dayOf(at), at, source
		list[i].macros = list[i].macros.rounded()
	}
	return list, nil, nil
}

// logFood apunta un habitual (amount 0: su ración).
func (s *service) logFood(ctx context.Context, owner string, id int64, amount float64, meal string, at time.Time) (mealEntry, error) {
	f, err := s.store.foodOne(ctx, owner, id)
	if err != nil {
		return mealEntry{}, err
	}
	r := foodReq{Amount: amount}
	if amount > 0 && f.Base != baseUd {
		r.Unit = f.Base
	}
	e, err := ownEntry(f, r)
	if err != nil {
		return e, err
	}
	list, _, err := s.saveEntries(ctx, owner, []mealEntry{e}, meal, at, "web")
	if err != nil {
		return e, err
	}
	return list[0], nil
}

// logGeneric apunta un alimento de la tabla básica: gramos (o ml) o, con
// unit "ud", unidades.
func (s *service) logGeneric(ctx context.Context, owner, key string, amount float64, unit, meal string, at time.Time) (mealEntry, error) {
	g, ok := genericByKey(key)
	if !ok {
		return mealEntry{}, errNotFound
	}
	r := foodReq{Amount: amount, Unit: g.base()}
	if unit == baseUd {
		r.Unit = ""
	}
	e, err := genericEntry(g, r)
	if err != nil {
		return e, err
	}
	list, _, err := s.saveEntries(ctx, owner, []mealEntry{e}, meal, at, "web")
	if err != nil {
		return e, err
	}
	return list[0], nil
}

// copyMeals copia lo de un día (o solo una comida suya) a otro momento; con
// target, a esa comida.
func (s *service) copyMeals(ctx context.Context, owner string, from time.Time, meal, target string, at time.Time, source string) ([]mealEntry, error) {
	list, err := s.store.meals(ctx, owner, dayOf(from), dayOf(from))
	if err != nil {
		return nil, err
	}
	var out []mealEntry
	for _, e := range list {
		if meal == "" || e.Meal == meal {
			e.ID = 0
			if target != "" {
				e.Meal = target
			}
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("ese día no hay nada apuntado que copiar")
	}
	if source != "asistente" {
		source = "web"
	}
	ids, err := s.store.addMeals(ctx, owner, at, source, out)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ID, out[i].Day, out[i].At, out[i].Source = ids[i], dayOf(at), at, source
	}
	return out, nil
}

// saveMealAsFood guarda lo de una comida de un día como comida habitual,
// para apuntarla entera con un toque.
func (s *service) saveMealAsFood(ctx context.Context, owner string, day time.Time, meal, name string) (int64, error) {
	list, err := s.store.meals(ctx, owner, dayOf(day), dayOf(day))
	if err != nil {
		return 0, err
	}
	f := food{Name: name, Base: baseUd, Portion: 1, Unit: "ración"}
	for _, e := range list {
		if e.Meal != meal {
			continue
		}
		f.Items = append(f.Items, mealItem{Name: e.Name, Amount: e.Amount, Unit: e.Unit, macros: e.macros})
		f.Per = f.Per.add(e.macros)
	}
	if len(f.Items) == 0 {
		return 0, errors.New("en esa comida no hay nada apuntado")
	}
	f.Per = f.Per.rounded()
	if strings.TrimSpace(f.Name) == "" {
		f.Name = slotLabel(meal) + " del " + day.Format("02/01")
	}
	return s.store.saveFood(ctx, owner, f)
}

// foodFromGeneric es un habitual hecho a partir de la tabla básica.
func foodFromGeneric(g genericFood) food {
	f := food{Name: g.Name, Base: g.base(), Per: g.per100(), Portion: 100, Origin: fmt.Sprintf("usda:%d", g.FDC)}
	if g.UnitG > 0 {
		f.Portion = g.UnitG
	}
	return f
}
