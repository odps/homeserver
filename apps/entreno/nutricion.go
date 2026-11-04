package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Nutrición: objetivos del día, alimentos habituales y lo que se come. Los
// totales, las medias y los frecuentes se calculan aquí, sin IA.

const (
	sealGoals = "entreno/goals"
	sealFood  = "entreno/food"
	sealMeal  = "entreno/meal"

	maxFoods    = 500 // habituales por persona
	maxEntryDay = 80  // cosas apuntadas en un día
)

// mealSlot es una comida del día, en orden.
type mealSlot struct{ Key, Label string }

var mealSlots = []mealSlot{
	{"desayuno", "Desayuno"},
	{"almuerzo", "Almuerzo"},
	{"comida", "Comida"},
	{"merienda", "Merienda"},
	{"cena", "Cena"},
	{"otro", "Otro"},
}

func slotLabel(key string) string {
	for _, s := range mealSlots {
		if s.Key == key {
			return s.Label
		}
	}
	return ""
}

// slotFor es la comida que toca a esa hora (de Madrid).
func slotFor(t time.Time) string {
	t = t.In(madrid)
	m := t.Hour()*60 + t.Minute()
	switch {
	case m < 11*60:
		return "desayuno"
	case m < 13*60:
		return "almuerzo"
	case m < 16*60+30:
		return "comida"
	case m < 19*60+30:
		return "merienda"
	}
	return "cena"
}

// macros son las kcal y los gramos de proteína, hidratos y grasa.
type macros struct {
	Kcal float64 `json:"kcal"`
	Prot float64 `json:"proteina"`
	Carb float64 `json:"hidratos,omitempty"`
	Fat  float64 `json:"grasa,omitempty"`
}

func (m macros) add(o macros) macros {
	return macros{m.Kcal + o.Kcal, m.Prot + o.Prot, m.Carb + o.Carb, m.Fat + o.Fat}
}

func (m macros) scale(f float64) macros {
	return macros{m.Kcal * f, m.Prot * f, m.Carb * f, m.Fat * f}
}

// rounded deja las kcal enteras y los gramos con un decimal.
func (m macros) rounded() macros {
	return macros{math.Round(m.Kcal), round1(m.Prot), round1(m.Carb), round1(m.Fat)}
}

func (m macros) check() error {
	if m.Kcal < 0 || m.Kcal > 20000 || m.Prot < 0 || m.Prot > 1000 || m.Carb < 0 || m.Carb > 3000 || m.Fat < 0 || m.Fat > 1000 {
		return errors.New("los valores no son válidos")
	}
	return nil
}

// macrosText es «520 kcal · 32 g prot», con hidratos y grasa si los hay.
func macrosText(m macros) string {
	m = m.rounded()
	s := trimNum(m.Kcal) + " kcal · " + trimNum(m.Prot) + " g prot"
	if m.Carb > 0 {
		s += " · " + trimNum(m.Carb) + " g hidr"
	}
	if m.Fat > 0 {
		s += " · " + trimNum(m.Fat) + " g grasa"
	}
	return s
}

// ---- Objetivos ----

// goals son los objetivos del día. Cero: sin objetivo.
type goals struct {
	Kcal float64 `json:"kcal,omitempty"`
	Prot float64 `json:"proteina,omitempty"`
	Carb float64 `json:"hidratos,omitempty"`
	Fat  float64 `json:"grasa,omitempty"`
}

func (g goals) set() bool { return g.Kcal > 0 || g.Prot > 0 || g.Carb > 0 || g.Fat > 0 }

func (g goals) check() error {
	if g.Kcal != 0 && (g.Kcal < 800 || g.Kcal > 8000) {
		return errors.New("las kcal del día tienen que estar entre 800 y 8000")
	}
	if g.Prot != 0 && (g.Prot < 20 || g.Prot > 500) {
		return errors.New("la proteína tiene que estar entre 20 y 500 g")
	}
	if g.Carb != 0 && (g.Carb < 20 || g.Carb > 1200) {
		return errors.New("los hidratos tienen que estar entre 20 y 1200 g")
	}
	if g.Fat != 0 && (g.Fat < 10 || g.Fat > 400) {
		return errors.New("la grasa tiene que estar entre 10 y 400 g")
	}
	return nil
}

func (s store) goals(ctx context.Context, owner string) (goals, error) {
	var g goals
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT sealed FROM nutrition_goals WHERE owner = $1`, owner).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	return g, s.box.OpenJSON(sealGoals, sealed, &g)
}

// saveGoals guarda los objetivos; sin ninguno, los borra.
func (s store) saveGoals(ctx context.Context, owner string, g goals) error {
	if err := g.check(); err != nil {
		return err
	}
	if !g.set() {
		_, err := s.db.Exec(ctx, `DELETE FROM nutrition_goals WHERE owner = $1`, owner)
		return err
	}
	sealed, err := s.box.SealJSON(sealGoals, g)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO nutrition_goals (owner, sealed) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET sealed = EXCLUDED.sealed, updated_at = now()`, owner, sealed)
	return err
}

// proteinHint es la proteína que se suele recomendar para quien entrena
// (1,6–2,2 g por kg), según el peso. Cero sin peso.
func proteinHint(kg float64) (lo, hi float64) {
	if kg <= 0 {
		return 0, 0
	}
	return math.Round(kg * 1.6), math.Round(kg * 2.2)
}

// ---- Alimentos habituales ----

// Bases de un alimento: los valores van por 100 g, por 100 ml o por unidad.
const (
	baseG  = "g"
	baseML = "ml"
	baseUd = "ud"
)

// food es un alimento o una comida habitual de alguien.
type food struct {
	ID      int64      `json:"-"`
	Name    string     `json:"nombre"`
	Base    string     `json:"base"`             // g, ml (valores por 100) o ud (por unidad)
	Per     macros     `json:"por"`              // por 100 g/ml o por unidad
	Portion float64    `json:"racion"`           // lo que se apunta con un toque (g, ml o unidades)
	Unit    string     `json:"unidad,omitempty"` // con base ud: «yogur», «batido», «ración»
	Items   []mealItem `json:"items,omitempty"`  // si es una comida hecha de varias cosas
	Origin  string     `json:"origen,omitempty"` // usda:<id>, off:<código> o vacío (a mano)
	Note    string     `json:"nota,omitempty"`
}

// mealItem es una cosa dentro de una comida habitual.
type mealItem struct {
	Name   string  `json:"nombre"`
	Amount float64 `json:"cantidad"`
	Unit   string  `json:"unidad"`
	macros
}

func (f *food) check() error {
	f.Name = strings.Join(strings.Fields(f.Name), " ")
	if f.Name == "" || utf8.RuneCountInString(f.Name) > 80 {
		return errors.New("el nombre tiene que tener entre 1 y 80 caracteres")
	}
	switch f.Base {
	case baseG, baseML, baseUd:
	default:
		return errors.New("la base tiene que ser g, ml o ud")
	}
	if err := f.Per.check(); err != nil {
		return err
	}
	if f.Per.Kcal == 0 && f.Per.Prot == 0 && f.Per.Carb == 0 && f.Per.Fat == 0 {
		return errors.New("faltan las kcal o la proteína")
	}
	if f.Base != baseUd && f.Per.Kcal > 950 {
		return errors.New("más de 950 kcal por 100 g no es posible: ¿son los valores de la ración?")
	}
	if f.Portion <= 0 {
		f.Portion = 1
		if f.Base != baseUd {
			f.Portion = 100
		}
	}
	if f.Portion > 5000 {
		return errors.New("la ración es demasiado grande")
	}
	f.Unit = strings.TrimSpace(f.Unit)
	if f.Base != baseUd {
		f.Unit = ""
	}
	if utf8.RuneCountInString(f.Unit) > 30 {
		return errors.New("el nombre de la unidad es demasiado largo")
	}
	if len(f.Items) > 40 {
		return errors.New("demasiados ingredientes")
	}
	f.Note = strings.TrimSpace(f.Note)
	if utf8.RuneCountInString(f.Note) > 300 {
		return errors.New("la nota pasa de 300 caracteres")
	}
	return nil
}

// unitName es cómo se llama una unidad del alimento.
func (f food) unitName() string {
	switch {
	case f.Base == baseG:
		return "g"
	case f.Base == baseML:
		return "ml"
	case f.Unit != "":
		return f.Unit
	}
	return "ración"
}

// valuesFor calcula los valores de una cantidad (en la base del alimento).
func (f food) valuesFor(amount float64) macros {
	if f.Base == baseUd {
		return f.Per.scale(amount)
	}
	return f.Per.scale(amount / 100)
}

func (s store) saveFood(ctx context.Context, owner string, f food) (int64, error) {
	if err := f.check(); err != nil {
		return 0, err
	}
	sealed, err := s.box.SealJSON(sealFood, f)
	if err != nil {
		return 0, err
	}
	if f.ID != 0 {
		tag, err := s.db.Exec(ctx, `UPDATE foods SET sealed = $1 WHERE id = $2 AND owner = $3`, sealed, f.ID, owner)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			return 0, errNotFound
		}
		return f.ID, nil
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM foods WHERE owner = $1`, owner).Scan(&n); err != nil {
		return 0, err
	}
	if n >= maxFoods {
		return 0, fmt.Errorf("ya tienes %d alimentos guardados: borra alguno", maxFoods)
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO foods (owner, sealed) VALUES ($1, $2) RETURNING id`, owner, sealed).Scan(&id)
	return id, err
}

// foods son los habituales de alguien, por nombre.
func (s store) foods(ctx context.Context, owner string) ([]food, error) {
	rows, err := s.db.Query(ctx, `SELECT id, sealed FROM foods WHERE owner = $1`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []food
	for rows.Next() {
		var f food
		var id int64
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			return nil, err
		}
		if err := s.box.OpenJSON(sealFood, sealed, &f); err != nil {
			return nil, err
		}
		f.ID = id
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return fold(out[i].Name) < fold(out[j].Name) })
	return out, rows.Err()
}

func (s store) foodOne(ctx context.Context, owner string, id int64) (food, error) {
	var f food
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT sealed FROM foods WHERE id = $1 AND owner = $2`, id, owner).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, errNotFound
	}
	if err != nil {
		return f, err
	}
	if err := s.box.OpenJSON(sealFood, sealed, &f); err != nil {
		return f, err
	}
	f.ID = id
	return f, nil
}

func (s store) deleteFood(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM foods WHERE id = $1 AND owner = $2`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

// ---- Lo que se come ----

// mealEntry es una cosa apuntada, con sus valores ya calculados.
type mealEntry struct {
	ID      int64     `json:"-"`
	Day     time.Time `json:"-"`
	At      time.Time `json:"-"`
	Source  string    `json:"-"`
	Meal    string    `json:"comida"` // desayuno, almuerzo, comida, merienda, cena, otro
	Name    string    `json:"nombre"`
	Amount  float64   `json:"cantidad,omitempty"`
	Unit    string    `json:"unidad,omitempty"`   // g, ml, o el nombre de la unidad
	FoodID  int64     `json:"alimento,omitempty"` // habitual de donde sale
	Generic string    `json:"generico,omitempty"` // alimento de la tabla básica
	Guess   bool      `json:"estimado,omitempty"` // valores aproximados (Talia, una foto…)
	Note    string    `json:"nota,omitempty"`
	macros
}

func (e *mealEntry) check() error {
	if slotLabel(e.Meal) == "" {
		return errors.New("la comida tiene que ser desayuno, almuerzo, comida, merienda, cena u otro")
	}
	e.Name = strings.Join(strings.Fields(e.Name), " ")
	if e.Name == "" || utf8.RuneCountInString(e.Name) > 120 {
		return errors.New("el nombre tiene que tener entre 1 y 120 caracteres")
	}
	if e.Amount < 0 || e.Amount > 10000 {
		return errors.New("la cantidad no es válida")
	}
	if utf8.RuneCountInString(e.Unit) > 30 {
		return errors.New("la unidad es demasiado larga")
	}
	if err := e.macros.check(); err != nil {
		return err
	}
	if e.Kcal > 6000 {
		return errors.New("más de 6000 kcal de una vez no parece correcto")
	}
	e.macros = e.macros.rounded()
	e.Note = strings.TrimSpace(e.Note)
	if utf8.RuneCountInString(e.Note) > 300 {
		return errors.New("la nota pasa de 300 caracteres")
	}
	return nil
}

// addMeals apunta varias cosas de una vez (en la misma transacción).
func (s store) addMeals(ctx context.Context, owner string, at time.Time, source string, list []mealEntry) ([]int64, error) {
	if len(list) == 0 {
		return nil, errors.New("no hay nada que apuntar")
	}
	for i := range list {
		if err := list[i].check(); err != nil {
			return nil, err
		}
	}
	if source != "asistente" {
		source = "web"
	}
	day := dayOf(at).Format("2006-01-02")
	var ids []int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM food_logs WHERE owner = $1 AND day = $2`, owner, day).Scan(&n); err != nil {
			return err
		}
		if n+len(list) > maxEntryDay {
			return fmt.Errorf("como mucho %d cosas apuntadas por día", maxEntryDay)
		}
		for _, e := range list {
			sealed, err := s.box.SealJSON(sealMeal, e)
			if err != nil {
				return err
			}
			var id int64
			if err := tx.QueryRow(ctx, `INSERT INTO food_logs (owner, day, logged_at, source, sealed) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
				owner, day, at, source, sealed).Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err
}

// meals devuelve lo comido entre dos días (incluidos), en orden de día y
// hora.
func (s store) meals(ctx context.Context, owner string, from, to time.Time) ([]mealEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT id, day, logged_at, source, sealed FROM food_logs
		WHERE owner = $1 AND day BETWEEN $2 AND $3 ORDER BY day, logged_at, id`,
		owner, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mealEntry
	for rows.Next() {
		var e mealEntry
		var id int64
		var day, at time.Time
		var source string
		var sealed []byte
		if err := rows.Scan(&id, &day, &at, &source, &sealed); err != nil {
			return nil, err
		}
		if err := s.box.OpenJSON(sealMeal, sealed, &e); err != nil {
			return nil, err
		}
		e.ID, e.Day, e.At, e.Source = id, day, at, source
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s store) mealOne(ctx context.Context, owner string, id int64) (mealEntry, error) {
	var e mealEntry
	var day, at time.Time
	var source string
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT day, logged_at, source, sealed FROM food_logs WHERE id = $1 AND owner = $2`, id, owner).
		Scan(&day, &at, &source, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, errNotFound
	}
	if err != nil {
		return e, err
	}
	if err := s.box.OpenJSON(sealMeal, sealed, &e); err != nil {
		return e, err
	}
	e.ID, e.Day, e.At, e.Source = id, day, at, source
	return e, nil
}

func (s store) deleteMeal(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM food_logs WHERE id = $1 AND owner = $2`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

// ---- Resúmenes ----

// mealGroup es una comida del día con lo apuntado en ella.
type mealGroup struct {
	Slot    mealSlot
	Entries []mealEntry
	Total   macros
}

// dayFood es lo comido un día, por comidas.
type dayFood struct {
	Day    time.Time
	Groups []mealGroup // solo las comidas con algo
	Total  macros
	Count  int
	Guess  bool // algo lleva valores aproximados
}

// dayOfFood agrupa lo de un día (entries de ese día).
func dayOfFood(day time.Time, entries []mealEntry) dayFood {
	d := dayFood{Day: dayOf(day)}
	for _, slot := range mealSlots {
		g := mealGroup{Slot: slot}
		for _, e := range entries {
			if e.Meal == slot.Key {
				g.Entries = append(g.Entries, e)
				g.Total = g.Total.add(e.macros)
				d.Guess = d.Guess || e.Guess
			}
		}
		if len(g.Entries) > 0 {
			g.Total = g.Total.rounded()
			d.Groups = append(d.Groups, g)
			d.Total = d.Total.add(g.Total)
			d.Count += len(g.Entries)
		}
	}
	d.Total = d.Total.rounded()
	return d
}

// foodWeek es la media de los últimos días con algo apuntado.
type foodWeek struct {
	Days     int       // días con algo apuntado de los 7
	Avg      macros    // media de esos días
	ProtDays int       // días que llegaron a la proteína del objetivo
	KcalDays int       // días dentro de ±10 % de las kcal del objetivo
	Totals   []float64 // kcal de cada uno de los 7 días, del más antiguo a hoy
}

// weekOfFood resume los 7 días que acaban en to (entries de esos días). Un
// día sin nada apuntado no cuenta para la media: no se sabe si se comió.
func weekOfFood(entries []mealEntry, to time.Time, g goals) foodWeek {
	var w foodWeek
	end := dayOf(to)
	byDay := map[string]macros{}
	for _, e := range entries {
		k := e.Day.Format("2006-01-02")
		byDay[k] = byDay[k].add(e.macros)
	}
	var sum macros
	for i := 6; i >= 0; i-- {
		k := end.AddDate(0, 0, -i).Format("2006-01-02")
		m, ok := byDay[k]
		w.Totals = append(w.Totals, math.Round(m.Kcal))
		if !ok {
			continue
		}
		w.Days++
		sum = sum.add(m)
		if g.Prot > 0 && m.Prot >= g.Prot {
			w.ProtDays++
		}
		if g.Kcal > 0 && math.Abs(m.Kcal-g.Kcal) <= g.Kcal*0.1 {
			w.KcalDays++
		}
	}
	if w.Days > 0 {
		w.Avg = sum.scale(1 / float64(w.Days)).rounded()
	}
	return w
}

// frequent es algo que se apunta a menudo, para apuntarlo con un toque.
type frequent struct {
	FoodID  int64
	Generic string
	Name    string
	Amount  float64
	Unit    string
	Times   int
	Last    time.Time
	Values  macros
}

// frequents son las cosas más apuntadas (entries recientes), con la última
// cantidad usada. Lo que ya no existe (un habitual borrado) no sale.
func frequents(entries []mealEntry, own map[int64]food, n int) []frequent {
	byKey := map[string]*frequent{}
	for _, e := range entries {
		var k string
		switch {
		case e.FoodID != 0:
			if _, ok := own[e.FoodID]; !ok {
				continue
			}
			k = fmt.Sprintf("f%d", e.FoodID)
		case e.Generic != "":
			if _, ok := genericByKey(e.Generic); !ok {
				continue
			}
			k = "g" + e.Generic
		default:
			continue
		}
		f := byKey[k]
		if f == nil {
			f = &frequent{FoodID: e.FoodID, Generic: e.Generic}
			byKey[k] = f
		}
		f.Times++
		if !e.At.Before(f.Last) {
			f.Last, f.Name, f.Amount, f.Unit, f.Values = e.At, e.Name, e.Amount, e.Unit, e.macros
		}
	}
	var out []frequent
	for _, f := range byKey {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Times != out[j].Times {
			return out[i].Times > out[j].Times
		}
		return out[i].Last.After(out[j].Last)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// entryText es «200 g Arroz blanco, cocido · 260 kcal · 5.4 g prot».
func entryText(e mealEntry) string {
	s := e.Name
	if a := amountText(e.Amount, e.Unit); a != "" {
		s = a + " " + s
	}
	return s + " · " + macrosText(e.macros)
}

// amountText es «200 g», «250 ml», «2 huevos» (tal cual: la unidad ya va en
// la forma que toque) o vacío.
func amountText(amount float64, unit string) string {
	if amount <= 0 {
		return ""
	}
	if unit == "" {
		return trimNum(amount)
	}
	return trimNum(amount) + " " + unit
}
