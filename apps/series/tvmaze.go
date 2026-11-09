package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	_ "image/png" // algunos carteles vienen en PNG
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TVmaze (api.tvmaze.com) es la fuente de las series: pública, sin clave ni
// cuenta, con datos CC BY-SA (por eso se cita en cada página). A ella solo va
// lo que se busca o el id de la serie que se sigue, nunca la cuenta. Su
// límite es de unas 20 peticiones cada 10 segundos: aquí se deja una pausa
// entre peticiones y, si se queja (429), se espera.
//
// Los carteles (static.tvmaze.com) los pide el servidor, los vuelve a
// codificar y los guarda: el navegador nunca se conecta a TVmaze.
//
// Solo se conecta por HTTPS a esos dos dominios (y nunca a la red local).

// tvURLs son la API y los carteles: los dos únicos sitios a los que se
// conecta (en los tests, un servidor local).
type tvURLs struct {
	API, Images string
}

var defaultURLs = tvURLs{API: "https://api.tvmaze.com", Images: "https://static.tvmaze.com"}

const userAgent = "homeserver-series/1.0 (seguimiento personal de series; +https://github.com/odps/homeserver)"

const (
	maxJSON   = 8 << 20
	maxImage  = 2 << 20
	posterQ   = 82
	posterMax = 400 // lado largo del cartel guardado, en píxeles
)

var (
	errSourceWait = errors.New("TVmaze no responde ahora; se volverá a intentar más tarde")
	errSourceBad  = errors.New("TVmaze ha respondido algo que no se entiende")
	errNoMatch    = errors.New("no se ha encontrado")
)

type tvmaze struct {
	urls      tvURLs
	http      *http.Client
	allowHTTP bool          // solo en los tests: servidores locales por HTTP
	gap       time.Duration // pausa mínima entre peticiones (0 en los tests)
	now       func() time.Time

	mu     sync.Mutex
	last   time.Time // última petición
	until  time.Time // no se pide nada hasta entonces (espera tras errores)
	fails  int
	okAt   time.Time
	errMsg string

	slot sync.Mutex // de una en una, para respetar el límite
}

func newTVmaze(urls tvURLs, allowPrivate bool) *tvmaze {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	t := &tvmaze{urls: urls, allowHTTP: allowPrivate, gap: 600 * time.Millisecond, now: time.Now}
	if !allowPrivate {
		// Aunque un dominio permitido resolviera a una dirección interna, no
		// se conecta a la red local ni al propio móvil.
		dialer.Control = func(_, address string, _ syscall.RawConn) error { return dialCheck(address) }
	}
	t.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("demasiadas redirecciones")
			}
			if !t.allowed(req.URL) {
				return errors.New("redirección a un sitio no permitido")
			}
			return nil
		},
	}
	return t
}

// dialCheck rechaza las direcciones de la red local, del propio móvil y de
// Tailscale.
func dialCheck(address string) error {
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) { // CGNAT: Tailscale
		return fmt.Errorf("dirección no permitida: %s", host)
	}
	return nil
}

// allowed dice si se puede pedir esa URL: solo HTTPS (HTTP en los tests) y
// solo a la API o a los carteles.
func (t *tvmaze) allowed(u *url.URL) bool {
	return t.isHost(u, t.urls.API) || t.isHost(u, t.urls.Images)
}

func (t *tvmaze) isHost(u *url.URL, base string) bool {
	b, err := url.Parse(base)
	if err != nil || u.User != nil || u.Scheme != b.Scheme || !strings.EqualFold(u.Host, b.Host) {
		return false
	}
	return u.Scheme == "https" || (t.allowHTTP && u.Scheme == "http")
}

// ready dice si se le puede pedir algo ahora.
func (t *tvmaze) ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.now().Before(t.until)
}

func (t *tvmaze) ok() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.okAt, t.errMsg, t.until, t.fails = t.now(), "", time.Time{}, 0
}

// fail apunta un error y deja a TVmaze en espera: más larga si se ha quejado
// del límite y creciente con los fallos seguidos.
func (t *tvmaze) fail(msg string, limited bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fails++
	t.errMsg = msg
	base := time.Minute
	if limited {
		base = 5 * time.Minute
	}
	t.until = t.now().Add(min(base<<min(t.fails-1, 5), 6*time.Hour))
}

// status es cómo va TVmaze, sin detalles internos.
func (t *tvmaze) status() (okAt time.Time, msg string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.okAt, t.errMsg, t.until
}

// wait respeta la pausa entre peticiones. Se llama con slot cogido.
func (t *tvmaze) wait(ctx context.Context) error {
	if t.gap <= 0 {
		return nil
	}
	t.mu.Lock()
	d := t.gap - time.Since(t.last)
	t.mu.Unlock()
	if d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

// get hace una petición a TVmaze (o a sus carteles) y devuelve el cuerpo, con
// límite de tamaño. Un 404 es «no existe» y no cuenta como fallo.
func (t *tvmaze) get(ctx context.Context, raw, accept string, limit int64) ([]byte, string, error) {
	if !t.ready() {
		return nil, "", errSourceWait
	}
	u, err := url.Parse(raw)
	if err != nil || !t.allowed(u) {
		return nil, "", errors.New("sitio no permitido")
	}
	t.slot.Lock()
	defer t.slot.Unlock()
	if err := t.wait(ctx); err != nil {
		return nil, "", err
	}
	defer func() {
		t.mu.Lock()
		t.last = time.Now()
		t.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", raw, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	resp, err := t.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, "", err // se ha cancelado desde fuera: no es culpa de TVmaze
		}
		t.fail("no responde", false)
		return nil, "", errSourceWait
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		t.ok()
		return nil, "", errNoMatch
	case resp.StatusCode == http.StatusTooManyRequests:
		t.fail("límite de peticiones: se espera antes de volver a pedir", true)
		return nil, "", errSourceWait
	case resp.StatusCode != http.StatusOK:
		t.fail("error "+strconv.Itoa(resp.StatusCode), false)
		return nil, "", errSourceWait
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		t.fail("respuesta no válida", false)
		return nil, "", errSourceBad
	}
	t.ok()
	return b, resp.Header.Get("Content-Type"), nil
}

func (t *tvmaze) getJSON(ctx context.Context, path string, v any) error {
	b, _, err := t.get(ctx, t.urls.API+path, "application/json", maxJSON)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.fail("respuesta no válida", false)
		return errSourceBad
	}
	return nil
}

// ---- Formato de TVmaze ----

type tvImage struct {
	Medium   string `json:"medium"`
	Original string `json:"original"`
}

type tvChannel struct {
	Name string `json:"name"`
}

type tvShow struct {
	ID             int64      `json:"id"`
	URL            string     `json:"url"`
	Name           string     `json:"name"`
	Type           string     `json:"type"`
	Language       string     `json:"language"`
	Genres         []string   `json:"genres"`
	Status         string     `json:"status"`
	Runtime        *int       `json:"runtime"`
	AverageRuntime *int       `json:"averageRuntime"`
	Premiered      string     `json:"premiered"`
	Ended          string     `json:"ended"`
	Network        *tvChannel `json:"network"`
	WebChannel     *tvChannel `json:"webChannel"`
	Image          *tvImage   `json:"image"`
	Summary        string     `json:"summary"`
	Updated        int64      `json:"updated"`
	Embedded       *struct {
		Episodes []tvEpisode `json:"episodes"`
	} `json:"_embedded"`
}

type tvEpisode struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Season   int    `json:"season"`
	Number   *int   `json:"number"`
	Type     string `json:"type"`
	Airdate  string `json:"airdate"`
	Airtime  string `json:"airtime"`
	Airstamp string `json:"airstamp"`
	Runtime  *int   `json:"runtime"`
}

// ---- Lo que se guarda ----

// episode es un capítulo, con las claves cortas: una serie larga tiene cientos.
type episode struct {
	ID      int64     `json:"i"`
	Season  int       `json:"s"`
	Number  int       `json:"n"`
	Name    string    `json:"t,omitempty"`
	Date    string    `json:"d,omitempty"` // fecha de emisión (AAAA-MM-DD), si se sabe
	At      time.Time `json:"a,omitzero"`  // instante de emisión, solo si TVmaze da la hora
	Runtime int       `json:"r,omitempty"`
}

// showData es una serie de TVmaze con sus capítulos (sin especiales), en
// orden de temporada y número.
type showData struct {
	TVmazeID  int64     `json:"tvmaze"`
	Name      string    `json:"nombre"`
	URL       string    `json:"url,omitempty"` // su página en TVmaze, para citarla
	Status    string    `json:"estado,omitempty"`
	Network   string    `json:"cadena,omitempty"`
	Language  string    `json:"idioma,omitempty"`
	Genres    []string  `json:"generos,omitempty"`
	Premiered string    `json:"estreno,omitempty"`
	Ended     string    `json:"fin,omitempty"`
	Runtime   int       `json:"duracion,omitempty"`
	Summary   string    `json:"resumen,omitempty"`
	Image     string    `json:"cartel,omitempty"`
	Updated   int64     `json:"actualizada"`
	Episodes  []episode `json:"capitulos"`
}

// found es una serie encontrada al buscar.
type found struct {
	TVmazeID  int64
	Name      string
	Year      string
	Status    string
	Network   string
	Genres    []string
	Language  string
	HasPoster bool
}

var (
	reTag   = regexp.MustCompile(`<[^>]*>`)
	reSpace = regexp.MustCompile(`\s+`)
	reDate  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reClock = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// plainText quita el HTML de los resúmenes de TVmaze.
func plainText(s string, n int) string {
	s = reTag.ReplaceAllString(strings.ReplaceAll(s, "</p>", " "), "")
	return clip(reSpace.ReplaceAllString(html.UnescapeString(s), " "), n)
}

func channelName(sh tvShow) string {
	switch {
	case sh.Network != nil && sh.Network.Name != "":
		return clip(sh.Network.Name, 60)
	case sh.WebChannel != nil && sh.WebChannel.Name != "":
		return clip(sh.WebChannel.Name, 60)
	}
	return ""
}

func validDate(s string) string {
	if reDate.MatchString(s) {
		if _, err := parseDay(s); err == nil {
			return s
		}
	}
	return ""
}

// tvmazeLink solo deja enlaces a la web de TVmaze.
func tvmazeLink(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host != "www.tvmaze.com" || !strings.HasPrefix(u.Path, "/shows/") {
		return ""
	}
	return u.String()
}

// convert pasa una serie de TVmaze a lo que se guarda, validando cada campo.
func convert(sh tvShow) (showData, error) {
	if sh.ID <= 0 || strings.TrimSpace(sh.Name) == "" {
		return showData{}, errSourceBad
	}
	d := showData{TVmazeID: sh.ID, Name: clip(sh.Name, 120), URL: tvmazeLink(sh.URL), Status: clip(sh.Status, 30), Network: channelName(sh),
		Language: clip(sh.Language, 30), Premiered: validDate(sh.Premiered), Ended: validDate(sh.Ended),
		Summary: plainText(sh.Summary, 700), Updated: sh.Updated, Episodes: []episode{}}
	for _, g := range sh.Genres {
		if len(d.Genres) < 6 {
			d.Genres = append(d.Genres, clip(g, 30))
		}
	}
	switch {
	case sh.Runtime != nil && *sh.Runtime > 0:
		d.Runtime = *sh.Runtime
	case sh.AverageRuntime != nil && *sh.AverageRuntime > 0:
		d.Runtime = *sh.AverageRuntime
	}
	if sh.Image != nil {
		d.Image = sh.Image.Medium
	}
	if sh.Embedded != nil {
		seen := map[int64]bool{}
		for _, e := range sh.Embedded.Episodes {
			// Sin número (especiales) no entran en el orden de la serie.
			if e.ID <= 0 || e.Number == nil || *e.Number <= 0 || e.Season <= 0 || seen[e.ID] || e.Type == "significant_special" || e.Type == "insignificant_special" {
				continue
			}
			seen[e.ID] = true
			ep := episode{ID: e.ID, Season: e.Season, Number: *e.Number, Name: clip(e.Name, 120), Date: validDate(e.Airdate)}
			if e.Runtime != nil && *e.Runtime > 0 {
				ep.Runtime = *e.Runtime
			}
			// Solo con hora se toma el instante: sin ella (las plataformas),
			// TVmaze pone uno inventado y vale la fecha.
			if ep.Date != "" && reClock.MatchString(e.Airtime) {
				if at, err := time.Parse(time.RFC3339, e.Airstamp); err == nil {
					ep.At = at.UTC()
				}
			}
			d.Episodes = append(d.Episodes, ep)
			if len(d.Episodes) >= 3000 {
				break
			}
		}
	}
	sort.SliceStable(d.Episodes, func(i, j int) bool { return epLess(d.Episodes[i], d.Episodes[j]) })
	return d, nil
}

// search busca series por nombre (como mucho, las 10 que da TVmaze).
func (t *tvmaze) search(ctx context.Context, q string) ([]found, error) {
	var raw []struct {
		Show tvShow `json:"show"`
	}
	if err := t.getJSON(ctx, "/search/shows?q="+url.QueryEscape(q), &raw); err != nil {
		if errors.Is(err, errNoMatch) {
			return nil, nil
		}
		return nil, err
	}
	var out []found
	for _, r := range raw {
		sh := r.Show
		if sh.ID <= 0 || strings.TrimSpace(sh.Name) == "" {
			continue
		}
		f := found{TVmazeID: sh.ID, Name: clip(sh.Name, 120), Status: clip(sh.Status, 30), Network: channelName(sh), Language: clip(sh.Language, 30),
			HasPoster: sh.Image != nil && sh.Image.Medium != ""}
		if p := validDate(sh.Premiered); p != "" {
			f.Year = p[:4]
		}
		for _, g := range sh.Genres {
			if len(f.Genres) < 3 {
				f.Genres = append(f.Genres, clip(g, 30))
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// show pide una serie con todos sus capítulos (sin especiales).
func (t *tvmaze) show(ctx context.Context, id int64) (showData, error) {
	if id <= 0 {
		return showData{}, errNoMatch
	}
	var sh tvShow
	if err := t.getJSON(ctx, "/shows/"+strconv.FormatInt(id, 10)+"?embed=episodes", &sh); err != nil {
		return showData{}, err
	}
	if sh.ID != id {
		return showData{}, errSourceBad
	}
	return convert(sh)
}

// updates son las series de TVmaze que han cambiado en el periodo (day,
// week o month), con la hora de su último cambio.
func (t *tvmaze) updates(ctx context.Context, since string) (map[int64]int64, error) {
	var raw map[string]int64
	if err := t.getJSON(ctx, "/updates/shows?since="+url.QueryEscape(since), &raw); err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(raw))
	for k, v := range raw {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil && id > 0 {
			out[id] = v
		}
	}
	return out, nil
}

// poster descarga un cartel y lo vuelve a codificar en JPEG (como mucho de
// posterMax píxeles): así solo se sirve una imagen hecha aquí, nunca los bytes
// tal cual llegan.
func (t *tvmaze) poster(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || !t.isHost(u, t.urls.Images) {
		return nil, errors.New("cartel no permitido")
	}
	b, ctype, err := t.get(ctx, raw, "image/jpeg, image/png", maxImage)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(ctype, "image/jpeg") && !strings.HasPrefix(ctype, "image/png") {
		return nil, errSourceBad
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 6000 || cfg.Height > 6000 {
		return nil, errSourceBad
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, errSourceBad
	}
	img = shrink(img, posterMax)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: posterQ}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// shrink reduce la imagen a como mucho side píxeles de lado largo (por
// vecino más cercano: basta para un cartel pequeño).
func shrink(src image.Image, side int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return src
	}
	dw, dh := side, h*side/w
	if h > w {
		dw, dh = w*side/h, side
	}
	dw, dh = max(dw, 1), max(dh, 1)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*w/dw, b.Min.Y+y*h/dh))
		}
	}
	return dst
}
