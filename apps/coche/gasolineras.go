package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"homeserver/platform/seal"
)

// Precios de las gasolineras: el servicio REST público del Ministerio
// (Geoportal de Hidrocarburos), sin clave ni registro. Se le pide la lista
// de municipios (una vez al mes) y las estaciones de cada municipio elegido
// (unas pocas veces al día; el Ministerio los actualiza cada media hora).
// Solo va el número del municipio, nunca la cuenta.
//
// Solo se conecta por HTTPS a ese dominio (y nunca a la red local).

const defaultFuelURL = "https://sedeaplicaciones.minetur.gob.es/ServiciosRESTCarburantes/PreciosCarburantes"

// fuelHosts son los dominios a los que se puede conectar.
var fuelHosts = []string{"sedeaplicaciones.minetur.gob.es"}

const userAgent = "homeserver-coche/1.0 (cuaderno personal del coche; +https://github.com/odps/homeserver)"

// Carburantes que se pueden buscar, con su campo en la respuesta.
var products = []struct{ Key, Label, Field string }{
	{"g95", "Gasolina 95 E5", "Precio Gasolina 95 E5"},
	{"g95e10", "Gasolina 95 E10", "Precio Gasolina 95 E10"},
	{"g98", "Gasolina 98 E5", "Precio Gasolina 98 E5"},
	{"goa", "Gasóleo A (diésel)", "Precio Gasoleo A"},
	{"goap", "Gasóleo Premium", "Precio Gasoleo Premium"},
	{"glp", "GLP (autogás)", "Precio Gases licuados del petróleo"},
	{"gnc", "Gas natural comprimido", "Precio Gas Natural Comprimido"},
	{"dren", "Diésel renovable", "Precio Diésel Renovable"},
}

func productLabel(k string) string {
	for _, p := range products {
		if p.Key == k {
			return p.Label
		}
	}
	return ""
}

func validProduct(k string) bool { return productLabel(k) != "" }

// priceList son las gasolineras de un municipio con sus precios.
type priceList struct {
	Fetched  time.Time `json:"pedido"` // cuándo se pidió
	Updated  time.Time `json:"fecha"`  // la de los precios, según el Ministerio
	Stations []station `json:"estaciones"`
}

type station struct {
	ID      string             `json:"id"`
	Brand   string             `json:"rotulo"`
	Address string             `json:"direccion"`
	Town    string             `json:"localidad,omitempty"`
	Postal  string             `json:"cp,omitempty"`
	Hours   string             `json:"horario,omitempty"`
	Prices  map[string]float64 `json:"precios"` // carburante → €/l
}

// pricedStation es una gasolinera con el precio de un carburante.
type pricedStation struct {
	station
	Price float64
}

// cheapest son las n gasolineras más baratas de un carburante, y la media de
// todas las que lo tienen.
func (p priceList) cheapest(product string, n int) ([]pricedStation, float64) {
	var out []pricedStation
	sum := 0.0
	for _, s := range p.Stations {
		if px := s.Prices[product]; px > 0 {
			out = append(out, pricedStation{s, px})
			sum += px
		}
	}
	if len(out) == 0 {
		return nil, 0
	}
	avg := sum / float64(len(out))
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Price != out[j].Price {
			return out[i].Price < out[j].Price
		}
		return out[i].Brand < out[j].Brand
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, avg
}

// ---- Cliente ----

// sourceStatus es cómo va el Ministerio. El error es genérico: nada de URLs
// ni de lo que se pedía.
type sourceStatus struct {
	OK    time.Time
	ErrAt time.Time
	Err   string
	Until time.Time // no se le pide nada hasta entonces
	fails int
}

type fuelSource struct {
	base      string
	http      *http.Client
	allowHTTP bool // solo en los tests: un servidor local por HTTP
	now       func() time.Time

	mu     sync.Mutex
	status sourceStatus
}

func newFuelSource(base string, allowPrivate bool) *fuelSource {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	f := &fuelSource{base: strings.TrimRight(base, "/"), allowHTTP: allowPrivate, now: time.Now}
	if !allowPrivate {
		// Aunque el dominio resolviera a una dirección interna, no se conecta
		// a la red local ni al propio móvil.
		dialer.Control = func(_, address string, _ syscall.RawConn) error { return dialCheck(address) }
	}
	f.http = &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second, MaxIdleConnsPerHost: 1, IdleConnTimeout: 60 * time.Second, TLSClientConfig: ministryTLS()},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("demasiadas redirecciones")
			}
			if !f.allowed(req.URL) {
				return errors.New("redirección a un sitio no permitido")
			}
			return nil
		},
	}
	return f
}

// ministryTLS es la configuración TLS para el Ministerio. Su servidor solo
// habla TLS 1.2 con intercambio de clave RSA (AES-GCM), que Go ya no ofrece
// por defecto: se añaden esas dos suites detrás de las buenas. El
// certificado se sigue verificando como siempre; solo se usa con ese
// dominio y para datos públicos.
func ministryTLS() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, CipherSuites: []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_RSA_WITH_AES_256_GCM_SHA384, tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	}}
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

func (f *fuelSource) allowed(u *url.URL) bool {
	if f.allowHTTP {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range fuelHosts {
		if host == h {
			return true
		}
	}
	return false
}

var (
	errSourceWait = errors.New("el Ministerio no responde ahora; se volverá a intentar más tarde")
	errSourceBad  = errors.New("el Ministerio ha respondido algo que no se entiende")
)

func (f *fuelSource) ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.now().Before(f.status.Until)
}

func (f *fuelSource) ok() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = sourceStatus{OK: f.now()}
}

// fail apunta un error y espera antes de volver a pedir: 5 minutos, luego
// el doble cada vez, hasta 6 horas.
func (f *fuelSource) fail(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := &f.status
	st.fails++
	st.ErrAt, st.Err = f.now(), msg
	st.Until = f.now().Add(min(5*time.Minute<<min(st.fails-1, 7), 6*time.Hour))
}

func (f *fuelSource) state() sourceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

// getJSON pide una ruta del servicio y lee su JSON (con límite de tamaño).
func (f *fuelSource) getJSON(ctx context.Context, path string, v any) error {
	if !f.ready() {
		return errSourceWait
	}
	raw := f.base + path
	u, err := url.Parse(raw)
	if err != nil || !f.allowed(u) {
		return errors.New("sitio no permitido")
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", raw, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return err // se ha cancelado desde fuera: no es culpa del Ministerio
		}
		f.fail("no responde")
		return errSourceWait
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.fail("error " + strconv.Itoa(resp.StatusCode))
		return errSourceWait
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v); err != nil {
		f.fail("respuesta no válida")
		return errSourceBad
	}
	f.ok()
	return nil
}

// municipios es la lista de todos los municipios.
func (f *fuelSource) municipios(ctx context.Context) ([]municipio, error) {
	var raw []struct {
		ID       string `json:"IDMunicipio"`
		Name     string `json:"Municipio"`
		Province string `json:"Provincia"`
	}
	if err := f.getJSON(ctx, "/Listados/Municipios/", &raw); err != nil {
		return nil, err
	}
	var out []municipio
	seen := map[int]bool{}
	for _, r := range raw {
		id, err := strconv.Atoi(strings.TrimSpace(r.ID))
		name := clip(r.Name, 80)
		if err != nil || id <= 0 || id > 1e6 || name == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, municipio{ID: id, Name: name, Province: titleCase(clip(r.Province, 60))})
	}
	if len(out) < 100 {
		f.fail("respuesta no válida")
		return nil, errSourceBad // la lista entera tiene más de 8.000
	}
	return out, nil
}

// stations son las gasolineras de un municipio con sus precios.
func (f *fuelSource) stations(ctx context.Context, municipioID int) (priceList, error) {
	var raw struct {
		Fecha     string              `json:"Fecha"`
		Lista     []map[string]string `json:"ListaEESSPrecio"`
		Resultado string              `json:"ResultadoConsulta"`
	}
	p := priceList{Fetched: f.now()}
	if municipioID <= 0 {
		return p, errors.New("municipio no válido")
	}
	if err := f.getJSON(ctx, "/EstacionesTerrestres/FiltroMunicipio/"+strconv.Itoa(municipioID), &raw); err != nil {
		return p, err
	}
	if !strings.EqualFold(strings.TrimSpace(raw.Resultado), "OK") {
		f.fail("respuesta no válida")
		return p, errSourceBad
	}
	if t, err := time.ParseInLocation("02/01/2006 15:04:05", strings.TrimSpace(raw.Fecha), madrid); err == nil {
		p.Updated = t
	}
	for _, e := range raw.Lista {
		// «R»: venta restringida (a socios de una cooperativa, por ejemplo).
		if strings.EqualFold(strings.TrimSpace(e["Tipo Venta"]), "R") {
			continue
		}
		s := station{ID: clip(e["IDEESS"], 12), Brand: clip(e["Rótulo"], 60), Address: titleCase(clip(e["Dirección"], 120)),
			Town: titleCase(clip(e["Localidad"], 60)), Postal: clip(e["C.P."], 5), Hours: clip(e["Horario"], 120), Prices: map[string]float64{}}
		if s.ID == "" {
			continue
		}
		if s.Brand == "" {
			s.Brand = "Sin rótulo"
		}
		for _, pr := range products {
			v, err := parseNum(e[pr.Field])
			if err == nil && v > 0.2 && v < 10 {
				s.Prices[pr.Key] = math.Round(v*1000) / 1000
			}
		}
		if len(s.Prices) > 0 {
			p.Stations = append(p.Stations, s)
		}
	}
	return p, nil
}

// titleCase pasa «CALLE DE EJEMPLO, 12» a «Calle de Ejemplo, 12»: las
// palabras cortas de enlace, en minúscula.
func titleCase(s string) string {
	small := map[string]bool{"de": true, "del": true, "la": true, "las": true, "el": true, "los": true, "y": true, "a": true, "en": true}
	var b strings.Builder
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		if i > 0 && small[w] {
			b.WriteString(w)
			continue
		}
		up := true
		for _, r := range w {
			if up && unicode.IsLetter(r) {
				b.WriteRune(unicode.ToUpper(r))
				up = false
				continue
			}
			b.WriteRune(r)
			if r == '/' || r == '(' || r == '-' || r == '.' {
				up = true
			}
		}
	}
	return b.String()
}

// searchMunicipios busca por el nombre (sin tildes ni mayúsculas): primero
// el que se llama justo así, luego los que empiezan así y luego los que lo
// contienen (también en la provincia).
func searchMunicipios(all []municipio, q string, n int) []municipio {
	q = strings.Join(seal.Words(q), " ")
	if q == "" {
		return nil
	}
	var exact, starts, contains []municipio
	for _, m := range all {
		name := strings.Join(seal.Words(m.Name), " ")
		full := strings.Join(seal.Words(m.Name+" "+m.Province), " ")
		switch {
		case name == q:
			exact = append(exact, m)
		case strings.HasPrefix(full, q):
			starts = append(starts, m)
		case strings.Contains(full, q):
			contains = append(contains, m)
		}
	}
	out := append(append(exact, starts...), contains...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}
