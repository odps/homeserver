package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Descarga de páginas. Las direcciones las escribe la persona o se las pasa
// Talia, así que nada de fiarse:
//   - solo se habla HTTPS (un enlace http:// se pide por https://), también
//     tras las redirecciones, y nunca se desactiva la verificación del
//     certificado;
//   - nunca con la red local, el propio móvil, Tailscale ni direcciones
//     especiales: se comprueba la IP a la que de verdad se conecta (después
//     de resolver el nombre y en cada redirección);
//   - solo los puertos de siempre, un máximo de 5 MB, 25 segundos y 5
//     redirecciones;
//   - sin cookies, sin ejecutar nada y con un User-Agent propio y honesto.

const (
	maxPage      = 5 << 20
	maxRedirects = 5
	maxURL       = 2048
	userAgent    = "homeserver-leer/1.0 (leer después, uso personal; +https://github.com/odps/homeserver)"
)

// fetchError es un fallo que se le puede contar a la persona tal cual (sin
// la dirección). Los permanentes no se reintentan solos.
type fetchError struct {
	msg       string
	permanent bool
}

func (e *fetchError) Error() string { return e.msg }

var (
	errBadURL   = &fetchError{"La dirección debe ser de una página web: https://… (o http://…).", true}
	errBlocked  = &fetchError{"Esa dirección es de la red local o del propio servidor: no se descarga.", true}
	errPort     = &fetchError{"Solo se descargan páginas de los puertos normales de la web.", true}
	errTooBig   = &fetchError{"La página es demasiado grande (más de 5 MB).", true}
	errNotHTML  = &fetchError{"No es una página web: es un PDF, una imagen u otro tipo de archivo.", true}
	errNoText   = &fetchError{"No se ha encontrado el texto del artículo: quizá la página necesita JavaScript o pide suscripción.", true}
	errNoHTTPS  = &fetchError{"La página no se puede abrir por HTTPS (conexión segura).", true}
	errTooMany  = &fetchError{"La página redirige demasiadas veces.", true}
	errTimeout  = &fetchError{"La página no responde.", false}
	errNoAnswer = &fetchError{"No se ha podido conectar con la página.", false}
)

func statusError(code int) *fetchError {
	// 408 y 429 son pasajeros, y los 5xx suelen serlo.
	permanent := code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
	return &fetchError{"La página respondió con un error (" + strconv.Itoa(code) + ").", permanent}
}

// asFetchError da un fetchError para cualquier error de la descarga.
func asFetchError(err error) *fetchError {
	var fe *fetchError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return fe
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return errTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errTimeout
	}
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) {
		return errNoHTTPS
	}
	return errNoAnswer
}

// fetcher descarga páginas con esas reglas. allowURL, allowDial y fakeDNS
// son solo para los tests (servidores locales): direcciones host:puerto que
// se dejan pasar a pesar de ser locales, y nombres que «resuelven» a una IP
// sin preguntar al DNS.
type fetcher struct {
	http      *http.Client
	allowURL  map[string]bool
	allowDial map[string]bool
	fakeDNS   map[string]string
}

func newFetcher() *fetcher { return newFetcherWith(nil, 25*time.Second) }

func newFetcherWith(roots *x509.CertPool, timeout time.Duration) *fetcher {
	f := &fetcher{allowURL: map[string]bool{}, allowDial: map[string]bool{}, fakeDNS: map[string]string{}}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error { return f.dialCheck(address) }}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, port, err := net.SplitHostPort(address); err == nil && f.fakeDNS[host] != "" {
			address = net.JoinHostPort(f.fakeDNS[host], port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            dial,
		TLSClientConfig:        &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  20 * time.Second,
		MaxIdleConns:           4,
		IdleConnTimeout:        60 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	f.http = &http.Client{
		Timeout:   timeout,
		Transport: httpsOnly{tr},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errTooMany
			}
			return f.target(req.URL)
		},
	}
	return f
}

// httpsOnly no deja salir nada que no sea HTTPS.
type httpsOnly struct{ rt http.RoundTripper }

func (h httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, errNoHTTPS
	}
	return h.rt.RoundTrip(r)
}

// Rangos a los que nunca se conecta, además de los que ya marca netip
// (privados, loopback, enlace local, multicast).
var blockedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // «esta red»
		"100.64.0.0/10",   // CGNAT: Tailscale y otros dispositivos
		"192.0.0.0/24",    // asignaciones especiales
		"192.0.2.0/24",    // documentación
		"198.18.0.0/15",   // pruebas de red
		"198.51.100.0/24", // documentación
		"203.0.113.0/24",  // documentación
		"240.0.0.0/4",     // reservada (y difusión)
		"64:ff9b::/96",    // NAT64: llevaría a cualquier IPv4
		"64:ff9b:1::/48",
		"2001:db8::/32", // documentación
		"2002::/16",     // 6to4
		"fec0::/10",     // site-local antigua
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// blockedIP dice si una IP es de la red local, del propio equipo o especial.
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// dialCheck mira la IP a la que se va a conectar de verdad, ya resuelto el
// nombre: así no vale un dominio que apunte a la red local.
func (f *fetcher) dialCheck(address string) error {
	if f.allowDial[address] {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil || blockedIP(ap.Addr()) {
		return errBlocked
	}
	return nil
}

// Nombres que solo existen en la red local.
var localSuffixes = []string{".localhost", ".local", ".lan", ".home", ".internal", ".intranet", ".corp", ".home.arpa", ".onion", ".ts.net"}

// target comprueba una dirección antes de pedirla (también las de las
// redirecciones) y pasa http:// a https://.
func (f *fetcher) target(u *url.URL) error {
	if u.Scheme == "http" {
		u.Scheme = "https"
		if u.Port() == "80" {
			u.Host = strings.TrimSuffix(u.Host, ":80")
		}
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return errBadURL
	}
	if f.allowURL[u.Host] {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		if blockedIP(ip) {
			return errBlocked
		}
	} else {
		if host == "localhost" || !strings.Contains(host, ".") {
			return errBlocked
		}
		for _, s := range localSuffixes {
			if strings.HasSuffix(host, s) {
				return errBlocked
			}
		}
	}
	switch u.Port() {
	case "", "443":
		return nil
	}
	return errPort
}

// Parámetros de seguimiento que se quitan al guardar un enlace: no hacen
// falta para leerlo y dicen de dónde viene.
var trackingParams = map[string]bool{"fbclid": true, "gclid": true, "dclid": true, "gbraid": true, "wbraid": true, "msclkid": true,
	"mc_cid": true, "mc_eid": true, "igshid": true, "_hsenc": true, "_hsmi": true, "mkt_tok": true, "yclid": true, "twclid": true, "ref_src": true}

// cleanURL comprueba y normaliza una dirección que llega de la persona o
// de Talia: sin espacios, con https:// si no lleva esquema, sin el
// fragmento (#…) ni los parámetros de seguimiento.
func (f *fetcher) cleanURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURL || strings.ContainsAny(raw, " \t\r\n") {
		return "", errBadURL
	}
	if !strings.Contains(raw, "://") {
		if strings.Contains(strings.SplitN(raw, "/", 2)[0], ":") && !strings.HasPrefix(raw, "[") {
			return "", errBadURL // javascript:, data:, mailto:…
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errBadURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errBadURL
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	check := *u
	if err := f.target(&check); err != nil {
		return "", err
	}
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if u.RawQuery != "" {
		q := u.Query()
		removed := false
		for k := range q {
			if lk := strings.ToLower(k); trackingParams[lk] || strings.HasPrefix(lk, "utm_") {
				q.Del(k)
				removed = true
			}
		}
		if removed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String(), nil
}

// page es lo descargado.
type page struct {
	URL         *url.URL // la dirección final, tras las redirecciones
	ContentType string
	Body        []byte
}

// get descarga una página HTML (ya pasada por cleanURL).
func (f *fetcher) get(ctx context.Context, raw string) (*page, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errBadURL
	}
	if err := f.target(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errBadURL
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, asFetchError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, statusError(resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); ct != "" && (err != nil || (mt != "text/html" && mt != "application/xhtml+xml")) {
		return nil, errNotHTML
	}
	if resp.ContentLength > maxPage {
		return nil, errTooBig
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage+1))
	if err != nil {
		return nil, asFetchError(err)
	}
	if len(body) > maxPage {
		return nil, errTooBig
	}
	if ct == "" && !strings.Contains(strings.ToLower(http.DetectContentType(body)), "text/html") {
		return nil, errNotHTML
	}
	return &page{URL: resp.Request.URL, ContentType: ct, Body: body}, nil
}

// hostOf es el nombre del sitio de una dirección, sin «www.».
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
