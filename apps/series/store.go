package main

import (
	"context"
	"errors"
	"sort"
	"strconv"
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
	maxFollows = 300
	maxItems   = 3000 // por tipo
	keepNotice = 7 * 24 * time.Hour
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas de lo personal filtran por el dueño, salvo las de la tarea de
// fondo (allFollows), que recorre lo de todos para pedir cada serie una sola
// vez y preparar los avisos.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealShow   = "series/shows"
	sealPoster = "series/posters"
	sealFollow = "series/follows"
	sealItem   = "series/items"
	sealNotice = "series/notices"
)

// ---- Series de TVmaze ----

func (s store) showToken(tvmazeID int64) int64 {
	return s.box.Token("tvmaze", strconv.FormatInt(tvmazeID, 10))
}

// saveShow guarda (o pone al día) una serie de TVmaze y devuelve su id.
func (s store) saveShow(ctx context.Context, d showData, at time.Time) (int64, error) {
	sealed, err := s.box.SealJSON(sealShow, d)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO shows (key_token, checked_at, sealed) VALUES ($1, $2, $3)
		ON CONFLICT (key_token) DO UPDATE SET checked_at = excluded.checked_at, version = shows.version + 1, sealed = excluded.sealed
		RETURNING id`, s.showToken(d.TVmazeID), at, sealed).Scan(&id)
	return id, err
}

// touchShow apunta que se ha mirado una serie y no había nada nuevo.
func (s store) touchShow(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE shows SET checked_at = $2 WHERE id = $1`, id, at)
	return err
}

// showByTVmaze es el id y la última consulta de una serie ya guardada.
func (s store) showByTVmaze(ctx context.Context, tvmazeID int64) (int64, time.Time, error) {
	var id int64
	var at time.Time
	err := s.db.QueryRow(ctx, `SELECT id, checked_at FROM shows WHERE key_token = $1`, s.showToken(tvmazeID)).Scan(&id, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, at, errNotFound
	}
	return id, at, err
}

// showStamp es la versión de lo guardado de cada serie y cuándo se miró.
type showStamp struct {
	Version int64
	Checked time.Time
}

// showStamps son las de unas series (todas las seguidas si ids es nil).
func (s store) showStamps(ctx context.Context, ids []int64) (map[int64]showStamp, error) {
	var rows pgx.Rows
	if ids == nil {
		rows, _ = s.db.Query(ctx, `SELECT id, version, checked_at FROM shows s WHERE EXISTS (SELECT 1 FROM follows f WHERE f.show_id = s.id)`)
	} else {
		rows, _ = s.db.Query(ctx, `SELECT id, version, checked_at FROM shows WHERE id = ANY($1)`, ids)
	}
	out := map[int64]showStamp{}
	var id int64
	var st showStamp
	_, err := pgx.ForEachRow(rows, []any{&id, &st.Version, &st.Checked}, func() error { out[id] = st; return nil })
	return out, err
}

// showsData descifra unas series.
func (s store) showsData(ctx context.Context, ids []int64) (map[int64]*showData, map[int64]int64, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, version, sealed FROM shows WHERE id = ANY($1)`, ids)
	out := map[int64]*showData{}
	stamps := map[int64]int64{}
	var id, at int64
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&id, &at, &sealed}, func() error {
		d := &showData{}
		if err := s.box.OpenJSON(sealShow, sealed, d); err != nil {
			return err
		}
		out[id], stamps[id] = d, at
		return nil
	})
	return out, stamps, err
}

// gcShows borra las series que ya no sigue nadie (con sus carteles).
func (s store) gcShows(ctx context.Context) ([]int64, error) {
	rows, _ := s.db.Query(ctx, `DELETE FROM shows s WHERE NOT EXISTS (SELECT 1 FROM follows f WHERE f.show_id = s.id) RETURNING id`)
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// ---- Carteles ----

func (s store) posterToken(src string) int64 { return s.box.Token("cartel", src) }

func (s store) setPoster(ctx context.Context, showID int64, src string, img []byte, at time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO posters (show_id, src_token, fetched_at, sealed) VALUES ($1, $2, $3, $4)
		ON CONFLICT (show_id) DO UPDATE SET src_token = excluded.src_token, fetched_at = excluded.fetched_at, sealed = excluded.sealed`,
		showID, s.posterToken(src), at, s.box.Seal(sealPoster+"/"+idStr(showID), img))
	return err
}

// posterSources dice, de cada serie con cartel, la huella de su URL.
func (s store) posterSources(ctx context.Context, ids []int64) (map[int64]int64, error) {
	rows, _ := s.db.Query(ctx, `SELECT show_id, src_token FROM posters WHERE show_id = ANY($1)`, ids)
	out := map[int64]int64{}
	var id, tok int64
	_, err := pgx.ForEachRow(rows, []any{&id, &tok}, func() error { out[id] = tok; return nil })
	return out, err
}

// poster es el cartel de una serie que sigue la persona (por el id de su
// seguimiento, no el de la serie).
func (s store) poster(ctx context.Context, owner string, followID int64) ([]byte, error) {
	var showID int64
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT p.show_id, p.sealed FROM posters p JOIN follows f ON f.show_id = p.show_id
		WHERE f.owner = $1 AND f.id = $2`, owner, followID).Scan(&showID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.box.Open(sealPoster+"/"+idStr(showID), sealed)
}

// ---- Series que sigue cada persona ----

// followData es lo cifrado de cada serie seguida.
type followData struct {
	State     string  `json:"estado"`
	Rating    int     `json:"valoracion,omitempty"` // 1–5; 0 sin valorar
	Note      string  `json:"nota,omitempty"`
	Watched   []int64 `json:"vistos,omitempty"`     // ids de TVmaze de los capítulos vistos
	Notified  []int64 `json:"avisados,omitempty"`   // capítulos de los que ya se ha avisado
	Announced []int   `json:"anunciadas,omitempty"` // temporadas cuya vuelta ya se ha avisado
}

type follow struct {
	ID      int64
	Owner   string
	ShowID  int64
	Created time.Time
	followData
}

const followCols = `id, owner, show_id, created_at, sealed`

func (s store) scanFollows(rows pgx.Rows) ([]follow, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (follow, error) {
		var f follow
		var sealed []byte
		if err := r.Scan(&f.ID, &f.Owner, &f.ShowID, &f.Created, &sealed); err != nil {
			return f, err
		}
		return f, s.box.OpenJSON(sealFollow, sealed, &f.followData)
	})
}

func (s store) follows(ctx context.Context, owner string) ([]follow, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+followCols+` FROM follows WHERE owner = $1 ORDER BY id`, owner)
	return s.scanFollows(rows)
}

func (s store) follow(ctx context.Context, owner string, id int64) (follow, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+followCols+` FROM follows WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanFollows(rows)
	if err != nil {
		return follow{}, err
	}
	if len(list) == 0 {
		return follow{}, errNotFound
	}
	return list[0], nil
}

// allFollows son las de todos: solo para la tarea de fondo.
func (s store) allFollows(ctx context.Context) ([]follow, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+followCols+` FROM follows ORDER BY id`)
	return s.scanFollows(rows)
}

// addFollow sigue una serie ya guardada; si ya la seguía, devuelve la que
// había.
func (s store) addFollow(ctx context.Context, owner string, showID int64, d followData) (follow, bool, error) {
	var out follow
	existed := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "series|"+owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+followCols+` FROM follows WHERE owner = $1 AND show_id = $2`, owner, showID)
		list, err := s.scanFollows(rows)
		if err != nil {
			return err
		}
		if len(list) > 0 {
			out, existed = list[0], true
			return nil
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM follows WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxFollows {
			return errFull
		}
		sealed, err := s.box.SealJSON(sealFollow, d)
		if err != nil {
			return err
		}
		out = follow{Owner: owner, ShowID: showID, followData: d}
		return tx.QueryRow(ctx, `INSERT INTO follows (owner, show_id, sealed) VALUES ($1, $2, $3) RETURNING id, created_at`,
			owner, showID, sealed).Scan(&out.ID, &out.Created)
	})
	return out, existed, err
}

// updateFollow cambia una serie seguida dentro de una transacción (la web,
// Talia y los avisos pueden tocarla a la vez). fn dice si hay que guardar.
func (s store) updateFollow(ctx context.Context, owner string, id int64, fn func(*follow) (bool, error)) (follow, error) {
	var out follow
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, `SELECT `+followCols+` FROM follows WHERE owner = $1 AND id = $2 FOR UPDATE`, owner, id)
		list, err := s.scanFollows(rows)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return errNotFound
		}
		out = list[0]
		save, err := fn(&out)
		if err != nil || !save {
			return err
		}
		sealed, err := s.box.SealJSON(sealFollow, out.followData)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE follows SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, id, sealed)
		return err
	})
	return out, err
}

// deleteFollow deja de seguir una serie y, si ya no la sigue nadie, la borra
// con su cartel.
func (s store) deleteFollow(ctx context.Context, owner string, id int64) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var showID int64
		err := tx.QueryRow(ctx, `DELETE FROM follows WHERE owner = $1 AND id = $2 RETURNING show_id`, owner, id).Scan(&showID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM shows s WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM follows f WHERE f.show_id = s.id)`, showID)
		return err
	})
}

// ---- Pelis y juegos ----

// itemData es lo cifrado de una peli o un juego. Source y SourceID quedan
// para una fuente futura (hoy todo es «manual»).
type itemData struct {
	Title    string `json:"titulo"`
	Year     int    `json:"anio,omitempty"`
	Platform string `json:"plataforma,omitempty"`
	State    string `json:"estado"`
	Rating   int    `json:"valoracion,omitempty"`
	Day      string `json:"fecha,omitempty"` // AAAA-MM-DD: cuándo se vio o se terminó
	Note     string `json:"nota,omitempty"`
	Source   string `json:"fuente,omitempty"`
	SourceID string `json:"id_fuente,omitempty"`
	Origin   string `json:"origen,omitempty"` // web o asistente
}

type item struct {
	ID      int64
	Kind    string
	Created time.Time
	itemData
}

func (s store) scanItems(rows pgx.Rows) ([]item, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) {
		var it item
		var sealed []byte
		if err := r.Scan(&it.ID, &it.Kind, &it.Created, &sealed); err != nil {
			return it, err
		}
		return it, s.box.OpenJSON(sealItem, sealed, &it.itemData)
	})
}

// items son las pelis o los juegos de una persona: los últimos apuntados
// primero.
func (s store) items(ctx context.Context, owner, kind string) ([]item, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, kind, created_at, sealed FROM items WHERE owner = $1 AND kind = $2 ORDER BY id DESC`, owner, kind)
	return s.scanItems(rows)
}

func (s store) item(ctx context.Context, owner string, id int64) (item, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, kind, created_at, sealed FROM items WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanItems(rows)
	if err != nil {
		return item{}, err
	}
	if len(list) == 0 {
		return item{}, errNotFound
	}
	return list[0], nil
}

func (s store) addItem(ctx context.Context, owner, kind string, d itemData) (int64, error) {
	sealed, err := s.box.SealJSON(sealItem, d)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM items WHERE owner = $1 AND kind = $2`, owner, kind).Scan(&n); err != nil {
			return err
		}
		if n >= maxItems {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO items (owner, kind, sealed) VALUES ($1, $2, $3) RETURNING id`, owner, kind, sealed).Scan(&id)
	})
	return id, err
}

func (s store) updateItem(ctx context.Context, owner string, it item) error {
	sealed, err := s.box.SealJSON(sealItem, it.itemData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE items SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, it.ID, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteItem(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM items WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
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
// día ya no se mandan) y los marca como entregados. De paso borra los de más
// de 7 días.
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
		out = append(out, notice{Kind: "series", Text: x.text})
	}
	return out, nil
}

// sameTitle compara títulos sin mayúsculas ni tildes.
func sameTitle(a, b string) bool {
	return strings.Join(seal.Words(a), " ") == strings.Join(seal.Words(b), " ")
}
