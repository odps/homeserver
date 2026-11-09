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
	server := mcp.NewServer(&mcp.Implementation{Name: "series", Version: "test"}, nil)
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
	for _, n := range []string{"series_buscar", "series_seguir", "series_visto", "series_pendientes", "series_estrenos",
		"pelis_anadir", "pelis_lista", "juegos_anadir", "juegos_lista"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if e := call(nadie, "series_pendientes", nil, nil); !strings.Contains(e, "no está asociado") {
		t.Fatalf("sin persona: %q", e)
	}

	// Buscar y seguir.
	var found searchOut
	if e := call(ana, "series_buscar", map[string]any{"consulta": "ejemplo"}, &found); e != "" || len(found.Resultados) != 1 || found.Resultados[0].TVmazeID != 1001 ||
		found.Resultados[0].LaSigues || found.Resultados[0].Estado != "En emisión" || !strings.Contains(found.Fuente, "TVmaze") {
		t.Fatalf("buscar: %+v %s", found, e)
	}
	var fo followOut
	if e := call(ana, "series_seguir", map[string]any{"tvmaze_id": 1001, "estado": "viendo"}, &fo); e != "" || fo.Serie != "Serie de Ejemplo" || fo.Estado != "Viendo" ||
		fo.Siguiente == nil || fo.Siguiente.Codigo != "1x01" || fo.Emitidos != 5 {
		t.Fatalf("seguir: %+v %s", fo, e)
	}
	if e := call(ana, "series_seguir", map[string]any{"tvmaze_id": 1002, "estado": "quiero ver"}, nil); e != "" {
		t.Fatal(e)
	}
	if e := call(ana, "series_seguir", map[string]any{"serie": "otra serie", "valoracion": 4, "nota": "pinta bien"}, &fo); e != "" || fo.Valoracion != 4 || fo.Nota != "pinta bien" || !fo.YaLaSeguia || fo.Estado != "Quiero verla" {
		t.Fatalf("cambiar valoración: %+v %s", fo, e)
	}
	if e := call(ana, "series_seguir", map[string]any{"tvmaze_id": 1001, "estado": "regular"}, nil); !strings.Contains(e, "estado no válido") {
		t.Fatalf("estado raro: %q", e)
	}
	if e := call(ana, "series_seguir", map[string]any{}, nil); !strings.Contains(e, "falta") {
		t.Fatalf("sin serie: %q", e)
	}

	// Marcar vistos.
	var wo watchedOut
	if e := call(ana, "series_visto", map[string]any{"serie": "serie de ejemplo", "temporada": 1, "episodio": 3, "hasta": true}, &wo); e != "" || wo.Cambiados != 3 ||
		wo.Siguiente == nil || wo.Siguiente.Codigo != "2x01" || wo.Siguiente.Estreno != "estreno de la temporada 2" {
		t.Fatalf("hasta el 1x03: %+v %s", wo, e)
	}
	if e := call(ana, "series_visto", map[string]any{"serie": "Serie de Ejemplo"}, &wo); e != "" || wo.Cambiados != 1 || wo.Siguiente.Codigo != "2x02" || wo.Siguiente.Hora != "03:00" || wo.Siguiente.Fecha != "2026-11-09" {
		t.Fatalf("el siguiente: %+v %s", wo, e)
	}
	if e := call(ana, "series_visto", map[string]any{"serie": "Serie de Ejemplo", "desmarcar": true}, &wo); e != "" || wo.Siguiente.Codigo != "2x01" {
		t.Fatalf("desmarcar el último: %+v %s", wo, e)
	}
	if e := call(ana, "series_visto", map[string]any{"serie": "Serie de Ejemplo", "temporada": 7, "episodio": 1}, nil); !strings.Contains(e, "no tiene el 7x1") {
		t.Fatalf("capítulo que no existe: %q", e)
	}
	if e := call(ana, "series_visto", map[string]any{"serie": "serie"}, nil); !strings.Contains(e, "varias") {
		t.Fatalf("ambigua: %q", e)
	}
	if e := call(ana, "series_visto", map[string]any{"serie": "Inexistente"}, nil); !strings.Contains(e, "no sigues") {
		t.Fatalf("no la sigue: %q", e)
	}

	// Lo que toca y lo que sale.
	var pend pendingOut
	if e := call(ana, "series_pendientes", nil, &pend); e != "" || len(pend.TeToca) != 1 || pend.TeToca[0].Quedan != 2 || len(pend.QuieroVer) != 1 {
		t.Fatalf("pendientes: %+v %s", pend, e)
	}
	var est airingOut
	if e := call(ana, "series_estrenos", map[string]any{"dias": 7}, &est); e != "" || est.Desde != "2026-11-09" || est.Hasta != "2026-11-15" || len(est.Capitulos) != 3 ||
		est.Capitulos[0].Serie != "Otra Serie Inventada" || est.Capitulos[0].Estreno == "" || est.Capitulos[2].Codigo != "2x03" || est.Capitulos[2].Fecha != "2026-11-12" {
		t.Fatalf("estrenos: %+v %s", est, e)
	}

	// Pelis y juegos.
	var io itemAddOut
	if e := call(ana, "pelis_anadir", map[string]any{"titulo": "Peli de Prueba", "anio": 2024, "estado": "quiero ver"}, &io); e != "" || io.Estado != "Quiero verla" || io.Actualizado {
		t.Fatalf("añadir peli: %+v %s", io, e)
	}
	if e := call(ana, "pelis_anadir", map[string]any{"titulo": "peli de prueba", "estado": "vista", "valoracion": 4}, &io); e != "" || !io.Actualizado || io.Estado != "Vista" || io.Fecha != "2026-11-09" || io.Anio != 2024 {
		t.Fatalf("ponerla al día: %+v %s", io, e)
	}
	if e := call(ana, "pelis_anadir", map[string]any{"titulo": ""}, nil); !strings.Contains(e, "Falta el título") {
		t.Fatalf("sin título: %q", e)
	}
	if e := call(ana, "juegos_anadir", map[string]any{"titulo": "Juego de Prueba", "estado": "jugando", "plataforma": "Consola"}, &io); e != "" || io.Estado != "Jugando" {
		t.Fatalf("añadir juego: %+v %s", io, e)
	}
	var lo listOut
	if e := call(ana, "pelis_lista", nil, &lo); e != "" || len(lo.Lista) != 1 || lo.Lista[0].Valoracion != 4 {
		t.Fatalf("lista de pelis: %+v %s", lo, e)
	}
	if e := call(ana, "juegos_lista", map[string]any{"estado": "terminado"}, &lo); e != "" || len(lo.Lista) != 0 {
		t.Fatalf("juegos terminados: %+v %s", lo, e)
	}
	items, _ := svc.store.items(ctx, "ana", kindGame)
	if len(items) != 1 || items[0].Origin != "asistente" {
		t.Fatalf("origen: %+v", items)
	}

	// luis no ve nada de ana.
	if e := call(luis, "pelis_lista", nil, &lo); e != "" || len(lo.Lista) != 0 {
		t.Fatalf("luis ve las pelis de ana: %+v", lo)
	}
	pend = pendingOut{}
	if e := call(luis, "series_pendientes", nil, &pend); e != "" || len(pend.TeToca)+len(pend.QuieroVer) != 0 {
		t.Fatalf("luis ve las series de ana: %+v", pend)
	}
	if e := call(luis, "series_visto", map[string]any{"serie": "1"}, nil); !strings.Contains(e, "no sigues") {
		t.Fatalf("luis marca por id una de ana: %q", e)
	}

	// Dejar de seguir.
	if e := call(ana, "series_seguir", map[string]any{"serie": "Otra Serie Inventada", "dejar_de_seguir": true}, &fo); e != "" || !fo.Dejada {
		t.Fatalf("dejar: %+v %s", fo, e)
	}
}
