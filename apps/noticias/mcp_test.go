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
	r.Header.Set("X-Auth-User", u.user)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPSettingsTools(t *testing.T) {
	h := newHarness(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "noticias", Version: "test"}, nil)
	addTools(server, h.svc)
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
		reflect.ValueOf(out).Elem().SetZero() // que no quede nada de la respuesta anterior
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: %v %s", name, err, b)
		}
	}
	fails := func(name string, args map[string]any, want string) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !res.IsError {
			t.Fatalf("%s %v: debería fallar: %v %+v", name, args, err, res)
		}
		if b, _ := json.Marshal(res.Content); !strings.Contains(string(b), want) {
			t.Fatalf("%s: falta «%s» en el error: %s", name, want, b)
		}
	}
	active := func(l []optionView, key string) bool {
		for _, o := range l {
			if o.Clave == key {
				return o.Activa
			}
		}
		t.Fatalf("no está %s", key)
		return false
	}

	var v prefsView
	call("noticias_ajustes_ver", nil, &v)
	if v.Hora != "07:00" || !v.ResumenIA || v.Importantes != 10 || len(v.Medios) != 5 || len(v.Secciones) != 2 || len(v.Intereses) != 0 || !active(v.Medios, "a") {
		t.Fatalf("ajustes por defecto: %+v", v)
	}
	// Solo cambia lo que se manda.
	call("noticias_ajustes_cambiar", map[string]any{"hora": "6:30", "por_seccion": 4, "desactivar_medios": []string{"a"},
		"activar_secciones": []string{"hackernews"}, "anadir_subforos": []string{"r/golang", "https://www.reddit.com/r/worldnews/"}}, &v)
	if v.Hora != "06:30" || v.PorSeccion != 4 || !v.ResumenIA || v.Importantes != 10 || v.NoticiasMundo != 8 || !v.Mundo ||
		active(v.Medios, "a") || !active(v.Medios, "b") || !active(v.Secciones, "hackernews") || active(v.Secciones, "tecnologia") ||
		strings.Join(v.Subforos, ",") != "golang,worldnews" {
		t.Fatalf("tras cambiar: %+v", v)
	}
	// Las mismas comprobaciones que la web; si falla, no cambia nada.
	fails("noticias_ajustes_cambiar", map[string]any{"importantes": 50, "resumen_ia": false}, "de 3 a 30")
	fails("noticias_ajustes_cambiar", map[string]any{"hora": "25:00"}, "la hora como 07:00")
	fails("noticias_ajustes_cambiar", map[string]any{"activar_secciones": []string{"astrologia"}}, "astrologia")
	fails("noticias_ajustes_cambiar", map[string]any{"anadir_subforos": []string{"no vale!"}}, "no vale!")
	call("noticias_ajustes_cambiar", map[string]any{"quitar_subforos": []string{"WorldNews"}, "activar_medios": []string{"a"}}, &v)
	if v.Hora != "06:30" || !v.ResumenIA || !active(v.Medios, "a") || strings.Join(v.Subforos, ",") != "golang" {
		t.Fatalf("tras quitar: %+v", v)
	}
	p, _ := h.svc.store.prefs(ctx, "ana")
	if p.DigestAt != "06:30" || p.TopicCount != 4 || len(p.Disabled) != 0 || strings.Join(p.Presets, ",") != "hackernews" || strings.Join(p.Reddit, ",") != "golang" {
		t.Fatalf("guardado: %+v", p)
	}

	// Intereses y feeds propios.
	var in interestsOut
	call("noticias_interes_anadir", map[string]any{"nombre": "Programación", "busqueda": "golang"}, &in)
	if len(in.Intereses) != 1 || in.Intereses[0].Nombre != "Programación" || in.Intereses[0].Busqueda != "golang" {
		t.Fatalf("añadir interés: %+v", in)
	}
	id := in.Intereses[0].ID
	fails("noticias_interes_anadir", map[string]any{"nombre": "programacion"}, "ya tienes un interés con ese nombre")
	fails("noticias_interes_anadir", map[string]any{"nombre": "  "}, "ponle un nombre")
	call("noticias_feed_anadir", map[string]any{"interes_id": id, "url": h.news.URL + "/propio.xml"}, &in)
	if f := in.Intereses[0].Feeds; len(f) != 1 || f[0].Titulo != "Mi blog" {
		t.Fatalf("añadir feed: %+v", in)
	}
	fails("noticias_feed_anadir", map[string]any{"interes_id": id, "url": h.news.URL + "/no-existe"}, "no se ha podido leer el feed")
	call("noticias_interes_cambiar", map[string]any{"id": id, "nombre": "Go"}, &in)
	if in.Intereses[0].Nombre != "Go" || in.Intereses[0].Busqueda != "golang" || len(in.Intereses[0].Feeds) != 1 {
		t.Fatalf("renombrar: %+v", in)
	}
	call("noticias_feed_quitar", map[string]any{"id": in.Intereses[0].Feeds[0].ID}, &in)
	if len(in.Intereses[0].Feeds) != 0 {
		t.Fatalf("quitar feed: %+v", in)
	}
	fails("noticias_interes_cambiar", map[string]any{"id": id + 100, "nombre": "X"}, "no existe ese interés")
	call("noticias_interes_borrar", map[string]any{"id": id}, &in)
	if len(in.Intereses) != 0 {
		t.Fatalf("borrar interés: %+v", in)
	}
	fails("noticias_interes_borrar", map[string]any{"id": id}, "no existe ese interés")
}
