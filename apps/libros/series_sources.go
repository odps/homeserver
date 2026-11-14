package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fuentes de series de cómic. Cada una sabe buscar series y leer la ficha de
// una con su lista de capítulos; nunca se descargan las páginas.

type seriesItem struct {
	Source   string     `json:"source"`
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Status   string     `json:"status,omitempty"`
	Kind     string     `json:"kind,omitempty"`
	Chapters int        `json:"chapters,omitempty"`
	LatestAt *time.Time `json:"latest_at,omitempty"`
	HasCover bool       `json:"has_cover"`
	SeriesID int64      `json:"series_id,omitempty"` // ya está en la base
	Followed bool       `json:"followed"`
	coverURL string
}

type chapterData struct {
	Number      float32
	Title       string
	URL         string
	PublishedAt *time.Time
	EarlyUntil  *time.Time
}

type seriesData struct {
	Title       string
	URL         string
	CoverURL    string
	Status      string
	Kind        string
	Authors     []string
	Genres      []string
	Description string
	Chapters    []chapterData
}

// seriesQuery es una búsqueda: texto, página, orden (popular, updated,
// rating, title) y estado (ongoing, completed, hiatus, dropped).
type seriesQuery struct {
	Q      string
	Page   int
	Sort   string
	Status string
}

var (
	seriesSorts    = map[string]bool{"": true, "popular": true, "updated": true, "rating": true, "title": true}
	seriesStatuses = map[string]bool{"": true, "ongoing": true, "completed": true, "hiatus": true, "dropped": true}
)

type comicSource interface {
	search(ctx context.Context, sq seriesQuery) ([]seriesItem, bool, error)
	fetch(ctx context.Context, id string) (seriesData, error)
	validID(id string) bool
}

type seriesURLs struct {
	AsuraAPI  string // https://api.asurascans.com
	AsuraSite string // https://asurascans.com
	FlameSite string // https://flamecomics.xyz
	FlameCDN  string // https://cdn.flamecomics.xyz
}

var defaultSeriesURLs = seriesURLs{
	AsuraAPI:  "https://api.asurascans.com",
	AsuraSite: "https://asurascans.com",
	FlameSite: "https://flamecomics.xyz",
	FlameCDN:  "https://cdn.flamecomics.xyz",
}

var seriesHosts = []string{"asurascans.com", "flamecomics.xyz"}

// numberLabel escribe un número de capítulo sin decimales sobrantes.
func numberLabel(n float32) string { return strconv.FormatFloat(float64(n), 'f', -1, 32) }

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// --- Asura Scans (API JSON pública) ---

type asura struct {
	c    *catalog
	urls seriesURLs
}

var asuraSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,149}$`)

func (a asura) validID(id string) bool { return asuraSlug.MatchString(id) }

type asuraSeries struct {
	Slug         string     `json:"slug"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	Cover        string     `json:"cover"`
	Status       string     `json:"status"`
	Type         string     `json:"type"`
	Author       string     `json:"author"`
	Artist       string     `json:"artist"`
	ChapterCount int        `json:"chapter_count"`
	LastChapter  *time.Time `json:"last_chapter_at"`
	PublicURL    string     `json:"public_url"`
	Genres       []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// asuraPage son los resultados por página de la API de Asura, que pagina
// con offset (el parámetro page lo ignora).
const asuraPage = 20

func (a asura) search(ctx context.Context, sq seriesQuery) ([]seriesItem, bool, error) {
	v := url.Values{"offset": {strconv.Itoa((sq.Page - 1) * asuraPage)}}
	if sq.Q != "" {
		v.Set("search", sq.Q)
	}
	if sq.Sort != "" {
		v.Set("sort", sq.Sort)
	}
	if sq.Status != "" {
		v.Set("status", sq.Status)
	}
	var res struct {
		Data []asuraSeries `json:"data"`
		Meta struct {
			HasMore bool `json:"has_more"`
		} `json:"meta"`
	}
	if err := a.c.getJSON(ctx, a.urls.AsuraAPI+"/api/series?"+v.Encode(), &res); err != nil {
		return nil, false, err
	}
	items := []seriesItem{}
	for _, s := range res.Data {
		if !a.validID(s.Slug) || s.Title == "" {
			continue
		}
		items = append(items, seriesItem{Source: "asura", ID: s.Slug, Title: s.Title, Status: s.Status, Kind: s.Type,
			Chapters: s.ChapterCount, LatestAt: s.LastChapter, HasCover: s.Cover != "", coverURL: s.Cover})
	}
	return items, res.Meta.HasMore, nil
}

func (a asura) fetch(ctx context.Context, id string) (seriesData, error) {
	if !a.validID(id) {
		return seriesData{}, bad("serie no válida")
	}
	var detail struct {
		Series asuraSeries `json:"series"`
	}
	if err := a.c.getJSON(ctx, a.urls.AsuraAPI+"/api/series/"+id, &detail); err != nil {
		return seriesData{}, err
	}
	var chapters struct {
		Data []struct {
			Number    float32    `json:"number"`
			Title     string     `json:"title"`
			Published *time.Time `json:"published_at"`
			Premium   bool       `json:"is_premium"`
			Early     *time.Time `json:"early_access_until"`
		} `json:"data"`
	}
	if err := a.c.getJSON(ctx, a.urls.AsuraAPI+"/api/series/"+id+"/chapters", &chapters); err != nil {
		return seriesData{}, err
	}
	s := detail.Series
	if s.Title == "" || !strings.HasPrefix(s.PublicURL, "/comics/") {
		return seriesData{}, errUpstream
	}
	d := seriesData{Title: s.Title, URL: a.urls.AsuraSite + s.PublicURL, CoverURL: s.Cover, Status: s.Status, Kind: s.Type,
		Description: stripTags(s.Description)}
	for _, p := range []string{s.Author, s.Artist} {
		if p = strings.TrimSpace(p); p != "" && p != "_" {
			d.Authors = append(d.Authors, titleCase(p))
		}
	}
	for _, g := range s.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	now := time.Now()
	for _, ch := range chapters.Data {
		// Los de acceso anticipado (de pago) no cuentan hasta que son para
		// todos (decisión del usuario): entonces llegan como nuevos y avisan.
		// is_premium pasa a false al liberarse; early_access_until es la hora.
		if ch.Premium && (ch.Early == nil || ch.Early.After(now)) {
			continue
		}
		d.Chapters = append(d.Chapters, chapterData{Number: ch.Number, Title: ch.Title, URL: d.URL + "/chapter/" + numberLabel(ch.Number), PublishedAt: ch.Published})
	}
	return d, nil
}

// --- Flame Comics (datos JSON de su web Next.js) ---

type flame struct {
	c    *catalog
	urls seriesURLs

	mu      sync.Mutex
	build   string
	buildAt time.Time
	list    []flameSeries
	listAt  time.Time
}

// flexString acepta un número o una cadena (Flame mezcla ambos).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return nil
	}
	*f = flexString(n.String())
	return nil
}

type flameSeries struct {
	ID          flexString   `json:"series_id"`
	Title       string       `json:"title"`
	AltTitles   stringOrList `json:"altTitles"`
	Description string       `json:"description"`
	Type        string       `json:"type"`
	Categories  stringOrList `json:"categories"`
	Tags        stringOrList `json:"tags"`
	Author      stringOrList `json:"author"`
	Artist      stringOrList `json:"artist"`
	Status      string       `json:"status"`
	Cover       string       `json:"cover"`
	Rank        flexString   `json:"popularityRank"`
	LastEdit    flexString   `json:"last_edit"`
}

var (
	flameBuildID = regexp.MustCompile(`"buildId":"([A-Za-z0-9_-]{1,100})"`)
	flameToken   = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	flameCover   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

func (f *flame) validID(id string) bool { return numericID.MatchString(id) }

// buildID es la versión desplegada de la web, necesaria para sus rutas de
// datos. Cambia con cada despliegue suyo: se vuelve a leer si caduca o falla.
func (f *flame) buildID(ctx context.Context, fresh bool) (string, error) {
	f.mu.Lock()
	if !fresh && f.build != "" && time.Since(f.buildAt) < time.Hour {
		b := f.build
		f.mu.Unlock()
		return b, nil
	}
	f.mu.Unlock()
	resp, err := f.c.get(ctx, f.urls.FlameSite+"/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	m := flameBuildID.FindSubmatch(page)
	if m == nil {
		return "", errUpstream
	}
	f.mu.Lock()
	f.build, f.buildAt = string(m[1]), time.Now()
	f.mu.Unlock()
	return string(m[1]), nil
}

// data pide una ruta de datos de Next.js; si la versión ha cambiado (404),
// lee la nueva y lo intenta otra vez.
func (f *flame) data(ctx context.Context, route string, v any) error {
	for _, fresh := range []bool{false, true} {
		b, err := f.buildID(ctx, fresh)
		if err != nil {
			return err
		}
		err = f.c.getJSON(ctx, f.urls.FlameSite+"/_next/data/"+b+route, v)
		if !errors.Is(err, errNotFound) {
			return err
		}
	}
	return errNotFound
}

func (f *flame) coverURL(id, cover string) string {
	if !flameCover.MatchString(cover) {
		return ""
	}
	return fmt.Sprintf("%s/uploads/images/series/%s/%s", f.urls.FlameCDN, id, cover)
}

// all es el catálogo completo de Flame (unas 170 series), guardado 15 minutos.
func (f *flame) all(ctx context.Context) ([]flameSeries, error) {
	f.mu.Lock()
	if f.list != nil && time.Since(f.listAt) < 15*time.Minute {
		l := f.list
		f.mu.Unlock()
		return l, nil
	}
	f.mu.Unlock()
	var res struct {
		PageProps struct {
			Series []flameSeries `json:"series"`
		} `json:"pageProps"`
	}
	if err := f.data(ctx, "/browse.json", &res); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.list, f.listAt = res.PageProps.Series, time.Now()
	f.mu.Unlock()
	return res.PageProps.Series, nil
}

func (f *flame) search(ctx context.Context, sq seriesQuery) ([]seriesItem, bool, error) {
	list, err := f.all(ctx)
	if err != nil {
		return nil, false, err
	}
	q := strings.ToLower(sq.Q)
	type match struct {
		item     seriesItem
		rank     int
		lastEdit int
	}
	found := []match{}
	for _, s := range list {
		id := string(s.ID)
		if !f.validID(id) || s.Title == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.Title+" "+strings.Join(s.AltTitles, " ")), q) {
			continue
		}
		if sq.Status != "" && !strings.EqualFold(s.Status, sq.Status) {
			continue
		}
		cover := f.coverURL(id, s.Cover)
		rank, _ := strconv.Atoi(string(s.Rank))
		if rank <= 0 {
			rank = 1 << 30
		}
		edit, _ := strconv.Atoi(string(s.LastEdit))
		found = append(found, match{seriesItem{Source: "flame", ID: id, Title: s.Title, Status: s.Status, Kind: s.Type,
			HasCover: cover != "", coverURL: cover}, rank, edit})
	}
	// Flame no ordena por valoración: «valoradas» usa la popularidad.
	switch sq.Sort {
	case "updated":
		sort.SliceStable(found, func(i, j int) bool { return found[i].lastEdit > found[j].lastEdit })
	case "title":
		sort.SliceStable(found, func(i, j int) bool {
			return strings.ToLower(found[i].item.Title) < strings.ToLower(found[j].item.Title)
		})
	case "popular", "rating":
		sort.SliceStable(found, func(i, j int) bool { return found[i].rank < found[j].rank })
	}
	matches := make([]seriesItem, len(found))
	for i, m := range found {
		matches[i] = m.item
	}
	const per = 24
	from := (sq.Page - 1) * per
	if from >= len(matches) {
		return []seriesItem{}, false, nil
	}
	to := min(from+per, len(matches))
	return matches[from:to], to < len(matches), nil
}

func (f *flame) fetch(ctx context.Context, id string) (seriesData, error) {
	if !f.validID(id) {
		return seriesData{}, bad("serie no válida")
	}
	var res struct {
		PageProps struct {
			Series   flameSeries `json:"series"`
			Chapters []struct {
				Chapter flexString `json:"chapter"`
				Title   string     `json:"title"`
				Release int64      `json:"release_date"`
				Token   string     `json:"token"`
			} `json:"chapters"`
		} `json:"pageProps"`
	}
	if err := f.data(ctx, "/series/"+id+".json?id="+id, &res); err != nil {
		return seriesData{}, err
	}
	s := res.PageProps.Series
	if s.Title == "" {
		return seriesData{}, errNotFound
	}
	d := seriesData{Title: s.Title, URL: f.urls.FlameSite + "/series/" + id, CoverURL: f.coverURL(id, s.Cover), Status: s.Status,
		Kind: s.Type, Description: stripTags(s.Description), Authors: append(append([]string{}, s.Author...), s.Artist...)}
	d.Genres = append(d.Genres, s.Tags...)
	if len(d.Genres) == 0 {
		d.Genres = append(d.Genres, s.Categories...)
	}
	for _, ch := range res.PageProps.Chapters {
		n, err := strconv.ParseFloat(string(ch.Chapter), 32)
		if err != nil || !flameToken.MatchString(ch.Token) {
			continue
		}
		cd := chapterData{Number: float32(n), Title: strings.TrimSpace(ch.Title), URL: d.URL + "/" + ch.Token}
		if ch.Release > 0 {
			t := time.Unix(ch.Release, 0).UTC()
			cd.PublishedAt = &t
		}
		d.Chapters = append(d.Chapters, cd)
	}
	return d, nil
}
