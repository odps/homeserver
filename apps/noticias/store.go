package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

// store guarda las noticias recogidas (públicas, en claro) y lo de cada
// persona. Sus intereses, feeds propios y digests van cifrados
// (platform/seal): de los nombres y direcciones solo queda una huella para no
// repetirlos.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

var (
	errNotFound    = errors.New("no encontrado")
	errTopicExists = errors.New("ya tienes un interés con ese nombre")
)

type item struct {
	ID        int64     `json:"id"`
	Feed      string    `json:"-"`
	Source    string    `json:"fuente"`
	Link      string    `json:"enlace"`
	Title     string    `json:"titulo"`
	Summary   string    `json:"extracto,omitempty"`
	Published time.Time `json:"publicada"`
}

// saveItems guarda las noticias nuevas de un feed y devuelve cuántas eran
// nuevas. En los feeds por orden de la red (ranked) la fecha es la posición
// en la última lectura: se actualiza también en las que ya estaban.
func (s store) saveItems(ctx context.Context, feed string, items []feedItem) (int, error) {
	n := 0
	conflict := "DO NOTHING"
	if ranked(feed) {
		conflict = "DO UPDATE SET published = excluded.published, title = excluded.title, summary = excluded.summary"
	}
	key := feedKey(s.box, feed)
	for _, it := range items {
		tag, err := s.db.Exec(ctx, `INSERT INTO items (feed_key, source, link, title, summary, published)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (feed_key, link) `+conflict, key, it.Source, it.Link, it.Title, it.Summary, it.Published)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

func (s store) feedChecked(ctx context.Context, url, title string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	key := feedKey(s.box, url)
	if title == "" { // se conserva el último título conocido
		var old []byte
		if s.db.QueryRow(ctx, `SELECT sealed FROM feeds WHERE key = $1`, key).Scan(&old) == nil {
			var prev feedData
			if s.box.OpenJSON(sealFeedState, old, &prev) == nil {
				title = prev.Title
			}
		}
	}
	sealed, _ := s.box.SealJSON(sealFeedState, feedData{Title: title, Error: msg})
	s.db.Exec(ctx, `INSERT INTO feeds (key, sealed, checked_at, ok_at) VALUES ($1, $2, now(), CASE WHEN $3 THEN now() END)
		ON CONFLICT (key) DO UPDATE SET sealed = excluded.sealed, checked_at = now(), ok_at = coalesce(excluded.ok_at, feeds.ok_at)`,
		key, sealed, msg == "")
}

type feedState struct {
	Title string
	OKAt  *time.Time
	Error string
}

// feedStates es el estado de los feeds pedidos, por URL (en la base solo
// están sus huellas).
func (s store) feedStates(ctx context.Context, urls []string) (map[string]feedState, error) {
	byKey := make(map[int64]string, len(urls))
	keys := make([]int64, 0, len(urls))
	for _, u := range urls {
		k := feedKey(s.box, u)
		byKey[k] = u
		keys = append(keys, k)
	}
	rows, err := s.db.Query(ctx, `SELECT key, sealed, ok_at FROM feeds WHERE key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]feedState{}
	for rows.Next() {
		var k int64
		var sealed []byte
		var f feedState
		if err := rows.Scan(&k, &sealed, &f.OKAt); err != nil {
			return nil, err
		}
		var d feedData
		if err := s.box.OpenJSON(sealFeedState, sealed, &d); err != nil {
			return nil, err
		}
		f.Title, f.Error = d.Title, d.Error
		out[byKey[k]] = f
	}
	return out, rows.Err()
}

// dropOtherFeeds borra el estado y las noticias de los feeds que ya no lee
// nadie (intereses y feeds propios quitados): no se guarda lo que no hace falta.
func (s store) dropOtherFeeds(ctx context.Context, urls []string) error {
	keys := make([]int64, 0, len(urls))
	for _, u := range urls {
		keys = append(keys, feedKey(s.box, u))
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM items WHERE feed_key <> ALL($1)`, keys); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM feeds WHERE key <> ALL($1)`, keys)
	return err
}

func (s store) prune(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM items WHERE published < now() - interval '8 days'`); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM digests WHERE day < current_date - 60`)
	return err
}

// feedURLs son todos los feeds que hay que leer: los medios generales y los
// de los intereses y feeds propios de todas las cuentas.
func (s store) feedURLs(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, g := range generalSources {
		add(g.URL)
	}
	// Categorías y fuentes de redes que tenga activadas alguien, y sus subforos.
	var keys, subs []string
	if err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT p), '{}') FROM prefs, unnest(presets) p`).Scan(&keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		if p, ok := presetByKey(k); ok {
			for _, f := range p.Feeds() {
				add(f)
			}
		}
	}
	if err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT lower(r)), '{}') FROM prefs, unnest(reddit) r`).Scan(&subs); err != nil {
		return nil, err
	}
	for _, r := range subs {
		add(redditFeedURL(r))
	}
	// Búsquedas y feeds propios van cifrados: se descifran todos y add quita
	// los repetidos.
	rows, _ := s.db.Query(ctx, `SELECT sealed FROM topics ORDER BY id`)
	sealed, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	for _, b := range sealed {
		t, err := s.openTopic(b)
		if err != nil {
			return nil, err
		}
		if t.Query != "" {
			add(topicFeedURL(t.Query))
		}
	}
	rows, _ = s.db.Query(ctx, `SELECT sealed FROM own_feeds ORDER BY id`)
	if sealed, err = pgx.CollectRows(rows, pgx.RowTo[[]byte]); err != nil {
		return nil, err
	}
	for _, b := range sealed {
		u, err := s.box.OpenString(sealFeed, b)
		if err != nil {
			return nil, err
		}
		add(u)
	}
	return out, nil
}

func (s store) itemsSince(ctx context.Context, feeds []string, since time.Time) ([]item, error) {
	byKey := make(map[int64]string, len(feeds))
	keys := make([]int64, 0, len(feeds))
	for _, u := range feeds {
		k := feedKey(s.box, u)
		byKey[k] = u
		keys = append(keys, k)
	}
	rows, err := s.db.Query(ctx, `SELECT id, feed_key, source, link, title, summary, published FROM items
		WHERE feed_key = ANY($1) AND published >= $2 ORDER BY published DESC LIMIT 3000`, keys, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item
	for rows.Next() {
		var it item
		var k int64
		if err := rows.Scan(&it.ID, &k, &it.Source, &it.Link, &it.Title, &it.Summary, &it.Published); err != nil {
			return nil, err
		}
		it.Feed = byKey[k]
		out = append(out, it)
	}
	return out, rows.Err()
}

// --- Preferencias, intereses y feeds propios ---

type prefs struct {
	DigestAt   string
	AISummary  bool
	TopCount   int
	TopicCount int
	Disabled   []string // medios generales desactivados
	Presets    []string // categorías y fuentes de redes activadas
	Reddit     []string // subforos
	World      bool     // sección «En el mundo»
	WorldCount int
}

func defaultPrefs() prefs {
	return prefs{DigestAt: "07:00", AISummary: true, TopCount: 10, TopicCount: 5, Disabled: []string{}, Presets: []string{},
		Reddit: []string{}, World: true, WorldCount: 8}
}

func (s store) prefs(ctx context.Context, user string) (prefs, error) {
	p := defaultPrefs()
	err := s.db.QueryRow(ctx, `SELECT digest_at, ai_summary, top_count, topic_count, disabled, presets, reddit, world, world_count
		FROM prefs WHERE username = $1`, user).
		Scan(&p.DigestAt, &p.AISummary, &p.TopCount, &p.TopicCount, &p.Disabled, &p.Presets, &p.Reddit, &p.World, &p.WorldCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	return p, err
}

func (s store) savePrefs(ctx context.Context, user string, p prefs) error {
	_, err := s.db.Exec(ctx, `INSERT INTO prefs (username, digest_at, ai_summary, top_count, topic_count, disabled, presets, reddit, world, world_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (username) DO UPDATE SET digest_at = $2, ai_summary = $3, top_count = $4, topic_count = $5, disabled = $6,
			presets = $7, reddit = $8, world = $9, world_count = $10, updated_at = now()`,
		user, p.DigestAt, p.AISummary, p.TopCount, p.TopicCount, p.Disabled, p.Presets, p.Reddit, p.World, p.WorldCount)
	return err
}

// ensureUser da de alta a quien entra por primera vez (para preparar su digest).
func (s store) ensureUser(ctx context.Context, user string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO prefs (username) VALUES ($1) ON CONFLICT DO NOTHING`, user)
	return err
}

func (s store) users(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT username FROM prefs ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

type topic struct {
	ID    int64
	Name  string
	Query string
	Feeds []ownFeed
}

type ownFeed struct {
	ID  int64
	URL string
}

func (s store) openTopic(sealed []byte) (topicData, error) {
	var t topicData
	return t, s.box.OpenJSON(sealTopic, sealed, &t)
}

func (s store) sealTopic(name, query string) []byte {
	b, _ := s.box.SealJSON(sealTopic, topicData{Name: name, Query: query}) // dos textos: no puede fallar
	return b
}

// topics devuelve los intereses en su orden (posición, que va en claro) con
// sus feeds propios.
func (s store) topics(ctx context.Context, user string) ([]topic, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.sealed, coalesce(f.id, 0), f.sealed
		FROM topics t LEFT JOIN own_feeds f ON f.topic_id = t.id
		WHERE t.username = $1 ORDER BY t.position, t.id, f.id`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []topic
	for rows.Next() {
		var t topic
		var f ownFeed
		var ts, fs []byte
		if err := rows.Scan(&t.ID, &ts, &f.ID, &fs); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != t.ID {
			d, err := s.openTopic(ts)
			if err != nil {
				return nil, err
			}
			t.Name, t.Query = d.Name, d.Query
			out = append(out, t)
		}
		if f.ID != 0 {
			if f.URL, err = s.box.OpenString(sealFeed, fs); err != nil {
				return nil, err
			}
			out[len(out)-1].Feeds = append(out[len(out)-1].Feeds, f)
		}
	}
	return out, rows.Err()
}

// uniqueTopic traduce el choque de nombres (la huella ya existe) a un error
// que se puede enseñar.
func uniqueTopic(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return errTopicExists
	}
	return err
}

func (s store) addTopic(ctx context.Context, user, name, query string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO topics (username, sealed, name_token, position)
		VALUES ($1, $2, $3, (SELECT coalesce(max(position), 0) + 1 FROM topics WHERE username = $1)) RETURNING id`,
		user, s.sealTopic(name, query), topicToken(s.box, name)).Scan(&id)
	return id, uniqueTopic(err)
}

func (s store) updateTopic(ctx context.Context, user string, id int64, name, query string) error {
	tag, err := s.db.Exec(ctx, `UPDATE topics SET sealed = $3, name_token = $4 WHERE id = $2 AND username = $1`,
		user, id, s.sealTopic(name, query), topicToken(s.box, name))
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return uniqueTopic(err)
}

func (s store) deleteTopic(ctx context.Context, user string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM topics WHERE id = $2 AND username = $1`, user, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) addOwnFeed(ctx context.Context, user string, topicID int64, url string) error {
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topics WHERE id = $1 AND username = $2)`, topicID, user).Scan(&ok); err != nil || !ok {
		return errNotFound
	}
	_, err := s.db.Exec(ctx, `INSERT INTO own_feeds (username, topic_id, sealed, url_token) VALUES ($1, $2, $3, $4)
		ON CONFLICT (username, url_token) DO UPDATE SET topic_id = excluded.topic_id`,
		user, topicID, s.box.SealString(sealFeed, url), feedToken(s.box, url))
	return err
}

func (s store) deleteOwnFeed(ctx context.Context, user string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM own_feeds WHERE id = $2 AND username = $1`, user, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// --- Digest ---

type digestItem struct {
	Title   string    `json:"titulo"`
	Link    string    `json:"enlace"`
	Source  string    `json:"fuente"`
	Summary string    `json:"extracto,omitempty"`
	Time    time.Time `json:"publicada"`
	Others  []string  `json:"tambien,omitempty"` // otros medios que la cuentan
}

type digestSection struct {
	Title string `json:"titulo"`
	// Kind: «espana», «mundo», «tema» (categoría elegida), «interes» o «red»
	// (por orden de la red: tendencias, votos; las demás, por cobertura).
	Kind  string       `json:"tipo,omitempty"`
	Items []digestItem `json:"noticias"`
}

// summaryBlock es un bloque del resumen del día (España, el mundo, tus
// intereses), una frase por línea.
type summaryBlock struct {
	Title string   `json:"titulo"`
	Lines []string `json:"lineas"`
}

type digest struct {
	Day      string          `json:"dia"`
	Summary  string          `json:"resumen,omitempty"` // de los digest anteriores a los bloques
	Blocks   []summaryBlock  `json:"resumen_bloques,omitempty"`
	Sections []digestSection `json:"secciones"`
	Created  time.Time       `json:"preparado"`
}

func (s store) saveDigest(ctx context.Context, user string, d digest) error {
	b, err := s.box.SealJSON(sealDigest, digestData{Summary: d.Summary, Blocks: d.Blocks, Sections: d.Sections})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO digests (username, day, sealed, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (username, day) DO UPDATE SET sealed = $3, created_at = $4`, user, d.Day, b, d.Created)
	return err
}

func (s store) digest(ctx context.Context, user, day string) (digest, error) {
	var d digest
	var sealed []byte
	var t time.Time
	err := s.db.QueryRow(ctx, `SELECT day, sealed, created_at FROM digests WHERE username = $1 AND day = $2`, user, day).
		Scan(&t, &sealed, &d.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errNotFound
	}
	if err != nil {
		return d, err
	}
	d.Day = t.Format("2006-01-02")
	var data digestData
	if err := s.box.OpenJSON(sealDigest, sealed, &data); err != nil {
		return d, err
	}
	d.Summary, d.Blocks, d.Sections = data.Summary, data.Blocks, data.Sections
	return d, nil
}

func (s store) days(ctx context.Context, user string, limit int) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT day FROM digests WHERE username = $1 ORDER BY day DESC LIMIT $2`, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t.Format("2006-01-02"))
	}
	return out, rows.Err()
}
