package main

import (
	"context"
	"encoding/json"
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

// Búsqueda web (Brave si hay clave; si no, DuckDuckGo) y lectura de páginas.
// El cliente no se conecta a direcciones de la red local ni del propio móvil,
// también tras una redirección: una página no puede llevar al asistente a la
// pasarela, a PostgreSQL ni al router.

type webClient struct {
	http         *http.Client
	braveURL     string // https://api.search.brave.com
	ddgURL       string // https://html.duckduckgo.com
	allowPrivate bool   // solo en los tests
}

type searchResult struct {
	Title   string `json:"titulo"`
	URL     string `json:"url"`
	Snippet string `json:"extracto"`
}

const webUserAgent = "Mozilla/5.0 (compatible; homeserver-asistente/1.0)"

func newWebClient(allowPrivate bool) *webClient {
	w := &webClient{braveURL: "https://api.search.brave.com", ddgURL: "https://html.duckduckgo.com", allowPrivate: allowPrivate}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, _ := net.SplitHostPort(address)
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() ||
				// Rango de Tailscale (CGNAT): ahí están tus otros dispositivos.
				(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) {
				return fmt.Errorf("dirección no permitida: %s", host)
			}
			return nil
		}
	}
	w.http = &http.Client{
		Timeout: 25 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 60 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("demasiadas redirecciones")
			}
			if !w.okScheme(req.URL) {
				return errors.New("redirección a una dirección no permitida")
			}
			return nil
		},
	}
	return w
}

func (w *webClient) okScheme(u *url.URL) bool {
	return u.Scheme == "https" || u.Scheme == "http"
}

func (w *webClient) get(ctx context.Context, rawURL string, header map[string]string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !w.okScheme(u) || u.Host == "" {
		return nil, "", errors.New("dirección no válida")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("la página respondió %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	return b, resp.Header.Get("Content-Type"), err
}

func (w *webClient) search(ctx context.Context, q, braveKey string) ([]searchResult, error) {
	if braveKey != "" {
		res, err := w.brave(ctx, q, braveKey)
		if err == nil {
			return res, nil
		}
		// Si Brave falla (cuota, red), se prueba con DuckDuckGo.
	}
	return w.duckduckgo(ctx, q)
}

func (w *webClient) brave(ctx context.Context, q, key string) ([]searchResult, error) {
	v := url.Values{"q": {q}, "count": {"6"}, "search_lang": {"es"}, "safesearch": {"moderate"}}
	b, _, err := w.get(ctx, w.braveURL+"/res/v1/web/search?"+v.Encode(), map[string]string{"X-Subscription-Token": key, "Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	var res []searchResult
	for _, r := range out.Web.Results {
		res = append(res, searchResult{Title: stripTags(r.Title), URL: r.URL, Snippet: stripTags(r.Description)})
	}
	return res, nil
}

var (
	ddgResult  = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippet = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
)

// duckduckgo usa la versión HTML del buscador (no necesita clave).
func (w *webClient) duckduckgo(ctx context.Context, q string) ([]searchResult, error) {
	b, _, err := w.get(ctx, w.ddgURL+"/html/?"+url.Values{"q": {q}, "kl": {"es-es"}}.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("el buscador no responde: %w", err)
	}
	page := string(b)
	links := ddgResult.FindAllStringSubmatch(page, 8)
	snippets := ddgSnippet.FindAllStringSubmatch(page, 8)
	var res []searchResult
	for i, l := range links {
		href := html.UnescapeString(l[1])
		// Los enlaces van por un redireccionador: //duckduckgo.com/l/?uddg=<destino>
		if u, err := url.Parse(href); err == nil && u.Query().Get("uddg") != "" {
			href = u.Query().Get("uddg")
		}
		if !strings.HasPrefix(href, "http") || strings.Contains(href, "duckduckgo.com/y.js") {
			continue // anuncios
		}
		r := searchResult{Title: stripTags(l[2]), URL: href}
		if i < len(snippets) {
			r.Snippet = stripTags(snippets[i][1])
		}
		res = append(res, r)
	}
	if len(res) == 0 {
		return nil, errors.New("el buscador no ha devuelto resultados (puede que esté limitando las búsquedas)")
	}
	return res, nil
}

var (
	reDrop  = dropTags("script", "style", "noscript", "svg", "nav", "footer", "header", "form", "iframe", "template")
	reBlock = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/h[1-6]|/tr|/section|/article)[^>]*>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reSpace = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines = regexp.MustCompile(`\n\s*\n+`)
)

// dropTags quita esas etiquetas con todo su contenido (RE2 no admite
// referencias hacia atrás: una expresión por etiqueta).
func dropTags(tags ...string) []*regexp.Regexp {
	var res []*regexp.Regexp
	for _, t := range tags {
		res = append(res, regexp.MustCompile(`(?is)<`+t+`\b.*?</\s*`+t+`\s*>`))
	}
	return res
}

func stripTags(s string) string {
	return strings.TrimSpace(reSpace.ReplaceAllString(html.UnescapeString(reTag.ReplaceAllString(s, "")), " "))
}

// read descarga una página y devuelve su título y su texto (recortado).
func (w *webClient) read(ctx context.Context, rawURL string) (string, string, error) {
	b, ct, err := w.get(ctx, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(ct, "text/plain") || strings.Contains(ct, "json") {
		return "", truncateRunes(string(b), 15000), nil
	}
	if !strings.Contains(ct, "html") && ct != "" {
		return "", "", fmt.Errorf("no es una página de texto (%s)", ct)
	}
	page := string(b)
	title := ""
	if m := reTitle.FindStringSubmatch(page); m != nil {
		title = stripTags(m[1])
	}
	for _, re := range reDrop {
		page = re.ReplaceAllString(page, " ")
	}
	page = reBlock.ReplaceAllString(page, "\n")
	page = html.UnescapeString(reTag.ReplaceAllString(page, " "))
	page = reSpace.ReplaceAllString(page, " ")
	page = reLines.ReplaceAllString(page, "\n\n")
	return title, truncateRunes(strings.TrimSpace(page), 15000), nil
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…(recortado)"
}
