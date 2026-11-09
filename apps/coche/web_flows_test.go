package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// ---- Flujos por HTTP con la base de datos de pruebas ----

type webEnv struct {
	t   *testing.T
	svc *service
	fm  *fakeMinisterio
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	svc, fm, _ := newTestService(t)
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, fm: fm, srv: srv}
}

// do hace una petición como user ("" sin identidad) y devuelve el estado,
// la redirección y el cuerpo.
func (e *webEnv) do(method, path, user string, form url.Values) (int, string, string) {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user != "" {
		req.Header.Set("X-Auth-User", user)
		req.Header.Set("X-Auth-Name", user)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") && resp.Header.Get("Content-Security-Policy") != csp {
		e.t.Errorf("%s %s sin su CSP", method, path)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

func (e *webEnv) get(path, user string) (int, string) {
	code, _, body := e.do("GET", path, user, nil)
	return code, body
}

func (e *webEnv) post(path, user string, form url.Values) (int, string, string) {
	if form == nil {
		form = url.Values{}
	}
	return e.do("POST", path, user, form)
}

// idFrom saca el id de una redirección como «/tarea/12?ok=tarea».
func idFrom(loc, prefix string) string {
	s := strings.TrimPrefix(loc, prefix)
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func TestWebFlows(t *testing.T) {
	e := newWebEnv(t)
	ctx := context.Background()
	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Añade tu vehículo") {
		t.Fatalf("resumen vacío: %d", code)
	}
	for _, f := range []string{"app.css", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}
	if _, loc, _ := e.do("GET", "/repostajes", "ana", nil); loc != "/" {
		t.Fatalf("pestaña sin vehículo: %q", loc)
	}

	// Vehículo: con un error se vuelve con lo escrito.
	code, _, body := e.post("/vehiculos", "ana", url.Values{"nombre": {""}, "km": {"85.000"}, "combustible": {"diesel"}})
	if code != 200 || !strings.Contains(body, "Ponle un nombre") || !strings.Contains(body, `value="85.000"`) {
		t.Fatalf("vehículo sin nombre: %d", code)
	}
	code, loc, _ := e.post("/vehiculos", "ana", url.Values{"nombre": {"Furgo de prueba"}, "modelo": {"Furgoneta Ejemplo"}, "matricula": {"0000 tst"},
		"km": {"85.000"}, "combustible": {"diesel"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/v/") {
		t.Fatalf("vehículo: %d %q", code, loc)
	}
	vid := idFrom(loc, "/v/")
	if code, body = e.get("/v/"+vid, "ana"); code != 200 || !strings.Contains(body, "Furgo de prueba") || !strings.Contains(body, "0000 TST") ||
		!strings.Contains(body, "85.000 km") {
		t.Fatalf("resumen del vehículo: %d", code)
	}
	if _, loc, _ = e.do("GET", "/gastos", "ana", nil); loc != "/v/"+vid+"/gastos" {
		t.Fatalf("pestaña: %q", loc)
	}

	// Repostajes.
	rf := url.Values{"fecha": {"2026-10-01"}, "km": {"86.000"}, "litros": {"50"}, "precio": {""}, "lleno": {"1"}, "gasolinera": {"Gasolinera de la esquina"}}
	if code, _, body = e.post("/v/"+vid+"/repostajes", "ana", rf); code != 200 || !strings.Contains(body, "Falta el precio") || !strings.Contains(body, `value="86.000"`) {
		t.Fatalf("sin precio: %d", code)
	}
	rf.Set("total", "70,50")
	if code, loc, _ = e.post("/v/"+vid+"/repostajes", "ana", rf); code != http.StatusSeeOther || loc != "/v/"+vid+"/repostajes?ok=repostaje" {
		t.Fatalf("repostaje: %d %q", code, loc)
	}
	rf = url.Values{"fecha": {"2026-10-20"}, "km": {"86800"}, "litros": {"44,8"}, "precio": {"1,40"}, "lleno": {"1"}}
	if code, _, _ = e.post("/v/"+vid+"/repostajes", "ana", rf); code != http.StatusSeeOther {
		t.Fatalf("segundo repostaje: %d", code)
	}
	if code, body = e.get("/v/"+vid+"/repostajes", "ana"); code != 200 || !strings.Contains(body, "5,6 l/100 km") || !strings.Contains(body, "70,50 €") {
		t.Fatalf("lista de repostajes: %d", code)
	}
	list, _ := e.svc.store.refuels(ctx, "ana", 0)
	if len(list) != 2 || !near(list[0].Price, 1.41) || list[0].Origin != "web" || !list[1].Full {
		t.Fatalf("guardados: %+v", list)
	}
	rid := strconv.FormatInt(list[1].ID, 10)
	if code, body = e.get("/repostaje/"+rid, "ana"); code != 200 || !strings.Contains(body, `value="44,8"`) {
		t.Fatalf("editar: %d", code)
	}
	rf.Set("litros", "40")
	if code, loc, _ = e.post("/repostaje/"+rid, "ana", rf); code != http.StatusSeeOther || !strings.Contains(loc, "ok=guardado") {
		t.Fatalf("guardar: %d %q", code, loc)
	}
	if _, loc, _ = e.post("/repostaje/"+rid+"/borrar", "ana", nil); loc != "/repostaje/"+rid+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}

	// Mantenimiento: crear, hecho (con coste) y su gasto.
	code, loc, _ = e.post("/v/"+vid+"/mantenimiento", "ana", url.Values{"tipo": {"aceite"}, "ultima_fecha": {"2025-10-01"}, "ultimo_km": {"80.000"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/tarea/") {
		t.Fatalf("tarea: %d %q", code, loc)
	}
	tid := idFrom(loc, "/tarea/")
	if code, body = e.get("/tarea/"+tid, "ana"); code != 200 || !strings.Contains(body, "Aceite y filtro") || !strings.Contains(body, "Vencida") {
		t.Fatalf("ficha de la tarea: %d", code)
	}
	if code, _, body = e.post("/tarea/"+tid+"/hecho", "ana", url.Values{"fecha": {"2026-11-01"}, "coste": {"abc"}}); code != 200 || !strings.Contains(body, "El coste no es un número") {
		t.Fatalf("coste raro: %d", code)
	}
	if code, loc, _ = e.post("/tarea/"+tid+"/hecho", "ana", url.Values{"fecha": {"2026-11-01"}, "km": {"87.000"}, "coste": {"89,90"}, "nota": {"Taller de prueba"}}); code != http.StatusSeeOther || loc != "/tarea/"+tid+"?ok=hecho" {
		t.Fatalf("hecho: %d %q", code, loc)
	}
	if _, body = e.get("/tarea/"+tid, "ana"); !strings.Contains(body, "Al día") || !strings.Contains(body, "89,90 €") || !strings.Contains(body, "102.000 km") {
		t.Fatal("tras hacerla")
	}

	// Gastos.
	if code, _, body = e.post("/v/"+vid+"/gastos", "ana", url.Values{"tipo": {"peaje"}, "fecha": {"2026-11-03"}, "importe": {""}}); code != 200 || !strings.Contains(body, "Falta el importe") {
		t.Fatalf("gasto sin importe: %d", code)
	}
	if code, loc, _ = e.post("/v/"+vid+"/gastos", "ana", url.Values{"tipo": {"peaje"}, "fecha": {"2026-11-03"}, "importe": {"12,40"}, "nota": {"Autopista de ejemplo"}}); code != http.StatusSeeOther || !strings.Contains(loc, "ok=gasto") {
		t.Fatalf("gasto: %d %q", code, loc)
	}
	if _, body = e.get("/v/"+vid+"/gastos", "ana"); !strings.Contains(body, "Autopista de ejemplo") || !strings.Contains(body, "Mantenimiento o taller") {
		t.Fatal("lista de gastos")
	}
	exps, _ := e.svc.store.expenses(ctx, "ana", 0)
	if len(exps) != 2 {
		t.Fatalf("gastos: %+v", exps)
	}

	// Lo de ana no lo ve ni lo toca luis.
	for _, p := range []string{"/v/" + vid, "/v/" + vid + "/repostajes", "/repostaje/" + rid, "/tarea/" + tid, "/gasto/" + strconv.FormatInt(exps[0].ID, 10), "/v/" + vid + "/editar"} {
		if code, _ := e.get(p, "luis"); code != http.StatusNotFound {
			t.Errorf("luis ve %s: %d", p, code)
		}
	}
	for _, p := range []string{"/repostaje/" + rid + "/borrar", "/tarea/" + tid + "/borrar", "/v/" + vid + "/borrar", "/tarea/" + tid + "/hecho"} {
		if code, _, _ := e.post(p, "luis", url.Values{"confirmar": {"1"}, "fecha": {"2026-11-01"}}); code != http.StatusNotFound {
			t.Errorf("luis toca %s: %d", p, code)
		}
	}
	if code, _, _ := e.post("/v/"+vid+"/repostajes", "luis", url.Values{"fecha": {"2026-11-01"}, "km": {"90000"}, "litros": {"1"}, "precio": {"1"}}); code != http.StatusNotFound {
		t.Errorf("luis apunta en el vehículo de ana: %d", code)
	}

	// Gasolineras: municipio por POST y preferencias.
	if code, body = e.get("/gasolineras", "ana"); code != 200 || !strings.Contains(body, "Gasóleo A (diésel) en Valencia") {
		t.Fatalf("gasolineras: %d", code)
	}
	if code, _, body = e.post("/gasolineras/buscar", "ana", url.Values{"q": {"valen"}}); code != 200 || !strings.Contains(body, `name="municipio" value="7214"`) {
		t.Fatalf("buscar municipio: %d", code)
	}
	if code, loc, _ = e.post("/gasolineras/municipio", "ana", url.Values{"municipio": {"4354"}}); code != http.StatusSeeOther || loc != "/gasolineras?ok=municipio" {
		t.Fatalf("elegir municipio: %d %q", code, loc)
	}
	if code, _, body = e.post("/gasolineras/municipio", "ana", url.Values{"municipio": {"x"}}); code != 200 || !strings.Contains(body, "Elige un municipio") {
		t.Fatalf("municipio raro: %d", code)
	}
	if code, loc, _ = e.post("/gasolineras", "ana", url.Values{"carburante": {"g95"}, "resumen": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("preferencias: %d %q", code, loc)
	}
	if set, _ := e.svc.store.settings(ctx, "ana"); set.Municipio.Name != "Madrid" || set.Product != "g95" || !set.Summary {
		t.Fatalf("preferencias guardadas: %+v", set)
	}
	if code, _, _ = e.post("/gasolineras", "ana", url.Values{"carburante": {"queroseno"}}); code != 200 {
		t.Fatalf("carburante raro: %d", code)
	}

	// Avisos para la pasarela: una sola vez.
	e.svc.store.addNotice(ctx, "ana", "🔧 Furgo de prueba: ITV — vence mañana", e.svc.clock())
	code, body = e.get("/api/avisos", "ana")
	var ns []notice
	if code != 200 || json.Unmarshal([]byte(body), &ns) != nil || len(ns) != 2 || ns[0].Kind != "coche" || ns[0].Text != "🔧 Furgo de prueba: Aceite y filtro — venció el 1 oct" {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, body = e.get("/api/avisos", "ana"); strings.TrimSpace(body) != "[]" {
		t.Fatalf("avisos repetidos: %s", body)
	}

	// Resumen de la mañana: solo por 127.0.0.1 sin pasar por la pasarela.
	req, _ := http.NewRequest("POST", e.srv.URL+"/interno/hoy", strings.NewReader(`{"usuario":"ana"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Lines []string `json:"lineas"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || out.Lines == nil {
		t.Fatalf("interno/hoy: %d %+v", resp.StatusCode, out)
	}
	req, _ = http.NewRequest("POST", e.srv.URL+"/interno/hoy", strings.NewReader(`{"usuario":"ana"}`))
	req.Header.Set("X-Forwarded-Prefix", "/coche")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("interno/hoy desde la pasarela: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.srv.URL+"/interno/hoy", strings.NewReader(`{}`))
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("interno/hoy sin usuario: %d", resp.StatusCode)
	}

	// Borrar el vehículo, con confirmación.
	if _, loc, _ = e.post("/v/"+vid+"/borrar", "ana", nil); loc != "/v/"+vid+"/editar?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}
	if code, body = e.get("/v/"+vid+"/editar?borrar=1", "ana"); code != 200 || !strings.Contains(body, "¿Borrar Furgo de prueba?") {
		t.Fatalf("confirmar: %d", code)
	}
	if _, loc, _ = e.post("/v/"+vid+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/?ok=borrado" {
		t.Fatalf("borrar: %q", loc)
	}
	if l, _ := e.svc.store.vehicles(ctx, "ana"); len(l) != 0 {
		t.Fatal("no se ha borrado")
	}
}
