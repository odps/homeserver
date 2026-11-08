package main

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestSources(t *testing.T) {
	c := &clock{t: testNow()}
	fm := newFakeMarket(t, c.now)
	m := newMarket(testURLs(fm), true)
	m.now = c.now
	ctx := context.Background()

	// Kraken: BTC es XBT; solo pares en euros.
	list, err := m.searchKraken(ctx, "btc")
	if err != nil || len(list) != 1 || list[0].SourceID != "XBTEUR" || list[0].Symbol != "BTC" || list[0].Currency != "EUR" || list[0].ref() != "kraken:XBTEUR" {
		t.Fatalf("buscar en Kraken: %+v %v", list, err)
	}
	if list, _ := m.searchKraken(ctx, "E"); len(list) != 0 {
		t.Fatalf("con una letra no busca por el principio: %+v", list)
	}
	qs, err := m.krakenQuotes(ctx, []string{"XBTEUR", "ETHEUR", "NOEXISTE"})
	if err != nil || len(qs) != 2 || qs["XBTEUR"].Price != 60000 || qs["XBTEUR"].Prev != 58000 || qs["ETHEUR"].Price != 2000 {
		t.Fatalf("cotizaciones de Kraken: %+v %v", qs, err)
	}
	if fm.count("/0/public/Ticker") != 1 || fm.count("/0/public/AssetPairs") != 1 {
		t.Fatal("una sola llamada para todos los pares, y los pares se guardan")
	}
	h, err := m.krakenHistory(ctx, "XBTEUR")
	if err != nil || h.len() != 400 || h.Closes[0] != 30000 {
		t.Fatalf("histórico de Kraken: %d %v", h.len(), err)
	}

	// CoinGecko: la variación de 24 h da el «cierre anterior».
	gl, err := m.searchGecko(ctx, "ejemplo")
	if err != nil || len(gl) != 1 || gl[0].SourceID != "ejemplo-coin" || gl[0].Symbol != "EJC" {
		t.Fatalf("buscar en CoinGecko: %+v %v", gl, err)
	}
	gq, err := m.geckoQuotes(ctx, []string{"ejemplo-coin"})
	if err != nil || !near(gq["ejemplo-coin"].change(), 2.5) {
		t.Fatalf("precio de CoinGecko: %+v %v", gq, err)
	}
	if gh, err := m.geckoHistory(ctx, "ejemplo-coin", 365); err != nil || gh.len() != 200 {
		t.Fatalf("histórico de CoinGecko: %d %v", gh.len(), err)
	}

	// Yahoo: por ISIN, sin índices; la moneda la da el gráfico.
	yl, err := m.searchYahoo(ctx, "IE0000000002")
	if err != nil || len(yl) != 1 || yl[0].SourceID != "EJEM.AS" || yl[0].Kind != "etf" || yl[0].ISIN != "IE0000000002" {
		t.Fatalf("buscar en Yahoo: %+v %v", yl, err)
	}
	info, err := m.yahooChart(ctx, "ACME", "5d")
	if err != nil || info.Currency != "USD" || info.Price != 110 || info.Prev != 108 || info.Kind != "accion" || info.Name != "Acme Corp" {
		t.Fatalf("gráfico de Yahoo: %+v %v", info, err)
	}
	if _, err := m.yahooChart(ctx, "NADA", "5d"); !errors.Is(err, errNoMatch) {
		t.Fatalf("símbolo que no existe: %v", err)
	}
	if st := m.statuses()[sourceYahoo]; st.Err != "" {
		t.Fatal("un 404 no es un fallo de la fuente")
	}

	// Un 429 de Yahoo: se marca el error y se espera, sin reintentar.
	fm.set(func(f *fakeMarket) { f.yahoo429 = true })
	before := fm.count("/v8/finance/chart")
	if _, err := m.yahooChart(ctx, "ACME", "5d"); !errors.Is(err, errSourceWait) {
		t.Fatalf("429: %v", err)
	}
	if _, err := m.yahooChart(ctx, "ACME", "5d"); !errors.Is(err, errSourceWait) || fm.count("/v8/finance/chart") != before+1 {
		t.Fatal("tras un 429 se vuelve a pedir enseguida")
	}
	st := m.statuses()[sourceYahoo]
	if st.Err == "" || st.Until.Sub(c.now()) < 14*time.Minute {
		t.Fatalf("estado tras el 429: %+v", st)
	}
	fm.set(func(f *fakeMarket) { f.yahoo429 = false })
	c.add(16 * time.Minute)
	if _, err := m.yahooChart(ctx, "ACME", "5d"); err != nil || m.statuses()[sourceYahoo].Err != "" {
		t.Fatalf("pasada la espera vuelve: %v", err)
	}
	if !m.ready(sourceKraken) {
		t.Fatal("un fallo de Yahoo no afecta a Kraken")
	}

	// Frankfurter.
	r, err := m.fxSeries(ctx, day("2026-11-02"))
	if err != nil || len(r) != 5 || r["2026-11-02"]["USD"] != 1.10 {
		t.Fatalf("cambios: %+v %v", r, err)
	}

	// Referencias: solo fuentes conocidas e ids sin caracteres raros.
	for ref, ok := range map[string]bool{"kraken:XBTEUR": true, "yahoo:EJEM.AS": true, "coingecko:ejemplo-coin": true,
		"manual:X": false, "kraken:": false, "yahoo:../x": false, "yahoo:A B": false, "otra:X": false} {
		if _, _, got := parseRef(ref); got != ok {
			t.Errorf("parseRef(%q) = %v", ref, got)
		}
	}
}

func TestAllowedHosts(t *testing.T) {
	m := newMarket(defaultURLs, false)
	for raw, ok := range map[string]bool{
		"https://api.kraken.com/0/public/Ticker": true, "https://query1.finance.yahoo.com/v8/finance/chart/X": true,
		"https://api.coingecko.com/api/v3/search": true, "https://api.frankfurter.dev/v1/latest": true,
		"http://api.kraken.com/0/public/Ticker": false, "https://evil.example/x": false, "https://api.kraken.com.evil.example/": false,
		"https://127.0.0.1/": false,
	} {
		u, _ := url.Parse(raw)
		if m.allowed(u) != ok {
			t.Errorf("%s: %v", raw, !ok)
		}
	}
	// Ni a otro sitio ni a la red local, aunque se lo pidan.
	if err := m.getJSON(context.Background(), sourceKraken, "https://localhost:1/x", &struct{}{}); err == nil {
		t.Fatal("se conecta a localhost")
	}
	if err := m.dialCheck("127.0.0.1:443"); err == nil {
		t.Fatal("el control de la conexión deja pasar 127.0.0.1")
	}
	if err := m.dialCheck("100.101.102.103:443"); err == nil {
		t.Fatal("el control de la conexión deja pasar Tailscale")
	}
	if err := m.dialCheck("104.16.0.1:443"); err != nil {
		t.Fatalf("una dirección pública: %v", err)
	}
}
