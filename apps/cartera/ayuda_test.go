package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Datos de ejemplo genéricos (el repositorio es público): BTC y ETH en
// Kraken, una moneda inventada en CoinGecko y dos valores inventados en
// Yahoo. Nunca se llama a la red real.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// El viernes 6 de noviembre de 2026 a mediodía.
func testNow() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, madrid) }

func day(s string) time.Time {
	d, err := parseDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

// fakeMarket hace de Kraken, CoinGecko, Yahoo y Frankfurter a la vez.
type fakeMarket struct {
	*httptest.Server
	mu       sync.Mutex
	now      func() time.Time
	kraken   map[string][2]float64 // altname → último, apertura
	gecko    map[string][2]float64 // id → precio, variación 24 h
	yahoo    map[string][3]float64 // símbolo → precio, cierre anterior, 0
	yahoo429 bool
	calls    map[string]int
	usd      float64 // USD por 1 EUR
}

func newFakeMarket(t *testing.T, now func() time.Time) *fakeMarket {
	f := &fakeMarket{now: now, calls: map[string]int{}, usd: 1.10,
		kraken: map[string][2]float64{"XBTEUR": {60000, 58000}, "ETHEUR": {2000, 2100}},
		gecko:  map[string][2]float64{"ejemplo-coin": {0.1234, 2.5}},
		yahoo:  map[string][3]float64{"ACME": {110, 108}, "EJEM.AS": {80.5, 80}},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMarket) count(k string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[k] }

func (f *fakeMarket) set(fn func(f *fakeMarket)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

// closes son n cierres diarios hasta ayer, subiendo desde base.
func (f *fakeMarket) closes(n int, base, step float64) ([]int64, []float64) {
	today := dayOf(f.now())
	var ts []int64
	var cs []float64
	for i := n; i >= 1; i-- {
		ts = append(ts, today.Add(-time.Duration(i)*dayUnit).Unix())
		cs = append(cs, base+step*float64(n-i))
	}
	return ts, cs
}

func (f *fakeMarket) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("User-Agent") != userAgent {
		http.Error(w, "sin user-agent propio", 400)
		return
	}
	p := r.URL.Path
	q := r.URL.Query()
	key := p
	if strings.HasPrefix(p, "/v8/finance/chart/") {
		key = "/v8/finance/chart"
	}
	f.calls[key]++
	js := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case p == "/0/public/AssetPairs":
		js(map[string]any{"error": []string{}, "result": map[string]any{
			"XXBTZEUR": map[string]string{"altname": "XBTEUR", "wsname": "XBT/EUR", "quote": "ZEUR", "status": "online"},
			"XETHZEUR": map[string]string{"altname": "ETHEUR", "wsname": "ETH/EUR", "quote": "ZEUR", "status": "online"},
			"XXBTZUSD": map[string]string{"altname": "XBTUSD", "wsname": "XBT/USD", "quote": "ZUSD", "status": "online"},
		}})
	case p == "/0/public/Ticker":
		res := map[string]any{}
		for _, alt := range strings.Split(q.Get("pair"), ",") {
			v, ok := f.kraken[alt]
			if !ok {
				js(map[string]any{"error": []string{"EQuery:Unknown asset pair"}})
				return
			}
			k := map[string]string{"XBTEUR": "XXBTZEUR", "ETHEUR": "XETHZEUR"}[alt]
			res[k] = map[string]any{"c": []string{fmt.Sprint(v[0]), "0.1"}, "o": fmt.Sprint(v[1])}
		}
		js(map[string]any{"error": []string{}, "result": res})
	case p == "/0/public/OHLC":
		alt := q.Get("pair")
		k := map[string]string{"XBTEUR": "XXBTZEUR", "ETHEUR": "XETHZEUR"}[alt]
		ts, cs := f.closes(400, 30000, 50)
		var rows [][]any
		for i := range ts {
			rows = append(rows, []any{ts[i], "1", "1", "1", fmt.Sprint(cs[i]), "1", "1", 1})
		}
		js(map[string]any{"error": []string{}, "result": map[string]any{k: rows, "last": 1}})
	case p == "/api/v3/search":
		coins := []map[string]string{}
		if strings.Contains("ejemplo-coin ejemplo ejc", strings.ToLower(q.Get("query"))) {
			coins = append(coins, map[string]string{"id": "ejemplo-coin", "name": "Ejemplo Coin", "symbol": "ejc"})
		}
		js(map[string]any{"coins": coins})
	case p == "/api/v3/simple/price":
		out := map[string]any{}
		for _, id := range strings.Split(q.Get("ids"), ",") {
			if v, ok := f.gecko[id]; ok {
				out[id] = map[string]float64{"eur": v[0], "eur_24h_change": v[1]}
			}
		}
		js(out)
	case strings.HasPrefix(p, "/api/v3/coins/") && strings.HasSuffix(p, "/market_chart"):
		ts, cs := f.closes(200, 0.1, 0.0001)
		var prices [][]float64
		for i := range ts {
			prices = append(prices, []float64{float64(ts[i] * 1000), cs[i]})
		}
		js(map[string]any{"prices": prices})
	case p == "/v1/finance/search":
		if f.yahoo429 {
			http.Error(w, "too many", http.StatusTooManyRequests)
			return
		}
		s := strings.ToUpper(q.Get("q"))
		quotes := []map[string]string{}
		if strings.Contains("ACME CORP", s) || s == "US0000000001" {
			quotes = append(quotes, map[string]string{"symbol": "ACME", "longname": "Acme Corp", "quoteType": "EQUITY", "exchDisp": "NASDAQ"})
		}
		if strings.Contains("EJEMPLO WORLD ETF EJEM.AS", s) || s == "IE0000000002" {
			quotes = append(quotes, map[string]string{"symbol": "EJEM.AS", "longname": "Ejemplo World ETF", "quoteType": "ETF", "exchDisp": "Amsterdam"})
		}
		quotes = append(quotes, map[string]string{"symbol": "^IDX", "shortname": "Un índice", "quoteType": "INDEX"})
		js(map[string]any{"quotes": quotes})
	case strings.HasPrefix(p, "/v8/finance/chart/"):
		if f.yahoo429 {
			http.Error(w, "too many", http.StatusTooManyRequests)
			return
		}
		sym := strings.TrimPrefix(p, "/v8/finance/chart/")
		v, ok := f.yahoo[sym]
		if !ok {
			http.Error(w, `{"chart":{"result":null,"error":{"code":"Not Found"}}}`, 404)
			return
		}
		cur, kind, name := "USD", "EQUITY", "Acme Corp"
		if sym == "EJEM.AS" {
			cur, kind, name = "EUR", "ETF", "Ejemplo World ETF"
		}
		n := 300
		if q.Get("range") == "5d" {
			n = 4
		}
		ts, cs := f.closes(n, 50, 0.2)
		cs[len(cs)-1] = v[1] // el cierre de ayer
		ts = append(ts, f.now().Unix())
		cs = append(cs, v[0])
		var closes []*float64
		for i := range cs {
			closes = append(closes, &cs[i])
		}
		js(map[string]any{"chart": map[string]any{"result": []any{map[string]any{
			"meta":      map[string]any{"currency": cur, "symbol": sym, "regularMarketPrice": v[0], "regularMarketTime": f.now().Unix(), "longName": name, "instrumentType": kind},
			"timestamp": ts, "indicators": map[string]any{"quote": []any{map[string]any{"close": closes}}},
		}}}})
	case p == "/v1/latest":
		js(map[string]any{"base": "EUR", "date": isoDay(dayOf(f.now())), "rates": map[string]float64{"USD": f.usd, "GBP": 0.85}})
	case strings.HasPrefix(p, "/v1/") && strings.HasSuffix(p, ".."):
		from, err := parseDay(strings.TrimSuffix(strings.TrimPrefix(p, "/v1/"), ".."))
		if err != nil {
			http.Error(w, "fecha", 422)
			return
		}
		rates := map[string]map[string]float64{}
		for d := from; !d.After(dayOf(f.now())); d = d.Add(dayUnit) {
			if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
				continue
			}
			rates[isoDay(d)] = map[string]float64{"USD": f.usd, "GBP": 0.85}
		}
		js(map[string]any{"base": "EUR", "rates": rates})
	default:
		http.NotFound(w, r)
	}
}

func testURLs(f *fakeMarket) sourceURLs {
	return sourceURLs{Kraken: f.URL, Gecko: f.URL, Yahoo: f.URL, FX: f.URL}
}

// newTestService da un servicio con base de datos de pruebas y el mercado
// falso.
func newTestService(t *testing.T) (*service, *fakeMarket, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := &clock{t: testNow()}
	fm := newFakeMarket(t, c.now)
	m := newMarket(testURLs(fm), true)
	m.now = c.now
	return &service{store: store{pool, box}, market: m, log: slog.New(slog.DiscardHandler), now: c.now}, fm, c
}

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	if dump == "" {
		t.Errorf("%s está vacía: la prueba no comprueba nada", table)
	}
	for _, s := range texts {
		if strings.Contains(strings.ToLower(dump), strings.ToLower(s)) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}
