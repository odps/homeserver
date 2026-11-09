package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

var (
	errNotFound  = errors.New("no existe")
	errFull      = errors.New("has llegado al máximo")
	errDuplicate = errors.New("ya hay uno con ese nombre")
)

const (
	maxHabits  = 100
	keepNotice = 7 * 24 * time.Hour
	// freshNotice: un recordatorio que no se ha podido entregar en este
	// tiempo ya no se manda (sería para otro momento del día).
	freshNotice = 3 * time.Hour
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño, salvo las de la tarea de los avisos
// (allHabits, allSettings), que recorren los de todos para saber a quién
// avisar.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealHabit   = "habitos/habits"
	sealCheckin = "habitos/checkins"
	sealEntry   = "habitos/journal"
	sealSetting = "habitos/settings"
	sealNotice  = "habitos/notices"
)

// ---- Hábitos ----

// nameToken es la huella del nombre (sin tildes, mayúsculas ni signos):
// «Beber agua» y «beber  AGUA» son el mismo hábito.
func (s store) nameToken(name string) int64 {
	return s.box.Token("habito", strings.Join(seal.Words(name), " "))
}

const habitCols = `id, owner, archived, created_at, reminded_on, sealed`

type ownedHabit struct {
	Owner string
	habit
}

func (s store) scanHabits(rows pgx.Rows) ([]ownedHabit, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ownedHabit, error) {
		var h ownedHabit
		var reminded *time.Time
		var sealed []byte
		if err := r.Scan(&h.ID, &h.Owner, &h.Archived, &h.Created, &reminded, &sealed); err != nil {
			return h, err
		}
		if reminded != nil {
			h.RemindedOn = *reminded
		}
		return h, s.box.OpenJSON(sealHabit, sealed, &h.habitData)
	})
	sort.SliceStable(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, err
}

func plain(list []ownedHabit) []habit {
	out := make([]habit, 0, len(list))
	for _, h := range list {
		out = append(out, h.habit)
	}
	return out
}

// habits son los hábitos de una persona, en el orden en que los creó.
func (s store) habits(ctx context.Context, owner string) ([]habit, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+habitCols+` FROM habits WHERE owner = $1`, owner)
	list, err := s.scanHabits(rows)
	return plain(list), err
}

func (s store) habit(ctx context.Context, owner string, id int64) (habit, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+habitCols+` FROM habits WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanHabits(rows)
	if err != nil {
		return habit{}, err
	}
	if len(list) == 0 {
		return habit{}, errNotFound
	}
	return list[0].habit, nil
}

// allHabits son los activos de todos: solo para la tarea de los avisos.
func (s store) allHabits(ctx context.Context) ([]ownedHabit, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+habitCols+` FROM habits WHERE NOT archived`)
	return s.scanHabits(rows)
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// addHabit crea un hábito. No puede haber dos con el mismo nombre.
func (s store) addHabit(ctx context.Context, owner string, d habitData, now time.Time) (habit, error) {
	sealed, err := s.box.SealJSON(sealHabit, d)
	if err != nil {
		return habit{}, err
	}
	h := habit{habitData: d, Created: now}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "habitos|"+owner); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM habits WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxHabits {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO habits (owner, name_token, created_at, sealed) VALUES ($1, $2, $3, $4) RETURNING id`,
			owner, s.nameToken(d.Name), now, sealed).Scan(&h.ID)
	})
	if isUnique(err) {
		return habit{}, errDuplicate
	}
	return h, err
}

// updateHabit guarda lo editable (nombre, tipo, objetivo, frecuencia…).
func (s store) updateHabit(ctx context.Context, owner string, h habit) error {
	sealed, err := s.box.SealJSON(sealHabit, h.habitData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE habits SET name_token = $3, sealed = $4 WHERE owner = $1 AND id = $2`,
		owner, h.ID, s.nameToken(h.Name), sealed)
	switch {
	case isUnique(err):
		return errDuplicate
	case err == nil && tag.RowsAffected() == 0:
		return errNotFound
	}
	return err
}

func (s store) setArchived(ctx context.Context, owner string, id int64, on bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE habits SET archived = $3 WHERE owner = $1 AND id = $2`, owner, id, on)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// deleteHabit borra el hábito con todo lo apuntado.
func (s store) deleteHabit(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM habits WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// claimReminder apunta que hoy ya se ha avisado de ese hábito; false si
// ya estaba (así un aviso no sale dos veces).
func (s store) claimReminder(ctx context.Context, owner string, id int64, day time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE habits SET reminded_on = $3 WHERE owner = $1 AND id = $2 AND reminded_on IS DISTINCT FROM $3::date`,
		owner, id, isoDay(day))
	return err == nil && tag.RowsAffected() > 0, err
}

// ---- Marcas de cada día ----

type checkinData struct {
	Amount float64 `json:"cantidad"`
	Origin string  `json:"origen,omitempty"` // web o asistente
}

// marks es todo lo apuntado de los hábitos de una persona, por hábito.
func (s store) marks(ctx context.Context, owner string) (map[int64]marks, error) {
	rows, _ := s.db.Query(ctx, `SELECT habit_id, day, sealed FROM checkins WHERE owner = $1`, owner)
	return s.collectMarks(rows)
}

// habitMarks es lo apuntado de un hábito.
func (s store) habitMarks(ctx context.Context, owner string, id int64) (marks, error) {
	rows, _ := s.db.Query(ctx, `SELECT habit_id, day, sealed FROM checkins WHERE owner = $1 AND habit_id = $2`, owner, id)
	all, err := s.collectMarks(rows)
	if err != nil {
		return nil, err
	}
	if m := all[id]; m != nil {
		return m, nil
	}
	return marks{}, nil
}

func (s store) collectMarks(rows pgx.Rows) (map[int64]marks, error) {
	out := map[int64]marks{}
	var id int64
	var day time.Time
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&id, &day, &sealed}, func() error {
		var c checkinData
		if err := s.box.OpenJSON(sealCheckin, sealed, &c); err != nil {
			return err
		}
		if out[id] == nil {
			out[id] = marks{}
		}
		out[id][isoDay(day)] = c.Amount
		return nil
	})
	return out, err
}

// setMark guarda la cantidad hecha un día (0 la quita). El hábito tiene que
// ser de la misma persona.
func (s store) setMark(ctx context.Context, owner string, habitID int64, day time.Time, amount float64, origin string) error {
	if amount <= eps {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM habits WHERE owner = $1 AND id = $2)`, owner, habitID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		_, err := s.db.Exec(ctx, `DELETE FROM checkins WHERE owner = $1 AND habit_id = $2 AND day = $3`, owner, habitID, isoDay(day))
		return err
	}
	sealed, err := s.box.SealJSON(sealCheckin, checkinData{Amount: amount, Origin: origin})
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO checkins (habit_id, owner, day, updated_at, sealed)
		SELECT id, owner, $3::date, now(), $4::bytea FROM habits WHERE owner = $1 AND id = $2
		ON CONFLICT (habit_id, day) DO UPDATE SET updated_at = excluded.updated_at, sealed = excluded.sealed`,
		owner, habitID, isoDay(day), sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Diario ----

const entryCols = `day, updated_at, sealed`

func (s store) scanEntries(rows pgx.Rows) ([]entry, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
		var e entry
		var sealed []byte
		if err := r.Scan(&e.Day, &e.Updated, &sealed); err != nil {
			return e, err
		}
		return e, s.box.OpenJSON(sealEntry, sealed, &e.entryData)
	})
}

// entries son las entradas del diario entre dos días (incluidos), en orden.
func (s store) entries(ctx context.Context, owner string, from, to time.Time) ([]entry, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+entryCols+` FROM journal WHERE owner = $1 AND day BETWEEN $2 AND $3 ORDER BY day`,
		owner, isoDay(from), isoDay(to))
	return s.scanEntries(rows)
}

// entry es la entrada de un día; false si no hay.
func (s store) entry(ctx context.Context, owner string, day time.Time) (entry, bool, error) {
	list, err := s.entries(ctx, owner, day, day)
	if err != nil || len(list) == 0 {
		return entry{Day: day}, false, err
	}
	return list[0], true, nil
}

// saveEntry guarda la entrada de un día; si queda vacía, la borra.
func (s store) saveEntry(ctx context.Context, owner string, day time.Time, d entryData, now time.Time) error {
	if d.empty() {
		return s.deleteEntry(ctx, owner, day)
	}
	sealed, err := s.box.SealJSON(sealEntry, d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO journal (owner, day, updated_at, sealed) VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner, day) DO UPDATE SET updated_at = excluded.updated_at, sealed = excluded.sealed`,
		owner, isoDay(day), now, sealed)
	return err
}

func (s store) deleteEntry(ctx context.Context, owner string, day time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM journal WHERE owner = $1 AND day = $2`, owner, isoDay(day))
	return err
}

// hasEntry dice si hay algo escrito ese día (sin descifrarlo).
func (s store) hasEntry(ctx context.Context, owner string, day time.Time) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journal WHERE owner = $1 AND day = $2)`, owner, isoDay(day)).Scan(&ok)
	return ok, err
}

// ---- Preferencias ----

// settingsData: el aviso de la tarde para escribir el diario (apagado si no
// se enciende).
type settingsData struct {
	Nudge     bool   `json:"aviso_diario,omitempty"`
	NudgeTime string `json:"hora_aviso_diario,omitempty"` // HH:MM
}

const defaultNudge = "21:30"

func (d settingsData) nudgeTime() string {
	if d.NudgeTime == "" {
		return defaultNudge
	}
	return d.NudgeTime
}

func (s store) settings(ctx context.Context, owner string) (settingsData, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT sealed FROM settings WHERE owner = $1`, owner).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return settingsData{}, nil
	}
	var d settingsData
	if err != nil {
		return d, err
	}
	return d, s.box.OpenJSON(sealSetting, sealed, &d)
}

func (s store) saveSettings(ctx context.Context, owner string, d settingsData) error {
	sealed, err := s.box.SealJSON(sealSetting, d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO settings (owner, sealed) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET sealed = excluded.sealed`, owner, sealed)
	return err
}

type ownedSettings struct {
	Owner    string
	NudgedOn time.Time
	settingsData
}

// allSettings son las preferencias de todos: solo para la tarea de los
// avisos.
func (s store) allSettings(ctx context.Context) ([]ownedSettings, error) {
	rows, _ := s.db.Query(ctx, `SELECT owner, nudged_on, sealed FROM settings`)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ownedSettings, error) {
		var o ownedSettings
		var on *time.Time
		var sealed []byte
		if err := r.Scan(&o.Owner, &on, &sealed); err != nil {
			return o, err
		}
		if on != nil {
			o.NudgedOn = *on
		}
		return o, s.box.OpenJSON(sealSetting, sealed, &o.settingsData)
	})
}

// claimNudge apunta que hoy ya se ha avisado de escribir el diario; false
// si ya estaba.
func (s store) claimNudge(ctx context.Context, owner string, day time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE settings SET nudged_on = $2 WHERE owner = $1 AND nudged_on IS DISTINCT FROM $2::date`, owner, isoDay(day))
	return err == nil && tag.RowsAffected() > 0, err
}

// ---- Avisos para Telegram ----

func (s store) addNotice(ctx context.Context, owner, text string, at time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO notices (owner, created_at, sealed) VALUES ($1, $2, $3)`,
		owner, at, s.box.SealString(sealNotice, text))
	return err
}

type notice struct {
	Kind string `json:"tipo"`
	Text string `json:"texto"`
}

// takeNotices entrega los avisos pendientes de una persona (los de hace más
// de 3 horas ya no se mandan: eran para otro momento) y los marca como
// entregados. De paso borra los de más de 7 días.
func (s store) takeNotices(ctx context.Context, owner string, now time.Time) ([]notice, error) {
	rows, _ := s.db.Query(ctx, `UPDATE notices SET delivered_at = $2
		WHERE owner = $1 AND delivered_at IS NULL AND created_at > $3
		RETURNING id, sealed`, owner, now, now.Add(-freshNotice))
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
	if err := s.purgeNotices(ctx, now); err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	out := []notice{}
	for _, x := range list {
		out = append(out, notice{Kind: "habitos", Text: x.text})
	}
	return out, nil
}

// purgeNotices borra los avisos de más de 7 días.
func (s store) purgeNotices(ctx context.Context, now time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM notices WHERE created_at < $1`, now.Add(-keepNotice))
	return err
}
