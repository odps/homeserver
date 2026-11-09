package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// El resumen de la mañana trae la sección de Cartera si la app responde y
// la persona no la ha quitado en los ajustes.
func TestWalletInSummary(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	monday := time.Date(2027, 3, 1, 7, 0, 0, 0, madrid)
	asked := 0
	svc.wallet = func(ctx context.Context, owner string) ([]string, error) {
		asked++
		return []string{"Valor: 12.345 € (+1.234 € en total)", "Desde ayer +0,8 % (+98 €)."}, nil
	}
	text, err := svc.summary(ctx, "ana", monday, false)
	if err != nil || !strings.Contains(text, "💰 CARTERA\nValor: 12.345 € (+1.234 € en total)\nDesde ayer +0,8 % (+98 €).") {
		t.Fatalf("resumen:\n%s\n%v", text, err)
	}
	// Sin nada que decir (no usa la Cartera), no sale la sección.
	svc.wallet = func(ctx context.Context, owner string) ([]string, error) { return nil, nil }
	if text, _ := svc.summary(ctx, "ana", monday, false); strings.Contains(text, "CARTERA") {
		t.Fatalf("sección vacía:\n%s", text)
	}
	// Quitada en los ajustes: ni se pregunta.
	p, _ := svc.store.prefs(ctx, "ana")
	p.NoWallet = true
	svc.store.savePrefs(ctx, "ana", p)
	asked = 0
	svc.wallet = func(ctx context.Context, owner string) ([]string, error) { asked++; return []string{"x"}, nil }
	if text, _ := svc.summary(ctx, "ana", monday, false); strings.Contains(text, "CARTERA") || asked != 0 {
		t.Fatalf("quitada:\n%s (%d)", text, asked)
	}
	// Por Talia se puede volver a poner.
	on := true
	if err := svc.changeSettings(ctx, "ana", settingsIn{ResumenCartera: &on}); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.settingsView(ctx, "ana"); !v.ResumenCartera {
		t.Fatal("no se ha vuelto a poner")
	}
}

func TestAppTodayClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if r.Method != "POST" || r.URL.Path != "/interno/hoy" || in["usuario"] != "ana" || r.URL.RawQuery != "" {
			http.Error(w, "no", 400)
			return
		}
		w.Write([]byte(`{"lineas":["Valor: 1.000 €",""]}`))
	}))
	defer srv.Close()
	lines, err := appToday("cartera", srv.URL)(context.Background(), "ana")
	if err != nil || strings.Join(lines, "|") != "Valor: 1.000 €" {
		t.Fatalf("%v %v", lines, err)
	}
	if _, err := appToday("cartera", srv.URL)(context.Background(), "luis"); err == nil || !strings.HasPrefix(err.Error(), "cartera:") {
		t.Fatalf("error: %v", err)
	}
}

// Las secciones de las otras apps salen en su orden, se quitan una a una
// (en la web y con Talia) y un nombre desconocido es un error.
func TestExtrasInSummary(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	monday := time.Date(2027, 3, 1, 7, 0, 0, 0, madrid)
	fake := func(line string) func(context.Context, string) ([]string, error) {
		return func(context.Context, string) ([]string, error) { return []string{line}, nil }
	}
	svc.extras = []extra{
		{Name: "habitos", Title: "✅ HÁBITOS", today: fake("Leer: racha de 3 días")},
		{Name: "coche", Title: "🚗 COCHE", today: func(context.Context, string) ([]string, error) { return nil, nil }},
		{Name: "series", Title: "📺 SERIES", today: fake("Hoy: un episodio nuevo")},
	}
	text, err := svc.summary(ctx, "ana", monday, false)
	h, s := strings.Index(text, "✅ HÁBITOS\nLeer: racha de 3 días"), strings.Index(text, "📺 SERIES\nHoy: un episodio nuevo")
	if err != nil || h < 0 || s < h || strings.Contains(text, "COCHE") {
		t.Fatalf("resumen:\n%s\n%v", text, err)
	}
	off := false
	if err := svc.changeSettings(ctx, "ana", settingsIn{ResumenApps: map[string]bool{"series": off}}); err != nil {
		t.Fatal(err)
	}
	if text, _ := svc.summary(ctx, "ana", monday, false); strings.Contains(text, "SERIES") || !strings.Contains(text, "HÁBITOS") {
		t.Fatalf("quitada:\n%s", text)
	}
	if v, _ := svc.settingsView(ctx, "ana"); v.ResumenApps["series"] || !v.ResumenApps["habitos"] {
		t.Fatalf("vista: %v", v.ResumenApps)
	}
	if err := svc.changeSettings(ctx, "ana", settingsIn{ResumenApps: map[string]bool{"nada": true}}); err == nil {
		t.Fatal("acepta una sección desconocida")
	}
	if err := svc.changeSettings(ctx, "ana", settingsIn{ResumenApps: map[string]bool{"series": true}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.store.prefs(ctx, "ana"); len(p.NoSections) != 0 {
		t.Fatalf("no se ha vuelto a poner: %v", p.NoSections)
	}
	if len(newExtras()) != len(extraApps) {
		t.Fatal("newExtras")
	}
}
