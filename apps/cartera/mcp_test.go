package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// asUser añade la identidad que pondría la pasarela.
type asUser struct{ user string }

func (u asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if u.user != "" {
		r.Header.Set("X-Auth-User", u.user)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestTools(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "cartera", Version: "test"}, nil)
	addTools(server, svc)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer srv.Close()
	connect := func(user string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
		sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: asUser{user}}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}
	// call devuelve el texto del error si la tool falla.
	call := func(sess *mcp.ClientSession, name string, args map[string]any, out any) string {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			var b strings.Builder
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					b.WriteString(tc.Text)
				}
			}
			return "error: " + b.String()
		}
		if out != nil {
			b, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		return ""
	}
	ana, luis, nadie := connect("ana"), connect("luis"), connect("")

	tools, err := ana.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"cartera_resumen", "cartera_activo", "cartera_operaciones", "cartera_apuntar", "cartera_precio",
		"cartera_seguimiento", "cartera_alertas", "cartera_alerta_crear", "cartera_alerta_borrar"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if e := call(nadie, "cartera_resumen", nil, nil); !strings.Contains(e, "no está asociado") {
		t.Fatalf("sin persona: %q", e)
	}

	// Cotizar algo que no se tiene, y apuntar con su ref.
	var lk lookupOut
	if e := call(ana, "cartera_precio", map[string]any{"consulta": "BTC"}, &lk); e != "" || len(lk.Resultados) != 1 || lk.Resultados[0].Ref != "kraken:XBTEUR" || lk.Resultados[0].Precio != 60000 {
		t.Fatalf("precio de BTC: %+v %s", lk, e)
	}
	lk = lookupOut{}
	if e := call(ana, "cartera_precio", map[string]any{"consulta": "US0000000001"}, &lk); e != "" || len(lk.Resultados) != 1 || lk.Resultados[0].Moneda != "USD" || lk.Resultados[0].HoyPct == 0 {
		t.Fatalf("precio por ISIN: %+v %s", lk, e)
	}
	if e := call(ana, "cartera_apuntar", map[string]any{"activo": "BTC", "tipo": "compra", "fecha": "2026-10-01", "cantidad": 0.1, "precio": 40000}, nil); !strings.Contains(e, "cartera_precio") {
		t.Fatalf("activo que no está: %q", e)
	}
	if e := call(ana, "cartera_apuntar", map[string]any{"activo": "kraken:XBTEUR", "tipo": "compra", "cantidad": 0.1, "precio": 40000}, nil); !strings.Contains(e, "falta la fecha") {
		t.Fatalf("sin fecha: %q", e)
	}
	var ap addOpOut
	if e := call(ana, "cartera_apuntar", map[string]any{"activo": "kraken:XBTEUR", "tipo": "compra", "fecha": "2026-10-01", "cantidad": 0.1, "precio": 40000, "comision": 4, "plataforma": "kraken"}, &ap); e != "" {
		t.Fatal(e)
	}
	if ap.Apuntada.Activo != "BTC" || ap.Apuntada.Plataforma != "Kraken" || ap.Posicion.Cantidad != 0.1 || ap.Posicion.CosteEUR != 4004 || ap.Posicion.ValorEUR != 6000 {
		t.Fatalf("apuntada: %+v", ap)
	}
	if e := call(ana, "cartera_apuntar", map[string]any{"activo": "btc", "tipo": "venta", "fecha": "2026-10-02", "cantidad": 0.05}, nil); !strings.Contains(e, "Falta el precio") {
		t.Fatalf("venta sin precio: %q", e)
	}
	if e := call(ana, "cartera_apuntar", map[string]any{"activo": "btc", "tipo": "recompensa", "fecha": "2026-10-20", "cantidad": 0.001}, nil); e != "" {
		t.Fatalf("recompensa con el precio del histórico: %s", e)
	}

	// Resumen, detalle y operaciones.
	var sum summaryOut
	if e := call(ana, "cartera_resumen", nil, &sum); e != "" || len(sum.Posiciones) != 1 || sum.Posiciones[0].Simbolo != "BTC" || sum.ValorEUR != 6060 ||
		sum.Reparto["plataforma"][0].Nombre != "Kraken" {
		t.Fatalf("resumen: %+v %s", sum, e)
	}
	var det assetOut
	if e := call(ana, "cartera_activo", map[string]any{"activo": "btc", "dias": 90}, &det); e != "" || len(det.Operaciones) != 2 || len(det.Historico) == 0 || len(det.Historico) > 61 {
		t.Fatalf("detalle: %d operaciones, %d cierres %s", len(det.Operaciones), len(det.Historico), e)
	}
	var ops opsOut
	if e := call(ana, "cartera_operaciones", map[string]any{"desde": "2026-10-10"}, &ops); e != "" || ops.Total != 1 || ops.Operaciones[0].Tipo != "recompensa" {
		t.Fatalf("operaciones: %+v %s", ops, e)
	}

	// Seguimiento y alertas.
	var w watchOut
	if e := call(ana, "cartera_seguimiento", map[string]any{"anadir": "coingecko:ejemplo-coin"}, &w); e != "" || len(w.Seguimiento) != 1 || w.Seguimiento[0].Simbolo != "EJC" {
		t.Fatalf("seguir: %+v %s", w, e)
	}
	var al alertOut
	if e := call(ana, "cartera_alerta_crear", map[string]any{"activo": "BTC", "tipo": "encima", "umbral": 70000}, &al); e != "" || al.Descripcion != "BTC por encima de 70.000 € (una vez)" {
		t.Fatalf("alerta: %+v %s", al, e)
	}
	if e := call(ana, "cartera_alerta_crear", map[string]any{"tipo": "caida", "umbral": 10}, nil); !strings.Contains(e, "solo vale para un activo") {
		t.Fatalf("caída de la cartera: %q", e)
	}
	var als alertsListOut
	if e := call(ana, "cartera_alertas", nil, &als); e != "" || len(als.Alertas) != 1 {
		t.Fatalf("alertas: %+v %s", als, e)
	}

	// Luis no ve ni toca nada de ana.
	sum = summaryOut{}
	if e := call(luis, "cartera_resumen", nil, &sum); e != "" || len(sum.Posiciones) != 0 || sum.ValorEUR != 0 {
		t.Fatalf("luis ve la cartera de ana: %+v", sum)
	}
	if e := call(luis, "cartera_activo", map[string]any{"activo": "BTC"}, nil); e == "" {
		t.Fatal("luis ve BTC de ana")
	}
	if e := call(luis, "cartera_alerta_borrar", map[string]any{"id": als.Alertas[0].ID}, nil); !strings.Contains(e, "no existe") {
		t.Fatalf("luis borra una alerta de ana: %q", e)
	}
	var del okOut
	if e := call(ana, "cartera_alerta_borrar", map[string]any{"id": als.Alertas[0].ID}, &del); e != "" || del.Borrada.ID != als.Alertas[0].ID {
		t.Fatalf("borrar alerta: %+v %s", del, e)
	}
}
