package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png" // portadas de Flame
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/app"
)

// Seguimiento de series de cómic: buscar en las fuentes, seguir series, ver
// los capítulos nuevos y apuntar por dónde va cada persona. Los capítulos se
// leen en la web de origen; aquí solo se guarda su enlace.

type Series struct {
	ID           int64      `json:"id"`
	Source       string     `json:"source"`
	SourceID     string     `json:"source_id"`
	Title        string     `json:"title"`
	URL          string     `json:"url"`
	HasCover     bool       `json:"has_cover"`
	Status       string     `json:"status"`
	Kind         string     `json:"kind"`
	Authors      []string   `json:"authors"`
	Genres       []string   `json:"genres"`
	Description  string     `json:"description"`
	LatestNumber *float32   `json:"latest_number"`
	LatestAt     *time.Time `json:"latest_at"`
	CheckedAt    *time.Time `json:"checked_at"`
	// De quien pregunta.
	Followed bool     `json:"followed"`
	LastRead *float32 `json:"last_read"`
	Unread   int      `json:"unread"`
	Next     *Chapter `json:"next"` // primer capítulo sin leer
}

type Chapter struct {
	Number      float32    `json:"number"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	PublishedAt *time.Time `json:"published_at"`
	EarlyUntil  *time.Time `json:"early_until"`
}

type seriesStore struct{ db *pgxpool.Pool }

const seriesCols = `s.id, s.source, s.source_id, s.title, s.url, s.cover_url <> '', s.status, s.kind, s.authors, s.genres,
	s.description, s.latest_number, s.latest_at, s.checked_at, f.username IS NOT NULL, f.last_read,
	(SELECT count(*) FROM comic_chapters c WHERE c.series_id = s.id AND f.username IS NOT NULL AND (f.last_read IS NULL OR c.number > f.last_read)),
	n.number, n.title, n.url, n.published_at, n.early_until`

// De la persona ($1): si la sigue, hasta dónde ha leído y el siguiente capítulo.
const seriesPersonal = `LEFT JOIN comic_follows f ON f.series_id = s.id AND f.username = $1
	LEFT JOIN LATERAL (SELECT * FROM comic_chapters c WHERE c.series_id = s.id AND (f.last_read IS NULL OR c.number > f.last_read)
		ORDER BY c.number LIMIT 1) n ON true`

func scanSeries(row pgx.Row) (Series, error) {
	var s Series
	var nNum *float32
	var nTitle, nURL *string
	var nPub, nEarly *time.Time
	err := row.Scan(&s.ID, &s.Source, &s.SourceID, &s.Title, &s.URL, &s.HasCover, &s.Status, &s.Kind, &s.Authors, &s.Genres,
		&s.Description, &s.LatestNumber, &s.LatestAt, &s.CheckedAt, &s.Followed, &s.LastRead, &s.Unread,
		&nNum, &nTitle, &nURL, &nPub, &nEarly)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, errNotFound
	}
	if nNum != nil {
		s.Next = &Chapter{Number: *nNum, Title: *nTitle, URL: *nURL, PublishedAt: nPub, EarlyUntil: nEarly}
	}
	return s, err
}

// followed son las series que sigue la persona: primero las empezadas con
// capítulos sin leer y, dentro, las actualizadas hace menos.
func (s seriesStore) followed(ctx context.Context, who app.Identity) ([]Series, error) {
	rows, err := s.db.Query(ctx, `SELECT `+seriesCols+` FROM comic_series s `+seriesPersonal+`
		WHERE f.username IS NOT NULL ORDER BY (f.last_read IS NOT NULL AND n.number IS NOT NULL) DESC, s.latest_at DESC NULLS LAST`, who.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Series{}
	for rows.Next() {
		sr, err := scanSeries(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

func (s seriesStore) get(ctx context.Context, who app.Identity, id int64) (Series, error) {
	return scanSeries(s.db.QueryRow(ctx, `SELECT `+seriesCols+` FROM comic_series s `+seriesPersonal+` WHERE s.id = $2`, who.User, id))
}

func (s seriesStore) chapters(ctx context.Context, id int64) ([]Chapter, error) {
	rows, _ := s.db.Query(ctx, `SELECT number, title, url, published_at, early_until FROM comic_chapters
		WHERE series_id = $1 ORDER BY number DESC`, id)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Chapter])
}

// save guarda (o actualiza) una serie con todos sus capítulos y devuelve su
// id y cuántos capítulos son nuevos.
func (s seriesStore) save(ctx context.Context, source, sourceID string, d seriesData) (int64, int, error) {
	var id int64
	var added int
	nums := make([]float32, 0, len(d.Chapters))
	titles := make([]string, 0, len(d.Chapters))
	urls := make([]string, 0, len(d.Chapters))
	pubs := make([]*time.Time, 0, len(d.Chapters))
	earlies := make([]*time.Time, 0, len(d.Chapters))
	seen := map[float32]bool{}
	var latest *float32
	var latestAt *time.Time
	for _, c := range d.Chapters {
		if seen[c.Number] || c.Number < 0 {
			continue
		}
		seen[c.Number] = true
		nums = append(nums, c.Number)
		titles = append(titles, clean(c.Title, 300))
		urls = append(urls, c.URL)
		pubs = append(pubs, c.PublishedAt)
		earlies = append(earlies, c.EarlyUntil)
		if latest == nil || c.Number > *latest {
			n := c.Number
			latest = &n
		}
		if c.PublishedAt != nil && (latestAt == nil || c.PublishedAt.After(*latestAt)) {
			latestAt = c.PublishedAt
		}
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO comic_series (source, source_id, title, url, cover_url, status, kind, authors, genres,
				description, latest_number, latest_at, checked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
			ON CONFLICT (source, source_id) DO UPDATE SET title = excluded.title, url = excluded.url, cover_url = excluded.cover_url,
				status = excluded.status, kind = excluded.kind, authors = excluded.authors, genres = excluded.genres,
				description = excluded.description, latest_number = excluded.latest_number, latest_at = excluded.latest_at,
				checked_at = now()
			RETURNING id`,
			source, sourceID, clean(d.Title, 500), d.URL, d.CoverURL, clean(d.Status, 40), clean(d.Kind, 40),
			cleanList(d.Authors, 10, 200), cleanList(d.Genres, 15, 60), optionalText(d.Description, 5000), latest, latestAt).Scan(&id)
		if err != nil {
			return err
		}
		var before int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM comic_chapters WHERE series_id = $1`, id).Scan(&before); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM comic_chapters WHERE series_id = $1 AND NOT (number = ANY($2))`, id, nums); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO comic_chapters (series_id, number, title, url, published_at, early_until)
			SELECT $1, * FROM unnest($2::real[], $3::text[], $4::text[], $5::timestamptz[], $6::timestamptz[])
			ON CONFLICT (series_id, number) DO UPDATE SET title = excluded.title, url = excluded.url,
				published_at = excluded.published_at, early_until = excluded.early_until`,
			id, nums, titles, urls, pubs, earlies)
		if err != nil {
			return err
		}
		var after int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM comic_chapters WHERE series_id = $1`, id).Scan(&after); err != nil {
			return err
		}
		added = max(0, after-before)
		return nil
	})
	return id, added, err
}

func optionalText(s string, max int) string {
	if p := optional(s, max); p != nil {
		return *p
	}
	return ""
}

func (s seriesStore) follow(ctx context.Context, who app.Identity, id int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO comic_follows (username, series_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, who.User, id)
	return err
}

func (s seriesStore) unfollow(ctx context.Context, who app.Identity, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM comic_follows WHERE username = $1 AND series_id = $2`, who.User, id)
	return err
}

func (s seriesStore) setRead(ctx context.Context, who app.Identity, id int64, n *float32) error {
	tag, err := s.db.Exec(ctx, `UPDATE comic_follows SET last_read = $3 WHERE username = $1 AND series_id = $2`, who.User, id, n)
	if err == nil && tag.RowsAffected() == 0 {
		return bad("sigue la serie para apuntar lo que llevas leído")
	}
	return err
}

// known dice qué resultados de búsqueda ya están en la base y si la persona
// los sigue.
func (s seriesStore) known(ctx context.Context, who app.Identity, items []seriesItem) error {
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.Source + ":" + it.ID
	}
	rows, err := s.db.Query(ctx, `SELECT s.source || ':' || s.source_id, s.id, f.username IS NOT NULL FROM comic_series s
		LEFT JOIN comic_follows f ON f.series_id = s.id AND f.username = $1
		WHERE s.source || ':' || s.source_id = ANY($2)`, who.User, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	type k struct {
		id       int64
		followed bool
	}
	found := map[string]k{}
	for rows.Next() {
		var key string
		var v k
		if err := rows.Scan(&key, &v.id, &v.followed); err != nil {
			return err
		}
		found[key] = v
	}
	for i := range items {
		if v, ok := found[items[i].Source+":"+items[i].ID]; ok {
			items[i].SeriesID, items[i].Followed = v.id, v.followed
		}
	}
	return rows.Err()
}

func (s seriesStore) coverURL(ctx context.Context, id int64) (string, error) {
	var u string
	err := s.db.QueryRow(ctx, `SELECT cover_url FROM comic_series WHERE id = $1`, id).Scan(&u)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && u == "") {
		return "", errNotFound
	}
	return u, err
}

// stale son las series que alguien sigue y que no se han comprobado hace rato.
func (s seriesStore) stale(ctx context.Context, older time.Duration) ([]Series, error) {
	rows, err := s.db.Query(ctx, `SELECT s.id, s.source, s.source_id, s.title FROM comic_series s
		WHERE EXISTS (SELECT 1 FROM comic_follows f WHERE f.series_id = s.id)
		  AND (s.checked_at IS NULL OR s.checked_at < now() - $1::interval)
		ORDER BY s.checked_at NULLS FIRST LIMIT 100`, older.String())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Series, error) {
		var sr Series
		err := r.Scan(&sr.ID, &sr.Source, &sr.SourceID, &sr.Title)
		return sr, err
	})
}

// --- Servicio ---

type seriesService struct {
	store   seriesStore
	sources map[string]comicSource
	covers  *coverCache
	log     *slog.Logger

	mu      sync.Mutex
	results map[string]string // portada de cada resultado de búsqueda reciente (fuente:id → URL)
}

func newSeriesService(db *pgxpool.Pool, dir string, urls seriesURLs, hosts []string, allowPrivate bool, log *slog.Logger) (*seriesService, error) {
	c := newCatalog(catalogURLs{}, hosts, allowPrivate)
	covers, err := newCoverCache(filepath.Join(dir, "series-covers"), c)
	if err != nil {
		return nil, err
	}
	return &seriesService{
		store:   seriesStore{db},
		sources: map[string]comicSource{"asura": asura{c: c, urls: urls}, "flame": &flame{c: c, urls: urls}},
		covers:  covers,
		log:     log,
		results: map[string]string{},
	}, nil
}

// search busca en una fuente o en todas (resultados alternados).
func (sv *seriesService) search(ctx context.Context, who app.Identity, source, q string, page int) (map[string]any, error) {
	names := []string{source}
	if source == "todas" {
		names = []string{"asura", "flame"}
	} else if sv.sources[source] == nil {
		return nil, bad("fuente no válida")
	}
	q = clean(q, 100)
	if page < 1 || page > 100 {
		page = 1
	}
	lists := make([][]seriesItem, len(names))
	mores := make([]bool, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i], mores[i], errs[i] = sv.sources[n].search(ctx, q, page)
		}()
	}
	wg.Wait()
	items := []seriesItem{}
	more := false
	failed := []string{}
	for i := range names {
		if errs[i] != nil {
			failed = append(failed, names[i])
			sv.log.Warn("buscar series", "fuente", names[i], "err", errs[i])
		}
		more = more || mores[i]
	}
	if len(failed) == len(names) {
		return nil, errs[0]
	}
	for i := 0; ; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) {
				items = append(items, l[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	sv.mu.Lock()
	if len(sv.results) > 2000 {
		sv.results = map[string]string{}
	}
	for _, it := range items {
		if it.coverURL != "" {
			sv.results[it.Source+":"+it.ID] = it.coverURL
		}
	}
	sv.mu.Unlock()
	if err := sv.store.known(ctx, who, items); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "more": more, "failed": failed}, nil
}

// refresh vuelve a leer una serie de su fuente y guarda los capítulos.
func (sv *seriesService) refresh(ctx context.Context, source, sourceID string) (int64, int, error) {
	src := sv.sources[source]
	if src == nil {
		return 0, 0, bad("fuente no válida")
	}
	if !src.validID(sourceID) {
		return 0, 0, bad("serie no válida")
	}
	d, err := src.fetch(ctx, sourceID)
	if err != nil {
		return 0, 0, err
	}
	return sv.store.save(ctx, source, sourceID, d)
}

// watch comprueba cada poco las series que alguien sigue, de una en una y
// sin prisa para no cargar las webs de origen.
func (sv *seriesService) watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		list, err := sv.store.stale(ctx, every)
		if err != nil {
			sv.log.Error("series pendientes de comprobar", "err", err)
		}
		for _, s := range list {
			rctx, cancel := context.WithTimeout(ctx, time.Minute)
			_, added, err := sv.refresh(rctx, s.Source, s.SourceID)
			cancel()
			if err != nil {
				sv.log.Warn("comprobar serie", "serie", s.Title, "err", err)
			} else if added > 0 {
				sv.log.Info("capítulos nuevos", "serie", s.Title, "nuevos", added)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- API ---

func (a *api) seriesRoutes(mux *http.ServeMux) {
	if a.series == nil {
		return
	}
	mux.HandleFunc("GET /api/series", a.user(a.seriesList))
	mux.HandleFunc("POST /api/series", a.user(a.seriesFollow))
	mux.HandleFunc("GET /api/series/buscar", a.user(a.seriesSearch))
	mux.HandleFunc("GET /api/series/portada", a.user(a.seriesResultCover))
	mux.HandleFunc("GET /api/series/{id}", a.user(a.seriesGet))
	mux.HandleFunc("DELETE /api/series/{id}", a.user(a.seriesUnfollow))
	mux.HandleFunc("PUT /api/series/{id}/leido", a.user(a.seriesRead))
	mux.HandleFunc("GET /api/series/{id}/portada", a.user(a.seriesCover))
}

func (a *api) seriesList(w http.ResponseWriter, r *http.Request, who app.Identity) {
	list, err := a.series.store.followed(r.Context(), who)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *api) seriesSearch(w http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	res, err := a.series.search(r.Context(), who, q.Get("source"), q.Get("q"), page)
	if err != nil {
		a.upstreamFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// upstreamFail distingue los errores de la web de origen de los nuestros.
func (a *api) upstreamFail(w http.ResponseWriter, err error) {
	if errors.Is(err, errUpstream) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, errNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "la serie no existe en la web de origen"})
		return
	}
	a.fail(w, err)
}

func (a *api) seriesFollow(w http.ResponseWriter, r *http.Request, who app.Identity) {
	var in struct {
		Source string `json:"source"`
		ID     string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil {
		a.fail(w, bad("datos no válidos"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	id, _, err := a.series.refresh(ctx, in.Source, in.ID)
	if err != nil {
		a.upstreamFail(w, err)
		return
	}
	if err := a.series.store.follow(r.Context(), who, id); err != nil {
		a.fail(w, err)
		return
	}
	a.log.Info("serie seguida", "usuario", who.User, "fuente", in.Source, "id", in.ID)
	a.seriesDetail(w, r, who, id)
}

func seriesID(r *http.Request) (int64, error) { return bookID(r) }

func (a *api) seriesGet(w http.ResponseWriter, r *http.Request, who app.Identity) {
	id, err := seriesID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	s, err := a.series.store.get(r.Context(), who, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	// Si hace rato que no se comprueba, se mira ahora (sin esperar mucho: si
	// la web de origen no responde, se muestra lo guardado).
	if s.CheckedAt == nil || time.Since(*s.CheckedAt) > 15*time.Minute {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		if _, _, err := a.series.refresh(ctx, s.Source, s.SourceID); err != nil {
			a.log.Warn("comprobar serie", "serie", s.Title, "err", err)
		}
		cancel()
	}
	a.seriesDetail(w, r, who, id)
}

func (a *api) seriesDetail(w http.ResponseWriter, r *http.Request, who app.Identity, id int64) {
	s, err := a.series.store.get(r.Context(), who, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	chapters, err := a.series.store.chapters(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": s, "chapters": chapters})
}

func (a *api) seriesUnfollow(w http.ResponseWriter, r *http.Request, who app.Identity) {
	id, err := seriesID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if err := a.series.store.unfollow(r.Context(), who, id); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// seriesRead apunta el último capítulo leído (null: ninguno).
func (a *api) seriesRead(w http.ResponseWriter, r *http.Request, who app.Identity) {
	id, err := seriesID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	var in struct {
		Number *float32 `json:"number"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil || (in.Number != nil && *in.Number < 0) {
		a.fail(w, bad("capítulo no válido"))
		return
	}
	if err := a.series.store.setRead(r.Context(), who, id, in.Number); err != nil {
		a.fail(w, err)
		return
	}
	s, err := a.series.store.get(r.Context(), who, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *api) seriesCover(w http.ResponseWriter, r *http.Request, who app.Identity) {
	id, err := seriesID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, err := a.series.store.coverURL(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.series.covers.serve(w, r, u)
}

// seriesResultCover sirve la portada de un resultado de búsqueda reciente.
// La URL la conoce el servidor por la búsqueda: el navegador solo manda la
// fuente y el id.
func (a *api) seriesResultCover(w http.ResponseWriter, r *http.Request, who app.Identity) {
	key := r.URL.Query().Get("source") + ":" + r.URL.Query().Get("id")
	a.series.mu.Lock()
	u := a.series.results[key]
	a.series.mu.Unlock()
	if u == "" {
		http.NotFound(w, r)
		return
	}
	a.series.covers.serve(w, r, u)
}

// --- Portadas ---

// coverCache descarga cada portada una vez, la reduce si es grande (las de
// Flame son PNG de 2 MB) y la guarda en disco.
type coverCache struct {
	dir  string
	c    *catalog
	sem  chan struct{} // descargas simultáneas hacia las webs de origen
	lock sync.Map      // una descarga por URL a la vez
}

func newCoverCache(dir string, c *catalog) (*coverCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &coverCache{dir: dir, c: c, sem: make(chan struct{}, 4)}, nil
}

func (cc *coverCache) serve(w http.ResponseWriter, r *http.Request, rawURL string) {
	sum := sha256.Sum256([]byte(rawURL))
	path := filepath.Join(cc.dir, hex.EncodeToString(sum[:16]))
	mu, _ := cc.lock.LoadOrStore(path, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	data, err := os.ReadFile(path)
	if err != nil {
		data, err = cc.fetch(r.Context(), rawURL)
		if err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
	}
	mu.(*sync.Mutex).Unlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(data)
}

func (cc *coverCache) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	select {
	case cc.sem <- struct{}{}:
		defer func() { <-cc.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, ct, err := cc.c.fetchImage(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if len(data) > 300<<10 && (ct == "image/png" || ct == "image/jpeg") {
		if small, err := shrinkImage(data, 360); err == nil {
			return small, nil
		}
	}
	return data, nil
}

// shrinkImage reduce una imagen a ese ancho (promediando píxeles) y la
// guarda en JPEG.
func shrinkImage(data []byte, width int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if b.Dx() <= width {
		width = b.Dx()
	}
	height := b.Dy() * width / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/height, b.Min.Y+(y+1)*b.Dy()/height
		for x := 0; x < width; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/width, b.Min.X+(x+1)*b.Dx()/width
			var r, g, bl, n uint64
			for sy := y0; sy < max(y1, y0+1); sy++ {
				for sx := x0; sx < max(x1, x0+1); sx++ {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 255
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
