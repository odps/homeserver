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

// Fuentes de precios, todas públicas y sin registro: Kraken (cripto en
// euros, la preferida), CoinGecko (cripto que no esté en Kraken; límite bajo,
// así que una sola llamada para todas), Yahoo Finance (acciones, ETF y
// fondos; no es oficial: poca frecuencia, un User-Agent propio y honesto y,
// si se queja, se espera) y Frankfurter (cambios del BCE). A ellas solo va
// lo que se busca o se cotiza, nunca la cuenta.
//
// Solo se conecta por HTTPS a esos dominios (y nunca a la red local).

const (
	sourceKraken = "kraken"
	sourceGecko  = "coingecko"
	sourceYahoo  = "yahoo"
	sourceFX     = "frankfurter"
	sourceManual = "manual"
)

var sourceNames = map[string]string{sourceKraken: "Kraken", sourceGecko: "CoinGecko", sourceYahoo: "Yahoo Finance", sourceFX: "Cambio de divisas (BCE)", sourceManual: "Manual"}

func sourceName(s string) string {
	if n, ok := sourceNames[s]; ok {
		return n
	}
	return s
}

type sourceURLs struct {
	Kraken, Gecko, Yahoo, FX string
}

var defaultURLs = sourceURLs{
	Kraken: "https://api.kraken.com",
	Gecko:  "https://api.coingecko.com",
	Yahoo:  "https://query1.finance.yahoo.com",
	FX:     "https://api.frankfurter.dev",
}

// Dominios a los que se puede conectar (y sus subdominios).
var marketHosts = []string{"api.kraken.com", "api.coingecko.com", "query1.finance.yahoo.com", "query2.finance.yahoo.com", "api.frankfurter.dev"}

const userAgent = "homeserver-cartera/1.0 (cartera personal; +https://github.com/odps/homeserver)"

// sourceStatus es cómo va cada fuente. El error es genérico: nada de URLs
// ni de lo que se pedía.
type sourceStatus struct {
	OK    time.Time // última vez que respondió bien
	ErrAt time.Time
	Err   string
	Until time.Time // no se le pide nada hasta entonces (espera tras errores)
	fails int
}

type market struct {
	urls      sourceURLs
	http      *http.Client
	allowHTTP bool // solo en los tests: servidores locales por HTTP
	now       func() time.Time

	mu      sync.Mutex
	status  map[string]*sourceStatus
	pairs   []krakenPair
	pairsAt time.Time
}

func newMarket(urls sourceURLs, allowPrivate bool) *market {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	m := &market{urls: urls, allowHTTP: allowPrivate, now: time.Now, status: map[string]*sourceStatus{}}
	if !allowPrivate {
		// Aunque un dominio permitido resolviera a una dirección interna, no
		// se conecta a la red local ni al propio móvil.
		dialer.Control = func(_, address string, _ syscall.RawConn) error { return m.dialCheck(address) }
	}
	m.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("demasiadas redirecciones")
			}
			if !m.allowed(req.URL) {
				return errors.New("redirección a un sitio no permitido")
			}
			return nil
		},
	}
	return m
}

// dialCheck rechaza las direcciones de la red local, del propio móvil y de
// Tailscale.
func (m *market) dialCheck(address string) error {
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) { // CGNAT: Tailscale
		return fmt.Errorf("dirección no permitida: %s", host)
	}
	return nil
}

func (m *market) allowed(u *url.URL) bool {
	if m.allowHTTP {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range marketHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

var (
	errSourceWait = errors.New("la fuente de precios no responde ahora; se volverá a intentar más tarde")
	errSourceBad  = errors.New("la fuente de precios ha respondido algo que no se entiende")
	errNoMatch    = errors.New("no se ha encontrado")
)

// Esperas tras un error: Yahoo, que no es oficial, más larga y creciente.
func backoff(source string, fails int, limited bool) time.Duration {
	base := 2 * time.Minute
	if source == sourceYahoo || limited {
		base = 15 * time.Minute
	}
	d := base << min(fails-1, 5)
	return min(d, 6*time.Hour)
}

func (m *market) st(source string) *sourceStatus {
	st := m.status[source]
	if st == nil {
		st = &sourceStatus{}
		m.status[source] = st
	}
	return st
}

// ready dice si se le puede pedir algo a la fuente ahora.
func (m *market) ready(source string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.now().Before(m.st(source).Until)
}

func (m *market) ok(source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.st(source)
	st.OK, st.Err, st.Until, st.fails = m.now(), "", time.Time{}, 0
}

func (m *market) fail(source, msg string, limited bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.st(source)
	st.fails++
	st.ErrAt, st.Err = m.now(), msg
	st.Until = m.now().Add(backoff(source, st.fails, limited))
}

// statuses es una copia del estado de cada fuente.
func (m *market) statuses() map[string]sourceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]sourceStatus{}
	for k, v := range m.status {
		out[k] = *v
	}
	return out
}

// getJSON pide a una fuente y lee su JSON (con límite de tamaño). Un 404 es
// «no existe» y no cuenta como fallo de la fuente.
func (m *market) getJSON(ctx context.Context, source, raw string, v any) error {
	if !m.ready(source) {
		return errSourceWait
	}
	u, err := url.Parse(raw)
	if err != nil || !m.allowed(u) {
		return errors.New("sitio no permitido")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", raw, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return err // se ha cancelado desde fuera: no es culpa de la fuente
		}
		m.fail(source, "no responde", false)
		return errSourceWait
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		m.ok(source)
		return errNoMatch
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden:
		m.fail(source, "límite de peticiones: se espera antes de volver a pedir", true)
		return errSourceWait
	case resp.StatusCode != http.StatusOK:
		m.fail(source, "error "+strconv.Itoa(resp.StatusCode), false)
		return errSourceWait
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v); err != nil {
		m.fail(source, "respuesta no válida", false)
		return errSourceBad
	}
	m.ok(source)
	return nil
}

// ---- Resultados ----

// found es un activo encontrado al buscar.
type found struct {
	Source   string
	SourceID string
	Symbol   string
	Name     string
	Kind     string
	Currency string // vacía si la fuente no la da al buscar
	Market   string // bolsa o par, para distinguir
	ISIN     string
}

// ref es cómo se pide añadirlo: «kraken:XBTEUR».
func (f found) ref() string { return f.Source + ":" + f.SourceID }

func parseRef(s string) (source, id string, ok bool) {
	source, id, ok = strings.Cut(strings.TrimSpace(s), ":")
	switch source {
	case sourceKraken, sourceGecko, sourceYahoo:
	default:
		return "", "", false
	}
	id = strings.TrimSpace(id)
	return source, id, ok && id != "" && len(id) <= 60 && !strings.ContainsAny(id, "/?#&% \\")
}

// ---- Kraken ----

type krakenPair struct {
	Key     string // XXBTZEUR: como viene en el Ticker
	Altname string // XBTEUR: como se guarda
	Base    string // BTC (ya traducido)
}

// En Kraken, BTC es XBT y DOGE es XDG.
var krakenAlias = map[string]string{"XBT": "BTC", "XDG": "DOGE"}

func krakenBase(ws string) string {
	b, _, _ := strings.Cut(ws, "/")
	if a, ok := krakenAlias[b]; ok {
		return a
	}
	return b
}

type krakenReply struct {
	Error  []string        `json:"error"`
	Result json.RawMessage `json:"result"`
}

func (m *market) kraken(ctx context.Context, path string, v any) error {
	var r krakenReply
	if err := m.getJSON(ctx, sourceKraken, m.urls.Kraken+path, &r); err != nil {
		return err
	}
	if len(r.Error) > 0 {
		if strings.Contains(strings.Join(r.Error, " "), "Unknown") {
			return errNoMatch
		}
		m.fail(sourceKraken, "error de la API", false)
		return errSourceWait
	}
	if err := json.Unmarshal(r.Result, v); err != nil {
		return errSourceBad
	}
	return nil
}

// krakenPairs son los pares en euros de Kraken (se piden una vez al día).
func (m *market) krakenPairs(ctx context.Context) ([]krakenPair, error) {
	m.mu.Lock()
	if m.pairs != nil && m.now().Sub(m.pairsAt) < 24*time.Hour {
		p := m.pairs
		m.mu.Unlock()
		return p, nil
	}
	m.mu.Unlock()
	var raw map[string]struct {
		Altname string `json:"altname"`
		WSName  string `json:"wsname"`
		Quote   string `json:"quote"`
		Status  string `json:"status"`
	}
	if err := m.kraken(ctx, "/0/public/AssetPairs", &raw); err != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.pairs != nil {
			return m.pairs, nil // mejor los de ayer que nada
		}
		return nil, err
	}
	var out []krakenPair
	for key, p := range raw {
		if !strings.HasSuffix(p.WSName, "/EUR") || (p.Status != "" && p.Status != "online") || strings.HasSuffix(key, ".d") {
			continue
		}
		out = append(out, krakenPair{Key: key, Altname: p.Altname, Base: krakenBase(p.WSName)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Base < out[j].Base })
	m.mu.Lock()
	m.pairs, m.pairsAt = out, m.now()
	m.mu.Unlock()
	return out, nil
}

func (m *market) krakenPair(ctx context.Context, altname string) (krakenPair, error) {
	pairs, err := m.krakenPairs(ctx)
	if err != nil {
		return krakenPair{}, err
	}
	for _, p := range pairs {
		if strings.EqualFold(p.Altname, altname) {
			return p, nil
		}
	}
	return krakenPair{}, errNoMatch
}

// searchKraken busca por símbolo (Kraken no tiene nombres): «BTC», «eth».
func (m *market) searchKraken(ctx context.Context, q string) ([]found, error) {
	q = strings.ToUpper(strings.TrimSpace(q))
	if q == "" || len(q) > 12 {
		return nil, nil
	}
	pairs, err := m.krakenPairs(ctx)
	if err != nil {
		return nil, err
	}
	var exact, prefix []found
	for _, p := range pairs {
		f := found{Source: sourceKraken, SourceID: p.Altname, Symbol: p.Base, Name: p.Base, Kind: "cripto", Currency: "EUR", Market: p.Base + "/EUR"}
		switch {
		case p.Base == q:
			exact = append(exact, f)
		case len(q) >= 2 && strings.HasPrefix(p.Base, q):
			prefix = append(prefix, f)
		}
	}
	out := append(exact, prefix...)
	return out[:min(len(out), 5)], nil
}

// krakenQuotes pide en una sola llamada las cotizaciones de varios pares
// (por su altname). Los que Kraken ya no tiene se quedan sin cotización.
func (m *market) krakenQuotes(ctx context.Context, altnames []string) (map[string]quote, error) {
	pairs, err := m.krakenPairs(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string]string{}
	var ask []string
	for _, a := range altnames {
		for _, p := range pairs {
			if strings.EqualFold(p.Altname, a) {
				byKey[p.Key] = p.Altname
				byKey[p.Altname] = p.Altname
				ask = append(ask, p.Altname)
				break
			}
		}
	}
	out := map[string]quote{}
	if len(ask) == 0 {
		return out, nil
	}
	var raw map[string]struct {
		C []string `json:"c"` // último: [precio, volumen]
		O string   `json:"o"` // apertura del día (00:00 UTC)
	}
	if err := m.kraken(ctx, "/0/public/Ticker?pair="+url.QueryEscape(strings.Join(ask, ",")), &raw); err != nil {
		return nil, err
	}
	now := m.now()
	for k, t := range raw {
		alt, ok := byKey[k]
		if !ok || len(t.C) == 0 {
			continue
		}
		p, err1 := strconv.ParseFloat(t.C[0], 64)
		o, _ := strconv.ParseFloat(t.O, 64)
		if err1 != nil || p <= 0 || math.IsInf(p, 0) {
			continue
		}
		out[alt] = quote{Price: p, Prev: o, Currency: "EUR", At: now}
	}
	return out, nil
}

// krakenHistory son los cierres diarios (Kraken da los últimos 720 días).
func (m *market) krakenHistory(ctx context.Context, altname string) (series, error) {
	p, err := m.krakenPair(ctx, altname)
	if err != nil {
		return series{}, err
	}
	var raw map[string]json.RawMessage
	if err := m.kraken(ctx, "/0/public/OHLC?interval=1440&pair="+url.QueryEscape(p.Altname), &raw); err != nil {
		return series{}, err
	}
	var h series
	for k, v := range raw {
		if k == "last" {
			continue
		}
		var rows [][]any
		if json.Unmarshal(v, &rows) != nil {
			return series{}, errSourceBad
		}
		for _, r := range rows {
			if len(r) < 5 {
				continue
			}
			ts, ok := r[0].(float64)
			cs, ok2 := r[4].(string)
			if !ok || !ok2 {
				continue
			}
			c, err := strconv.ParseFloat(cs, 64)
			if err != nil || c <= 0 {
				continue
			}
			h.Days = append(h.Days, int32(int64(ts)/86400))
			h.Closes = append(h.Closes, c)
		}
	}
	return series{}.merge(h), nil
}

// ---- CoinGecko ----

func (m *market) searchGecko(ctx context.Context, q string) ([]found, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	var r struct {
		Coins []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Symbol string `json:"symbol"`
		} `json:"coins"`
	}
	if err := m.getJSON(ctx, sourceGecko, m.urls.Gecko+"/api/v3/search?query="+url.QueryEscape(q), &r); err != nil {
		return nil, err
	}
	var out []found
	for _, c := range r.Coins {
		if c.ID == "" || len(out) == 5 {
			continue
		}
		out = append(out, found{Source: sourceGecko, SourceID: c.ID, Symbol: strings.ToUpper(clip(c.Symbol, 12)), Name: clip(c.Name, 80),
			Kind: "cripto", Currency: "EUR"})
	}
	return out, nil
}

// geckoQuotes pide en una sola llamada el precio en euros de varias monedas.
// La variación que da es la de 24 horas: hace de «variación del día».
func (m *market) geckoQuotes(ctx context.Context, ids []string) (map[string]quote, error) {
	out := map[string]quote{}
	if len(ids) == 0 {
		return out, nil
	}
	var raw map[string]map[string]float64
	u := m.urls.Gecko + "/api/v3/simple/price?vs_currencies=eur&include_24hr_change=true&ids=" + url.QueryEscape(strings.Join(ids, ","))
	if err := m.getJSON(ctx, sourceGecko, u, &raw); err != nil {
		return nil, err
	}
	now := m.now()
	for id, v := range raw {
		p := v["eur"]
		if p <= 0 {
			continue
		}
		q := quote{Price: p, Currency: "EUR", At: now}
		if ch, ok := v["eur_24h_change"]; ok && ch > -100 {
			q.Prev = p / (1 + ch/100)
		}
		out[id] = q
	}
	return out, nil
}

// geckoHistory son los cierres diarios del último año (sin clave, CoinGecko
// no da más).
func (m *market) geckoHistory(ctx context.Context, id string, days int) (series, error) {
	var r struct {
		Prices [][]float64 `json:"prices"`
	}
	u := fmt.Sprintf("%s/api/v3/coins/%s/market_chart?vs_currency=eur&interval=daily&days=%d", m.urls.Gecko, url.PathEscape(id), days)
	if err := m.getJSON(ctx, sourceGecko, u, &r); err != nil {
		return series{}, err
	}
	var h series
	for _, p := range r.Prices {
		if len(p) < 2 || p[1] <= 0 {
			continue
		}
		// El último punto es «ahora»: cuenta como el día de hoy.
		h.Days = append(h.Days, epochDay(dayOf(time.UnixMilli(int64(p[0])))))
		h.Closes = append(h.Closes, p[1])
	}
	return series{}.merge(h), nil
}

// ---- Yahoo Finance ----

var yahooKinds = map[string]string{"EQUITY": "accion", "ETF": "etf", "MUTUALFUND": "fondo"}

// searchYahoo busca por texto, símbolo o ISIN (DEGIRO usa ISIN).
func (m *market) searchYahoo(ctx context.Context, q string) ([]found, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	var r struct {
		Quotes []struct {
			Symbol    string `json:"symbol"`
			Short     string `json:"shortname"`
			Long      string `json:"longname"`
			QuoteType string `json:"quoteType"`
			Exchange  string `json:"exchDisp"`
		} `json:"quotes"`
	}
	u := m.urls.Yahoo + "/v1/finance/search?quotesCount=6&newsCount=0&listsCount=0&q=" + url.QueryEscape(q)
	if err := m.getJSON(ctx, sourceYahoo, u, &r); err != nil {
		return nil, err
	}
	isin := ""
	if looksISIN(q) {
		isin = strings.ToUpper(q)
	}
	var out []found
	for _, x := range r.Quotes {
		kind, ok := yahooKinds[x.QuoteType]
		if !ok || x.Symbol == "" || len(out) == 5 {
			continue
		}
		name := x.Long
		if name == "" {
			name = x.Short
		}
		out = append(out, found{Source: sourceYahoo, SourceID: clip(x.Symbol, 30), Symbol: clip(x.Symbol, 30), Name: clip(name, 80),
			Kind: kind, Market: clip(x.Exchange, 40), ISIN: isin})
	}
	return out, nil
}

func looksISIN(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 12 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return false
	}
	for _, r := range s[2:] {
		if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

type yahooChart struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency      string  `json:"currency"`
				Symbol        string  `json:"symbol"`
				Price         float64 `json:"regularMarketPrice"`
				PrevClose     float64 `json:"previousClose"`
				ChartPrev     float64 `json:"chartPreviousClose"`
				MarketTime    int64   `json:"regularMarketTime"`
				LongName      string  `json:"longName"`
				ShortName     string  `json:"shortName"`
				InstrumentTyp string  `json:"instrumentType"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Close []*float64 `json:"close"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
	} `json:"chart"`
}

// yahooCurrency arregla las monedas en céntimos (la bolsa de Londres da
// «GBp»): devuelve la moneda y por cuánto dividir.
func yahooCurrency(c string) (string, float64) {
	switch c {
	case "GBp", "GBX":
		return "GBP", 100
	case "ZAc":
		return "ZAR", 100
	case "ILA":
		return "ILS", 100
	}
	return strings.ToUpper(c), 1
}

// yahooInfo es lo que da el gráfico de Yahoo: cotización, moneda, nombre e
// histórico.
type yahooInfo struct {
	quote
	Name string
	Kind string
	Hist series
}

func (m *market) yahooChart(ctx context.Context, symbol, rng string) (yahooInfo, error) {
	var r yahooChart
	u := fmt.Sprintf("%s/v8/finance/chart/%s?interval=1d&range=%s", m.urls.Yahoo, url.PathEscape(symbol), rng)
	if err := m.getJSON(ctx, sourceYahoo, u, &r); err != nil {
		return yahooInfo{}, err
	}
	if len(r.Chart.Result) == 0 {
		return yahooInfo{}, errNoMatch
	}
	x := r.Chart.Result[0]
	cur, div := yahooCurrency(x.Meta.Currency)
	if cur == "" || x.Meta.Price <= 0 {
		return yahooInfo{}, errNoMatch
	}
	info := yahooInfo{Name: clip(x.Meta.LongName, 80), Kind: yahooKinds[x.Meta.InstrumentTyp]}
	if info.Name == "" {
		info.Name = clip(x.Meta.ShortName, 80)
	}
	var closes []*float64
	if len(x.Indicators.Quote) > 0 {
		closes = x.Indicators.Quote[0].Close
	}
	for i, ts := range x.Timestamp {
		if i >= len(closes) || closes[i] == nil || *closes[i] <= 0 {
			continue
		}
		info.Hist.Days = append(info.Hist.Days, epochDay(time.Unix(ts, 0).UTC()))
		info.Hist.Closes = append(info.Hist.Closes, *closes[i]/div)
	}
	info.Hist = series{}.merge(info.Hist)
	at := time.Unix(x.Meta.MarketTime, 0)
	if x.Meta.MarketTime == 0 {
		at = m.now()
	}
	info.quote = quote{Price: x.Meta.Price / div, Currency: cur, At: at}
	// El cierre anterior: el del último día de antes del de la cotización.
	today := epochDay(at.UTC())
	for i := len(info.Hist.Days) - 1; i >= 0; i-- {
		if info.Hist.Days[i] < today {
			info.Prev = info.Hist.Closes[i]
			break
		}
	}
	if info.Prev == 0 {
		switch {
		case x.Meta.PrevClose > 0:
			info.Prev = x.Meta.PrevClose / div
		case x.Meta.ChartPrev > 0:
			info.Prev = x.Meta.ChartPrev / div
		}
	}
	return info, nil
}

// ---- Cambio de divisas (Frankfurter, datos del BCE) ----

// fxSeries son los cambios de todas las monedas desde un día (hasta hoy).
func (m *market) fxSeries(ctx context.Context, from time.Time) (map[string]map[string]float64, error) {
	var r struct {
		Rates map[string]map[string]float64 `json:"rates"`
	}
	if err := m.getJSON(ctx, sourceFX, m.urls.FX+"/v1/"+isoDay(from)+"..?base=EUR", &r); err != nil {
		return nil, err
	}
	for d := range r.Rates {
		if _, err := parseDay(d); err != nil {
			return nil, errSourceBad
		}
	}
	return r.Rates, nil
}

func (m *market) fxLatest(ctx context.Context) (map[string]map[string]float64, error) {
	var r struct {
		Date  string             `json:"date"`
		Rates map[string]float64 `json:"rates"`
	}
	if err := m.getJSON(ctx, sourceFX, m.urls.FX+"/v1/latest?base=EUR", &r); err != nil {
		return nil, err
	}
	if _, err := parseDay(r.Date); err != nil || len(r.Rates) == 0 {
		return nil, errSourceBad
	}
	return map[string]map[string]float64{r.Date: r.Rates}, nil
}
