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

var (
	errNotFound = errors.New("no existe")
	errFull     = errors.New("has llegado al máximo de artículos guardados: borra alguno antiguo")
)

const (
	maxArticles = 5000
	maxAttempts = 3 // descargas automáticas antes de dejarlo en error
	keepNotice  = 7 * 24 * time.Hour
)

// Estados de la descarga.
const (
	stateQueued = "cola"
	stateOK     = "ok"
	stateError  = "error"
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño, salvo la de la tarea de reintentos
// (toRetry) y la del aviso semanal (weeklyOwners), que recorren a todos.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una columna no se descifra como otra.
const (
	sealMeta   = "leer/articles"
	sealBody   = "leer/articles.body"
	sealNotice = "leer/notices"
)

// Campos de las huellas: palabras (t) y etiquetas exactas (e).
const (
	fieldWords = "t"
	fieldTag   = "e"
)

// meta es lo cifrado de cada artículo (sin el texto, que va aparte).
type meta struct {
	URL       string   `json:"url"`
	Final     string   `json:"final,omitempty"` // tras las redirecciones, si cambia
	Title     string   `json:"titulo,omitempty"`
	Site      string   `json:"sitio,omitempty"`
	Author    string   `json:"autor,omitempty"`
	Published string   `json:"fecha,omitempty"` // AAAA-MM-DD
	Image     string   `json:"imagen,omitempty"`
	Excerpt   string   `json:"extracto,omitempty"`
	Lang      string   `json:"idioma,omitempty"`
	Words     int      `json:"palabras,omitempty"`
	Minutes   int      `json:"minutos,omitempty"`
	Truncated bool     `json:"recortado,omitempty"`
	Tags      []string `json:"etiquetas,omitempty"`
	Err       string   `json:"error,omitempty"`
	Origin    string   `json:"origen,omitempty"` // web o asistente
}

type article struct {
	ID       int64
	Read     bool
	Favorite bool
	Archived bool
	State    string
	Attempts int
	Created  time.Time
	Fetched  time.Time
	ReadAt   time.Time
	meta
}

// title es el título que se enseña: el de la página o, si no, el sitio.
func (a article) title() string {
	if a.Title != "" {
		return a.Title
	}
	return hostOf(a.URL)
}

// site es el nombre del sitio: el que dice la página o su dominio.
func (a article) site() string {
	if a.Site != "" {
		return a.Site
	}
	if a.Final != "" {
		return hostOf(a.Final)
	}
	return hostOf(a.URL)
}

func (s store) urlToken(u string) int64 { return s.box.Token("url", u) }

// tokens son las huellas para buscar: las palabras del título, el sitio, el
// autor, el extracto, las etiquetas y el texto, y cada etiqueta exacta.
func (s store) tokens(m meta, blocks []block) []int64 {
	t := []int64{}
	t = s.box.Index(t, fieldWords, strings.Join([]string{m.Title, m.Site, hostOf(m.URL), m.Author, m.Excerpt, strings.Join(m.Tags, " ")}, " "))
	for _, tag := range m.Tags {
		t = append(t, s.tagToken(tag))
	}
	for _, b := range blocks {
		t = s.box.Index(t, fieldWords, b.Text+" "+strings.Join(b.Items, " "))
	}
	return t
}

func (s store) tagToken(tag string) int64 { return s.box.Token(fieldTag, seal.Fold(tag)) }

const cols = `id, read, favorite, archived, state, attempts, created_at, fetched_at, read_at, sealed`

func (s store) scan(rows pgx.Rows) ([]article, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (article, error) {
		var a article
		var fetched, readAt *time.Time
		var sealed []byte
		if err := r.Scan(&a.ID, &a.Read, &a.Favorite, &a.Archived, &a.State, &a.Attempts, &a.Created, &fetched, &readAt, &sealed); err != nil {
			return a, err
		}
		if fetched != nil {
			a.Fetched = *fetched
		}
		if readAt != nil {
			a.ReadAt = *readAt
		}
		return a, s.box.OpenJSON(sealMeta, sealed, &a.meta)
	})
}

// add guarda un enlace nuevo (aún sin descargar). Si ya estaba, devuelve el
// que había.
func (s store) add(ctx context.Context, owner string, m meta, at time.Time) (article, bool, error) {
	tok := s.urlToken(m.URL)
	var out article
	existed := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "leer|"+owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+cols+` FROM articles WHERE owner = $1 AND url_token = $2`, owner, tok)
		list, err := s.scan(rows)
		if err != nil {
			return err
		}
		if len(list) > 0 {
			out, existed = list[0], true
			return nil
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM articles WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxArticles {
			return errFull
		}
		sealed, err := s.box.SealJSON(sealMeta, m)
		if err != nil {
			return err
		}
		out = article{State: stateQueued, meta: m}
		return tx.QueryRow(ctx, `INSERT INTO articles (owner, url_token, tokens, sealed, created_at) VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
			owner, tok, s.tokens(m, nil), sealed, at).Scan(&out.ID, &out.Created)
	})
	return out, existed, err
}

func (s store) get(ctx context.Context, owner string, id int64) (article, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+cols+` FROM articles WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scan(rows)
	if err != nil {
		return article{}, err
	}
	if len(list) == 0 {
		return article{}, errNotFound
	}
	return list[0], nil
}

// body es el texto limpio de un artículo (vacío si aún no lo hay).
func (s store) body(ctx context.Context, owner string, id int64) ([]block, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT body FROM articles WHERE owner = $1 AND id = $2`, owner, id).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil || sealed == nil {
		return nil, err
	}
	var out []block
	return out, s.box.OpenJSON(sealBody, sealed, &out)
}

// Listas.
const (
	listPending  = "pendientes"
	listFavorite = "favoritos"
	listArchive  = "archivo"
	listAll      = "todos"
)

func validList(l string) bool {
	return l == listPending || l == listFavorite || l == listArchive || l == listAll
}

// filter elige qué artículos listar.
type filter struct {
	List  string // pendientes (por defecto), favoritos, archivo o todos
	Query string // palabras (todas)
	Tag   string
	Limit int
}

// list son los artículos de una persona, del más nuevo al más antiguo, y
// cuántos hay en total con ese filtro.
func (s store) list(ctx context.Context, owner string, f filter) ([]article, int, error) {
	where := `owner = $1`
	args := []any{owner}
	switch f.List {
	case listFavorite:
		where += ` AND favorite`
	case listArchive:
		where += ` AND archived`
	case listAll:
	default:
		where += ` AND NOT archived`
	}
	if q := s.box.Query(fieldWords, f.Query); len(q) > 0 {
		args = append(args, q)
		where += fmt.Sprintf(` AND tokens @> $%d`, len(args))
	}
	if t := strings.TrimSpace(f.Tag); t != "" {
		args = append(args, []int64{s.tagToken(t)})
		where += fmt.Sprintf(` AND tokens @> $%d`, len(args))
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM articles WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > maxArticles {
		limit = maxArticles
	}
	args = append(args, limit)
	rows, _ := s.db.Query(ctx, `SELECT `+cols+` FROM articles WHERE `+where+fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	list, err := s.scan(rows)
	return list, total, err
}

// counts son los números de cada lista.
type counts struct {
	Pending, Unread, Favorite, Archive int
}

func (s store) counts(ctx context.Context, owner string) (counts, error) {
	var c counts
	err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT archived), count(*) FILTER (WHERE NOT archived AND NOT read),
		count(*) FILTER (WHERE favorite), count(*) FILTER (WHERE archived) FROM articles WHERE owner = $1`, owner).
		Scan(&c.Pending, &c.Unread, &c.Favorite, &c.Archive)
	return c, err
}

// saveFetch guarda el resultado de una descarga: los datos, el texto (sin
// texto, si falló) y el estado.
func (s store) saveFetch(ctx context.Context, owner string, a article, blocks []block, at time.Time) error {
	sealed, err := s.box.SealJSON(sealMeta, a.meta)
	if err != nil {
		return err
	}
	var body []byte
	if len(blocks) > 0 {
		if body, err = s.box.SealJSON(sealBody, blocks); err != nil {
			return err
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE articles SET state = $3, attempts = $4, fetched_at = $5, tokens = $6, sealed = $7, body = $8
		WHERE owner = $1 AND id = $2`,
		owner, a.ID, a.State, a.Attempts, at, s.tokens(a.meta, blocks), sealed, body)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// setMeta guarda lo cifrado de un artículo (por ejemplo, sus etiquetas) y
// rehace sus huellas.
func (s store) setMeta(ctx context.Context, owner string, a article) error {
	blocks, err := s.body(ctx, owner, a.ID)
	if err != nil {
		return err
	}
	sealed, err := s.box.SealJSON(sealMeta, a.meta)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE articles SET sealed = $3, tokens = $4 WHERE owner = $1 AND id = $2`,
		owner, a.ID, sealed, s.tokens(a.meta, blocks))
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// flags son los interruptores que se cambian; nil deja el que hay.
type flags struct {
	Read, Favorite, Archived *bool
}

func (s store) setFlags(ctx context.Context, owner string, id int64, f flags, at time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE articles SET
		read = coalesce($3, read), favorite = coalesce($4, favorite), archived = coalesce($5, archived),
		read_at = CASE WHEN $3 IS NULL THEN read_at WHEN $3 THEN coalesce(read_at, $6) ELSE NULL END
		WHERE owner = $1 AND id = $2`, owner, id, f.Read, f.Favorite, f.Archived, at)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) delete(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM articles WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// retryItem es un artículo de alguien que toca volver a descargar.
type retryItem struct {
	Owner string
	article
}

// toRetry son los artículos de todos que conviene volver a descargar: los
// que fallaron por algo pasajero (cada vez se espera más) y los que se
// quedaron a medias (la app se paró mientras descargaba).
func (s store) toRetry(ctx context.Context, now time.Time, limit int) ([]retryItem, error) {
	rows, _ := s.db.Query(ctx, `SELECT owner, `+cols+` FROM articles
		WHERE (state = 'error' AND attempts < $2 AND fetched_at < $1::timestamptz - interval '1 hour' * attempts * attempts)
		   OR (state = 'cola' AND created_at < $1::timestamptz - interval '10 minutes')
		ORDER BY created_at LIMIT $3`, now, maxAttempts, limit)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (retryItem, error) {
		var it retryItem
		var fetched, readAt *time.Time
		var sealed []byte
		if err := r.Scan(&it.Owner, &it.ID, &it.Read, &it.Favorite, &it.Archived, &it.State, &it.Attempts, &it.Created, &fetched, &readAt, &sealed); err != nil {
			return it, err
		}
		if fetched != nil {
			it.Fetched = *fetched
		}
		return it, s.box.OpenJSON(sealMeta, sealed, &it.meta)
	})
}

// ---- Ajustes ----

// Tamaños de letra del lector.
var sizes = []struct{ Key, Label string }{{"s", "Pequeña"}, {"m", "Normal"}, {"l", "Grande"}, {"xl", "Muy grande"}}

func validSize(k string) bool {
	for _, x := range sizes {
		if x.Key == k {
			return true
		}
	}
	return false
}

type prefs struct {
	Size    string
	Summary bool // línea en el resumen de la mañana
	Weekly  bool // aviso semanal de pendientes por Telegram
}

func defaultPrefs() prefs { return prefs{Size: "m", Summary: true} }

func (s store) prefs(ctx context.Context, owner string) (prefs, error) {
	p := defaultPrefs()
	err := s.db.QueryRow(ctx, `SELECT size, summary, weekly FROM prefs WHERE owner = $1`, owner).Scan(&p.Size, &p.Summary, &p.Weekly)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultPrefs(), nil
	}
	if !validSize(p.Size) {
		p.Size = "m"
	}
	return p, err
}

func (s store) setPrefs(ctx context.Context, owner string, p prefs) error {
	_, err := s.db.Exec(ctx, `INSERT INTO prefs (owner, size, summary, weekly) VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner) DO UPDATE SET size = excluded.size, summary = excluded.summary, weekly = excluded.weekly`,
		owner, p.Size, p.Summary, p.Weekly)
	return err
}

// weeklyOwners son quienes tienen el aviso semanal y aún no lo han recibido
// ese día; los marca como avisados.
func (s store) weeklyOwners(ctx context.Context, day time.Time) ([]string, error) {
	rows, _ := s.db.Query(ctx, `UPDATE prefs SET weekly_on = $1 WHERE weekly AND (weekly_on IS NULL OR weekly_on < $1) RETURNING owner`, day.Format("2006-01-02"))
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	sort.Strings(list)
	return list, err
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
		out = append(out, notice{Kind: "leer", Text: x.text})
	}
	return out, nil
}
