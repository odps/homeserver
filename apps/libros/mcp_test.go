package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/dbtest"
)

// asUser añade la identidad que pondría la pasarela.
type asUser struct{ user string }

func (u asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Auth-User", u.user)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPCatalogTools(t *testing.T) {
	epub := zipBytes(t, map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/c.opf"/></rootfiles></container>`,
		"OEBPS/c.opf":            `<package><metadata><title>Don Quijote</title><language>es</language></metadata><manifest></manifest></package>`,
	})
	src := fakeSources(t, epub)
	pool := dbtest.Migrated(t, migrations)
	f, _ := newFiles(t.TempDir())
	a := &api{store: store{pool}, files: f, log: slog.New(slog.NewTextHandler(io.Discard, nil)), catalog: testCatalog(src)}
	server := mcp.NewServer(&mcp.Implementation{Name: "libros", Version: "test"}, nil)
	addTools(server, a, seriesStore{pool})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer srv.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: asUser{"ana"}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	call := func(name string, args map[string]any, out any) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: %v %s", name, err, b)
		}
	}

	// No lo tiene: la búsqueda lo dice y sugiere las bibliotecas libres.
	var none searchOut
	call("libros_buscar", map[string]any{"texto": "quijote"}, &none)
	if len(none.Libros) != 0 || !strings.Contains(none.Nota, "libros_catalogo_buscar") {
		t.Fatalf("sin libros: %+v", none)
	}
	var found catalogOut
	call("libros_catalogo_buscar", map[string]any{"texto": "quijote", "idioma": "es"}, &found)
	if len(found.Resultados) == 0 || found.Resultados[0].Fuente != "gutenberg" || found.Resultados[0].ID != "2000" {
		t.Fatalf("catálogo: %+v", found)
	}
	var added importOut
	call("libros_anadir", map[string]any{"fuente": "gutenberg", "id": "2000"}, &added)
	if added.Libro.Title != "Don Quijote" || added.YaLoTenia || added.Libro.Visibility != "privado" {
		t.Fatalf("añadir: %+v", added)
	}
	call("libros_anadir", map[string]any{"fuente": "gutenberg", "id": "2000"}, &added)
	if !added.YaLoTenia {
		t.Fatal("añadirlo dos veces no lo detecta")
	}

	// Varios a la vez, sin importar acentos, mayúsculas ni el orden de las palabras.
	var many searchOut
	call("libros_buscar", map[string]any{"varios": []string{"CERVÁNTES quijote", "Maquiavelo"}}, &many)
	if len(many.PorBusqueda) != 2 || len(many.PorBusqueda[0].Libros) != 1 || len(many.PorBusqueda[1].Libros) != 0 || many.Nota != "" {
		t.Fatalf("varios: %+v", many)
	}

	// Un libro con copyright no se puede añadir.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "libros_anadir", Arguments: map[string]any{"fuente": "gutenberg", "id": "9"}})
	if err != nil || !res.IsError {
		t.Fatalf("libro con copyright: %v %+v", err, res)
	}
}
