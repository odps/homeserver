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

// Peso corporal, grasa y medidas: una fila cifrada por persona y día. Las
// tendencias (media de 7 días, cambio por semana) se calculan aquí, sin IA.

const sealBody = "entreno/body"

// measureSite es un sitio donde medirse, en el orden de la web.
type measureSite struct{ Key, Label, Hint string }

var measureSites = []measureSite{
	{"cuello", "Cuello", "por debajo de la nuez"},
	{"hombros", "Hombros", "por la parte más ancha"},
	{"pecho", "Pecho", "a la altura de los pezones"},
	{"cintura", "Cintura", "a la altura del ombligo, sin meter tripa"},
	{"cadera", "Cadera", "por la parte más ancha del glúteo"},
	{"brazo", "Brazo", "bíceps contraído, el más ancho"},
	{"muslo", "Muslo", "a medio camino entre cadera y rodilla"},
	{"gemelo", "Gemelo", "por la parte más ancha"},
}

func siteLabel(key string) string {
	for _, s := range measureSites {
		if s.Key == key {
			return s.Label
		}
	}
	return key
}

// bodyLog es lo apuntado un día. Los campos que faltan no se midieron.
type bodyLog struct {
	Day      time.Time          `json:"-"`
	Kg       *float64           `json:"kg,omitempty"`
	Fat      *float64           `json:"grasa,omitempty"`   // %
	Measures map[string]float64 `json:"medidas,omitempty"` // cm
	Note     string             `json:"nota,omitempty"`
}

func (b bodyLog) empty() bool {
	return b.Kg == nil && b.Fat == nil && len(b.Measures) == 0 && strings.TrimSpace(b.Note) == ""
}

// check valida lo apuntado (en el servidor, venga de la web o de Talia).
func (b *bodyLog) check() error {
	if b.Kg != nil && (*b.Kg < 20 || *b.Kg > 400) {
		return errors.New("el peso tiene que estar entre 20 y 400 kg")
	}
	if b.Fat != nil && (*b.Fat < 2 || *b.Fat > 70) {
		return errors.New("la grasa tiene que estar entre el 2 y el 70 %")
	}
	for k, v := range b.Measures {
		if siteLabel(k) == k {
			return fmt.Errorf("medida desconocida: %s (usa %s)", k, siteKeys())
		}
		if v < 5 || v > 300 {
			return fmt.Errorf("la medida de %s tiene que estar entre 5 y 300 cm", strings.ToLower(siteLabel(k)))
		}
	}
	b.Note = strings.TrimSpace(b.Note)
	if utf8.RuneCountInString(b.Note) > 300 {
		return errors.New("la nota pasa de 300 caracteres")
	}
	return nil
}

func siteKeys() string {
	var keys []string
	for _, s := range measureSites {
		keys = append(keys, s.Key)
	}
	return strings.Join(keys, ", ")
}

// merge pone encima lo nuevo que venga (lo que no viene se queda).
func (b bodyLog) merge(upd bodyLog) bodyLog {
	if upd.Kg != nil {
		b.Kg = upd.Kg
	}
	if upd.Fat != nil {
		b.Fat = upd.Fat
	}
	if len(upd.Measures) > 0 {
		m := map[string]float64{}
		for k, v := range b.Measures {
			m[k] = v
		}
		for k, v := range upd.Measures {
			m[k] = v
		}
		b.Measures = m
	}
	if upd.Note != "" {
		b.Note = upd.Note
	}
	return b
}

// saveBody guarda lo de un día. Con merge, añade a lo que ya hubiera (Talia:
// «hoy peso 78,4»); sin él, lo sustituye (el formulario del día). Si queda
// vacío, se borra el día.
func (s store) saveBody(ctx context.Context, owner string, day time.Time, upd bodyLog, merge bool) (bodyLog, error) {
	if err := upd.check(); err != nil {
		return bodyLog{}, err
	}
	d := day.Format("2006-01-02")
	var out bodyLog
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		cur := bodyLog{}
		if merge {
			var sealed []byte
			err := tx.QueryRow(ctx, `SELECT sealed FROM body_logs WHERE owner = $1 AND day = $2 FOR UPDATE`, owner, d).Scan(&sealed)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				if err := s.box.OpenJSON(sealBody, sealed, &cur); err != nil {
					return err
				}
			}
			cur = cur.merge(upd)
		} else {
			cur = upd
		}
		cur.Day = dayOf(day)
		out = cur
		if cur.empty() {
			_, err := tx.Exec(ctx, `DELETE FROM body_logs WHERE owner = $1 AND day = $2`, owner, d)
			return err
		}
		sealed, err := s.box.SealJSON(sealBody, cur)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO body_logs (owner, day, sealed) VALUES ($1, $2, $3)
			ON CONFLICT (owner, day) DO UPDATE SET sealed = EXCLUDED.sealed, updated_at = now()`, owner, d, sealed)
		return err
	})
	return out, err
}

// bodyLogs devuelve lo apuntado entre dos días (incluidos), del más
// reciente al más antiguo. Sin límites de fecha, todo.
func (s store) bodyLogs(ctx context.Context, owner string, from, to time.Time) ([]bodyLog, error) {
	if from.IsZero() {
		from = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	rows, err := s.db.Query(ctx, `SELECT day, sealed FROM body_logs WHERE owner = $1 AND day BETWEEN $2 AND $3 ORDER BY day DESC`,
		owner, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bodyLog
	for rows.Next() {
		var day time.Time
		var sealed []byte
		if err := rows.Scan(&day, &sealed); err != nil {
			return nil, err
		}
		var b bodyLog
		if err := s.box.OpenJSON(sealBody, sealed, &b); err != nil {
			return nil, err
		}
		b.Day = day
		out = append(out, b)
	}
	return out, rows.Err()
}

// bodyOn es lo apuntado un día (vacío si nada).
func (s store) bodyOn(ctx context.Context, owner string, day time.Time) (bodyLog, bool, error) {
	list, err := s.bodyLogs(ctx, owner, day, day)
	if err != nil || len(list) == 0 {
		return bodyLog{Day: dayOf(day)}, false, err
	}
	return list[0], true, nil
}

func (s store) deleteBody(ctx context.Context, owner string, day time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM body_logs WHERE owner = $1 AND day = $2`, owner, day.Format("2006-01-02"))
	return err
}

// ---- Tendencias ----

// weightTrend resume el peso. El de un día suelto engaña (agua, comida):
// por eso se comparan medias de 7 días.
type weightTrend struct {
	Last     float64   // último peso apuntado
	LastDay  time.Time //
	Avg7     float64   // media de los 7 días que acaban hoy (0: sin datos)
	Week     *float64  // cambio de la media frente a la semana anterior
	Month    *float64  // cambio frente a la media de hace 4 semanas
	Count    int       // pesajes en total
	Min, Max float64
}

// avgIn es la media de los pesos entre dos días (incluidos). Se compara la
// fecha como texto: los días de la base de datos vienen a medianoche UTC y
// los límites, a medianoche de Madrid.
func avgIn(logs []bodyLog, from, to time.Time) (float64, bool) {
	sum, n := 0.0, 0
	f, t := from.Format("2006-01-02"), to.Format("2006-01-02")
	for _, b := range logs {
		if d := b.Day.Format("2006-01-02"); b.Kg != nil && d >= f && d <= t {
			sum += *b.Kg
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// trend calcula la tendencia del peso hasta hoy (logs de más reciente a más
// antiguo).
func trend(logs []bodyLog, today time.Time) *weightTrend {
	var t weightTrend
	for _, b := range logs {
		if b.Kg == nil {
			continue
		}
		if t.Count == 0 {
			t.Last, t.LastDay, t.Min, t.Max = *b.Kg, b.Day, *b.Kg, *b.Kg
		}
		t.Min, t.Max = math.Min(t.Min, *b.Kg), math.Max(t.Max, *b.Kg)
		t.Count++
	}
	if t.Count == 0 {
		return nil
	}
	day := func(n int) time.Time { return dayOf(today).AddDate(0, 0, n) }
	now, ok := avgIn(logs, day(-6), day(0))
	if !ok {
		return &t
	}
	t.Avg7 = round1(now)
	if prev, ok := avgIn(logs, day(-13), day(-7)); ok {
		v := round1(now - prev)
		t.Week = &v
	}
	if prev, ok := avgIn(logs, day(-34), day(-28)); ok {
		v := round1(now - prev)
		t.Month = &v
	}
	return &t
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// signed es «+0.4» o «−0.3» (con el menos de verdad).
func signed(v float64) string {
	switch {
	case v > 0:
		return "+" + trimNum(v)
	case v < 0:
		return "−" + trimNum(-v)
	}
	return "0"
}

// measureRow es cómo va una medida: la última, la anterior y la primera.
type measureRow struct {
	Key, Label string
	Last       float64
	LastDay    time.Time
	Prev       *float64 // la medida anterior a la última
	First      float64
	FirstDay   time.Time
	Times      int
}

// measureRows resume cada medida apuntada alguna vez (logs de más reciente
// a más antiguo), en el orden de measureSites.
func measureRows(logs []bodyLog) []measureRow {
	rows := map[string]*measureRow{}
	for _, b := range logs {
		for k, v := range b.Measures {
			r := rows[k]
			if r == nil {
				rows[k] = &measureRow{Key: k, Label: siteLabel(k), Last: v, LastDay: b.Day, First: v, FirstDay: b.Day, Times: 1}
				continue
			}
			if r.Times == 1 {
				p := v
				r.Prev = &p
			}
			r.First, r.FirstDay = v, b.Day
			r.Times++
		}
	}
	var out []measureRow
	for _, s := range measureSites {
		if r := rows[s.Key]; r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// bodyText es una línea legible de un día: «78.4 kg · 18 % grasa · cintura 82 cm».
func bodyText(b bodyLog) string {
	var parts []string
	if b.Kg != nil {
		parts = append(parts, trimNum(*b.Kg)+" kg")
	}
	if b.Fat != nil {
		parts = append(parts, trimNum(*b.Fat)+" % grasa")
	}
	keys := make([]string, 0, len(b.Measures))
	for k := range b.Measures {
		keys = append(keys, k)
	}
	order := map[string]int{}
	for i, s := range measureSites {
		order[s.Key] = i
	}
	sort.Slice(keys, func(i, j int) bool { return order[keys[i]] < order[keys[j]] })
	for _, k := range keys {
		parts = append(parts, strings.ToLower(siteLabel(k))+" "+trimNum(b.Measures[k])+" cm")
	}
	return strings.Join(parts, " · ")
}
