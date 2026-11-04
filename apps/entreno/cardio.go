package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Cardio: cada sesión con su tipo, duración, distancia, pulso medio, kcal y
// esfuerzo (1–10). El ritmo y los totales por semana los calcula la app.

const sealCardio = "entreno/cardio"

// cardioType es un tipo de cardio y cómo se mide su ritmo.
type cardioType struct {
	Key, Name string
	Pace      string // «km» (min/km), «kmh» (km/h), «500» (/500 m), «100» (/100 m) o vacío
	Words     []string
}

var cardioTypes = []cardioType{
	{"correr", "Correr", "km", []string{"correr", "carrera", "running", "run", "trote", "trotar"}},
	{"cinta", "Cinta", "km", []string{"cinta", "treadmill"}},
	{"caminar", "Caminar", "km", []string{"caminar", "andar", "paseo", "caminata", "senderismo", "ruta"}},
	{"bici", "Bici", "kmh", []string{"bici", "bicicleta", "ciclismo", "mtb", "bike"}},
	{"bici_estatica", "Bici estática", "kmh", []string{"estatica", "spinning", "ciclo indoor", "assault", "airbike"}},
	{"eliptica", "Elíptica", "", []string{"eliptica", "elliptical"}},
	{"remo", "Remo", "500", []string{"remo", "remoergometro", "rower", "concept2"}},
	{"natacion", "Natación", "100", []string{"natacion", "nadar", "piscina"}},
	{"comba", "Comba", "", []string{"comba", "cuerda", "saltar"}},
	{"escaleras", "Escaleras", "", []string{"escaleras", "stairmaster", "step"}},
	{"otro", "Otro", "", nil},
}

func cardioTypeOf(key string) (cardioType, bool) {
	for _, t := range cardioTypes {
		if t.Key == key {
			return t, true
		}
	}
	return cardioType{}, false
}

// cardioTypeFor reconoce el tipo por su clave o por como lo diga la persona
// («cinta», «bici estática», «nadar»).
func cardioTypeFor(s string) (cardioType, bool) {
	f := fold(strings.TrimSpace(s))
	f = strings.ReplaceAll(f, "_", " ")
	if t, ok := cardioTypeOf(strings.ReplaceAll(f, " ", "_")); ok {
		return t, true
	}
	// Lo más concreto primero: «bici estática» no es «bici».
	best, bestLen := cardioType{}, 0
	for _, t := range cardioTypes {
		for _, w := range t.Words {
			if strings.Contains(" "+f+" ", " "+w) && len(w) > bestLen {
				best, bestLen = t, len(w)
			}
		}
	}
	return best, bestLen > 0
}

// cardioLog es una sesión de cardio.
type cardioLog struct {
	ID      int64     `json:"-"`
	Day     time.Time `json:"-"`
	Start   time.Time `json:"-"`
	Source  string    `json:"-"`
	Type    string    `json:"tipo"`
	Seconds int       `json:"segundos"`
	Meters  float64   `json:"metros,omitempty"`
	HR      int       `json:"pulso,omitempty"`
	Kcal    int       `json:"kcal,omitempty"`
	RPE     int       `json:"esfuerzo,omitempty"` // 1–10
	Note    string    `json:"nota,omitempty"`
}

func (c *cardioLog) check() error {
	if _, ok := cardioTypeOf(c.Type); !ok {
		return errors.New("tipo de cardio desconocido")
	}
	if c.Seconds < 60 || c.Seconds > 24*3600 {
		return errors.New("la duración tiene que estar entre 1 minuto y 24 horas")
	}
	if c.Meters < 0 || c.Meters > 500000 {
		return errors.New("la distancia no es válida")
	}
	if c.HR != 0 && (c.HR < 40 || c.HR > 230) {
		return errors.New("el pulso medio tiene que estar entre 40 y 230")
	}
	if c.Kcal < 0 || c.Kcal > 10000 {
		return errors.New("las kcal no son válidas")
	}
	if c.RPE < 0 || c.RPE > 10 {
		return errors.New("el esfuerzo va del 1 al 10")
	}
	c.Note = strings.TrimSpace(c.Note)
	if utf8.RuneCountInString(c.Note) > 300 {
		return errors.New("la nota pasa de 300 caracteres")
	}
	return nil
}

func (s store) addCardio(ctx context.Context, owner string, c cardioLog) (int64, error) {
	if err := c.check(); err != nil {
		return 0, err
	}
	if c.Source != "asistente" {
		c.Source = "web"
	}
	sealed, err := s.box.SealJSON(sealCardio, c)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO cardio (owner, day, started_at, source, sealed) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		owner, dayOf(c.Start).Format("2006-01-02"), c.Start, c.Source, sealed).Scan(&id)
	return id, err
}

// cardioList devuelve el cardio entre dos días (incluidos), del más
// reciente al más antiguo; limit 0 es sin límite.
func (s store) cardioList(ctx context.Context, owner string, from, to time.Time, limit int) ([]cardioLog, error) {
	if from.IsZero() {
		from = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if limit <= 0 {
		limit = 100000
	}
	rows, err := s.db.Query(ctx, `SELECT id, day, started_at, source, sealed FROM cardio
		WHERE owner = $1 AND day BETWEEN $2 AND $3 ORDER BY day DESC, started_at DESC LIMIT $4`,
		owner, from.Format("2006-01-02"), to.Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cardioLog
	for rows.Next() {
		var c cardioLog
		var id int64
		var day, start time.Time
		var source string
		var sealed []byte
		if err := rows.Scan(&id, &day, &start, &source, &sealed); err != nil {
			return nil, err
		}
		if err := s.box.OpenJSON(sealCardio, sealed, &c); err != nil {
			return nil, err
		}
		c.ID, c.Day, c.Start, c.Source = id, day, start, source
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s store) cardioOne(ctx context.Context, owner string, id int64) (cardioLog, error) {
	var c cardioLog
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT day, started_at, source, sealed FROM cardio WHERE id = $1 AND owner = $2`, id, owner).
		Scan(&c.Day, &c.Start, &c.Source, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errNotFound
	}
	if err != nil {
		return c, err
	}
	day, start, source := c.Day, c.Start, c.Source
	if err := s.box.OpenJSON(sealCardio, sealed, &c); err != nil {
		return c, err
	}
	c.ID, c.Day, c.Start, c.Source = id, day, start, source
	return c, nil
}

func (s store) deleteCardio(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM cardio WHERE id = $1 AND owner = $2`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

// ---- Textos ----

// durText es «45 min», «1 h 5 min» o, con segundos, «28:30» / «1:05:30».
func durText(sec int) string {
	h, m, s := sec/3600, sec%3600/60, sec%60
	if s != 0 {
		if h > 0 {
			return fmt.Sprintf("%d:%02d:%02d", h, m, s)
		}
		return fmt.Sprintf("%d:%02d", m, s)
	}
	if h > 0 {
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
	return fmt.Sprintf("%d min", m)
}

// distText es «5.2 km» o «800 m».
func distText(m float64) string {
	if m >= 1000 {
		return trimNum(math.Round(m/10)/100) + " km"
	}
	return trimNum(math.Round(m)) + " m"
}

func mmss(sec float64) string {
	s := int(math.Round(sec))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// paceText es el ritmo según el tipo: «5:29 min/km», «24.3 km/h»,
// «2:05 /500 m» o «1:55 /100 m». Vacío sin distancia.
func paceText(c cardioLog) string {
	t, _ := cardioTypeOf(c.Type)
	if c.Meters <= 0 || c.Seconds <= 0 {
		return ""
	}
	sec := float64(c.Seconds)
	switch t.Pace {
	case "km":
		return mmss(sec/(c.Meters/1000)) + " min/km"
	case "kmh":
		return trimNum(math.Round(c.Meters/1000/(sec/3600)*10)/10) + " km/h"
	case "500":
		return mmss(sec/(c.Meters/500)) + " /500 m"
	case "100":
		return mmss(sec/(c.Meters/100)) + " /100 m"
	}
	return ""
}

// cardioText es la línea de una sesión: «Correr — 5.2 km en 28:30 · 5:29 min/km · 152 ppm».
func cardioText(c cardioLog) string {
	t, _ := cardioTypeOf(c.Type)
	s := t.Name + " — "
	if c.Meters > 0 {
		s += distText(c.Meters) + " en " + durText(c.Seconds)
	} else {
		s += durText(c.Seconds)
	}
	return s + cardioExtras(c)
}

func cardioExtras(c cardioLog) string {
	var parts []string
	if p := paceText(c); p != "" {
		parts = append(parts, p)
	}
	if c.HR > 0 {
		parts = append(parts, strconv.Itoa(c.HR)+" ppm")
	}
	if c.Kcal > 0 {
		parts = append(parts, strconv.Itoa(c.Kcal)+" kcal")
	}
	if c.RPE > 0 {
		parts = append(parts, "esfuerzo "+strconv.Itoa(c.RPE)+"/10")
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// ---- Entrada ----

var reDuration = regexp.MustCompile(`^(?:(\d{1,2}):)?(\d{1,3}):(\d{2})$`)

// parseDuration lee «45» (minutos), «45.5», «45:30» (min:s) o «1:05:30».
func parseDuration(s string) (int, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "min"), " ")
	if s == "" {
		return 0, errors.New("falta la duración")
	}
	if m := reDuration.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		se, _ := strconv.Atoi(m[3])
		if se >= 60 || (m[1] != "" && mi >= 60) {
			return 0, errors.New("la duración no es válida (usa 45, 45:30 o 1:05:30)")
		}
		return h*3600 + mi*60 + se, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || f > 24*60 {
		return 0, errors.New("la duración no es válida (usa 45, 45:30 o 1:05:30)")
	}
	return int(math.Round(f * 60)), nil
}

// parseNumber lee un número con coma o punto (vacío: 0).
func parseNumber(s string) (float64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("número no válido")
	}
	return f, nil
}

// ---- Semanas ----

// cardioWeek suma el cardio de una semana (de lunes a domingo).
type cardioWeek struct {
	Monday  time.Time
	Count   int
	Seconds int
	Meters  float64
}

func mondayOf(t time.Time) time.Time {
	d := dayOf(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// cardioWeeks suma las n últimas semanas, de la más antigua a la actual.
func cardioWeeks(list []cardioLog, today time.Time, n int) []cardioWeek {
	first := mondayOf(today).AddDate(0, 0, -7*(n-1))
	weeks := make([]cardioWeek, n)
	for i := range weeks {
		weeks[i].Monday = first.AddDate(0, 0, 7*i)
	}
	for _, c := range list {
		d := time.Date(c.Day.Year(), c.Day.Month(), c.Day.Day(), 0, 0, 0, 0, madrid)
		i := int(math.Round(d.Sub(first).Hours()/24)) / 7
		if d.Before(first) || i < 0 || i >= n {
			continue
		}
		weeks[i].Count++
		weeks[i].Seconds += c.Seconds
		weeks[i].Meters += c.Meters
	}
	return weeks
}
