package main

import (
	"context"
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
)

// Precio de la luz: el PVPC (la tarifa regulada 2.0TD, término de energía
// con peajes y cargos, sin impuestos) por horas, de la API pública de Red
// Eléctrica (apidatos.ree.es), sin clave ni cuenta. Es igual para todos: se
// pide una vez para el servidor y se guarda en claro. El de mañana se
// publica hacia las 20:30.
//
// Solo se conecta por HTTPS a ese dominio (y nunca a la red local). A REE no
// se le manda nada de nadie: solo las fechas.

const defaultREE = "https://apidatos.ree.es"

// Dominios a los que se puede conectar (y sus subdominios).
var reeHosts = []string{"apidatos.ree.es"}

const (
	userAgent = "homeserver-casa/1.0 (precio de la luz para una casa; +https://github.com/odps/homeserver)"
	reePath   = "/es/datos/mercados/precios-mercados-tiempo-real"
	reeGeo    = "8741" // sistema peninsular
	reePVPC   = "1001" // la serie del PVPC en la respuesta
)

var (
	errSourceWait = errors.New("Red Eléctrica no responde ahora; se volverá a intentar más tarde")
	errSourceBad  = errors.New("Red Eléctrica ha respondido algo que no se entiende")
)

// sourceStatus es cómo va la fuente. El error es genérico: nada de URLs.
type sourceStatus struct {
	OK    time.Time // última vez que respondió bien
	ErrAt time.Time
	Err   string
	Until time.Time // no se le pide nada hasta entonces (espera tras errores)
	fails int
}

type ree struct {
	base      string
	http      *http.Client
	allowHTTP bool // solo en los tests: un servidor local por HTTP
	now       func() time.Time

	mu sync.Mutex
	st sourceStatus
}

func newREE(base string, allowPrivate bool) *ree {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	c := &ree{base: strings.TrimRight(base, "/"), allowHTTP: allowPrivate, now: time.Now}
	if !allowPrivate {
		// Aunque el dominio resolviera a una dirección interna, no se conecta
		// a la red local ni al propio móvil.
		dialer.Control = func(_, address string, _ syscall.RawConn) error { return dialCheck(address) }
	}
	c.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConnsPerHost: 1, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("demasiadas redirecciones")
			}
			if !c.allowed(req.URL) {
				return errors.New("redirección a un sitio no permitido")
			}
			return nil
		},
	}
	return c
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

func (c *ree) allowed(u *url.URL) bool {
	if c.allowHTTP {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range reeHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// backoff es la espera tras fails errores seguidos: de 2 minutos a 1 hora.
func backoff(fails int) time.Duration {
	return min(2*time.Minute<<min(fails-1, 5), time.Hour)
}

// ready dice si se le puede pedir algo ahora.
func (c *ree) ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.now().Before(c.st.Until)
}

func (c *ree) ok() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.OK, c.st.Err, c.st.Until, c.st.fails = c.now(), "", time.Time{}, 0
}

func (c *ree) fail(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.fails++
	c.st.ErrAt, c.st.Err = c.now(), msg
	c.st.Until = c.now().Add(backoff(c.st.fails))
}

func (c *ree) status() sourceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// hourPrice es el precio de una hora.
type hourPrice struct {
	T time.Time `json:"t"` // cuándo empieza
	P float64   `json:"p"` // €/kWh
}

// reeReply es lo que interesa de la respuesta de REE.
type reeReply struct {
	Included []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Values []struct {
				Value    *float64 `json:"value"`
				Datetime string   `json:"datetime"`
			} `json:"values"`
		} `json:"attributes"`
	} `json:"included"`
}

// fetch pide los precios del PVPC de los días from a to (los dos incluidos)
// y devuelve los días que vengan completos.
func (c *ree) fetch(ctx context.Context, from, to time.Time) (map[time.Time][]hourPrice, error) {
	if !c.ready() {
		return nil, errSourceWait
	}
	q := url.Values{}
	q.Set("start_date", isoDay(from)+"T00:00")
	q.Set("end_date", isoDay(to)+"T23:59")
	q.Set("time_trunc", "hour")
	q.Set("geo_ids", reeGeo)
	raw := c.base + reePath + "?" + q.Encode()
	u, err := url.Parse(raw)
	if err != nil || !c.allowed(u) {
		return nil, errors.New("sitio no permitido")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", raw, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, err // se ha cancelado desde fuera: no es culpa de la fuente
		}
		c.fail("no responde")
		return nil, errSourceWait
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// REE responde 502 cuando los datos aún no están publicados.
		c.fail("error " + strconv.Itoa(resp.StatusCode))
		return nil, errSourceWait
	}
	var r reeReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&r); err != nil {
		c.fail("respuesta no válida")
		return nil, errSourceBad
	}
	days, err := parsePVPC(r)
	if err != nil {
		c.fail("respuesta no válida")
		return nil, err
	}
	c.ok()
	return days, nil
}

// parsePVPC saca de la respuesta los precios del PVPC por hora (si vinieran
// por cuartos de hora, la media de cada hora), agrupados por día de Madrid.
// Solo devuelve los días completos (23, 24 o 25 horas, según el cambio de
// hora).
func parsePVPC(r reeReply) (map[time.Time][]hourPrice, error) {
	out := map[time.Time][]hourPrice{}
	for _, inc := range r.Included {
		if inc.ID != reePVPC && !strings.EqualFold(inc.Type, "PVPC") {
			continue
		}
		sum, n := map[time.Time]float64{}, map[time.Time]int{}
		for _, v := range inc.Attributes.Values {
			if v.Value == nil {
				continue
			}
			t, err := time.Parse(time.RFC3339, v.Datetime)
			if err != nil || *v.Value < -1000 || *v.Value > 10000 || math.IsNaN(*v.Value) {
				return nil, errSourceBad
			}
			h := t.Truncate(time.Hour) // los husos de Madrid son de horas enteras
			sum[h] += *v.Value / 1000  // €/MWh → €/kWh
			n[h]++
		}
		byDay := map[time.Time][]hourPrice{}
		for h, s := range sum {
			d := dayOf(h)
			byDay[d] = append(byDay[d], hourPrice{T: h.UTC(), P: math.Round(s/float64(n[h])*1e5) / 1e5})
		}
		for d, hs := range byDay {
			sort.Slice(hs, func(i, j int) bool { return hs[i].T.Before(hs[j].T) })
			if len(hs) == hoursIn(d) {
				out[d] = hs
			}
		}
		break
	}
	return out, nil
}

// hoursIn son las horas que tiene un día en Madrid (23 o 25 al cambiar la
// hora).
func hoursIn(day time.Time) int {
	return int(midnight(day.Add(dayUnit)).Sub(midnight(day)).Hours())
}

// ---- Cuentas con los precios de un día ----

// dayPrices son los precios de un día, en orden.
type dayPrices struct {
	Day     time.Time
	Fetched time.Time
	Hours   []hourPrice
}

// block son unas horas seguidas.
type block struct {
	Start, End time.Time // End: cuando acaba la última hora
	Avg        float64
	N          int
}

func (d dayPrices) ok() bool { return len(d.Hours) > 0 }

// stats son el mínimo, el máximo y la media del día.
func (d dayPrices) stats() (lo, hi, avg float64) {
	return statsOf(d.Hours)
}

func statsOf(hs []hourPrice) (lo, hi, avg float64) {
	if len(hs) == 0 {
		return 0, 0, 0
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, h := range hs {
		lo, hi, avg = math.Min(lo, h.P), math.Max(hi, h.P), avg+h.P
	}
	return lo, hi, avg / float64(len(hs))
}

// ranked son n horas ordenadas por precio (las más baratas o las más caras).
func ranked(hs []hourPrice, n int, cheap bool) []hourPrice {
	list := append([]hourPrice(nil), hs...)
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].P == list[j].P {
			return list[i].T.Before(list[j].T)
		}
		return (list[i].P < list[j].P) == cheap
	})
	return list[:min(n, len(list))]
}

// bestBlock es el bloque de n horas seguidas con la media más baja (el
// primero, si hay empate).
func bestBlock(hs []hourPrice, n int) (block, bool) {
	if n <= 0 || len(hs) < n {
		return block{}, false
	}
	best, at := math.Inf(1), -1
	for i := 0; i+n <= len(hs); i++ {
		// Que sean seguidas de verdad (no saltarse una hora que faltara).
		if hs[i+n-1].T.Sub(hs[i].T) != time.Duration(n-1)*time.Hour {
			continue
		}
		s := 0.0
		for _, h := range hs[i : i+n] {
			s += h.P
		}
		if s < best-1e-9 {
			best, at = s, i
		}
	}
	if at < 0 {
		return block{}, false
	}
	return block{Start: hs[at].T, End: hs[at+n-1].T.Add(time.Hour), Avg: best / float64(n), N: n}, true
}

// from son las horas que aún no han acabado en el instante t.
func from(hs []hourPrice, t time.Time) []hourPrice {
	for i, h := range hs {
		if h.T.Add(time.Hour).After(t) {
			return hs[i:]
		}
	}
	return nil
}

// at es la hora en curso en el instante t.
func (d dayPrices) at(t time.Time) (hourPrice, bool) {
	for _, h := range d.Hours {
		if !t.Before(h.T) && t.Before(h.T.Add(time.Hour)) {
			return h, true
		}
	}
	return hourPrice{}, false
}

// levels reparte las horas del día en tres tramos por precio: el tercio más
// barato, el del medio y el más caro.
func levels(hs []hourPrice) (cheapMax, dearMin float64) {
	if len(hs) == 0 {
		return 0, 0
	}
	ps := make([]float64, len(hs))
	for i, h := range hs {
		ps[i] = h.P
	}
	sort.Float64s(ps)
	return ps[(len(ps)-1)/3], ps[len(ps)-1-(len(ps)-1)/3]
}

func level(p, cheapMax, dearMin float64) string {
	switch {
	case p <= cheapMax:
		return "barata"
	case p >= dearMin:
		return "cara"
	}
	return "media"
}

// hourOf es la hora de Madrid en que empieza t, de 0 a 23.
func hourOf(t time.Time) int { return t.In(madrid).Hour() }

// hourLabel es «14 h».
func hourLabel(t time.Time) string { return strconv.Itoa(hourOf(t)) + " h" }

// spanLabel es «14 a 17 h» (la hora final es cuando acaba, 24 a medianoche).
func spanLabel(start, end time.Time) string {
	e := hourOf(end)
	if e == 0 {
		e = 24
	}
	return strconv.Itoa(hourOf(start)) + " a " + strconv.Itoa(e) + " h"
}

func (b block) label() string { return spanLabel(b.Start, b.End) }

// hoursList junta las horas sueltas: «3 h, 4 h y 15 h».
func hoursList(hs []hourPrice) string {
	list := append([]hourPrice(nil), hs...)
	sort.Slice(list, func(i, j int) bool { return list[i].T.Before(list[j].T) })
	var parts []string
	for _, h := range list {
		parts = append(parts, hourLabel(h.T))
	}
	return joinY(parts)
}

// joinY junta con comas y una «y» al final.
func joinY(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " y " + parts[len(parts)-1]
}
