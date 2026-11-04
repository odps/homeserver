package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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

type mcpCaller struct {
	t    *testing.T
	sess *mcp.ClientSession
}

func newMCP(t *testing.T, svc *service, user string) *mcpCaller {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "papeles", Version: "test"}, nil)
	addTools(server, svc)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: asUser{user}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return &mcpCaller{t: t, sess: sess}
}

// call llama a la tool y devuelve el JSON de la respuesta.
func (c *mcpCaller) call(name string, args map[string]any, out any) string {
	c.t.Helper()
	res, err := c.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		b, _ := json.Marshal(res)
		c.t.Fatalf("%s: %v %s", name, err, b)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if out != nil {
		reflect.ValueOf(out).Elem().SetZero()
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s: %v %s", name, err, b)
		}
	}
	return string(b)
}

func (c *mcpCaller) fails(name string, args map[string]any, want string) {
	c.t.Helper()
	res, err := c.sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || !res.IsError {
		c.t.Fatalf("%s %v: debería fallar: %v %+v", name, args, err, res)
	}
	if b, _ := json.Marshal(res.Content); !strings.Contains(string(b), want) {
		c.t.Fatalf("%s: falta «%s» en el error: %s", name, want, b)
	}
}

func TestMCPTools(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	fa := newFakeAgenda(t)
	svc.agenda = fa.client()
	ana := newMCP(t, svc, "ana")

	tools, err := ana.sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if tl.Name == "papeles_borrar" && !strings.Contains(tl.Description, "confirme") {
			t.Errorf("papeles_borrar no pide confirmación: %s", tl.Description)
		}
	}
	if strings.Join(names, ",") != "papeles_borrar,papeles_guardar,papeles_lista,papeles_renovar,papeles_ver" {
		t.Fatalf("tools: %v", names)
	}

	// Crear: los avisos, los del tipo.
	var v docFull
	ana.call("papeles_guardar", map[string]any{"tipo": "itv", "titulo": "ITV del coche", "numero": "1234-BCD", "emisor": "Estación ITV Ejemplo",
		"caducidad": "2026-11-20", "cada_meses": 12, "importe": 45.5, "notas": "Llevar la ficha técnica"}, &v)
	if v.ID == 0 || v.Tipo != "itv" || v.TipoNombre != "ITV" || v.Numero != "1234-BCD" || len(v.Avisos) != 2 || v.Avisos[0] != 30 ||
		v.Estado != stSoon || v.Dias == nil || *v.Dias != 16 || v.Importe == nil || *v.Importe != 45.5 {
		t.Fatalf("crear: %+v", v)
	}
	itv := v.ID
	if s := fa.sent(); len(s) != 1 || strings.Contains(s[0], "1234-BCD") || strings.Contains(s[0], "ficha") {
		t.Fatalf("a la agenda: %v", s)
	}
	ana.call("papeles_guardar", map[string]any{"tipo": "dni", "titulo": "DNI", "titular": "Ana Pérez", "numero": "12345678Z", "caducidad": "2031-03-01", "avisos": []int{}}, &v)
	dni := v.ID
	if len(v.Avisos) != 0 || v.Estado != stValid {
		t.Fatalf("dni sin avisos: %+v", v)
	}
	ana.call("papeles_guardar", map[string]any{"tipo": "factura", "titulo": "Factura de la nevera"}, &v)
	ana.call("papeles_guardar", map[string]any{"tipo": "pasaporte", "titulo": "Pasaporte", "caducidad": "2026-01-10"}, &v)

	// Cambiar solo lo que llega.
	ana.call("papeles_guardar", map[string]any{"id": itv, "emisor": "Otra Estación", "importe": 0}, &v)
	if v.Emisor != "Otra Estación" || v.Numero != "1234-BCD" || v.Notas != "Llevar la ficha técnica" || v.CadaMeses != 12 || v.Importe != nil || len(v.Avisos) != 2 {
		t.Fatalf("mezclar: %+v", v)
	}
	ana.fails("papeles_guardar", map[string]any{"titulo": "Sin tipo"}, "falta el tipo")
	ana.fails("papeles_guardar", map[string]any{"tipo": "carnet_de_baile", "titulo": "X"}, "tipo no válido")
	ana.fails("papeles_guardar", map[string]any{"id": itv, "caducidad": "2020-01-01", "emision": "2021-01-01"}, "anterior a la emisión")
	ana.fails("papeles_guardar", map[string]any{"id": itv, "avisos": []int{1, 2, 3, 4, 5, 6}}, "Como mucho 5")

	// La lista: por defecto, lo próximo y lo caducado; sin número.
	var l listOut
	out := ana.call("papeles_lista", nil, &l)
	if l.Total != 4 || len(l.Documentos) != 2 || l.Documentos[0].Titulo != "Pasaporte" || l.Documentos[0].Estado != stExpired || l.Documentos[1].ID != itv {
		t.Fatalf("lista: %s", out)
	}
	if strings.Contains(out, "numero") || strings.Contains(out, "12345678Z") || strings.Contains(out, "1234-BCD") {
		t.Fatalf("la lista lleva números: %s", out)
	}
	ana.call("papeles_lista", map[string]any{"todos": true}, &l)
	if len(l.Documentos) != 4 || l.Documentos[3].Caducidad != "" {
		t.Fatalf("todos: %+v", l)
	}
	ana.call("papeles_lista", map[string]any{"dias": 2000}, &l)
	if len(l.Documentos) != 3 {
		t.Fatalf("2000 días: %+v", l)
	}
	ana.call("papeles_lista", map[string]any{"todos": true, "tipo": "dni"}, &l)
	if len(l.Documentos) != 1 || l.Documentos[0].ID != dni {
		t.Fatalf("por tipo: %+v", l)
	}
	ana.fails("papeles_lista", map[string]any{"tipo": "nada"}, "tipo no válido")

	// La ficha, con el número; los archivos solo se cuentan.
	if _, err := svc.addFile(ctx, "ana", dni, "dni-escaneo-secreto.pdf", []byte(testPDF)); err != nil {
		t.Fatal(err)
	}
	out = ana.call("papeles_ver", map[string]any{"id": dni}, &v)
	if v.Numero != "12345678Z" || v.Titular != "Ana Pérez" || v.Archivos != 1 || !strings.Contains(v.ArchivosVer, "solo se ven en la web") {
		t.Fatalf("ver: %s", out)
	}
	if strings.Contains(out, "dni-escaneo") || strings.Contains(out, "PDF-1.4") || strings.Contains(out, "archivo/") {
		t.Fatalf("salen los archivos: %s", out)
	}

	// Renovar.
	ana.call("papeles_renovar", map[string]any{"id": itv}, &v)
	if v.Caducidad != "2027-11-20" || v.Estado != stValid {
		t.Fatalf("renovar: %+v", v)
	}
	ana.fails("papeles_renovar", map[string]any{"id": dni}, "fecha de caducidad nueva")
	ana.call("papeles_renovar", map[string]any{"id": dni, "nueva_caducidad": "2041-03-01"}, &v)
	if v.Caducidad != "2041-03-01" {
		t.Fatalf("renovar con fecha: %+v", v)
	}

	// Otro no ve ni toca nada; sin identidad, nada.
	luis := newMCP(t, svc, "luis")
	luis.call("papeles_lista", map[string]any{"todos": true}, &l)
	if len(l.Documentos) != 0 || l.Total != 0 {
		t.Fatalf("luis ve: %+v", l)
	}
	luis.fails("papeles_ver", map[string]any{"id": dni}, "no existe")
	luis.fails("papeles_guardar", map[string]any{"id": dni, "titulo": "Mío"}, "no existe")
	luis.fails("papeles_renovar", map[string]any{"id": itv}, "no existe")
	luis.fails("papeles_borrar", map[string]any{"id": dni}, "no existe")
	newMCP(t, svc, "").fails("papeles_lista", nil, "no está asociado")

	// Borrar.
	var del deleteOut
	ana.call("papeles_borrar", map[string]any{"id": dni}, &del)
	if !del.Borrado || del.Titulo != "DNI" {
		t.Fatalf("borrar: %+v", del)
	}
	ana.fails("papeles_ver", map[string]any{"id": dni}, "no existe")
	if left, _ := svc.store.files(ctx, "ana", dni); len(left) != 0 {
		t.Fatal("quedan archivos")
	}
}
