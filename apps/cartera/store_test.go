package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas y
// el mercado falso: añadir activos, apuntar, la pasada de precios, las
// alertas, los avisos, que nadie vea lo de otro y que nada quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, fm, c := newTestService(t)
	ctx := context.Background()

	btc, existed, err := svc.addFound(ctx, "ana", "kraken:XBTEUR", "", "", false)
	if err != nil || existed || btc.Symbol != "BTC" || btc.Currency != "EUR" || btc.Kind != "cripto" {
		t.Fatalf("añadir BTC: %+v %v", btc, err)
	}
	if again, existed, err := svc.addFound(ctx, "ana", "kraken:xbteur", "", "", true); err != nil || !existed || again.ID != btc.ID || !again.Watch {
		t.Fatalf("añadirlo otra vez: %+v %v %v", again, existed, err)
	}
	acme, _, err := svc.addFound(ctx, "ana", "yahoo:ACME", "", "US0000000001", false)
	if err != nil || acme.Currency != "USD" || acme.Kind != "accion" || acme.Name != "Acme Corp" || acme.ISIN != "US0000000001" {
		t.Fatalf("añadir ACME: %+v %v", acme, err)
	}
	coin, _, err := svc.addFound(ctx, "ana", "coingecko:ejemplo-coin", "", "", true)
	if err != nil || coin.Symbol != "EJC" {
		t.Fatalf("añadir de CoinGecko: %+v %v", coin, err)
	}
	fondo, _, err := svc.addManual(ctx, "ana", "FONDOX", "Fondo de ejemplo garantizado", "fondo", "eur", 12.5, false)
	if err != nil || fondo.Currency != "EUR" || fondo.Manual != 12.5 {
		t.Fatalf("añadir a mano: %+v %v", fondo, err)
	}
	if _, _, err := svc.addFound(ctx, "ana", "yahoo:NADA", "", "", false); !isInput(err) {
		t.Fatalf("lo que la fuente no tiene: %v", err)
	}
	// Al añadir ya trae su histórico y su cotización.
	if h, _, _ := svc.store.history(ctx, "ana", btc.ID); h.len() != 400 {
		t.Fatalf("histórico al añadir: %d", h.len())
	}
	if q, _ := svc.store.quotes(ctx, "ana"); q[btc.ID].Price != 60000 || q[acme.ID].Price != 110 {
		t.Fatalf("cotización al añadir: %+v", q)
	}

	apunta := func(in opInput) operation {
		t.Helper()
		in.Origin = "web"
		o, err := svc.addOp(ctx, "ana", in)
		if err != nil {
			t.Fatalf("apuntar %+v: %v", in, err)
		}
		return o
	}
	apunta(opInput{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: 0.5, Price: 40000, Fee: 10, Platform: "kraken", Note: "primera compra de prueba"})
	apunta(opInput{AssetID: acme.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: 10, Total: 1000, Platform: "DEGIRO"})
	apunta(opInput{AssetID: fondo.ID, Kind: opBuy, Day: day("2026-04-01"), Qty: 100, Price: 10, Platform: "Banco de ejemplo"})
	// La recompensa sin precio toma el cierre de ese día del histórico.
	rw := apunta(opInput{AssetID: btc.ID, Kind: opReward, Day: dayOf(testNow()).Add(-10 * dayUnit), Qty: 0.001, Platform: "Kraken"})
	if !near(rw.Price, 30000+50*390) || rw.Currency != "EUR" {
		t.Fatalf("recompensa: %+v", rw)
	}
	for _, bad := range []opInput{
		{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: 1},                             // sin precio
		{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Price: 1},                           // sin cantidad
		{AssetID: btc.ID, Kind: opBuy, Day: dayOf(testNow()).Add(dayUnit), Qty: 1, Price: 1},       // futura
		{AssetID: btc.ID, Kind: opTransfer, Day: day("2026-03-02"), Qty: 1, Platform: "Kraken"},    // sin destino
		{AssetID: btc.ID, Kind: "regalo", Day: day("2026-03-02"), Qty: 1, Price: 1},                // tipo raro
		{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: 1, Price: 1, Currency: "EURO"}, // moneda
		{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: -1, Price: 1},                  // negativa
		{AssetID: btc.ID, Kind: opReward, Day: day("2020-01-01"), Qty: 1},                          // sin histórico
		{Kind: opBuy, Day: day("2026-03-02"), Qty: 1, Price: 1},                                    // sin activo
		{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: 1, Price: 1, Time: "25:00"},    // hora
		{AssetID: btc.ID, Kind: opDividend, Day: day("2026-03-02")},                                // sin importe
		{AssetID: btc.ID, Kind: opFee, Day: day("2026-03-02")},                                     // sin importe
	} {
		if _, err := svc.addOp(ctx, "ana", bad); !isInput(err) {
			t.Errorf("acepta %+v: %v", bad, err)
		}
	}
	// La plataforma se escribe como ya se conocía.
	if ops, _ := svc.store.ops(ctx, "ana"); ops[0].Platform != "Kraken" {
		t.Fatalf("plataforma: %q", ops[0].Platform)
	}

	// La pasada: cambios de divisas, cotizaciones e históricos.
	svc.round(ctx)
	fx, _ := svc.store.fx(ctx)
	if fx.first().After(day("2026-03-02")) || fx.last().Before(day("2026-11-06")) {
		t.Fatalf("cambios desde la primera operación: %v – %v", fx.first(), fx.last())
	}
	v, err := svc.load(ctx, "ana")
	if err != nil {
		t.Fatal(err)
	}
	if p := v.pf.pos(acme.ID); !near(p.Value, 1000) || !near(p.Cost, 1000/1.10) {
		t.Fatalf("ACME en euros: valor %v coste %v", p.Value, p.Cost)
	}
	if p := v.pf.pos(fondo.ID); !near(p.Value, 1250) || p.PriceFrom != "manual" {
		t.Fatalf("fondo a mano: %+v", p)
	}
	if fm.count("/0/public/Ticker") < 2 || fm.count("/api/v3/simple/price") < 2 {
		t.Fatal("la pasada no pide precios")
	}
	tickers := fm.count("/0/public/Ticker")
	svc.round(ctx)
	if fm.count("/0/public/Ticker") != tickers {
		t.Fatal("pide a Kraken antes de 5 minutos")
	}
	c.add(5 * time.Minute)
	svc.round(ctx)
	if fm.count("/0/public/Ticker") != tickers+1 {
		t.Fatal("no vuelve a pedir a los 5 minutos")
	}

	// Alertas: BTC por encima de 65.000 (cada vez) y la cartera por debajo
	// de 1.000.000 (una vez).
	a1, err := svc.store.addAlert(ctx, "ana", alert{AssetID: btc.ID, alertData: alertData{Kind: alertAbove, Threshold: 65000, Repeat: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.addAlert(ctx, "ana", alert{alertData: alertData{Kind: alertBelow, Threshold: 1000000}}); err != nil {
		t.Fatal(err)
	}
	fm.set(func(f *fakeMarket) { f.kraken["XBTEUR"] = [2]float64{70250, 68138} })
	c.add(5 * time.Minute)
	svc.round(ctx)
	got, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 2 || got[0].Kind != "cartera" || got[0].Text != "📈 BTC ha superado 65.000 €: ahora 70.250 € (+3,1 % hoy)" ||
		!strings.HasPrefix(got[1].Text, "💰 Tu cartera ha bajado de 1.000.000 €") {
		t.Fatalf("avisos: %+v %v", got, err)
	}
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatal("los avisos se entregan una sola vez")
	}
	c.add(5 * time.Minute)
	svc.round(ctx)
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatalf("repite mientras sigue cruzada: %+v", again)
	}
	alerts, _ := svc.store.alerts(ctx, "ana")
	if !alerts[0].Active || !alerts[0].Crossed || alerts[1].Active || alerts[0].Fired.IsZero() {
		t.Fatalf("estado: %+v", alerts)
	}

	// Lo de ana, ni lo ve ni lo toca luis.
	if l, _ := svc.store.assets(ctx, "luis"); len(l) != 0 {
		t.Fatal("luis ve los activos de ana")
	}
	if _, err := svc.store.asset(ctx, "luis", btc.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve un activo de ana")
	}
	if _, err := svc.addOp(ctx, "luis", opInput{AssetID: btc.ID, Kind: opBuy, Day: day("2026-03-02"), Qty: 1, Price: 1}); !errors.Is(err, errNotFound) {
		t.Fatalf("luis apunta en un activo de ana: %v", err)
	}
	ops, _ := svc.store.ops(ctx, "ana")
	if err := svc.store.deleteOp(ctx, "luis", ops[0].ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una operación de ana")
	}
	if err := svc.store.deleteAlert(ctx, "luis", a1); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una alerta de ana")
	}
	if err := svc.store.deleteAsset(ctx, "luis", btc.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra un activo de ana")
	}
	if _, err := svc.store.addAlert(ctx, "luis", alert{AssetID: btc.ID, alertData: alertData{Kind: alertAbove, Threshold: 1}}); !errors.Is(err, errNotFound) {
		t.Fatal("luis pone una alerta en un activo de ana")
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis recibe los avisos de ana")
	}
	svc.store.setQuote(ctx, "luis", btc.ID, quote{Price: 1, Currency: "EUR", At: c.now()})
	if q, _ := svc.store.quotes(ctx, "ana"); q[btc.ID].Price != 70250 {
		t.Fatal("luis cambia la cotización de ana")
	}
	if lines, err := svc.todayLines(ctx, "luis"); err != nil || len(lines) != 0 {
		t.Fatalf("el resumen de luis: %v %v", lines, err)
	}

	// Nada de lo de ana en claro, en ninguna tabla.
	words := []string{"BTC", "XBTEUR", "Acme", "ACME", "US0000000001", "EJC", "ejemplo-coin", "Fondo de ejemplo", "FONDOX", "Kraken", "DEGIRO",
		"Banco de ejemplo", "primera compra", "superado", "compra", "recompensa", "\"USD\""}
	for _, tb := range []string{"assets", "operations", "quotes", "history", "alerts", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}
	noPlain(t, svc.store.db, "fx", "BTC", "ACME", "ana") // datos públicos: solo cambios

	// Borrar un activo se lleva sus operaciones, precios y alertas.
	if err := svc.store.deleteAsset(ctx, "ana", btc.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	svc.store.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM operations WHERE asset_id = $1) + (SELECT count(*) FROM quotes WHERE asset_id = $1)
		+ (SELECT count(*) FROM history WHERE asset_id = $1) + (SELECT count(*) FROM alerts WHERE asset_id = $1)`, btc.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("quedan %d filas del activo borrado", left)
	}

	// Los avisos viejos se borran.
	svc.store.addNotice(ctx, "ana", "viejo", c.now().Add(-8*24*time.Hour))
	svc.store.takeNotices(ctx, "ana", c.now())
	var n int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM notices`).Scan(&n)
	if n != 2 {
		t.Fatalf("avisos guardados: %d", n)
	}
}

func TestTodayLines(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	btc, _, err := svc.addFound(ctx, "ana", "kraken:XBTEUR", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if lines, _ := svc.todayLines(ctx, "ana"); len(lines) != 0 {
		t.Fatalf("solo en seguimiento, sin líneas: %v", lines)
	}
	if _, err := svc.addOp(ctx, "ana", opInput{AssetID: btc.ID, Kind: opBuy, Day: dayOf(testNow()).Add(-30 * dayUnit), Qty: 0.1, Price: 40000}); err != nil {
		t.Fatal(err)
	}
	// Sin nada en seguimiento, los que tiene: precio por unidad, no el valor.
	lines, err := svc.todayLines(ctx, "ana")
	t.Logf("tenidos: %q", lines)
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], "BTC: 60.000 €") || strings.Contains(lines[0], "Valor") {
		t.Fatalf("líneas: %q %v", lines, err)
	}
	// Con algo en seguimiento, solo eso (lo tenga o no).
	eth, _, err := svc.addFound(ctx, "ana", "kraken:ETHEUR", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	lines, _ = svc.todayLines(ctx, "ana")
	t.Logf("seguidos: %q", lines)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], eth.Symbol+": ") {
		t.Fatalf("seguidos: %q", lines)
	}
	svc.store.setWatch(ctx, "ana", btc.ID, true)
	if lines, _ = svc.todayLines(ctx, "ana"); len(lines) != 2 || !strings.HasPrefix(lines[0], "BTC: ") {
		t.Fatalf("orden: %q", lines)
	}
}
