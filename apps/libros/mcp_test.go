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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
	"homeserver/platform/dbtest"
)

// asUser añade la identidad que pondría la pasarela.
type asUser struct{ user string }

func (u asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Auth-User", u.user)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession sirve las tools y se conecta como «ana». call falla el test si
// la tool da error.
func mcpSession(t *testing.T, a *api, ss seriesStore) (*mcp.ClientSession, func(string, map[string]any, any)) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "libros", Version: "test"}, nil)
	addTools(server, a, ss)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: asUser{"ana"}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, func(name string, args map[string]any, out any) {
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
}

func TestMCPSettingsTools(t *testing.T) {
	pool := dbtest.Migrated(t, migrations)
	st := store{pool}
	ctx := context.Background()
	sess, call := mcpSession(t, &api{store: st}, seriesStore{pool})

	var v settingsView
	call("libros_ajustes_ver", nil, &v)
	if v.TemaLector != "light" || v.TamanoLetra != 100 || v.Modo != "paginated" || v.IdiomaBusqueda != "es" || v.Visibilidad != "privado" || v.TemaApp != "system" {
		t.Fatalf("por defecto: %+v", v)
	}
	// Solo cambia lo que se manda.
	call("libros_ajustes_cambiar", map[string]any{"tema_lector": "sepia", "tamano_letra": 120}, &v)
	if v.TemaLector != "sepia" || v.TamanoLetra != 120 || v.Interlineado != 1.5 || !v.Justificar || v.IdiomaBusqueda != "es" {
		t.Fatalf("lector: %+v", v)
	}
	call("libros_ajustes_cambiar", map[string]any{"idioma_busqueda": "", "visibilidad": "compartido", "justificar": false}, &v)
	if v.IdiomaBusqueda != "" || v.Visibilidad != "compartido" || v.FuenteComics != "todas" || v.Justificar || v.TemaLector != "sepia" {
		t.Fatalf("preferencias: %+v", v)
	}
	d, _ := st.settings(ctx, app.Identity{User: "ana"})
	if d.Reader.Theme != "sepia" || d.Reader.Justify || d.UpdatedAt == nil || d.Prefs.Visibility != "compartido" || d.Prefs.SearchLang != "" {
		t.Fatalf("guardado: %+v", d)
	}
	// Lo que no vale no se guarda (ni lo demás de la misma llamada).
	for _, args := range []map[string]any{
		{"tamano_letra": 500},
		{"tema_lector": "rosa"},
		{"tema_app": "dark", "fuente_comics": "otra"},
		{"interlineado": 1.8, "idioma_busqueda": "xx"},
	} {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "libros_ajustes_cambiar", Arguments: args})
		if err != nil || !res.IsError {
			t.Fatalf("acepta %v: %v %+v", args, err, res)
		}
	}
	call("libros_ajustes_ver", nil, &v)
	if v.TamanoLetra != 120 || v.TemaLector != "sepia" || v.TemaApp != "system" || v.Interlineado != 1.5 {
		t.Fatalf("tras los errores: %+v", v)
	}
	// Un dispositivo con el reloj adelantado no impide el cambio.
	if _, err := st.saveSettings(ctx, app.Identity{User: "ana"}, d.Reader, time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	call("libros_ajustes_cambiar", map[string]any{"letra": "serif"}, &v)
	if v.Letra != "serif" || v.TemaLector != "sepia" {
		t.Fatalf("con un dispositivo adelantado: %+v", v)
	}
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
	ctx := context.Background()
	sess, call := mcpSession(t, a, seriesStore{pool})

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
	// Algunos modelos mandan objetos en vez de textos.
	call("libros_buscar", map[string]any{"varios": []any{map[string]any{"titulo": "Quijote", "autor": "Cervantes"}, "Maquiavelo"}}, &many)
	if len(many.PorBusqueda) != 2 || many.PorBusqueda[0].Texto != "Cervantes Quijote" || len(many.PorBusqueda[0].Libros) != 1 {
		t.Fatalf("varios con objetos: %+v", many)
	}

	// Un libro con copyright no se puede añadir.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "libros_anadir", Arguments: map[string]any{"fuente": "gutenberg", "id": "9"}})
	if err != nil || !res.IsError {
		t.Fatalf("libro con copyright: %v %+v", err, res)
	}
}
