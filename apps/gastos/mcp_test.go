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
	svc, _ := newTestService(t)
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "gastos", Version: "test"}, nil)
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
	for _, n := range []string{"gastos_apuntar", "gastos_lista", "gastos_resumen", "gastos_presupuesto", "gastos_suscripciones", "gastos_borrar"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if e := call(nadie, "gastos_resumen", nil, nil); !strings.Contains(e, "no está asociado") {
		t.Fatalf("sin persona: %q", e)
	}

	// Apuntar: la categoría la ponen las reglas, o la que diga.
	var ap addOut
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 23.4, "concepto": "Gasolina"}, &ap); e != "" || ap.Apuntado.Categoria != "Transporte" ||
		ap.Apuntado.Fecha != "2026-11-09" || ap.Apuntado.Origen != originAssistant || ap.Apuntado.Tipo != "gasto" {
		t.Fatalf("apuntar: %+v %s", ap, e)
	}
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 30, "concepto": "Cena", "categoria": "ocio", "fecha": "2026-11-07"}, &ap); e != "" || ap.Apuntado.Categoria != "Ocio" {
		t.Fatalf("con categoría: %+v %s", ap, e)
	}
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 30, "concepto": "Cena", "categoria": "caprichos"}, nil); !strings.Contains(e, "las que hay: ") {
		t.Fatalf("categoría que no existe: %q", e)
	}
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 0, "concepto": "Nada"}, nil); !strings.Contains(e, "Falta el importe") {
		t.Fatalf("sin importe: %q", e)
	}
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 1500, "concepto": "Nómina", "tipo": "ingreso", "fecha": "2026-11-01"}, nil); e != "" {
		t.Fatal(e)
	}
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 40, "concepto": "Supermercado", "fecha": "2026-10-15"}, nil); e != "" {
		t.Fatal(e)
	}

	// Presupuesto: ponerlo y ver cómo va; al apuntar, el de su categoría.
	var bo budgetOut
	if e := call(ana, "gastos_presupuesto", map[string]any{"categoria": "Transporte", "presupuesto": 50}, &bo); e != "" || bo.Cambiado != "Transporte: 50,00 € al mes" ||
		bo.Presupuesto != 50 || bo.Gastado != 23.4 || bo.Quedan != 26.6 || bo.DiasQuedan == nil || *bo.DiasQuedan != 21 {
		t.Fatalf("presupuesto: %+v %s", bo, e)
	}
	if e := call(ana, "gastos_apuntar", map[string]any{"importe": 40, "concepto": "Taxi"}, &ap); e != "" || ap.Presupuesto == nil || !ap.Presupuesto.Pasado ||
		*ap.Presupuesto.Quedan != -13.4 {
		t.Fatalf("presupuesto al apuntar: %+v %s", ap.Presupuesto, e)
	}

	// Lista y resumen.
	var ls listOut
	if e := call(ana, "gastos_lista", nil, &ls); e != "" || ls.Mes != "2026-11" || ls.Total != 4 || ls.Gastos != 93.4 || ls.Ingresos != 1500 {
		t.Fatalf("lista: %+v %s", ls, e)
	}
	ls = listOut{}
	if e := call(ana, "gastos_lista", map[string]any{"texto": "taxi"}, &ls); e != "" || ls.Total != 1 || ls.Movimientos[0].Concepto != "Taxi" {
		t.Fatalf("lista por texto: %+v %s", ls, e)
	}
	ls = listOut{}
	if e := call(ana, "gastos_lista", map[string]any{"mes": "2026-10", "categoria": "comida"}, &ls); e != "" || ls.Total != 1 {
		t.Fatalf("lista de octubre: %+v %s", ls, e)
	}
	if e := call(ana, "gastos_lista", map[string]any{"mes": "octubre"}, nil); !strings.Contains(e, "AAAA-MM") {
		t.Fatalf("mes mal: %q", e)
	}
	var sum summaryOut
	if e := call(ana, "gastos_resumen", nil, &sum); e != "" || sum.Gastos != 93.4 || sum.Balance != 1406.6 || sum.MesAnterior.Gastos != 40 ||
		sum.VariacionGastos == nil || len(sum.UltimosMeses) != 6 || len(sum.CategoriasPasada) != 1 || sum.PorCategoria[0].Categoria != "Transporte" {
		t.Fatalf("resumen: %+v %s", sum, e)
	}

	// Suscripciones: crear y registrar el cobro.
	var so subsOut
	if e := call(ana, "gastos_suscripciones", map[string]any{"nueva": map[string]any{"nombre": "Plataforma de ejemplo", "importe": 12.99, "periodicidad": "mensual"}}, nil); !strings.Contains(e, "proximo_cobro") {
		t.Fatalf("sin fecha: %q", e)
	}
	if e := call(ana, "gastos_suscripciones", map[string]any{"nueva": map[string]any{"nombre": "Plataforma de ejemplo", "importe": 12.99, "periodicidad": "mensual", "proximo_cobro": "2026-11-20"}}, &so); e != "" ||
		so.Creada == nil || so.Creada.Categoria != "Suscripciones" || *so.Creada.Aviso != 3 || so.AlMes != 12.99 || so.AlAno != 155.88 {
		t.Fatalf("crear suscripción: %+v %s", so, e)
	}
	sid := so.Creada.ID
	so = subsOut{}
	if e := call(ana, "gastos_suscripciones", map[string]any{"registrar_cobro": sid}, &so); e != "" || so.Registrado == nil || so.Registrado.Importe != 12.99 ||
		so.Suscripciones[0].Proximo != "2026-12-20" {
		t.Fatalf("registrar cobro: %+v %s", so, e)
	}

	// Luis no ve ni toca nada de ana.
	ls = listOut{}
	if e := call(luis, "gastos_lista", nil, &ls); e != "" || ls.Total != 0 {
		t.Fatalf("luis ve los movimientos de ana: %+v", ls)
	}
	so = subsOut{}
	if e := call(luis, "gastos_suscripciones", nil, &so); e != "" || len(so.Suscripciones) != 0 {
		t.Fatalf("luis ve las suscripciones de ana: %+v", so)
	}
	if e := call(luis, "gastos_suscripciones", map[string]any{"registrar_cobro": sid}, nil); !strings.Contains(e, "no hay ninguna suscripción") {
		t.Fatalf("luis registra un cobro de ana: %q", e)
	}
	if e := call(luis, "gastos_borrar", map[string]any{"id": ap.Apuntado.ID}, nil); !strings.Contains(e, "no existe") {
		t.Fatalf("luis borra un movimiento de ana: %q", e)
	}
	if e := call(luis, "gastos_borrar", map[string]any{"id": sid, "que": "suscripcion"}, nil); !strings.Contains(e, "no existe") {
		t.Fatalf("luis borra una suscripción de ana: %q", e)
	}

	// Borrar.
	var del delOut
	if e := call(ana, "gastos_borrar", map[string]any{"id": ap.Apuntado.ID}, &del); e != "" || !del.Borrado || del.Descripcion != "gasto de 40,00 € · Taxi · 2026-11-09" {
		t.Fatalf("borrar: %+v %s", del, e)
	}
	if e := call(ana, "gastos_borrar", map[string]any{"id": sid, "que": "suscripcion"}, &del); e != "" || del.Descripcion != "Plataforma de ejemplo · 12,99 € mensual" {
		t.Fatalf("borrar suscripción: %+v %s", del, e)
	}
	if e := call(ana, "gastos_borrar", map[string]any{"id": 1, "que": "otra"}, nil); !strings.Contains(e, "movimiento o suscripcion") {
		t.Fatalf("qué borrar: %q", e)
	}
}
