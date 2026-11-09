package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

var (
	errNotFound = errors.New("no existe")
	errFull     = errors.New("has llegado al máximo")
)

const (
	maxTasks     = 200
	maxReadings  = 6000
	keepNotice   = 7 * 24 * time.Hour
	keepPrices   = 60 * dayUnit
	keepReadings = 5 // años
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño, salvo las de la tarea periódica (lo que
// toca avisar, de todos) y las de los precios de la luz, que son públicos.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealTask     = "casa/tasks"
	sealReading  = "casa/readings"
	sealSettings = "casa/settings"
	sealNotice   = "casa/notices"
)

// ---- Precios de la luz (públicos) ----

func (s store) savePrices(ctx context.Context, day, fetched time.Time, hours []hourPrice) error {
	raw, err := json.Marshal(hours)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO prices (day, fetched_at, hours) VALUES ($1, $2, $3)
		ON CONFLICT (day) DO UPDATE SET fetched_at = excluded.fetched_at, hours = excluded.hours`, isoDay(day), fetched, raw)
	return err
}

// prices son los precios de un día; vacíos si aún no están.
func (s store) prices(ctx context.Context, day time.Time) (dayPrices, error) {
	d := dayPrices{Day: day}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT fetched_at, hours FROM prices WHERE day = $1`, isoDay(day)).Scan(&d.Fetched, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(raw, &d.Hours)
}

func (s store) prunePrices(ctx context.Context, before time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM prices WHERE day < $1`, isoDay(before))
	return err
}

// ---- Tareas de mantenimiento ----

// Unidades de la periodicidad.
const (
	unitDays   = "dias"
	unitWeeks  = "semanas"
	unitMonths = "meses"
	unitYears  = "anios"
)

var units = []struct{ Key, One, Many string }{
	{unitDays, "día", "días"}, {unitWeeks, "semana", "semanas"}, {unitMonths, "mes", "meses"}, {unitYears, "año", "años"},
}

func validUnit(u string) bool {
	for _, x := range units {
		if x.Key == u {
			return true
		}
	}
	return false
}

// taskData es lo cifrado de cada tarea.
type taskData struct {
	Name     string      `json:"nombre"`
	Every    int         `json:"cada"`
	Unit     string      `json:"unidad"`
	Last     time.Time   `json:"ultima,omitzero"`
	Done     []time.Time `json:"hechas,omitempty"` // las últimas veces, de la más reciente a la más antigua
	Note     string      `json:"nota,omitempty"`
	Template string      `json:"plantilla,omitempty"`
}

type task struct {
	ID       int64
	Due      time.Time
	Notified time.Time
	Created  time.Time
	taskData
}

// next es la fecha que toca después de hacerla el día d.
func (t taskData) next(d time.Time) time.Time {
	switch t.Unit {
	case unitDays:
		return d.AddDate(0, 0, t.Every)
	case unitWeeks:
		return d.AddDate(0, 0, 7*t.Every)
	case unitYears:
		return addMonths(d, 12*t.Every)
	}
	return addMonths(d, t.Every)
}

// addMonths suma meses sin pasarse al mes siguiente: el 31 de enero más un
// mes es el 28 (o 29) de febrero.
func addMonths(d time.Time, n int) time.Time {
	first := time.Date(d.Year(), d.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(d.Day(), last), 0, 0, 0, 0, time.UTC)
}

// periodDays son los días aproximados de la periodicidad.
func (t taskData) periodDays() int {
	switch t.Unit {
	case unitDays:
		return t.Every
	case unitWeeks:
		return 7 * t.Every
	case unitYears:
		return 365 * t.Every
	}
	return 30 * t.Every
}

// every es «cada 3 meses», «cada año».
func (t taskData) every() string {
	for _, u := range units {
		if u.Key == t.Unit {
			if t.Every == 1 {
				if u.Key == unitWeeks {
					return "cada semana"
				}
				return "cada " + u.One
			}
			return "cada " + plural(t.Every, u.One, u.Many)
		}
	}
	return ""
}

const taskCols = `id, due, notified, created_at, sealed`

func (s store) scanTasks(rows pgx.Rows) ([]task, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (task, error) {
		var t task
		var notified *time.Time
		var sealed []byte
		if err := r.Scan(&t.ID, &t.Due, &notified, &t.Created, &sealed); err != nil {
			return t, err
		}
		if notified != nil {
			t.Notified = *notified
		}
		return t, s.box.OpenJSON(sealTask, sealed, &t.taskData)
	})
	sortTasks(list)
	return list, err
}

// sortTasks las deja por fecha (lo que toca antes, primero) y por nombre.
func sortTasks(list []task) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].Due.Equal(list[j].Due) {
			return list[i].Due.Before(list[j].Due)
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
}

func (s store) tasks(ctx context.Context, owner string) ([]task, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE owner = $1`, owner)
	return s.scanTasks(rows)
}

func (s store) task(ctx context.Context, owner string, id int64) (task, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanTasks(rows)
	if err != nil {
		return task{}, err
	}
	if len(list) == 0 {
		return task{}, errNotFound
	}
	return list[0], nil
}

func nullDay(d time.Time) *string {
	if d.IsZero() {
		return nil
	}
	s := isoDay(d)
	return &s
}

func (s store) addTask(ctx context.Context, owner string, t task) (int64, error) {
	sealed, err := s.box.SealJSON(sealTask, t.taskData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "casa|"+owner); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxTasks {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO tasks (owner, due, notified, sealed) VALUES ($1, $2, $3, $4) RETURNING id`,
			owner, isoDay(t.Due), nullDay(t.Notified), sealed).Scan(&id)
	})
	return id, err
}

func (s store) updateTask(ctx context.Context, owner string, t task) error {
	sealed, err := s.box.SealJSON(sealTask, t.taskData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE tasks SET due = $3, notified = $4, sealed = $5 WHERE owner = $1 AND id = $2`,
		owner, t.ID, isoDay(t.Due), nullDay(t.Notified), sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteTask(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tasks WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// dueOwners son, de todos, quienes tienen tareas que tocan hasta today y de
// las que aún no se ha avisado: solo para la tarea periódica.
func (s store) dueOwners(ctx context.Context, today time.Time) ([]string, error) {
	rows, _ := s.db.Query(ctx, `SELECT DISTINCT owner FROM tasks WHERE due <= $1 AND notified IS DISTINCT FROM due ORDER BY owner`, isoDay(today))
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// notifyTasks apunta el aviso y marca las tareas como avisadas para su
// fecha, todo a la vez (si otra pasada ya lo hizo, no se repite).
func (s store) notifyTasks(ctx context.Context, owner string, list []task, text string, at time.Time) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		marked := 0
		for _, t := range list {
			tag, err := tx.Exec(ctx, `UPDATE tasks SET notified = due WHERE owner = $1 AND id = $2 AND due = $3 AND notified IS DISTINCT FROM due`,
				owner, t.ID, isoDay(t.Due))
			if err != nil {
				return err
			}
			marked += int(tag.RowsAffected())
		}
		if marked == 0 || text == "" {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO notices (owner, created_at, sealed) VALUES ($1, $2, $3)`, owner, at, s.box.SealString(sealNotice, text))
		return err
	})
}

// ---- Lecturas de los contadores ----

// Contadores.
const (
	meterPower = "luz"
	meterWater = "agua"
	meterGas   = "gas"
)

var meters = []struct{ Key, Label, Emoji string }{
	{meterPower, "Luz", "💡"}, {meterWater, "Agua", "💧"}, {meterGas, "Gas", "🔥"},
}

func validMeter(m string) bool {
	for _, x := range meters {
		if x.Key == m {
			return true
		}
	}
	return false
}

func meterLabel(m string) string {
	for _, x := range meters {
		if x.Key == m {
			return x.Label
		}
	}
	return m
}

type readingData struct {
	Meter string  `json:"contador"`
	Value float64 `json:"valor"`
	Note  string  `json:"nota,omitempty"`
}

type reading struct {
	ID  int64
	Day time.Time
	readingData
}

const readingCols = `id, day, sealed`

func (s store) scanReadings(rows pgx.Rows) ([]reading, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (reading, error) {
		var x reading
		var sealed []byte
		if err := r.Scan(&x.ID, &x.Day, &sealed); err != nil {
			return x, err
		}
		return x, s.box.OpenJSON(sealReading, sealed, &x.readingData)
	})
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].Day.Equal(list[j].Day) {
			return list[i].Day.Before(list[j].Day)
		}
		return list[i].ID < list[j].ID
	})
	return list, err
}

// readings son las lecturas de una persona, de la más antigua a la más
// nueva.
func (s store) readings(ctx context.Context, owner string) ([]reading, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+readingCols+` FROM readings WHERE owner = $1`, owner)
	return s.scanReadings(rows)
}

// addReading apunta una lectura. Si ya había una de ese contador ese día,
// la sustituye (y devuelve true).
func (s store) addReading(ctx context.Context, owner string, x reading) (int64, bool, error) {
	sealed, err := s.box.SealJSON(sealReading, x.readingData)
	if err != nil {
		return 0, false, err
	}
	var id int64
	replaced := false
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "casa|"+owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+readingCols+` FROM readings WHERE owner = $1 AND day = $2`, owner, isoDay(x.Day))
		same, err := s.scanReadings(rows)
		if err != nil {
			return err
		}
		for _, o := range same {
			if o.Meter == x.Meter {
				id, replaced = o.ID, true
				_, err := tx.Exec(ctx, `UPDATE readings SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, o.ID, sealed)
				return err
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM readings WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxReadings {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO readings (owner, day, sealed) VALUES ($1, $2, $3) RETURNING id`,
			owner, isoDay(x.Day), sealed).Scan(&id)
	})
	return id, replaced, err
}

func (s store) deleteReading(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM readings WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// pruneReadings borra las lecturas de antes de before (de todos).
func (s store) pruneReadings(ctx context.Context, before time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM readings WHERE day < $1`, isoDay(before))
	return err
}

// ---- Ajustes ----

// Umbrales por defecto de los avisos del precio de la luz (€/kWh).
const (
	defaultLow  = 0.10
	defaultHigh = 0.25
	defaultGas  = "m³"
)

type settingsData struct {
	MaintOff  bool    `json:"sin_avisos_mantenimiento,omitempty"` // por defecto, sí se avisa
	PriceLow  bool    `json:"aviso_barata,omitempty"`
	PriceHigh bool    `json:"aviso_cara,omitempty"`
	LowBelow  float64 `json:"barata_hasta,omitempty"`
	HighAbove float64 `json:"cara_desde,omitempty"`
	GasUnit   string  `json:"unidad_gas,omitempty"`
}

func (d settingsData) low() float64 {
	if d.LowBelow > 0 {
		return d.LowBelow
	}
	return defaultLow
}

func (d settingsData) high() float64 {
	if d.HighAbove > 0 {
		return d.HighAbove
	}
	return defaultHigh
}

func (d settingsData) gasUnit() string {
	if d.GasUnit != "" {
		return d.GasUnit
	}
	return defaultGas
}

// unit es la unidad de un contador.
func (d settingsData) unit(meter string) string {
	switch meter {
	case meterPower:
		return "kWh"
	case meterWater:
		return "m³"
	}
	return d.gasUnit()
}

// settings son los ajustes de una persona (los de por defecto si no tiene).
func (s store) settings(ctx context.Context, owner string) (settingsData, error) {
	var d settingsData
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT sealed FROM settings WHERE owner = $1`, owner).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	return d, s.box.OpenJSON(sealSettings, sealed, &d)
}

func (s store) saveSettings(ctx context.Context, owner string, d settingsData) error {
	sealed, err := s.box.SealJSON(sealSettings, d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO settings (owner, sealed) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET sealed = excluded.sealed`, owner, sealed)
	return err
}

// priceWatchers son, de todos, quienes tienen algún aviso del precio
// encendido y aún no se les ha avisado de los precios de day.
func (s store) priceWatchers(ctx context.Context, day time.Time) (map[string]settingsData, error) {
	rows, _ := s.db.Query(ctx, `SELECT owner, sealed FROM settings WHERE price_notified IS NULL OR price_notified < $1`, isoDay(day))
	out := map[string]settingsData{}
	var owner string
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&owner, &sealed}, func() error {
		var d settingsData
		if err := s.box.OpenJSON(sealSettings, sealed, &d); err != nil {
			return err
		}
		if d.PriceLow || d.PriceHigh {
			out[owner] = d
		}
		return nil
	})
	return out, err
}

// notifyPrice apunta el aviso de los precios de day (si hay texto) y lo da
// por avisado, a la vez y una sola vez.
func (s store) notifyPrice(ctx context.Context, owner string, day time.Time, text string, at time.Time) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE settings SET price_notified = $2 WHERE owner = $1 AND (price_notified IS NULL OR price_notified < $2)`, owner, isoDay(day))
		if err != nil || tag.RowsAffected() == 0 || text == "" {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notices (owner, created_at, sealed) VALUES ($1, $2, $3)`, owner, at, s.box.SealString(sealNotice, text))
		return err
	})
}

// ---- Avisos para Telegram ----

type notice struct {
	Kind string `json:"tipo"`
	Text string `json:"texto"`
}

// takeNotices entrega los avisos pendientes de una persona (los de más de un
// día ya no se mandan) y los marca como entregados. De paso borra los de
// más de 7 días.
func (s store) takeNotices(ctx context.Context, owner string, now time.Time) ([]notice, error) {
	rows, _ := s.db.Query(ctx, `UPDATE notices SET delivered_at = $2
		WHERE owner = $1 AND delivered_at IS NULL AND created_at > $2::timestamptz - interval '1 day'
		RETURNING id, sealed`, owner, now)
	type row struct {
		id   int64
		text string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var sealed []byte
		if err := r.Scan(&x.id, &sealed); err != nil {
			return x, err
		}
		var err error
		x.text, err = s.box.OpenString(sealNotice, sealed)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM notices WHERE created_at < $1`, now.Add(-keepNotice)); err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	out := []notice{}
	for _, x := range list {
		out = append(out, notice{Kind: "casa", Text: x.text})
	}
	return out, nil
}
