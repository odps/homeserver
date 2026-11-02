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

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	sealpkg "homeserver/platform/seal"
)

var (
	errNotFound = errors.New("no existe")
	errConflict = errors.New("ha cambiado mientras tanto")
)

// store guarda los datos personales cifrados (platform/seal): el texto de
// eventos, contactos, correos, avisos y ajustes va en columnas "sealed" y, para
// buscar, quedan huellas de las palabras en "tokens".
type store struct {
	db  *pgxpool.Pool
	box *sealpkg.Box
}

// Contextos de cifrado: lo de una columna no se descifra como otra.
const (
	sealPrefs   = "agenda/prefs"
	sealObject  = "agenda/objects"
	sealCard    = "agenda/contacts"
	sealNotice  = "agenda/notices"
	sealMessage = "agenda/messages"
)

// prefs son los ajustes de cada cuenta.
type prefs struct {
	SummaryOn   bool   `json:"summary_on"`   // resumen de la mañana por Telegram
	SummaryAt   string `json:"summary_at"`   // HH:MM
	SummaryAI   bool   `json:"summary_ai"`   // con resumen del correo hecho por la IA
	RemindersOn bool   `json:"reminders_on"` // avisos de eventos y tareas por Telegram
	DefaultMin  int    `json:"default_min"`  // aviso por defecto al crear un evento (0: sin aviso)
	Home        string `json:"home"`         // dirección de casa, origen de las rutas
	OutlookApp  string `json:"outlook_app"`  // ID de la app de Azure para Outlook
	// Remitentes (direcciones o dominios) que siempre son importantes o que
	// se ignoran (cuentan como comerciales: no van a la IA si no se pide).
	MailImportant []string `json:"mail_important,omitempty"`
	MailIgnore    []string `json:"mail_ignore,omitempty"`
	// Ciudad del tiempo en el resumen; vacía: la de la dirección de casa.
	WeatherCity string `json:"weather_city,omitempty"`
}

// weatherCity es la ciudad del tiempo: la elegida o la última parte de la
// dirección de casa sin números («Calle Mayor 3, 28013 Madrid» → «Madrid»).
// A Open-Meteo solo va este nombre, nunca la dirección.
func (p prefs) weatherCity() string {
	if c := strings.TrimSpace(p.WeatherCity); c != "" {
		return c
	}
	parts := strings.Split(p.Home, ",")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, parts[len(parts)-1]))
}

func defaultPrefs() prefs {
	return prefs{SummaryOn: true, SummaryAt: "07:45", SummaryAI: true, RemindersOn: true, DefaultMin: 15}
}

// fold pasa a minúsculas y quita las tildes, para buscar.
func fold(s string) string { return sealpkg.Fold(s) }

func etagOf(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:12])
}

// ensureUser da de alta la cuenta con su calendario y su libreta.
func (s store) ensureUser(ctx context.Context, user string) error {
	var created bool
	err := s.db.QueryRow(ctx, `INSERT INTO users (username, prefs_sealed) VALUES ($1, $2)
		ON CONFLICT (username) DO NOTHING RETURNING true`, user, s.box.Seal(sealPrefs, mustJSON(defaultPrefs()))).Scan(&created)
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
	if err := s.db.QueryRow(ctx, `SELECT prefs_sealed FROM users WHERE username = $1`, user).Scan(&raw); err != nil {
		return p, err
	}
	err := s.box.OpenJSON(sealPrefs, raw, &p)
	return p, err
}

func (s store) savePrefs(ctx context.Context, user string, p prefs) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET prefs_sealed = $2 WHERE username = $1`, user, s.box.Seal(sealPrefs, mustJSON(p)))
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

const objectCols = `o.id, o.collection_id, c.slug, o.name, o.sealed, o.etag, o.updated_at`

// scanObject lee objectCols (y dest, detrás) y descifra el iCalendar.
func (s store) scanObject(r pgx.CollectableRow, dest ...any) (object, error) {
	var o object
	var sealed []byte
	if err := r.Scan(append([]any{&o.ID, &o.CollID, &o.Calendar, &o.Name, &sealed, &o.ETag, &o.Updated}, dest...)...); err != nil {
		return o, err
	}
	var err error
	o.Data, err = s.box.OpenString(sealObject, sealed)
	return o, err
}

func (s store) objects(ctx context.Context, where string, args ...any) ([]object, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+objectCols+` FROM objects o JOIN collections c ON c.id = o.collection_id WHERE `+where, args...)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (object, error) { return s.scanObject(r) })
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

// searchObjects busca por huellas: cada palabra, entera o como comienzo de
// palabra (desde 3 letras), en el título, el lugar o la descripción.
func (s store) searchObjects(ctx context.Context, owner, q string, limit int) ([]object, error) {
	return s.objects(ctx, `c.owner = $1 AND c.kind = 'cal' AND o.tokens @> $2 ORDER BY o.starts_at DESC NULLS LAST LIMIT `+fmt.Sprint(limit),
		owner, s.box.Query("t", q))
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
	err = tx.QueryRow(ctx, `INSERT INTO objects (collection_id, name, uid, comp, sealed, etag,
			starts_at, ends_at, all_day, recurring, done, next_alarm_at, tokens, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (collection_id, name) DO UPDATE SET uid = $3, comp = $4, sealed = $5, etag = $6,
			starts_at = $7, ends_at = $8, all_day = $9, recurring = $10, done = $11,
			next_alarm_at = $12, tokens = $13, updated_at = $14
		RETURNING id, updated_at`,
		collID, name, d.UID, d.Comp, s.box.SealString(sealObject, data), etag,
		d.Start, d.End, d.AllDay, d.Recurring, d.Done, next, objectTokens(s.box, d.Summary, d.Location, d.Description), now).Scan(&o.ID, &o.Updated)
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
		var err error
		a.object, err = s.scanObject(r, &a.Owner, &a.Next)
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

const cardCols = `k.id, k.collection_id, k.name, k.sealed, k.etag, k.updated_at`

func (s store) cards(ctx context.Context, where string, args ...any) ([]cardRow, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+cardCols+` FROM contacts k JOIN collections c ON c.id = k.collection_id WHERE `+where, args...)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (cardRow, error) {
		var c cardRow
		var sealed []byte
		if err := r.Scan(&c.ID, &c.CollID, &c.Name, &sealed, &c.ETag, &c.Updated); err != nil {
			return c, err
		}
		var err error
		c.Data, err = s.box.OpenString(sealCard, sealed)
		return c, err
	})
}

// byName ordena contactos por su nombre (va cifrado: se ordena aquí, no en
// SQL) y se queda con los limit primeros (0: todos).
func byName(list []cardRow, limit int) []cardRow {
	key := make(map[int64]string, len(list))
	for _, r := range list {
		if c, err := decodeCard(r.Data); err == nil {
			key[r.ID] = fold(viewContact(r.ID, c).Name)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return key[list[i].ID] < key[list[j].ID] })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
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
	list, err := s.cards(ctx, `c.owner = $1 AND k.tokens @> $2`, owner, s.box.Query("t", q))
	return byName(list, limit), err
}

func (s store) birthdays(ctx context.Context, owner, mmdd string) ([]cardRow, error) {
	list, err := s.cards(ctx, `c.owner = $1 AND k.birthday_token = $2`, owner, s.box.Token("cumple", mmdd))
	return byName(list, 0), err
}

// birthdayToken es la huella del día y mes ("MM-DD") de un cumpleaños.
func birthdayToken(box *sealpkg.Box, birthday string) *int64 {
	if len(birthday) < 5 {
		return nil
	}
	t := box.Token("cumple", birthday[len(birthday)-5:])
	return &t
}

func objectTokens(box *sealpkg.Box, summary, location, description string) []int64 {
	return box.Index(nil, "t", summary+" "+location+" "+description)
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
	err = tx.QueryRow(ctx, `INSERT INTO contacts (collection_id, name, uid, sealed, etag, birthday_token, tokens, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (collection_id, name) DO UPDATE SET uid = $3, sealed = $4, etag = $5, birthday_token = $6,
			tokens = $7, updated_at = $8
		RETURNING id, updated_at`, collID, name, uid, s.box.SealString(sealCard, data), etag, birthdayToken(s.box, v.Birthday),
		s.box.Index(nil, "t", contactSearch(v)), now).Scan(&r.ID, &r.Updated)
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
	_, err := s.db.Exec(ctx, `INSERT INTO notices (owner, kind, sealed, created_at) VALUES ($1, $2, $3, $4)`,
		owner, kind, s.box.SealString(sealNotice, text), at)
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
		RETURNING kind, sealed, created_at`, owner, now)
	type row struct {
		notice
		at time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var sealed []byte
		if err := r.Scan(&x.Kind, &sealed, &x.at); err != nil {
			return x, err
		}
		var err error
		x.Text, err = s.box.OpenString(sealNotice, sealed)
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
