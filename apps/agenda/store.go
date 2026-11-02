package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

var (
	errNotFound = errors.New("no existe")
	errConflict = errors.New("ha cambiado mientras tanto")
)

type store struct {
	db *pgxpool.Pool
}

// prefs son los ajustes de cada cuenta.
type prefs struct {
	SummaryOn   bool   `json:"summary_on"`   // resumen de la mañana por Telegram
	SummaryAt   string `json:"summary_at"`   // HH:MM
	SummaryAI   bool   `json:"summary_ai"`   // con resumen del correo hecho por la IA
	RemindersOn bool   `json:"reminders_on"` // avisos de eventos y tareas por Telegram
	DefaultMin  int    `json:"default_min"`  // aviso por defecto al crear un evento (0: sin aviso)
	Home        string `json:"home"`         // dirección de casa, origen de las rutas
	OutlookApp  string `json:"outlook_app"`  // ID de la app de Azure para Outlook
}

func defaultPrefs() prefs {
	return prefs{SummaryOn: true, SummaryAt: "07:45", SummaryAI: true, RemindersOn: true, DefaultMin: 15}
}

// fold pasa a minúsculas y quita las tildes, para buscar.
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func etagOf(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:12])
}

// ensureUser da de alta la cuenta con su calendario y su libreta.
func (s store) ensureUser(ctx context.Context, user string) error {
	var created bool
	err := s.db.QueryRow(ctx, `INSERT INTO users (username, prefs) VALUES ($1, $2)
		ON CONFLICT (username) DO NOTHING RETURNING true`, user, mustJSON(defaultPrefs())).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO collections (owner, kind, slug, name, color, components) VALUES
		($1, 'cal', 'personal', 'Personal', '#2f6f5e', '{VEVENT,VTODO}'),
		($1, 'card', 'contactos', 'Contactos', '', '{}')
		ON CONFLICT DO NOTHING`, user)
	return err
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s store) users(ctx context.Context) ([]string, error) {
	rows, _ := s.db.Query(ctx, `SELECT username FROM users ORDER BY username`)
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s store) prefs(ctx context.Context, user string) (prefs, error) {
	p := defaultPrefs()
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT prefs FROM users WHERE username = $1`, user).Scan(&raw); err != nil {
		return p, err
	}
	err := json.Unmarshal(raw, &p)
	return p, err
}

func (s store) savePrefs(ctx context.Context, user string, p prefs) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET prefs = $2 WHERE username = $1`, user, mustJSON(p))
	return err
}

// ---- Colecciones ----

type collection struct {
	ID         int64
	Owner      string
	Kind       string
	Slug       string
	Name       string
	Color      string
	Components []string
}

func (s store) collections(ctx context.Context, owner, kind string) ([]collection, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, owner, kind, slug, name, color, components FROM collections
		WHERE owner = $1 AND kind = $2 ORDER BY id`, owner, kind)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[collection])
}

func (s store) collection(ctx context.Context, owner, kind, slug string) (collection, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, owner, kind, slug, name, color, components FROM collections
		WHERE owner = $1 AND kind = $2 AND slug = $3`, owner, kind, slug)
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[collection])
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errNotFound
	}
	return c, err
}

func (s store) createCollection(ctx context.Context, c collection) error {
	_, err := s.db.Exec(ctx, `INSERT INTO collections (owner, kind, slug, name, color, components)
		VALUES ($1, $2, $3, $4, $5, $6)`, c.Owner, c.Kind, c.Slug, c.Name, c.Color, c.Components)
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return errConflict
	}
	return err
}

func (s store) deleteCollection(ctx context.Context, owner, kind, slug string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM collections WHERE owner = $1 AND kind = $2 AND slug = $3`, owner, kind, slug)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Eventos y tareas ----

type object struct {
	ID       int64
	CollID   int64
	Calendar string // slug del calendario
	Name     string
	Data     string
	ETag     string
	Updated  time.Time
}

const objectCols = `o.id, o.collection_id, c.slug, o.name, o.data, o.etag, o.updated_at`

func (s store) objects(ctx context.Context, where string, args ...any) ([]object, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+objectCols+` FROM objects o JOIN collections c ON c.id = o.collection_id WHERE `+where, args...)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[object])
}

func (s store) object(ctx context.Context, where string, args ...any) (object, error) {
	list, err := s.objects(ctx, where+` LIMIT 1`, args...)
	if err != nil {
		return object{}, err
	}
	if len(list) == 0 {
		return object{}, errNotFound
	}
	return list[0], nil
}

func (s store) objectByName(ctx context.Context, collID int64, name string) (object, error) {
	return s.object(ctx, `o.collection_id = $1 AND o.name = $2`, collID, name)
}

func (s store) objectByID(ctx context.Context, owner string, id int64) (object, error) {
	return s.object(ctx, `c.owner = $1 AND o.id = $2`, owner, id)
}

func (s store) listObjects(ctx context.Context, collID int64) ([]object, error) {
	return s.objects(ctx, `o.collection_id = $1 ORDER BY o.id`, collID)
}

// objectsBetween son los eventos y tareas con alguna vez entre from y to
// (o sin fecha, si undated).
func (s store) objectsBetween(ctx context.Context, owner string, from, to time.Time) ([]object, error) {
	return s.objects(ctx, `c.owner = $1 AND c.kind = 'cal' AND o.starts_at < $3 AND (o.ends_at IS NULL OR o.ends_at >= $2)
		ORDER BY o.starts_at`, owner, from, to)
}

func (s store) openTasks(ctx context.Context, owner string) ([]object, error) {
	return s.objects(ctx, `c.owner = $1 AND o.comp = 'VTODO' AND NOT o.done ORDER BY o.starts_at NULLS LAST, o.id`, owner)
}

func (s store) searchObjects(ctx context.Context, owner string, words []string, limit int) ([]object, error) {
	where, args := wordFilter("o.search", words, 2)
	return s.objects(ctx, `c.owner = $1 AND c.kind = 'cal'`+where+` ORDER BY o.starts_at DESC NULLS LAST LIMIT `+fmt.Sprint(limit), append([]any{owner}, args...)...)
}

// wordFilter exige que la columna contenga todas las palabras.
func wordFilter(col string, words []string, first int) (string, []any) {
	var b strings.Builder
	var args []any
	for i, w := range words {
		fmt.Fprintf(&b, ` AND %s LIKE $%d`, col, first+i)
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(w)+"%")
	}
	return b.String(), args
}

func words(s string) []string {
	return strings.FieldsFunc(fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '@' && r != '.' })
}

// putObject guarda un evento o tarea. ifMatch (etag) evita pisar cambios
// ajenos; ifNoneMatch, crear encima de uno que ya existe.
func (s store) putObject(ctx context.Context, collID int64, name string, cal *ical.Calendar, ifMatch string, ifNoneMatch bool, now time.Time) (object, error) {
	d, err := derive(cal)
	if err != nil {
		return object{}, err
	}
	data, err := encodeCal(cal)
	if err != nil {
		return object{}, err
	}
	if len(data) > 1<<20 {
		return object{}, errors.New("el evento es demasiado grande")
	}
	var next *time.Time
	if t := nextAlarm(cal, now); !t.IsZero() {
		next = &t
	}
	etag := etagOf(data)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return object{}, err
	}
	defer tx.Rollback(ctx)
	var cur string
	err = tx.QueryRow(ctx, `SELECT etag FROM objects WHERE collection_id = $1 AND name = $2 FOR UPDATE`, collID, name).Scan(&cur)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return object{}, err
	}
	if (ifNoneMatch && exists) || (ifMatch != "" && (!exists || cur != ifMatch)) {
		return object{}, errConflict
	}
	if !exists {
		var dup bool
		tx.QueryRow(ctx, `SELECT true FROM objects WHERE collection_id = $1 AND uid = $2`, collID, d.UID).Scan(&dup)
		if dup {
			return object{}, fmt.Errorf("%w: ya hay otro elemento con el UID %s", errConflict, d.UID)
		}
	}
	var o object
	err = tx.QueryRow(ctx, `INSERT INTO objects (collection_id, name, uid, comp, data, etag, summary, location, description,
			starts_at, ends_at, all_day, recurring, done, next_alarm_at, search, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (collection_id, name) DO UPDATE SET uid = $3, comp = $4, data = $5, etag = $6, summary = $7,
			location = $8, description = $9, starts_at = $10, ends_at = $11, all_day = $12, recurring = $13, done = $14,
			next_alarm_at = $15, search = $16, updated_at = $17
		RETURNING id, updated_at`,
		collID, name, d.UID, d.Comp, data, etag, d.Summary, d.Location, d.Description,
		d.Start, d.End, d.AllDay, d.Recurring, d.Done, next, fold(d.Summary+" "+d.Location+" "+d.Description), now).Scan(&o.ID, &o.Updated)
	if err != nil {
		return object{}, err
	}
	o.CollID, o.Name, o.Data, o.ETag = collID, name, data, etag
	return o, tx.Commit(ctx)
}

func (s store) deleteObject(ctx context.Context, collID int64, name, ifMatch string) error {
	q, args := `DELETE FROM objects WHERE collection_id = $1 AND name = $2`, []any{collID, name}
	if ifMatch != "" {
		q, args = q+` AND etag = $3`, append(args, ifMatch)
	}
	tag, err := s.db.Exec(ctx, q, args...)
	if err == nil && tag.RowsAffected() == 0 {
		if ifMatch != "" {
			if _, err := s.objectByName(ctx, collID, name); err == nil {
				return errConflict
			}
		}
		return errNotFound
	}
	return err
}

// ---- Avisos de eventos ----

type dueAlarm struct {
	object
	Owner string
	Next  time.Time
}

func (s store) dueAlarms(ctx context.Context, now time.Time) ([]dueAlarm, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+objectCols+`, c.owner, o.next_alarm_at FROM objects o JOIN collections c ON c.id = o.collection_id
		WHERE o.next_alarm_at <= $1 ORDER BY o.next_alarm_at`, now)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (dueAlarm, error) {
		var a dueAlarm
		err := r.Scan(&a.ID, &a.CollID, &a.Calendar, &a.Name, &a.Data, &a.ETag, &a.Updated, &a.Owner, &a.Next)
		return a, err
	})
}

// setNextAlarm cambia el próximo aviso si nadie ha tocado el evento entretanto.
func (s store) setNextAlarm(ctx context.Context, id int64, etag string, next time.Time) error {
	var v *time.Time
	if !next.IsZero() {
		v = &next
	}
	_, err := s.db.Exec(ctx, `UPDATE objects SET next_alarm_at = $3 WHERE id = $1 AND etag = $2`, id, etag, v)
	return err
}

// ---- Contactos ----

type cardRow struct {
	ID      int64
	CollID  int64
	Name    string
	Data    string
	ETag    string
	Updated time.Time
}

const cardCols = `k.id, k.collection_id, k.name, k.data, k.etag, k.updated_at`

func (s store) cards(ctx context.Context, where string, args ...any) ([]cardRow, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+cardCols+` FROM contacts k JOIN collections c ON c.id = k.collection_id WHERE `+where, args...)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[cardRow])
}

func (s store) card(ctx context.Context, where string, args ...any) (cardRow, error) {
	list, err := s.cards(ctx, where+` LIMIT 1`, args...)
	if err != nil {
		return cardRow{}, err
	}
	if len(list) == 0 {
		return cardRow{}, errNotFound
	}
	return list[0], nil
}

func (s store) cardByName(ctx context.Context, collID int64, name string) (cardRow, error) {
	return s.card(ctx, `k.collection_id = $1 AND k.name = $2`, collID, name)
}

func (s store) cardByID(ctx context.Context, owner string, id int64) (cardRow, error) {
	return s.card(ctx, `c.owner = $1 AND k.id = $2`, owner, id)
}

func (s store) listCards(ctx context.Context, collID int64) ([]cardRow, error) {
	return s.cards(ctx, `k.collection_id = $1 ORDER BY k.id`, collID)
}

// searchCards busca contactos que tengan todas las palabras (en el nombre,
// la empresa, correos, teléfonos, direcciones o notas). Sin palabras, todos.
func (s store) searchCards(ctx context.Context, owner, q string, limit int) ([]cardRow, error) {
	where, args := wordFilter("k.search", words(q), 2)
	return s.cards(ctx, `c.owner = $1`+where+` ORDER BY k.fn LIMIT `+fmt.Sprint(limit), append([]any{owner}, args...)...)
}

func (s store) birthdays(ctx context.Context, owner, mmdd string) ([]cardRow, error) {
	return s.cards(ctx, `c.owner = $1 AND right(k.birthday, 5) = $2 ORDER BY k.fn`, owner, mmdd)
}

func (s store) putCard(ctx context.Context, collID int64, name string, c vcard.Card, ifMatch string, ifNoneMatch bool, now time.Time) (cardRow, error) {
	uid := c.Value(vcard.FieldUID)
	if uid == "" {
		uid = strings.TrimSuffix(name, ".vcf")
		c.SetValue(vcard.FieldUID, uid)
	}
	data, err := encodeCard(c)
	if err != nil {
		return cardRow{}, err
	}
	if len(data) > 2<<20 {
		return cardRow{}, errors.New("el contacto es demasiado grande")
	}
	v := viewContact(0, c)
	etag := etagOf(data)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return cardRow{}, err
	}
	defer tx.Rollback(ctx)
	var cur string
	err = tx.QueryRow(ctx, `SELECT etag FROM contacts WHERE collection_id = $1 AND name = $2 FOR UPDATE`, collID, name).Scan(&cur)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return cardRow{}, err
	}
	if (ifNoneMatch && exists) || (ifMatch != "" && (!exists || cur != ifMatch)) {
		return cardRow{}, errConflict
	}
	r := cardRow{CollID: collID, Name: name, Data: data, ETag: etag}
	err = tx.QueryRow(ctx, `INSERT INTO contacts (collection_id, name, uid, data, etag, fn, birthday, search, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (collection_id, name) DO UPDATE SET uid = $3, data = $4, etag = $5, fn = $6, birthday = $7,
			search = $8, updated_at = $9
		RETURNING id, updated_at`, collID, name, uid, data, etag, v.Name, v.Birthday, contactSearch(v), now).Scan(&r.ID, &r.Updated)
	if err != nil {
		return cardRow{}, err
	}
	return r, tx.Commit(ctx)
}

func (s store) deleteCard(ctx context.Context, collID int64, name, ifMatch string) error {
	q, args := `DELETE FROM contacts WHERE collection_id = $1 AND name = $2`, []any{collID, name}
	if ifMatch != "" {
		q, args = q+` AND etag = $3`, append(args, ifMatch)
	}
	tag, err := s.db.Exec(ctx, q, args...)
	if err == nil && tag.RowsAffected() == 0 {
		if ifMatch != "" {
			if _, err := s.cardByName(ctx, collID, name); err == nil {
				return errConflict
			}
		}
		return errNotFound
	}
	return err
}

// ---- Avisos para Telegram ----

func (s store) addNotice(ctx context.Context, owner, kind, text string, at time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO notices (owner, kind, text, created_at) VALUES ($1, $2, $3, $4)`, owner, kind, text, at)
	return err
}

type notice struct {
	Kind string `json:"tipo"`
	Text string `json:"texto"`
}

// takeNotices entrega los avisos pendientes de una cuenta (los de más de un
// día ya no se mandan) y los marca como entregados.
func (s store) takeNotices(ctx context.Context, owner string, now time.Time) ([]notice, error) {
	rows, _ := s.db.Query(ctx, `UPDATE notices SET delivered_at = $2
		WHERE owner = $1 AND delivered_at IS NULL AND created_at > $2::timestamptz - interval '1 day'
		RETURNING kind, text, created_at`, owner, now)
	type row struct {
		notice
		at time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.Kind, &x.Text, &x.at)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	s.db.Exec(ctx, `DELETE FROM notices WHERE created_at < $1::timestamptz - interval '7 days'`, now)
	out := []notice{}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	for _, x := range list {
		out = append(out, x.notice)
	}
	return out, nil
}
