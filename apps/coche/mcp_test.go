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
	server := mcp.NewServer(&mcp.Implementation{Name: "coche", Version: "test"}, nil)
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
	var raw []string // todo lo que devuelven, para buscar la matrícula
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
		b, _ := json.Marshal(res.StructuredContent)
		raw = append(raw, string(b))
		if out != nil {
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
	for _, n := range []string{"coche_repostaje_apuntar", "coche_resumen", "coche_mantenimiento", "coche_mantenimiento_hecho", "coche_gasolineras", "coche_gasto_apuntar"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if e := call(nadie, "coche_resumen", nil, nil); !strings.Contains(e, "no está asociado") {
		t.Fatalf("sin persona: %q", e)
	}
	if e := call(ana, "coche_repostaje_apuntar", map[string]any{"km": 1000, "litros": 40, "precio_litro": 1.5}, nil); !strings.Contains(e, "Aún no hay ningún vehículo") {
		t.Fatalf("sin vehículo: %q", e)
	}
	vid, err := svc.addVehicle(ctx, "ana", vehicleInput{Name: "Coche de prueba", Plate: "0000TST", Fuel: "diesel", StartKm: 60000})
	if err != nil {
		t.Fatal(err)
	}

	// Repostajes: sin km, error para que pregunte; dos llenados dan consumo.
	if e := call(ana, "coche_repostaje_apuntar", map[string]any{"litros": 40, "precio_litro": 1.5}, nil); !strings.Contains(e, "km") {
		t.Fatalf("sin km: %q", e)
	}
	var added refuelAdded
	if e := call(ana, "coche_repostaje_apuntar", map[string]any{"fecha": "2026-10-01", "km": 61000, "litros": 45, "precio_litro": 1.45, "gasolinera": "Gasolinera de la esquina"}, &added); e != "" {
		t.Fatal(e)
	}
	if !added.Apuntado.Lleno || added.Apuntado.Total != 65.25 || added.Apuntado.Unidad != "l" {
		t.Fatalf("primer repostaje: %+v", added)
	}
	if e := call(ana, "coche_repostaje_apuntar", map[string]any{"vehiculo": "coche de prueba", "fecha": "2026-11-01", "km": 61800, "litros": 48, "total": 68.16}, &added); e != "" {
		t.Fatal(e)
	}
	if added.Apuntado.Consumo != 6 || added.ConsumoMedio != 6 || !near(added.Apuntado.Precio, 1.42) {
		t.Fatalf("consumo: %+v", added)
	}
	refuels, _ := svc.store.refuels(ctx, "ana", vid)
	if len(refuels) != 2 || refuels[0].Origin != originAssistant {
		t.Fatalf("origen: %+v", refuels)
	}

	// Mantenimiento: crear la ITV y el aceite, listar y marcar hecho.
	var to tasksOut
	if e := call(ana, "coche_mantenimiento", map[string]any{"anadir": "ITV", "proxima_fecha": "2026-11-20"}, &to); e != "" {
		t.Fatal(e)
	}
	if to.Creada == nil || to.Creada.Nombre != "ITV" || to.Creada.Tipo != "itv" || to.Creada.Estado != dueSoon || *to.Creada.DiasRest != 14 {
		t.Fatalf("ITV: %+v", to.Creada)
	}
	if e := call(ana, "coche_mantenimiento", map[string]any{"anadir": "Cambio de aceite", "ultimos_km": 50000, "ultima_fecha": "2025-11-01"}, &to); e != "" {
		t.Fatal(e)
	}
	if to.Creada.Tipo != "aceite" || to.Creada.Estado != dueOver || len(to.Tareas) != 2 || to.Tareas[0].Nombre != "Cambio de aceite" {
		t.Fatalf("aceite: %+v", to)
	}
	to = tasksOut{}
	if e := call(ana, "coche_mantenimiento", nil, &to); e != "" || len(to.Tareas) != 2 || to.Creada != nil || to.Tareas[1].Cada != "cada 2 años" {
		t.Fatalf("listar: %+v %s", to, e)
	}
	if e := call(ana, "coche_mantenimiento_hecho", map[string]any{"tarea": "neumáticos"}, nil); !strings.Contains(e, "No hay ninguna tarea así") {
		t.Fatalf("tarea que no hay: %q", e)
	}
	var done doneOut
	if e := call(ana, "coche_mantenimiento_hecho", map[string]any{"tarea": "aceite", "km": 61900, "coste": 95, "nota": "Taller de prueba"}, &done); e != "" {
		t.Fatal(e)
	}
	if done.Tarea.Estado != dueOK || done.Tarea.UltKm != 61900 || done.Tarea.ProxKm != 76900 || done.Gasto.Importe != 95 || done.Gasto.Tipo != "mantenimiento" {
		t.Fatalf("hecho: %+v", done)
	}

	// Gastos.
	var eo expenseOut
	if e := call(ana, "coche_gasto_apuntar", map[string]any{"tipo": "peajes", "importe": 3.5, "nota": "Autopista de ejemplo"}, &eo); e != "" || eo.Tipo != "peaje" || eo.Fecha != "2026-11-06" {
		t.Fatalf("peaje: %+v %s", eo, e)
	}
	if e := call(ana, "coche_gasto_apuntar", map[string]any{"tipo": "seguro"}, nil); !strings.Contains(e, "importe") {
		t.Fatalf("sin importe: %q", e)
	}

	// Resumen.
	var sum summaryOut
	if e := call(ana, "coche_resumen", nil, &sum); e != "" {
		t.Fatal(e)
	}
	if len(sum.Vehiculos) != 1 {
		t.Fatalf("resumen: %+v", sum)
	}
	v := sum.Vehiculos[0]
	if v.ConsumoMedio != 6 || v.KmUltimos != 61900 || v.Combustible != "Diésel" || len(v.Mantenimiento) != 2 || len(v.Repostajes) != 2 ||
		len(v.Gastos) != 2 || v.MesActual != 68.16+95+3.5 || len(v.Meses) != 2 {
		t.Fatalf("resumen: %+v", v)
	}

	// Gasolineras: las de su municipio y carburante (diésel, por el
	// vehículo), y las de otro municipio y carburante si lo pide.
	var g gasOut
	if e := call(ana, "coche_gasolineras", nil, &g); e != "" {
		t.Fatal(e)
	}
	if g.Municipio != "Valencia" || g.Carburante != "Gasóleo A (diésel)" || len(g.Gasolineras) != 3 || g.Gasolineras[0].Rotulo != "LA DEL PUEBLO" ||
		g.Gasolineras[0].Precio != 1.299 || g.Actualizado != "2026-11-06 11:30" {
		t.Fatalf("gasolineras: %+v", g)
	}
	if e := call(ana, "coche_gasolineras", map[string]any{"municipio": "madrid", "combustible": "gasolina 95", "cuantas": 1}, &g); e != "" ||
		g.Municipio != "Madrid" || g.Carburante != "Gasolina 95 E5" || len(g.Gasolineras) != 1 {
		t.Fatalf("en Madrid: %+v %s", g, e)
	}
	if set, _ := svc.store.settings(ctx, "ana"); set.Municipio.ID != defaultMunicipio.ID {
		t.Fatal("preguntar por otro municipio no cambia el suyo")
	}
	if e := call(ana, "coche_gasolineras", map[string]any{"combustible": "queroseno"}, nil); !strings.Contains(e, "No conozco ese carburante") {
		t.Fatalf("carburante raro: %q", e)
	}
	if e := call(ana, "coche_gasolineras", map[string]any{"municipio": "zzzz"}, nil); !strings.Contains(e, "No encuentro ese municipio") {
		t.Fatalf("municipio raro: %q", e)
	}

	// luis no ve nada de ana.
	if e := call(luis, "coche_resumen", nil, nil); !strings.Contains(e, "Aún no hay ningún vehículo") {
		t.Fatalf("luis: %q", e)
	}
	if e := call(luis, "coche_mantenimiento_hecho", map[string]any{"vehiculo": "1", "tarea": "aceite"}, nil); !strings.Contains(e, "Aún no hay ningún vehículo") {
		t.Fatalf("luis marca: %q", e)
	}

	// La matrícula nunca sale por aquí.
	for _, r := range raw {
		if strings.Contains(r, "0000TST") || strings.Contains(r, "matricula") {
			t.Fatalf("sale la matrícula: %s", r)
		}
	}
}
