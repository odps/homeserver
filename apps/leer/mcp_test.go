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
	svc, s, _ := newTestService(t)
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "leer", Version: "test"}, nil)
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
	for _, n := range []string{"leer_guardar", "leer_lista", "leer_buscar", "leer_articulo", "leer_marcar", "leer_borrar"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if msg := call(nadie, "leer_lista", nil, nil); !strings.Contains(msg, "no está asociado a ninguna persona") {
		t.Fatalf("sin persona: %q", msg)
	}

	// Guardar.
	var saved saveOut
	if msg := call(ana, "leer_guardar", map[string]any{"url": s.url("/articulo/pan"), "etiquetas": []string{"cocina"}}, &saved); msg != "" {
		t.Fatal(msg)
	}
	if saved.ID == 0 || saved.YaEstaba || !saved.Leido || saved.Titulo != "Pan de masa madre en casa" || saved.Sitio != "Cocina de Ejemplo" ||
		saved.Minutos < 1 || saved.Etiquetas[0] != "cocina" {
		t.Fatalf("guardar: %+v", saved)
	}
	var again saveOut
	call(ana, "leer_guardar", map[string]any{"url": s.url("/articulo/pan")}, &again)
	if !again.YaEstaba || again.ID != saved.ID {
		t.Fatalf("otra vez: %+v", again)
	}
	var failed saveOut
	call(ana, "leer_guardar", map[string]any{"url": s.url("/pdf")}, &failed)
	if failed.Leido || !strings.Contains(failed.Error, "No es una página web") {
		t.Fatalf("un PDF: %+v", failed)
	}
	if msg := call(ana, "leer_guardar", map[string]any{"url": "http://169.254.169.254/latest/meta-data/"}, nil); !strings.Contains(msg, "red local") {
		t.Fatalf("dirección interna: %q", msg)
	}
	if msg := call(ana, "leer_guardar", map[string]any{"url": "file:///etc/passwd"}, nil); !strings.Contains(msg, "página web") {
		t.Fatalf("fichero: %q", msg)
	}
	call(ana, "leer_guardar", map[string]any{"url": s.url("/articulo/tomates"), "etiquetas": []string{"huerto"}}, nil)

	// Lista y búsqueda.
	var l listOut
	if msg := call(ana, "leer_lista", nil, &l); msg != "" {
		t.Fatal(msg)
	}
	if l.Lista != listPending || l.Total != 3 || l.PorLeer != 3 || len(l.Articulos) != 3 || l.Articulos[1].Error == "" || l.Articulos[0].Extracto == "" ||
		strings.Join(l.Etiquetas, ",") != "cocina,huerto" {
		t.Fatalf("lista: %+v", l)
	}
	call(ana, "leer_lista", map[string]any{"etiqueta": "Cocina"}, &l)
	if l.Total != 1 || l.Articulos[0].ID != saved.ID {
		t.Fatalf("por etiqueta: %+v", l)
	}
	if msg := call(ana, "leer_lista", map[string]any{"lista": "papelera"}, nil); !strings.Contains(msg, "la lista es") {
		t.Fatalf("lista rara: %q", msg)
	}
	call(ana, "leer_buscar", map[string]any{"texto": "masa madre"}, &l)
	if l.Total != 1 || l.Articulos[0].ID != saved.ID || l.Lista != listAll {
		t.Fatalf("buscar: %+v", l)
	}
	if msg := call(ana, "leer_buscar", map[string]any{"texto": " "}, nil); msg == "" {
		t.Fatal("buscar nada")
	}

	// El texto, para resumirlo.
	var art articleOut
	if msg := call(ana, "leer_articulo", map[string]any{"id": saved.ID}, &art); msg != "" {
		t.Fatal(msg)
	}
	if art.Titulo != "Pan de masa madre en casa" || art.Autor != "Luis Ejemplo" || art.Fecha != "2026-10-20" || art.URL != s.url("/articulo/pan") ||
		!strings.Contains(art.Texto, "## Lo básico de la masa madre") || strings.Contains(art.Texto, "<") && !strings.Contains(art.Texto, "<script>alert") || art.Recortado {
		t.Fatalf("artículo: %+v", art)
	}
	call(ana, "leer_articulo", map[string]any{"id": saved.ID, "max_caracteres": 50}, &art)
	if !art.Recortado || len([]rune(art.Texto)) > 51 {
		t.Fatalf("recortado: %d %v", len([]rune(art.Texto)), art.Recortado)
	}
	call(ana, "leer_articulo", map[string]any{"id": failed.ID}, &art)
	if !strings.Contains(art.Texto, "no se pudo leer") {
		t.Fatalf("sin texto: %q", art.Texto)
	}

	// Marcar.
	var it itemOut
	if msg := call(ana, "leer_marcar", map[string]any{"id": saved.ID, "archivado": true, "favorito": true, "etiquetas": []string{"pan", "receta"}}, &it); msg != "" {
		t.Fatal(msg)
	}
	if !it.Archivado || !it.Leido || !it.Favorito || strings.Join(it.Etiquetas, ",") != "pan,receta" {
		t.Fatalf("marcar: %+v", it)
	}
	if msg := call(ana, "leer_marcar", map[string]any{"id": saved.ID}, nil); !strings.Contains(msg, "di qué cambiar") {
		t.Fatalf("marcar nada: %q", msg)
	}
	call(ana, "leer_lista", map[string]any{"lista": "archivados"}, &l)
	if l.Total != 1 || l.Lista != listArchive {
		t.Fatalf("archivo: %+v", l)
	}

	// Lo de otro, no.
	if msg := call(luis, "leer_articulo", map[string]any{"id": saved.ID}, nil); !strings.Contains(msg, "no existe") {
		t.Fatalf("luis lee lo de ana: %q", msg)
	}
	if msg := call(luis, "leer_marcar", map[string]any{"id": saved.ID, "leido": false}, nil); !strings.Contains(msg, "no existe") {
		t.Fatalf("luis marca lo de ana: %q", msg)
	}
	if msg := call(luis, "leer_borrar", map[string]any{"id": saved.ID}, nil); !strings.Contains(msg, "no existe") {
		t.Fatalf("luis borra lo de ana: %q", msg)
	}
	call(luis, "leer_buscar", map[string]any{"texto": "masa"}, &l)
	if l.Total != 0 {
		t.Fatal("luis encuentra lo de ana")
	}

	// Borrar.
	var del deleteOut
	if msg := call(ana, "leer_borrar", map[string]any{"id": saved.ID}, &del); msg != "" || del.Borrado.ID != saved.ID {
		t.Fatalf("borrar: %+v %s", del, msg)
	}
	if msg := call(ana, "leer_articulo", map[string]any{"id": saved.ID}, nil); !strings.Contains(msg, "no existe") {
		t.Fatalf("tras borrar: %q", msg)
	}
}
