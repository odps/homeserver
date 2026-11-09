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
	svc, f, c := newTestService(t)
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "casa", Version: "test"}, nil)
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
	for _, n := range []string{"casa_luz", "casa_mantenimiento", "casa_mantenimiento_hecho", "casa_mantenimiento_crear", "casa_lectura_apuntar", "casa_consumos"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if e := call(nadie, "casa_luz", nil, nil); !strings.Contains(e, "no está asociado") {
		t.Fatalf("sin persona: %q", e)
	}

	// Luz: sin precios aún, y con los de hoy y mañana.
	var lz luzOut
	if e := call(ana, "casa_luz", nil, &lz); e != "" || lz.Hoy != nil || !strings.Contains(lz.Nota, "Aún no están") {
		t.Fatalf("sin precios: %+v %s", lz, e)
	}
	svc.refreshPrices(ctx)
	f.set(func(f *fakeREE) { f.published["2026-11-10"] = true })
	c.set(at("2026-11-09", 20, 45))
	svc.refreshPrices(ctx)
	c.set(testNow())
	lz = luzOut{}
	if e := call(ana, "casa_luz", map[string]any{"dia": "hoy"}, &lz); e != "" || lz.Hoy == nil || lz.Manana != nil {
		t.Fatalf("hoy: %+v %s", lz, e)
	}
	h := lz.Hoy
	if h.Fecha != "2026-11-09" || h.Min != 0.087 || h.Max != 0.395 || h.Ahora == nil || h.Ahora.Hora != "12-13" || h.Ahora.Precio != 0.203 ||
		h.Bloque3h == nil || h.Bloque3h.Desde != "13:00" || h.Bloque3h.Hasta != "16:00" || h.Bloque3h.Media != 0.135 || h.MasBaratas[0].Hora != "14-15" ||
		len(h.Horas) != 24 || h.Horas[23].Hora != "23-24" || h.MasCaras[0].Tramo != "cara" {
		t.Fatalf("hoy: %+v", h)
	}
	lz = luzOut{}
	if e := call(ana, "casa_luz", map[string]any{"dia": "mañana"}, &lz); e != "" || lz.Hoy != nil || lz.Manana == nil || lz.Manana.Ahora != nil ||
		lz.Manana.Bloque2h.Desde != "12:00" || lz.Manana.Min != 0.05 {
		t.Fatalf("mañana: %+v %s", lz, e)
	}

	// Mantenimiento.
	var tl tasksOut
	if e := call(ana, "casa_mantenimiento", nil, &tl); e != "" || len(tl.Tareas) != 0 || len(tl.Sugerencias) != len(suggestions) {
		t.Fatalf("vacío: %+v %s", tl, e)
	}
	var tr taskResult
	if e := call(ana, "casa_mantenimiento_crear", map[string]any{"sugerencia": "caldera", "ultima": "2026-02-15"}, &tr); e != "" || tr.Tarea.Proxima != "2027-02-15" || tr.Tarea.Estado != stOK {
		t.Fatalf("sugerencia: %+v %s", tr, e)
	}
	if e := call(ana, "casa_mantenimiento_crear", map[string]any{"nombre": "Limpiar el filtro de la campana", "cada": 2, "unidad": "meses"}, &tr); e != "" || tr.Tarea.Estado != stToday || tr.Tarea.Cada != "cada 2 meses" {
		t.Fatalf("propia: %+v %s", tr, e)
	}
	if e := call(ana, "casa_mantenimiento_crear", map[string]any{"nombre": "Algo", "cada": 2, "unidad": "lustros"}, nil); !strings.Contains(e, "Elige cada cuánto") {
		t.Fatalf("unidad rara: %q", e)
	}
	if e := call(ana, "casa_mantenimiento_crear", map[string]any{"nombre": "Algo", "cada": 1, "unidad": "años", "ultima": "ayer"}, nil); !strings.Contains(e, "AAAA-MM-DD") {
		t.Fatalf("fecha rara: %q", e)
	}
	tl = tasksOut{}
	if e := call(ana, "casa_mantenimiento", nil, &tl); e != "" || len(tl.Tareas) != 2 || tl.Hoy != 1 || tl.Tareas[0].Nombre != "Limpiar el filtro de la campana" || len(tl.Sugerencias) != len(suggestions)-1 {
		t.Fatalf("lista: %+v %s", tl, e)
	}
	if e := call(ana, "casa_mantenimiento_hecho", map[string]any{"tarea": "campana"}, &tr); e != "" || tr.Tarea.Proxima != "2027-01-09" || tr.Tarea.Ultima != "2026-11-09" {
		t.Fatalf("hecha: %+v %s", tr, e)
	}
	if e := call(ana, "casa_mantenimiento_hecho", map[string]any{"tarea": "lavadora"}, nil); !strings.Contains(e, "no tiene ninguna tarea así") {
		t.Fatalf("tarea que no hay: %q", e)
	}
	if e := call(ana, "casa_mantenimiento_crear", map[string]any{"nombre": "Limpiar el filtro del lavavajillas", "cada": 1, "unidad": "meses"}, nil); e != "" {
		t.Fatal(e)
	}
	if e := call(ana, "casa_mantenimiento_hecho", map[string]any{"tarea": "filtro"}, nil); !strings.Contains(e, "hay varias que encajan") {
		t.Fatalf("ambigua: %q", e)
	}

	// Lecturas.
	var ar addReadingOut
	if e := call(ana, "casa_lectura_apuntar", map[string]any{"contador": "agua", "valor": 300.5, "fecha": "2026-10-10"}, &ar); e != "" || ar.DesdeAntes != nil || ar.Contador.Unidad != "m³" {
		t.Fatalf("primera lectura: %+v %s", ar, e)
	}
	ar = addReadingOut{}
	if e := call(ana, "casa_lectura_apuntar", map[string]any{"contador": "agua", "valor": 309.5}, &ar); e != "" || ar.DesdeAntes == nil || ar.DesdeAntes.Consumo != 9 || ar.DesdeAntes.PorDia != 0.3 || ar.Apuntada.Fecha != "2026-11-09" {
		t.Fatalf("segunda lectura: %+v %s", ar, e)
	}
	if e := call(ana, "casa_lectura_apuntar", map[string]any{"contador": "vapor", "valor": 1}, nil); !strings.Contains(e, "luz, agua o gas") {
		t.Fatalf("contador raro: %q", e)
	}
	var us usageListOut
	if e := call(ana, "casa_consumos", nil, &us); e != "" || len(us.Contadores) != 3 || us.Contadores[1].MediaDiaria != 0.3 || us.Contadores[1].Ultima.Valor != 309.5 {
		t.Fatalf("consumos: %+v %s", us, e)
	}
	us = usageListOut{}
	if e := call(ana, "casa_consumos", map[string]any{"contador": "agua"}, &us); e != "" || len(us.Contadores) != 1 || len(us.Contadores[0].Periodos) != 1 {
		t.Fatalf("consumos del agua: %+v %s", us, e)
	}

	// Luis no ve ni toca nada de ana.
	tl = tasksOut{}
	if e := call(luis, "casa_mantenimiento", nil, &tl); e != "" || len(tl.Tareas) != 0 {
		t.Fatalf("luis ve las tareas de ana: %+v", tl)
	}
	if e := call(luis, "casa_mantenimiento_hecho", map[string]any{"tarea": "caldera"}, nil); !strings.Contains(e, "no tiene ninguna tarea así") {
		t.Fatalf("luis da por hecha la de ana: %q", e)
	}
	us = usageListOut{}
	if e := call(luis, "casa_consumos", nil, &us); e != "" || us.Contadores[1].Lecturas != 0 {
		t.Fatalf("luis ve las lecturas de ana: %+v", us)
	}
}
