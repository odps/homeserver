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
	"time"
)

// ---- Flujos por HTTP con la base de datos de pruebas ----

type webEnv struct {
	t   *testing.T
	svc *service
	c   *clock
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	svc, c := newTestService(t)
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, c: c, srv: srv}
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
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Sin movimientos en Noviembre 2026") {
		t.Fatalf("lista vacía: %d", code)
	}
	for _, f := range []string{"app.css", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}
	c, _ := e.svc.loadCats(ctx, "ana")
	comida := idStr(c.byKey["comida"].ID)

	// Apuntar: con un error, se vuelve al formulario con lo escrito.
	mov := url.Values{"tipo": {"gasto"}, "fecha": {"2026-11-03"}, "importe": {""}, "concepto": {"Supermercado del barrio"}, "categoria": {"0"}, "cuenta": {"Tarjeta"}, "mes": {"2026-11"}}
	if code, _, body := e.post("/movimiento/nuevo", "ana", mov); code != 200 || !strings.Contains(body, "Falta el importe") || !strings.Contains(body, `value="Supermercado del barrio"`) {
		t.Fatalf("sin importe: %d", code)
	}
	mov.Set("importe", "1.234,5")
	code, loc, _ := e.post("/movimiento/nuevo", "ana", mov)
	if code != http.StatusSeeOther || loc != "/?mes=2026-11&ok=apuntado" {
		t.Fatalf("apuntar: %d %q", code, loc)
	}
	movs, _ := e.svc.store.movements(ctx, "ana", time.Time{}, time.Time{})
	if len(movs) != 1 || movs[0].Amount != 1234.5 || idStr(movs[0].CatID) != comida || movs[0].Origin != originWeb || movs[0].Account != "Tarjeta" {
		t.Fatalf("guardado: %+v", movs)
	}
	id := idStr(movs[0].ID)
	if _, body := e.get("/?mes=2026-11", "ana"); !strings.Contains(body, "Supermercado del barrio") || !strings.Contains(body, "−1.234,50 €") || !strings.Contains(body, ">Comida<") {
		t.Fatal("no sale en la lista")
	}
	if _, body := e.get("/?mes=2026-11&cat="+comida, "ana"); !strings.Contains(body, "Comida: −1.234,50 € en 1 movimiento") {
		t.Fatal("filtro por categoría")
	}

	// Editar y borrar (con confirmación).
	if code, body := e.get("/movimiento/"+id, "ana"); code != 200 || !strings.Contains(body, `value="1234,5"`) {
		t.Fatalf("editar: %d", code)
	}
	mov.Set("importe", "34,50")
	mov.Set("fecha", "2026-10-30")
	if _, loc, _ = e.post("/movimiento/"+id, "ana", mov); loc != "/?mes=2026-10&ok=guardado" {
		t.Fatalf("guardar: %q", loc)
	}
	mov.Set("fecha", "2026-12-01")
	if code, _, body := e.post("/movimiento/"+id, "ana", mov); code != 200 || !strings.Contains(body, "no puede ser futura") {
		t.Fatalf("fecha futura: %d", code)
	}
	if _, loc, _ = e.post("/movimiento/"+id+"/borrar", "ana", nil); loc != "/movimiento/"+id+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}
	if _, body := e.get("/movimiento/"+id+"?borrar=1", "ana"); !strings.Contains(body, "¿Borrar este movimiento?") {
		t.Fatal("no pide confirmación")
	}

	// Categorías, presupuestos y reglas.
	if code, loc, _ = e.post("/categorias/nueva", "ana", url.Values{"nombre": {"Mascotas"}, "presupuesto": {"40"}}); code != http.StatusSeeOther {
		t.Fatalf("nueva categoría: %d", code)
	}
	if code, _, body := e.post("/categorias/nueva", "ana", url.Values{"nombre": {"COMIDA"}}); code != 200 || !strings.Contains(body, "Ya tienes una categoría") {
		t.Fatalf("categoría repetida: %d", code)
	}
	c, _ = e.svc.loadCats(ctx, "ana")
	pets, _ := c.find("mascotas")
	if code, loc, _ = e.post("/categorias/presupuestos", "ana", url.Values{"p" + comida: {"300"}, "p" + idStr(pets.ID): {""}}); loc != "/categorias?ok=presupuestos" {
		t.Fatalf("presupuestos: %d %q", code, loc)
	}
	if c, _ = e.svc.loadCats(ctx, "ana"); c.byKey["comida"].Budget != 300 || c.byID[pets.ID].Budget != 0 {
		t.Fatal("no guarda los presupuestos")
	}
	if code, _, body := e.post("/categorias/presupuestos", "ana", url.Values{"p" + comida: {"mucho"}}); code != 200 || !strings.Contains(body, "no es un número") {
		t.Fatalf("presupuesto malo: %d", code)
	}
	e.post("/movimiento/nuevo", "ana", url.Values{"fecha": {"2026-11-05"}, "importe": {"25"}, "concepto": {"Veterinario"}})
	if _, loc, _ = e.post("/reglas/nueva", "ana", url.Values{"texto": {"veterinario"}, "categoria": {idStr(pets.ID)}, "aplicar": {"1"}}); loc != "/categorias?ok=regla&cambiados=1#reglas" {
		t.Fatalf("regla: %q", loc)
	}
	if _, body := e.get("/categorias?ok=regla&cambiados=1", "ana"); !strings.Contains(body, "Regla guardada y aplicada a 1 movimiento ya apuntado.") || !strings.Contains(body, "veterinario") {
		t.Fatal("no sale la regla")
	}
	if _, body := e.get("/presupuesto", "ana"); !strings.Contains(body, "de 300,00 €") || !strings.Contains(body, "Mascotas") {
		t.Fatal("presupuesto")
	}
	if code, body := e.get("/categoria/"+idStr(pets.ID), "ana"); code != 200 || !strings.Contains(body, "veterinario") || !strings.Contains(body, "1 movimiento") {
		t.Fatalf("ficha de categoría: %d", code)
	}

	// Suscripciones.
	sub := url.Values{"nombre": {"Plataforma de ejemplo"}, "importe": {"12,99"}, "periodicidad": {"mensual"}, "proximo": {"2026-11-10"}, "aviso": {"2"}}
	if _, loc, _ = e.post("/suscripcion/nueva", "ana", sub); loc != "/suscripciones?ok=suscripcion" {
		t.Fatalf("suscripción: %q", loc)
	}
	if code, _, body := e.post("/suscripcion/nueva", "ana", url.Values{"nombre": {"x"}, "importe": {"1"}, "periodicidad": {"mensual"}, "proximo": {"mal"}}); code != 200 || !strings.Contains(body, "no es válida") {
		t.Fatalf("suscripción sin fecha: %d", code)
	}
	subs, _ := e.svc.store.subscriptions(ctx, "ana")
	if len(subs) != 1 || subs[0].Notice != 2 || subs[0].CatID != c.byKey[keySubs].ID {
		t.Fatalf("guardada: %+v", subs)
	}
	sid := idStr(subs[0].ID)
	if _, body := e.get("/suscripciones", "ana"); !strings.Contains(body, "Plataforma de ejemplo") || !strings.Contains(body, "mañana") {
		t.Fatal("no sale la suscripción")
	}
	sub.Set("aviso", "")
	if _, loc, _ = e.post("/suscripcion/"+sid, "ana", sub); loc != "/suscripciones?ok=guardado" {
		t.Fatalf("editar suscripción: %q", loc)
	}
	if x, _ := e.svc.store.subscription(ctx, "ana", subs[0].ID); x.Notice != -1 {
		t.Fatal("sin aviso")
	}
	if _, loc, _ = e.post("/suscripcion/"+sid+"/cobro", "ana", nil); loc != "/suscripciones?ok=cobro" {
		t.Fatalf("cobro: %q", loc)
	}
	if x, _ := e.svc.store.subscription(ctx, "ana", subs[0].ID); !x.Next.Equal(day("2026-12-10")) {
		t.Fatalf("siguiente cobro: %v", x.Next)
	}

	// Otra persona no ve ni toca nada.
	if _, body := e.get("/?mes=2026-11", "bob"); strings.Contains(body, "Veterinario") || strings.Contains(body, "Plataforma") {
		t.Fatal("bob ve los movimientos de ana")
	}
	if _, body := e.get("/suscripciones", "bob"); strings.Contains(body, "Plataforma de ejemplo") {
		t.Fatal("bob ve las suscripciones de ana")
	}
	for _, p := range []string{"/movimiento/" + id, "/suscripcion/" + sid, "/categoria/" + comida} {
		if code, _ := e.get(p, "bob"); code != 404 {
			t.Errorf("bob: GET %s = %d", p, code)
		}
	}
	rules, _ := e.svc.store.rules(ctx, "ana")
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/movimiento/" + id, mov},
		{"/movimiento/" + id + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/suscripcion/" + sid, sub},
		{"/suscripcion/" + sid + "/cobro", nil},
		{"/suscripcion/" + sid + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/categoria/" + comida, url.Values{"nombre": {"robada"}}},
		{"/categoria/" + comida + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/categorias/presupuestos", url.Values{"p" + comida: {"1"}}},
		{"/regla/" + idStr(rules[0].ID) + "/borrar", nil},
	} {
		if code, _, _ := e.post(p.path, "bob", p.form); code != 404 {
			t.Errorf("bob: POST %s = %d", p.path, code)
		}
	}
	if code, _, body := e.post("/movimiento/nuevo", "bob", url.Values{"fecha": {"2026-11-05"}, "importe": {"1"}, "concepto": {"x"}, "categoria": {comida}}); code != 200 || !strings.Contains(body, "Elige una categoría") {
		t.Errorf("bob apunta en una categoría de ana: %d", code)
	}
	if code, _, _ := e.post("/reglas/nueva", "bob", url.Values{"texto": {"x"}, "categoria": {comida}}); code != 200 {
		t.Errorf("bob pone una regla en una categoría de ana: %d", code)
	}
	if code, _, body := e.do("GET", "/api/avisos", "bob", nil); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("bob recibe avisos de ana: %s", body)
	}
	if after, _ := e.svc.store.rules(ctx, "ana"); len(after) != len(rules) {
		t.Fatal("bob ha cambiado las reglas de ana")
	}

	// Pasarse del presupuesto avisa (una vez), y la pasarela lo recoge.
	e.post("/movimiento/nuevo", "ana", url.Values{"fecha": {"2026-11-06"}, "importe": {"320"}, "concepto": {"Supermercado grande"}})
	code, _, body := e.do("GET", "/api/avisos", "ana", nil)
	var list []notice
	json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list) != 1 || list[0].Kind != "gastos" || !strings.Contains(list[0].Text, "presupuesto de Comida") {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, _, body = e.do("GET", "/api/avisos", "ana", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("avisos repetidos: %s", body)
	}

	// La Agenda pide las líneas de hoy, solo desde dentro.
	call := func(gateway bool, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/interno/hoy", strings.NewReader(`{"usuario":"`+user+`"}`))
		req.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			req.Header.Set("X-Forwarded-Prefix", "/gastos")
		}
		rec := httptest.NewRecorder()
		(&web{svc: e.svc}).internalToday(rec, req)
		return rec
	}
	if rec := call(true, "ana"); rec.Code != http.StatusForbidden {
		t.Fatalf("interno por la pasarela: %d", rec.Code)
	}
	rec := call(false, "ana")
	var out struct {
		Lines []string `json:"lineas"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Lines) != 2 || !strings.HasPrefix(out.Lines[0], "Presupuesto pasado en noviembre: Comida 320,00 € de 300,00 €") {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}
	if rec := call(false, "nadie"); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"lineas":[]}` {
		t.Fatalf("interno sin nada: %s", rec.Body)
	}

	// Borrar de verdad.
	if _, loc, _ = e.post("/movimiento/"+id+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/?mes=2026-10&ok=borrado" {
		t.Fatalf("borrar: %q", loc)
	}
	if _, loc, _ = e.post("/suscripcion/"+sid+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/suscripciones?ok=borrado" {
		t.Fatalf("borrar suscripción: %q", loc)
	}
	if _, loc, _ = e.post("/regla/"+idStr(rules[0].ID)+"/borrar", "ana", url.Values{"categoria": {strconv.FormatInt(rules[0].CatID, 10)}}); !strings.HasPrefix(loc, "/categoria/") {
		t.Fatalf("borrar regla: %q", loc)
	}
	if _, loc, _ = e.post("/categoria/"+idStr(pets.ID)+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/categorias?ok=borrado" {
		t.Fatalf("borrar categoría: %q", loc)
	}
	// Un mes futuro o mal escrito en la URL lleva al de hoy.
	if _, body := e.get("/presupuesto?mes=2030-01", "ana"); !strings.Contains(body, "Noviembre 2026") {
		t.Fatal("mes futuro")
	}
}
