package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

var errNotFound = errors.New("no existe")

// store guarda lo de cada persona cifrado (platform/seal).
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una columna no se descifra como otra.
const (
	sealSession = "entreno/sessions"
	sealEntry   = "entreno/entries"
	sealAlias   = "entreno/aliases"
	sealCustom  = "entreno/custom"
)

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

// exToken es la huella de un ejercicio (para su historial sin guardar su
// nombre en claro).
func (s store) exToken(key string) int64 { return s.box.Token("ejercicio", key) }

// ---- Sesiones ----

type sessionData struct {
	Notes []string `json:"notes,omitempty"`
}

// session es una sesión con sus ejercicios.
type session struct {
	ID        int64         `json:"id"`
	Day       time.Time     `json:"-"`
	Started   time.Time     `json:"inicio"`
	Source    string        `json:"origen"`
	Notes     []string      `json:"notas,omitempty"`
	Exercises []exerciseLog `json:"ejercicios"`
}

func dayOf(t time.Time) time.Time {
	t = t.In(madrid)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, madrid)
}

// saveSession guarda una sesión (sin ejercicios sin reconocer).
func (s store) saveSession(ctx context.Context, owner string, start time.Time, source string, notes []string, exs []exerciseLog) (int64, error) {
	sealed, err := s.box.SealJSON(sealSession, sessionData{Notes: notes})
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO sessions (owner, day, started_at, source, sealed) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			owner, dayOf(start).Format("2006-01-02"), start, source, sealed).Scan(&id); err != nil {
			return err
		}
		return s.insertEntries(ctx, tx, owner, id, 0, exs)
	})
	return id, err
}

func (s store) insertEntries(ctx context.Context, tx pgx.Tx, owner string, id int64, from int, exs []exerciseLog) error {
	for i, e := range exs {
		if e.Key == "" {
			continue
		}
		sealed, err := s.box.SealJSON(sealEntry, e)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO entries (session_id, owner, position, ex_token, sealed) VALUES ($1, $2, $3, $4, $5)`,
			id, owner, from+i, s.exToken(e.Key), sealed); err != nil {
			return err
		}
	}
	return nil
}

// addToSession añade ejercicios (y notas) a una sesión que ya existe.
func (s store) addToSession(ctx context.Context, owner string, id int64, notes []string, exs []exerciseLog) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var sealed []byte
		if err := tx.QueryRow(ctx, `SELECT sealed FROM sessions WHERE owner = $1 AND id = $2 FOR UPDATE`, owner, id).Scan(&sealed); err != nil {
			return errNotFound
		}
		if len(notes) > 0 {
			var d sessionData
			if err := s.box.OpenJSON(sealSession, sealed, &d); err != nil {
				return err
			}
			d.Notes = append(d.Notes, notes...)
			b, err := s.box.SealJSON(sealSession, d)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE sessions SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, id, b); err != nil {
				return err
			}
		}
		var n int
		tx.QueryRow(ctx, `SELECT coalesce(max(position) + 1, 0) FROM entries WHERE session_id = $1`, id).Scan(&n)
		return s.insertEntries(ctx, tx, owner, id, n, exs)
	})
}

// sessionOn es la sesión de un día de una fuente (la primera), si la hay.
func (s store) sessionOn(ctx context.Context, owner string, day time.Time, source string) (int64, bool) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM sessions WHERE owner = $1 AND day = $2 AND ($3 = '' OR source = $3) ORDER BY id LIMIT 1`,
		owner, dayOf(day).Format("2006-01-02"), source).Scan(&id)
	return id, err == nil
}

func (s store) deleteSession(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// sessions devuelve las sesiones (con ejercicios) entre dos días, de la más
// reciente a la más antigua; limit 0 = todas.
func (s store) sessions(ctx context.Context, owner string, from, to time.Time, limit int) ([]session, error) {
	q := `SELECT id, day, started_at, source, sealed FROM sessions WHERE owner = $1 AND day >= $2 AND day <= $3 ORDER BY started_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, _ := s.db.Query(ctx, q, owner, from.Format("2006-01-02"), to.Format("2006-01-02"))
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (session, error) {
		var x session
		var sealed []byte
		if err := r.Scan(&x.ID, &x.Day, &x.Started, &x.Source, &sealed); err != nil {
			return x, err
		}
		var d sessionData
		err := s.box.OpenJSON(sealSession, sealed, &d)
		x.Notes, x.Exercises = d.Notes, []exerciseLog{}
		return x, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]int64, len(list))
	byID := map[int64]int{}
	for i, x := range list {
		ids[i], byID[x.ID] = x.ID, i
	}
	rows, _ = s.db.Query(ctx, `SELECT session_id, sealed FROM entries WHERE owner = $1 AND session_id = ANY($2) ORDER BY session_id, position`, owner, ids)
	var sid int64
	var sealed []byte
	_, err = pgx.ForEachRow(rows, []any{&sid, &sealed}, func() error {
		var e exerciseLog
		if err := s.box.OpenJSON(sealEntry, sealed, &e); err != nil {
			return err
		}
		i := byID[sid]
		list[i].Exercises = append(list[i].Exercises, e)
		return nil
	})
	return list, err
}

func (s store) session(ctx context.Context, owner string, id int64) (session, error) {
	var day time.Time
	if err := s.db.QueryRow(ctx, `SELECT day FROM sessions WHERE owner = $1 AND id = $2`, owner, id).Scan(&day); err != nil {
		return session{}, errNotFound
	}
	list, err := s.sessions(ctx, owner, day, day, 0)
	if err != nil {
		return session{}, err
	}
	for _, x := range list {
		if x.ID == id {
			return x, nil
		}
	}
	return session{}, errNotFound
}

// ---- Historial de un ejercicio ----

// done es una vez que se hizo un ejercicio.
type done struct {
	SessionID int64       `json:"sesion"`
	Day       time.Time   `json:"dia"`
	Entry     exerciseLog `json:"ejercicio"`
}

// history son las veces que se hizo un ejercicio, de la más reciente a la
// más antigua (limit 0 = todas).
func (s store) history(ctx context.Context, owner, key string, limit int) ([]done, error) {
	q := `SELECT e.session_id, s.day, e.sealed FROM entries e JOIN sessions s ON s.id = e.session_id
		WHERE e.owner = $1 AND s.owner = $1 AND e.ex_token = $2 ORDER BY s.started_at DESC, e.position`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, _ := s.db.Query(ctx, q, owner, s.exToken(key))
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (done, error) {
		var d done
		var sealed []byte
		if err := r.Scan(&d.SessionID, &d.Day, &sealed); err != nil {
			return d, err
		}
		err := s.box.OpenJSON(sealEntry, sealed, &d.Entry)
		return d, err
	})
}

// exerciseStat resume un ejercicio de la persona.
type exerciseStat struct {
	Key   string    `json:"key"`
	Name  string    `json:"nombre"`
	Times int       `json:"veces"`
	Last  time.Time `json:"ultima"`
}

// exerciseStats son los ejercicios que ha hecho, de más a menos reciente.
func (s store) exerciseStats(ctx context.Context, owner string) ([]exerciseStat, error) {
	list, err := s.sessions(ctx, owner, time.Time{}, time.Now().AddDate(1, 0, 0), 0)
	if err != nil {
		return nil, err
	}
	custom, _ := s.customNames(ctx, owner)
	by := map[string]*exerciseStat{}
	var out []*exerciseStat
	for _, x := range list {
		for _, e := range x.Exercises {
			st, ok := by[e.Key]
			if !ok {
				st = &exerciseStat{Key: e.Key, Name: exerciseName(e.Key, e.Name, custom), Last: x.Day}
				by[e.Key] = st
				out = append(out, st)
			}
			st.Times++
		}
	}
	res := make([]exerciseStat, len(out))
	for i, st := range out {
		res[i] = *st
	}
	return res, nil
}

// ---- Alias y ejercicios propios ----

type aliasData struct {
	Alias string `json:"alias"`
	Key   string `json:"key"`
}

func (s store) aliases(ctx context.Context, owner string) (map[string]string, error) {
	rows, _ := s.db.Query(ctx, `SELECT sealed FROM aliases WHERE owner = $1`, owner)
	out := map[string]string{}
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&sealed}, func() error {
		var a aliasData
		if err := s.box.OpenJSON(sealAlias, sealed, &a); err != nil {
			return err
		}
		out[a.Alias] = a.Key
		return nil
	})
	return out, err
}

// setAlias recuerda que la persona llama así a ese ejercicio.
func (s store) setAlias(ctx context.Context, owner, alias, key string) error {
	sealed, err := s.box.SealJSON(sealAlias, aliasData{Alias: alias, Key: key})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO aliases (owner, alias_token, sealed) VALUES ($1, $2, $3)
		ON CONFLICT (owner, alias_token) DO UPDATE SET sealed = excluded.sealed`, owner, s.box.Token("alias", fold(alias)), sealed)
	return err
}

type customData struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // reps, time, distance
}

// addCustom crea un ejercicio propio y devuelve su clave («u12»).
func (s store) addCustom(ctx context.Context, owner, name, kind string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("falta el nombre del ejercicio")
	}
	if kind == "" {
		kind = "reps"
	}
	sealed, err := s.box.SealJSON(sealCustom, customData{Name: name, Kind: kind})
	if err != nil {
		return "", err
	}
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO custom_exercises (owner, sealed) VALUES ($1, $2) RETURNING id`, owner, sealed).Scan(&id); err != nil {
		return "", err
	}
	return fmt.Sprintf("u%d", id), nil
}

// customNames son los ejercicios propios: clave → nombre.
func (s store) customNames(ctx context.Context, owner string) (map[string]string, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, sealed FROM custom_exercises WHERE owner = $1 ORDER BY id`, owner)
	out := map[string]string{}
	var id int64
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&id, &sealed}, func() error {
		var c customData
		if err := s.box.OpenJSON(sealCustom, sealed, &c); err != nil {
			return err
		}
		out[fmt.Sprintf("u%d", id)] = c.Name
		return nil
	})
	return out, err
}

// matcherFor reconoce nombres con el catálogo, los alias de la persona y sus
// ejercicios propios.
func (s store) matcherFor(ctx context.Context, owner string) (*matcher, error) {
	al, err := s.aliases(ctx, owner)
	if err != nil {
		return nil, err
	}
	custom, err := s.customNames(ctx, owner)
	if err != nil {
		return nil, err
	}
	for key, name := range custom {
		al[name] = key
	}
	return newMatcher(al), nil
}

// sortedKeys es un ayudante para listados estables.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] > m[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
