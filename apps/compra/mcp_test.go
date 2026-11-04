package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	server := mcp.NewServer(&mcp.Implementation{Name: "compra", Version: "test"}, nil)
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
	call := func(sess *mcp.ClientSession, name string, args map[string]any, out any) bool {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != nil && !res.IsError {
			b, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		return res.IsError
	}
	ana, luis := connect("ana"), connect("luis")

	tools, err := ana.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"compra_anadir", "compra_lista", "compra_tachar", "compra_quitar", "compra_terminar"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}

	// Añadir: el texto tal cual.
	var add addOut
	if call(ana, "compra_anadir", map[string]any{"texto": "leche, huevos y 2 kg de patatas"}, &add) || len(add.Anadidas) != 3 {
		t.Fatalf("añadir: %+v", add)
	}
	if p := add.Anadidas[2]; p.Nombre != "patatas" || p.Cantidad != "2 kg" || p.Seccion != "Fruta y verdura" || p.Origen != "asistente" || p.Anadida != "2026-11-03" || p.ID == 0 {
		t.Fatalf("patatas: %+v", p)
	}
	add = addOut{}
	if call(ana, "compra_anadir", map[string]any{"texto": "leche y 1 kg de patatas"}, &add) || len(add.Anadidas) != 0 || len(add.YaEstaban) != 1 || len(add.Juntadas) != 1 || add.Juntadas[0].Cantidad != "3 kg" {
		t.Fatalf("repetidas: %+v", add)
	}
	if !call(ana, "compra_anadir", map[string]any{"texto": " , "}, nil) {
		t.Fatal("sin nada que añadir no da error")
	}

	// La lista, por secciones en su orden.
	var list listOut
	if call(ana, "compra_lista", nil, &list) || list.Total != 3 || len(list.Secciones) != 2 || list.Secciones[0].Seccion != "Fruta y verdura" || list.Secciones[1].Cosas[0].Nombre != "leche" {
		t.Fatalf("lista: %+v", list)
	}
	huevos := list.Secciones[1].Cosas[1]

	// Tachar por nombre y por id; repetir no es un error.
	var ch changedOut
	for range 2 {
		if call(ana, "compra_tachar", map[string]any{"nombre": "Patata"}, &ch) || len(ch.Cosas) != 1 || !ch.Cosas[0].Tachada {
			t.Fatalf("tachar: %+v", ch)
		}
	}
	if call(ana, "compra_tachar", map[string]any{"nombre": "patatas", "destachar": true}, &ch) || ch.Cosas[0].Tachada {
		t.Fatalf("destachar: %+v", ch)
	}
	if call(ana, "compra_tachar", map[string]any{"id": huevos.ID}, &ch) || ch.Cosas[0].Nombre != "huevos" || !ch.Cosas[0].Tachada {
		t.Fatalf("tachar por id: %+v", ch)
	}
	if !call(ana, "compra_tachar", map[string]any{"nombre": "caviar"}, nil) || !call(ana, "compra_tachar", nil, nil) {
		t.Fatal("tachar lo que no está no da error")
	}

	// Otra persona no ve ni toca nada.
	list = listOut{}
	if call(luis, "compra_lista", nil, &list) || list.Total != 0 || len(list.Secciones) != 0 {
		t.Fatalf("luis ve la lista: %+v", list)
	}
	if !call(luis, "compra_tachar", map[string]any{"id": huevos.ID, "destachar": true}, nil) || !call(luis, "compra_quitar", map[string]any{"nombre": "leche"}, nil) ||
		!call(luis, "compra_quitar", map[string]any{"id": huevos.ID}, nil) {
		t.Fatal("luis toca lo de ana")
	}
	var fin finishOut
	if call(luis, "compra_terminar", nil, &fin) || fin.AlHistorial != 0 {
		t.Fatalf("luis termina: %+v", fin)
	}
	// Sin persona detrás del token, nada.
	if !call(connect(""), "compra_lista", nil, nil) {
		t.Fatal("un token sin persona ve una lista")
	}

	// Quitar y terminar.
	if call(ana, "compra_quitar", map[string]any{"nombre": "leches"}, &ch) || ch.Cosas[0].Nombre != "leche" {
		t.Fatalf("quitar: %+v", ch)
	}
	list = listOut{}
	if call(ana, "compra_lista", map[string]any{"sin_tachadas": true}, &list) || list.Total != 2 || list.Tachadas != 1 || len(list.Secciones) != 1 || list.Secciones[0].Cosas[0].Nombre != "patatas" {
		t.Fatalf("sin tachadas: %+v", list)
	}
	if call(ana, "compra_terminar", nil, &fin) || fin.AlHistorial != 1 || fin.Quedan != 1 {
		t.Fatalf("terminar: %+v", fin)
	}
	if h, _ := svc.store.history(ctx, "ana"); len(h) != 1 || h[0].Name != "huevos" {
		t.Fatalf("historial: %+v", h)
	}
}
