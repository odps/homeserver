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
	server := mcp.NewServer(&mcp.Implementation{Name: "habitos", Version: "test"}, nil)
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
	// call devuelve el texto del error si la tool falla; raw, el JSON tal cual.
	call := func(sess *mcp.ClientSession, name string, args map[string]any, out any) (string, string) {
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
			return "error: " + b.String(), ""
		}
		b, _ := json.Marshal(res.StructuredContent)
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		return "", string(b)
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
	for _, n := range []string{"habitos_hoy", "habitos_marcar", "habitos_crear", "habitos_estadisticas", "diario_apuntar", "diario_ver"} {
		if !have[n] {
			t.Errorf("falta la tool %s", n)
		}
	}
	if msg, _ := call(nadie, "habitos_hoy", nil, nil); !strings.Contains(msg, "no está asociado") {
		t.Fatalf("sin persona: %q", msg)
	}

	// Crear.
	var h habitOut
	if msg, _ := call(ana, "habitos_crear", map[string]any{"nombre": "Beber agua", "emoji": "💧", "objetivo": 8, "unidad": "vasos", "recordatorio": "10:00"}, &h); msg != "" ||
		h.Tipo != "cantidad" || h.Objetivo != 8 || h.Frecuencia != "Cada día" || h.Recordatorio != "10:00" {
		t.Fatalf("crear agua: %q %+v", msg, h)
	}
	if msg, _ := call(ana, "habitos_crear", map[string]any{"nombre": "Estirar", "dias": []string{"lunes", "jueves"}}, &h); msg != "" || h.Frecuencia != "L y J" || h.Tipo != "sí o no" {
		t.Fatalf("crear de días: %q %+v", msg, h)
	}
	if msg, _ := call(ana, "habitos_crear", map[string]any{"nombre": "Correr", "veces": 3}, &h); msg != "" || h.Frecuencia != "3 veces por semana" || h.RachaEn != "semanas" {
		t.Fatalf("crear semanal: %q %+v", msg, h)
	}
	if msg, _ := call(ana, "habitos_crear", map[string]any{"nombre": "Correr"}, nil); !strings.Contains(msg, "Ya tienes") {
		t.Fatalf("repetido: %q", msg)
	}
	if msg, _ := call(ana, "habitos_crear", map[string]any{"nombre": "Nadar", "dias": []string{"festivos"}}, nil); !strings.Contains(msg, "festivos") {
		t.Fatalf("día raro: %q", msg)
	}

	// Marcar: sumar, completar, fijar, deshacer y días de antes.
	var mo markOut
	if msg, _ := call(ana, "habitos_marcar", map[string]any{"nombre": "agua", "cantidad": 2}, &mo); msg != "" || mo.Habito.Cantidad != 2 || mo.Habito.Hecho {
		t.Fatalf("sumar: %q %+v", msg, mo)
	}
	call(ana, "habitos_marcar", map[string]any{"nombre": "agua", "cantidad": 3}, &mo)
	if mo.Habito.Cantidad != 5 {
		t.Fatalf("sumar otra vez: %+v", mo.Habito)
	}
	call(ana, "habitos_marcar", map[string]any{"nombre": "agua", "cantidad": 4, "fijar": true}, &mo)
	if mo.Habito.Cantidad != 4 {
		t.Fatalf("fijar: %+v", mo.Habito)
	}
	call(ana, "habitos_marcar", map[string]any{"nombre": "agua"}, &mo)
	if mo.Habito.Cantidad != 8 || !mo.Habito.Hecho || mo.Habito.Racha != 1 {
		t.Fatalf("completar: %+v", mo.Habito)
	}
	for _, f := range []string{"ayer", "2026-11-10"} {
		if msg, _ := call(ana, "habitos_marcar", map[string]any{"nombre": "agua", "fecha": f}, &mo); msg != "" {
			t.Fatalf("%s: %q", f, msg)
		}
	}
	if mo.Fecha != "2026-11-10" || mo.Habito.Racha != 3 {
		t.Fatalf("racha con días de antes: %+v", mo)
	}
	call(ana, "habitos_marcar", map[string]any{"nombre": "estirar"}, &mo)
	if !mo.Habito.Hecho {
		t.Fatalf("sí o no: %+v", mo.Habito)
	}
	call(ana, "habitos_marcar", map[string]any{"nombre": "estirar", "deshacer": true}, &mo)
	if mo.Habito.Hecho {
		t.Fatalf("deshacer: %+v", mo.Habito)
	}
	if msg, _ := call(ana, "habitos_marcar", map[string]any{"nombre": "agua", "fecha": "2027-01-01"}, nil); !strings.Contains(msg, "aún no ha llegado") {
		t.Fatalf("futuro: %q", msg)
	}
	if msg, _ := call(ana, "habitos_marcar", map[string]any{"nombre": "yoga"}, nil); !strings.Contains(msg, "Tienes: «Beber agua»") {
		t.Fatalf("no existe: %q", msg)
	}

	var today todayOut
	if msg, _ := call(ana, "habitos_hoy", nil, &today); msg != "" || len(today.Habitos) != 3 || today.DiarioEscrito || !today.Habitos[0].Hecho ||
		!today.Habitos[1].Toca || today.Habitos[2].Semana != "0 de 3 veces" {
		t.Fatalf("hoy: %q %+v", msg, today)
	}

	// El diario: lo que no se pasa se queda; la nota se añade y no se devuelve.
	var jo journalOut
	_, raw := call(ana, "diario_apuntar", map[string]any{"animo": 4, "sueno": 7.5, "nota": "Una nota inventada muy personal", "etiquetas": []string{"trabajo"}}, &jo)
	if jo.Animo != 4 || *jo.Sueno != 7.5 || !jo.TieneNota || strings.Contains(raw, "inventada") {
		t.Fatalf("apuntar: %s", raw)
	}
	_, raw = call(ana, "diario_apuntar", map[string]any{"energia": 2, "nota": "Y otra frase", "etiquetas": []string{"Trabajo", "paseo"}}, &jo)
	if jo.Animo != 4 || jo.Energia != 2 || len(jo.Etiquetas) != 2 || strings.Contains(raw, "frase") {
		t.Fatalf("apuntar más: %s", raw)
	}
	if e, _, _ := svc.store.entry(ctx, "ana", dayOf(testNow())); e.Note != "Una nota inventada muy personal\nY otra frase" || e.Origin != "asistente" {
		t.Fatalf("nota guardada: %+v", e)
	}
	if msg, _ := call(ana, "diario_apuntar", map[string]any{"animo": 7}, nil); !strings.Contains(msg, "de 1 a 5") {
		t.Fatalf("ánimo raro: %q", msg)
	}
	if msg, _ := call(ana, "diario_apuntar", map[string]any{"fecha": "ayer"}, nil); !strings.Contains(msg, "Di qué apuntar") {
		t.Fatalf("vacío: %q", msg)
	}
	call(ana, "diario_apuntar", map[string]any{"fecha": "ayer", "animo": 3, "nota": "Otra nota del día anterior"}, nil)

	var vo viewOut
	_, raw = call(ana, "diario_ver", map[string]any{"desde": "ayer", "hasta": "hoy"}, &vo)
	if len(vo.Dias) != 2 || !vo.Dias[0].TieneNota || vo.Dias[0].Nota != "" || strings.Contains(raw, "nota del día") || *vo.AnimoMedio != 3.5 {
		t.Fatalf("ver sin notas: %s", raw)
	}
	call(ana, "diario_ver", map[string]any{"desde": "2026-11-11", "hasta": "2026-11-12", "incluir_notas": true}, &vo)
	if vo.Dias[0].Nota != "Otra nota del día anterior" || !strings.Contains(vo.Dias[1].Nota, "Y otra frase") {
		t.Fatalf("ver con notas: %+v", vo)
	}
	if msg, _ := call(ana, "diario_ver", map[string]any{"incluir_notas": true}, nil); !strings.Contains(msg, "desde") {
		t.Fatalf("notas sin fechas: %q", msg)
	}
	if msg, _ := call(ana, "diario_ver", map[string]any{"desde": "2026-01-01", "hasta": "2026-11-12"}, nil); !strings.Contains(msg, "93 días") {
		t.Fatalf("demasiados días: %q", msg)
	}

	var so statsOut
	_, raw = call(ana, "habitos_estadisticas", map[string]any{}, &so)
	if len(so.Habitos) != 3 || so.Habitos[0].Racha != 3 || so.Habitos[0].Diario == nil || !so.Habitos[0].Diario.FaltanDatos || strings.Contains(raw, "inventada") {
		t.Fatalf("estadísticas: %s", raw)
	}
	call(ana, "habitos_estadisticas", map[string]any{"nombre": "agua"}, &so)
	if len(so.Habitos) != 1 || so.Habitos[0].Nombre != "Beber agua" {
		t.Fatalf("estadísticas de uno: %+v", so)
	}

	// Lo de ana no lo ve ni lo toca luis.
	if msg, _ := call(luis, "habitos_marcar", map[string]any{"nombre": "agua"}, nil); !strings.Contains(msg, "aún no tienes ninguno") {
		t.Fatalf("luis marca: %q", msg)
	}
	call(luis, "habitos_hoy", nil, &today)
	if len(today.Habitos) != 0 || today.DiarioEscrito {
		t.Fatalf("luis ve: %+v", today)
	}
	_, raw = call(luis, "diario_ver", map[string]any{"desde": "2026-11-11", "hasta": "2026-11-12", "incluir_notas": true}, &vo)
	if len(vo.Dias) != 0 || strings.Contains(raw, "nota") && strings.Contains(raw, "inventada") {
		t.Fatalf("luis lee el diario: %s", raw)
	}
}
