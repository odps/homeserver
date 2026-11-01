package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Lectura de feeds RSS 2.0 y Atom. Los feeds los puede añadir cualquier
// cuenta: el cliente solo habla HTTPS (también tras redirecciones) y nunca con
// direcciones de la red local, del propio móvil o de Tailscale.

type fetcher struct {
	http      *http.Client
	userAgent string
	allowHTTP bool // solo en los tests: servidores locales por HTTP
}

func newFetcher(allowPrivate bool) *fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, _ := net.SplitHostPort(address)
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() ||
				(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) { // CGNAT: Tailscale
				return fmt.Errorf("dirección no permitida: %s", host)
			}
			return nil
		}
	}
	f := &fetcher{userAgent: "homeserver-noticias/1.0 (lector RSS personal)", allowHTTP: allowPrivate}
	f.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("demasiadas redirecciones")
			}
			if req.URL.Scheme != "https" && !allowPrivate {
				return errors.New("redirección a una dirección no segura")
			}
			return nil
		},
	}
	return f
}

// validFeedURL comprueba una URL que escribe una persona.
func validFeedURL(raw string, allowHTTP bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return "", errors.New("la dirección del feed debe empezar por https://")
	}
	u.Fragment = ""
	return u.String(), nil
}

type feedItem struct {
	Source    string
	Link      string
	Title     string
	Summary   string
	Published time.Time
}

type parsedFeed struct {
	Title string
	Items []feedItem
}

// Estructuras de RSS 2.0 y Atom (y lo que añade Google News).
type rssDoc struct {
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Date        string `xml:"http://purl.org/dc/elements/1.1/ date"`
	Source      string `xml:"source"`
	// Google Tendencias: cuánto se busca y las noticias relacionadas.
	Traffic string `xml:"approx_traffic"`
	News    []struct {
		Title  string `xml:"news_item_title"`
		URL    string `xml:"news_item_url"`
		Source string `xml:"news_item_source"`
	} `xml:"news_item"`
}

type atomDoc struct {
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title string `xml:"title"`
	Links []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
	Summary   string `xml:"summary"`
	Content   string `xml:"content"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
}

func (f *fetcher) fetch(ctx context.Context, feedURL string) (parsedFeed, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return parsedFeed{}, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	resp, err := f.http.Do(req)
	if err != nil {
		return parsedFeed{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parsedFeed{}, fmt.Errorf("respondió %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return parsedFeed{}, err
	}
	if t := strings.TrimSpace(string(data)); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return parseJSONFeed(feedURL, data, time.Now())
	}
	return parseFeed(data, time.Now())
}

// parseJSONFeed entiende las dos fuentes de redes que no dan RSS: los enlaces
// en tendencia de Mastodon y los temas del momento de Bluesky.
func parseJSONFeed(feedURL string, data []byte, now time.Time) (parsedFeed, error) {
	switch {
	case strings.Contains(feedURL, "/api/v1/trends/links"):
		var links []struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Provider    string `json:"provider_name"`
		}
		if err := json.Unmarshal(data, &links); err != nil {
			return parsedFeed{}, err
		}
		out := parsedFeed{Title: "Mastodon"}
		for _, l := range links {
			src := cleanText(l.Provider, 100)
			if src == "" {
				src = "Mastodon"
			}
			out.Items = append(out.Items, feedItem{Source: src, Link: l.URL, Title: cleanText(l.Title, 300), Summary: summarize(l.Description), Published: now})
		}
		return out.valid(), nil
	case strings.Contains(feedURL, "getTrendingTopics"):
		var tt struct {
			Topics []struct {
				Topic       string `json:"topic"`
				DisplayName string `json:"displayName"`
				Link        string `json:"link"`
			} `json:"topics"`
		}
		if err := json.Unmarshal(data, &tt); err != nil {
			return parsedFeed{}, err
		}
		out := parsedFeed{Title: "Bluesky"}
		for _, t := range tt.Topics {
			name := firstNonEmpty(t.DisplayName, t.Topic)
			out.Items = append(out.Items, feedItem{Source: "Bluesky", Link: "https://bsky.app" + t.Link, Title: cleanText(name, 200), Published: now})
		}
		return out.valid(), nil
	}
	return parsedFeed{}, errors.New("formato no reconocido")
}

func newDecoder(data []byte) *xml.Decoder {
	d := xml.NewDecoder(strings.NewReader(string(data)))
	d.Strict = false
	// Sin AutoClose: en HTML <link> no tiene contenido y en RSS es el enlace.
	d.Entity = xml.HTMLEntity
	// Algunos medios publican en ISO-8859-1 / Windows-1252: se pasa a UTF-8
	// (los demás juegos de caracteres se leen tal cual).
	d.CharsetReader = func(charset string, r io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "iso-8859-1", "iso-8859-15", "latin1", "windows-1252", "cp1252":
			b, err := io.ReadAll(r)
			if err != nil {
				return nil, err
			}
			runes := make([]rune, len(b))
			for i, c := range b {
				runes[i] = rune(c)
			}
			return strings.NewReader(string(runes)), nil
		}
		return r, nil
	}
	return d
}

func parseFeed(data []byte, now time.Time) (parsedFeed, error) {
	var rss rssDoc
	if err := newDecoder(data).Decode(&rss); err == nil && len(rss.Channel.Items) > 0 {
		out := parsedFeed{Title: cleanText(rss.Channel.Title, 200)}
		for _, it := range rss.Channel.Items {
			link := strings.TrimSpace(it.Link)
			if link == "" && strings.HasPrefix(it.GUID, "http") {
				link = strings.TrimSpace(it.GUID)
			}
			title, source := cleanText(it.Title, 300), cleanText(it.Source, 100)
			// Google News pone el medio al final del título: «Titular - El País».
			if source != "" && strings.HasSuffix(title, " - "+source) {
				title = strings.TrimSuffix(title, " - "+source)
			}
			fi := feedItem{Source: source, Link: link, Title: title,
				Summary: summarize(it.Description), Published: parseDate(firstNonEmpty(it.PubDate, it.Date), now)}
			if len(it.News) > 0 { // Google Tendencias: el término y la noticia que lo explica
				n := it.News[0]
				fi.Summary = cleanText(n.Title, 300)
				if t := cleanText(it.Traffic, 20); t != "" {
					fi.Summary = t + " búsquedas · " + fi.Summary
				}
				if strings.HasPrefix(n.URL, "http") {
					fi.Link = n.URL
				}
				fi.Source = firstNonEmpty(cleanText(n.Source, 100), "Google Tendencias")
			}
			out.Items = append(out.Items, fi)
		}
		return out.valid(), nil
	}
	var atom atomDoc
	if err := newDecoder(data).Decode(&atom); err == nil && len(atom.Entries) > 0 {
		out := parsedFeed{Title: cleanText(atom.Title, 200)}
		for _, e := range atom.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			out.Items = append(out.Items, feedItem{Link: strings.TrimSpace(link), Title: cleanText(e.Title, 300),
				Summary: summarize(firstNonEmpty(e.Summary, e.Content)), Published: parseDate(firstNonEmpty(e.Published, e.Updated), now)})
		}
		return out.valid(), nil
	}
	return parsedFeed{}, errors.New("no es un feed RSS o Atom (o está vacío)")
}

// valid deja solo las noticias con título y enlace web. Una entradilla que
// solo repite el titular (Google News pone el titular y el medio) se quita.
func (p parsedFeed) valid() parsedFeed {
	var items []feedItem
	for _, it := range p.Items {
		if strings.HasPrefix(it.Summary, it.Title) && len([]rune(it.Summary)) < len([]rune(it.Title))+60 {
			it.Summary = ""
		}
		it.Summary = socialSummary(it.Summary)
		if strings.HasPrefix(p.Title, "Hacker News") {
			it.Source = "Hacker News"
		}
		if it.Title != "" && (strings.HasPrefix(it.Link, "https://") || strings.HasPrefix(it.Link, "http://")) {
			items = append(items, it)
		}
	}
	p.Items = items
	return p
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var dateLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 -0700", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"}

// parseDate entiende los formatos habituales; sin fecha (o con una del
// futuro) se toma la de ahora.
func parseDate(s string, now time.Time) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			if t.After(now.Add(time.Hour)) {
				return now
			}
			return t
		}
	}
	return now
}

var (
	reTags   = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces = regexp.MustCompile(`\s+`)
)

func cleanText(s string, max int) string {
	s = html.UnescapeString(reTags.ReplaceAllString(strings.ToValidUTF8(s, ""), " "))
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}

var (
	reHNStats   = regexp.MustCompile(`Points: (\d+).*Comments: (\d+)`)
	reRedditTag = regexp.MustCompile(`(?s)\s*submitted by\s+/u/\S+.*$`)
)

// socialSummary arregla las entradillas de Hacker News («Article URL: …
// Points: 51 # Comments: 22» → «51 puntos · 22 comentarios») y quita el pie
// que Reddit pone a cada entrada.
func socialSummary(s string) string {
	if m := reHNStats.FindStringSubmatch(s); m != nil && strings.Contains(s, "Comments URL:") {
		return m[1] + " puntos · " + m[2] + " comentarios"
	}
	return strings.TrimSpace(reRedditTag.ReplaceAllString(s, ""))
}

// summarize deja un extracto corto: el feed puede traer el artículo entero,
// pero el digest solo enseña una entradilla y enlaza al medio.
func summarize(s string) string { return cleanText(s, 320) }
