package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type store struct{ db *pgxpool.Pool }

var errNotFound = errors.New("no encontrado")

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
	for _, it := range items {
		tag, err := s.db.Exec(ctx, `INSERT INTO items (feed, source, link, title, summary, published)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (feed, link) `+conflict, feed, it.Source, it.Link, it.Title, it.Summary, it.Published)
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
	s.db.Exec(ctx, `INSERT INTO feeds (url, title, checked_at, ok_at, error) VALUES ($1, $2, now(), CASE WHEN $3 = '' THEN now() END, $3)
		ON CONFLICT (url) DO UPDATE SET title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE feeds.title END,
			checked_at = now(), ok_at = coalesce(excluded.ok_at, feeds.ok_at), error = excluded.error`, url, title, msg)
}

type feedState struct {
	Title string
	OKAt  *time.Time
	Error string
}

func (s store) feedStates(ctx context.Context) (map[string]feedState, error) {
	rows, err := s.db.Query(ctx, `SELECT url, title, ok_at, error FROM feeds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]feedState{}
	for rows.Next() {
		var u string
		var f feedState
		if err := rows.Scan(&u, &f.Title, &f.OKAt, &f.Error); err != nil {
			return nil, err
		}
		out[u] = f
	}
	return out, rows.Err()
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
	rows, err := s.db.Query(ctx, `SELECT DISTINCT query FROM topics WHERE query <> ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			rows.Close()
			return nil, err
		}
		add(topicFeedURL(q))
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT DISTINCT url FROM own_feeds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		add(u)
	}
	return out, rows.Err()
}

func (s store) itemsSince(ctx context.Context, feeds []string, since time.Time) ([]item, error) {
	rows, err := s.db.Query(ctx, `SELECT id, feed, source, link, title, summary, published FROM items
		WHERE feed = ANY($1) AND published >= $2 ORDER BY published DESC LIMIT 3000`, feeds, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Feed, &it.Source, &it.Link, &it.Title, &it.Summary, &it.Published); err != nil {
			return nil, err
		}
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

func (s store) topics(ctx context.Context, user string) ([]topic, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.query, coalesce(f.id, 0), coalesce(f.url, '')
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
		if err := rows.Scan(&t.ID, &t.Name, &t.Query, &f.ID, &f.URL); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != t.ID {
			out = append(out, t)
		}
		if f.ID != 0 {
			out[len(out)-1].Feeds = append(out[len(out)-1].Feeds, f)
		}
	}
	return out, rows.Err()
}

func (s store) addTopic(ctx context.Context, user, name, query string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO topics (username, name, query, position)
		VALUES ($1, $2, $3, (SELECT coalesce(max(position), 0) + 1 FROM topics WHERE username = $1))`, user, name, query)
	return err
}

func (s store) updateTopic(ctx context.Context, user string, id int64, name, query string) error {
	tag, err := s.db.Exec(ctx, `UPDATE topics SET name = $3, query = $4 WHERE id = $2 AND username = $1`, user, id, name, query)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteTopic(ctx context.Context, user string, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM topics WHERE id = $2 AND username = $1`, user, id)
	return err
}

func (s store) addOwnFeed(ctx context.Context, user string, topicID int64, url string) error {
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topics WHERE id = $1 AND username = $2)`, topicID, user).Scan(&ok); err != nil || !ok {
		return errNotFound
	}
	_, err := s.db.Exec(ctx, `INSERT INTO own_feeds (username, topic_id, url) VALUES ($1, $2, $3)
		ON CONFLICT (username, url) DO UPDATE SET topic_id = excluded.topic_id`, user, topicID, url)
	return err
}

func (s store) deleteOwnFeed(ctx context.Context, user string, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM own_feeds WHERE id = $2 AND username = $1`, user, id)
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
	Title string       `json:"titulo"`
	Kind  string       `json:"tipo,omitempty"` // «red»: por orden de la red (tendencias, votos); si no, por cobertura
	Items []digestItem `json:"noticias"`
}

type digest struct {
	Day      string          `json:"dia"`
	Summary  string          `json:"resumen,omitempty"`
	Sections []digestSection `json:"secciones"`
	Created  time.Time       `json:"preparado"`
}

func (s store) saveDigest(ctx context.Context, user string, d digest) error {
	b, err := json.Marshal(d.Sections)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO digests (username, day, content, summary, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (username, day) DO UPDATE SET content = $3, summary = $4, created_at = $5`, user, d.Day, b, d.Summary, d.Created)
	return err
}

func (s store) digest(ctx context.Context, user, day string) (digest, error) {
	var d digest
	var raw []byte
	var t time.Time
	err := s.db.QueryRow(ctx, `SELECT day, content, summary, created_at FROM digests WHERE username = $1 AND day = $2`, user, day).
		Scan(&t, &raw, &d.Summary, &d.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errNotFound
	}
	if err != nil {
		return d, err
	}
	d.Day = t.Format("2006-01-02")
	return d, json.Unmarshal(raw, &d.Sections)
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
