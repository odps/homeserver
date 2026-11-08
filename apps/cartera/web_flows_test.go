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
	fm  *fakeMarket
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

func TestWebFlows(t *testing.T) {
	e := newWebEnv(t)
	ctx := context.Background()
	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no hay nada en la cartera") {
		t.Fatalf("resumen vacío: %d", code)
	}
	for _, f := range []string{"app.css", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}

	// Buscar (por POST: lo buscado no va en la URL) y añadir.
	code, _, body := e.post("/buscar", "ana", url.Values{"q": {"btc"}})
	if code != 200 || !strings.Contains(body, `name="ref" value="kraken:XBTEUR"`) {
		t.Fatalf("buscar: %d", code)
	}
	code, loc, _ := e.post("/activos/nuevo", "ana", url.Values{"ref": {"kraken:XBTEUR"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/activo/") {
		t.Fatalf("añadir: %d %q", code, loc)
	}
	btc := strings.TrimSuffix(strings.TrimPrefix(loc, "/activo/"), "?ok=anadido")
	if code, _, body = e.post("/activos/nuevo", "ana", url.Values{"ref": {"yahoo:../../x"}}); code != 200 || !strings.Contains(body, "no es válido") {
		t.Fatalf("referencia rara: %d", code)
	}
	if code, body = e.get("/activo/"+btc, "ana"); code != 200 || !strings.Contains(body, "60.000 €") || !strings.Contains(body, `class="line"`) {
		t.Fatalf("ficha: %d", code)
	}

	// Apuntar: con un error, se vuelve al formulario con lo escrito.
	op := url.Values{"tipo": {"compra"}, "activo": {btc}, "fecha": {"2026-10-01"}, "cantidad": {"0,1"}, "precio": {""}, "plataforma": {"kraken"}}
	if code, _, body = e.post("/operacion/nueva", "ana", op); code != 200 || !strings.Contains(body, "Falta el precio") || !strings.Contains(body, `value="0,1"`) {
		t.Fatalf("sin precio: %d", code)
	}
	op.Set("total", "4.000,50")
	if code, loc, _ = e.post("/operacion/nueva", "ana", op); code != http.StatusSeeOther || loc != "/activo/"+btc+"?ok=apuntada#operaciones" {
		t.Fatalf("apuntar: %d %q", code, loc)
	}
	ops, _ := e.svc.store.ops(ctx, "ana")
	if len(ops) != 1 || !near(ops[0].Price, 40005) || ops[0].Platform != "Kraken" || ops[0].Origin != "web" {
		t.Fatalf("guardada: %+v", ops)
	}
	opID := strconv.FormatInt(ops[0].ID, 10)
	if _, body = e.get("/", "ana"); !strings.Contains(body, "6.000,00 €") || !strings.Contains(body, "Posiciones") {
		t.Fatal("el resumen no la cuenta")
	}

	// Editar y borrar (con confirmación).
	op.Set("total", "")
	op.Set("precio", "39000")
	if code, loc, _ = e.post("/operacion/"+opID, "ana", op); code != http.StatusSeeOther || !strings.Contains(loc, "ok=guardado") {
		t.Fatalf("editar: %d %q", code, loc)
	}
	if _, loc, _ = e.post("/operacion/"+opID+"/borrar", "ana", nil); loc != "/operacion/"+opID+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}
	if _, body = e.get("/operacion/"+opID+"?borrar=1", "ana"); !strings.Contains(body, "¿Borrar esta operación?") {
		t.Fatal("no pide confirmación")
	}

	// Alertas desde la ficha y desde Alertas.
	if code, loc, _ = e.post("/alertas/nueva", "ana", url.Values{"activo": {btc}, "desde": {"ficha"}, "tipo": {"encima"}, "umbral": {"65.000"}, "repetir": {"1"}}); code != http.StatusSeeOther || loc != "/activo/"+btc+"?ok=alerta#alertas" {
		t.Fatalf("alerta: %d %q", code, loc)
	}
	if code, _, body = e.post("/alertas/nueva", "ana", url.Values{"activo": {"0"}, "tipo": {"caida"}, "umbral": {"10"}}); code != 200 || !strings.Contains(body, "solo vale para un activo") {
		t.Fatalf("caída de la cartera: %d", code)
	}
	if _, body = e.get("/alertas", "ana"); !strings.Contains(body, "BTC por encima de 65.000 € (cada vez)") {
		t.Fatal("no sale la alerta")
	}
	// Como ya se cumple (60.000 no, pero el precio sube), salta en cuanto se evalúa.
	e.fm.set(func(f *fakeMarket) { f.kraken["XBTEUR"] = [2]float64{66000, 64000} })
	q, _ := e.svc.market.krakenQuotes(ctx, []string{"XBTEUR"})
	id, _ := strconv.ParseInt(btc, 10, 64)
	e.svc.store.setQuote(ctx, "ana", id, q["XBTEUR"])
	if n, err := e.svc.checkAlerts(ctx, "ana"); err != nil || n != 1 {
		t.Fatalf("alerta al evaluar: %d %v", n, err)
	}

	// Otra persona no ve ni toca nada.
	if _, body = e.get("/activos", "bob"); strings.Contains(body, "/activo/"+btc) || strings.Contains(body, "Lo que tienes") {
		t.Fatal("bob ve los activos de ana")
	}
	for _, p := range []string{"/activo/" + btc, "/operacion/" + opID} {
		if code, _ = e.get(p, "bob"); code != 404 {
			t.Errorf("bob: GET %s = %d", p, code)
		}
	}
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/activo/" + btc, url.Values{"nombre": {"robado"}}},
		{"/activo/" + btc + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/operacion/" + opID, op},
		{"/operacion/" + opID + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/seguimiento", url.Values{"id": {btc}, "seguir": {"1"}}},
	} {
		if code, _, _ = e.post(p.path, "bob", p.form); code != 404 {
			t.Errorf("bob: POST %s = %d", p.path, code)
		}
	}
	if code, _, _ = e.post("/operacion/nueva", "bob", url.Values{"tipo": {"compra"}, "activo": {btc}, "fecha": {"2026-10-01"}, "cantidad": {"1"}, "precio": {"1"}}); code != 200 {
		t.Errorf("bob apunta en un activo de ana: %d", code)
	}
	if code, _, body = e.do("GET", "/api/avisos", "bob", nil); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("bob recibe avisos de ana: %s", body)
	}
	if ops, _ := e.svc.store.ops(ctx, "ana"); len(ops) != 1 {
		t.Fatal("bob ha cambiado algo")
	}

	// Borrar de verdad.
	if _, loc, _ = e.post("/operacion/"+opID+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/activo/"+btc+"?ok=borrado#operaciones" {
		t.Fatalf("borrar: %q", loc)
	}
	if code, _, _ = e.post("/activos/manual", "ana", url.Values{"nombre": {"Fondo de ejemplo"}, "moneda": {"EUR"}, "precio": {"10,5"}, "tipo": {"fondo"}}); code != http.StatusSeeOther {
		t.Fatalf("activo a mano: %d", code)
	}
	if code, _, body = e.post("/activos/manual", "ana", url.Values{"nombre": {"Otro"}, "moneda": {"EURO"}}); code != 200 || !strings.Contains(body, "tres letras") {
		t.Fatalf("moneda mala: %d", code)
	}

	// Seguimiento.
	if _, loc, _ = e.post("/activos/nuevo", "ana", url.Values{"ref": {"coingecko:ejemplo-coin"}, "seguir": {"1"}}); loc != "/seguimiento?ok=siguiendo" {
		t.Fatalf("seguir: %q", loc)
	}
	if _, body = e.get("/seguimiento", "ana"); !strings.Contains(body, "EJC") || !strings.Contains(body, "0,1234 €") {
		t.Fatal("no sale en seguimiento")
	}

	// La pasarela recoge los avisos (una vez); la Agenda, las líneas de hoy.
	code, _, body = e.do("GET", "/api/avisos", "ana", nil)
	var list []notice
	json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list) != 1 || list[0].Kind != "cartera" || !strings.Contains(list[0].Text, "BTC ha superado 65.000 €") {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, _, body = e.do("GET", "/api/avisos", "ana", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("avisos repetidos: %s", body)
	}
	call := func(gateway bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/interno/hoy", strings.NewReader(`{"usuario":"ana"}`))
		req.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			req.Header.Set("X-Forwarded-Prefix", "/cartera")
		}
		rec := httptest.NewRecorder()
		(&web{svc: e.svc}).internalToday(rec, req)
		return rec
	}
	if rec := call(true); rec.Code != http.StatusForbidden {
		t.Fatalf("interno por la pasarela: %d", rec.Code)
	}
	if rec := call(false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lineas":`) {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}

	// Borrar el activo, con confirmación.
	if _, loc, _ = e.post("/activo/"+btc+"/borrar", "ana", nil); loc != "/activo/"+btc+"?borrar=1#borrar" {
		t.Fatalf("borrar activo sin confirmar: %q", loc)
	}
	if _, loc, _ = e.post("/activo/"+btc+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/activos?ok=borrado" {
		t.Fatalf("borrar activo: %q", loc)
	}
}
