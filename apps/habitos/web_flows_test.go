package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
	today := isoDay(dayOf(testNow()))
	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no tienes hábitos") {
		t.Fatalf("hoy vacío: %d", code)
	}
	for _, f := range []string{"app.css", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}

	// Crear: con un error, se vuelve al formulario con lo escrito.
	nuevo := url.Values{"nombre": {"Beber agua"}, "emoji": {"💧"}, "tipo": {"cantidad"}, "objetivo": {""}, "unidad": {"vasos"}, "frecuencia": {"diaria"}, "recordatorio": {"10:00"}}
	if code, _, body := e.post("/habito/nuevo", "ana", nuevo); code != 200 || !strings.Contains(body, "Pon el objetivo") || !strings.Contains(body, `value="Beber agua"`) {
		t.Fatalf("sin objetivo: %d", code)
	}
	nuevo.Set("objetivo", "8")
	code, loc, _ := e.post("/habito/nuevo", "ana", nuevo)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/habito/") {
		t.Fatalf("crear: %d %q", code, loc)
	}
	water := strings.TrimSuffix(strings.TrimPrefix(loc, "/habito/"), "?ok=creado")
	if _, _, body := e.post("/habito/nuevo", "ana", nuevo); !strings.Contains(body, "Ya tienes un hábito") {
		t.Fatal("repetido")
	}
	code, loc, _ = e.post("/habito/nuevo", "ana", url.Values{"nombre": {"Estirar"}, "tipo": {"si_no"}, "frecuencia": {"dias"}, "dias": {"1", "4"}})
	if code != http.StatusSeeOther {
		t.Fatalf("crear de días concretos: %d", code)
	}
	stretch := strings.TrimSuffix(strings.TrimPrefix(loc, "/habito/"), "?ok=creado")
	if code, body := e.get("/habito/"+stretch+"?ok=creado", "ana"); code != 200 || !strings.Contains(body, "L y J") || !strings.Contains(body, "Hábito creado") {
		t.Fatalf("ficha: %d", code)
	}

	// Marcar desde Hoy: hecho, sumar, fijar y deshacer.
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Beber agua") || !strings.Contains(body, "1 vasos</span>") || !strings.Contains(body, "Marcar") {
		t.Fatalf("hoy: %d %s", code, body)
	}
	mark := func(id, action, qty, from string) (int, string) {
		code, loc, _ := e.post("/marcar", "ana", url.Values{"habito": {id}, "dia": {today}, "accion": {action}, "cantidad": {qty}, "desde": {from}})
		return code, loc
	}
	if code, loc := mark(stretch, "hecho", "", ""); code != http.StatusSeeOther || loc != "/#h"+stretch {
		t.Fatalf("hecho: %d %q", code, loc)
	}
	mark(water, "sumar", "1", "")
	mark(water, "sumar", "1", "")
	if m, _ := e.svc.store.marks(ctx, "ana"); m[mustID(water)][today] != 2 || m[mustID(stretch)][today] != 1 {
		t.Fatalf("marcas: %v", m)
	}
	if _, loc := mark(water, "fijar", "abc", ""); loc != "/?mal=numero#h"+water {
		t.Fatalf("cantidad mala: %q", loc)
	}
	if _, body := e.get("/?mal=numero", "ana"); !strings.Contains(body, "no es válida") {
		t.Fatal("no dice que la cantidad está mal")
	}
	if _, loc := mark(water, "fijar", "8", "ficha"); loc != "/habito/"+water+"?dia="+today+"#dia" {
		t.Fatalf("desde la ficha: %q", loc)
	}
	if _, body := e.get("/", "ana"); !strings.Contains(body, "8 de 8 vasos") || !strings.Contains(body, "is-done") {
		t.Fatal("no sale hecho")
	}
	mark(stretch, "deshacer", "", "")
	// Un día de antes, y nunca el futuro.
	if code, _, _ := e.post("/marcar", "ana", url.Values{"habito": {stretch}, "dia": {isoDay(ago(3))}, "accion": {"hecho"}}); code != http.StatusSeeOther {
		t.Fatalf("día de antes: %d", code)
	}
	if _, loc, _ := e.post("/marcar", "ana", url.Values{"habito": {stretch}, "dia": {isoDay(ago(-1))}, "accion": {"hecho"}}); !strings.Contains(loc, "mal=dia") {
		t.Fatalf("mañana: %q", loc)
	}
	if code, body := e.get("/?dia="+isoDay(ago(3)), "ana"); code != 200 || !strings.Contains(body, "Lunes 9 de noviembre") || !strings.Contains(body, `name="dia" value="`+isoDay(ago(3))+`"`) {
		t.Fatalf("hoy de otro día: %d", code)
	}
	if code, body := e.get("/habito/"+stretch+"?dia="+isoDay(ago(3)), "ana"); code != 200 || !strings.Contains(body, "hecho ✓") || !strings.Contains(body, `class="c-done on"`) {
		t.Fatalf("ficha de otro día: %d", code)
	}

	// Editar, archivar y recuperar.
	edit := url.Values{"nombre": {"Estirar la espalda"}, "tipo": {"si_no"}, "frecuencia": {"semana"}, "veces": {"9"}}
	if code, _, body := e.post("/habito/"+stretch, "ana", edit); code != 200 || !strings.Contains(body, "de 1 a 7") || !strings.Contains(body, `value="Estirar la espalda"`) {
		t.Fatalf("editar mal: %d", code)
	}
	edit.Set("veces", "2")
	if _, loc, _ := e.post("/habito/"+stretch, "ana", edit); loc != "/habito/"+stretch+"?ok=guardado" {
		t.Fatalf("editar: %q", loc)
	}
	if _, loc, _ := e.post("/habito/"+stretch+"/archivar", "ana", url.Values{"on": {"1"}}); loc != "/habito/"+stretch+"?ok=archivado" {
		t.Fatalf("archivar: %q", loc)
	}
	if _, body := e.get("/habitos", "ana"); !strings.Contains(body, "Archivados (1)") {
		t.Fatal("no sale en archivados")
	}
	e.post("/habito/"+stretch+"/archivar", "ana", url.Values{"on": {"0"}})

	// Diario.
	if code, body := e.get("/diario", "ana"); code != 200 || !strings.Contains(body, "Nada escrito este mes") || !strings.Contains(body, `name="animo" value="0" checked`) {
		t.Fatalf("diario vacío: %d", code)
	}
	entry := url.Values{"dia": {today}, "animo": {"4"}, "energia": {"3"}, "sueno": {"7,5"}, "nota": {"Una nota inventada para la prueba"}, "etiquetas": {"trabajo, paseo"}}
	if _, loc, _ := e.post("/diario", "ana", entry); loc != "/diario?ok=diario" {
		t.Fatalf("diario: %q", loc)
	}
	if _, body := e.get("/diario", "ana"); !strings.Contains(body, "Una nota inventada") || !strings.Contains(body, `name="animo" value="4" checked`) ||
		!strings.Contains(body, `<polyline class="mood"`) && !strings.Contains(body, `class="pt-mood"`) || !strings.Contains(body, "7,5 h de sueño") {
		t.Fatal("no sale lo escrito")
	}
	entry.Set("sueno", "mucho")
	if code, _, body := e.post("/diario", "ana", entry); code != 200 || !strings.Contains(body, "Las horas de sueño") || !strings.Contains(body, "Una nota inventada") {
		t.Fatalf("sueño mal: %d", code)
	}
	if _, body := e.get("/", "ana"); !strings.Contains(body, "Diario de hoy: escrito") {
		t.Fatal("hoy no sabe que el diario está escrito")
	}
	if _, loc, _ := e.post("/ajustes", "ana", url.Values{"aviso": {"1"}, "hora": {"22:15"}}); loc != "/diario?ok=ajustes#aviso" {
		t.Fatalf("ajustes: %q", loc)
	}
	if p, _ := e.svc.store.settings(ctx, "ana"); !p.Nudge || p.NudgeTime != "22:15" {
		t.Fatalf("ajustes guardados: %+v", p)
	}
	if _, loc, _ := e.post("/ajustes", "ana", url.Values{"aviso": {"1"}, "hora": {"99"}}); !strings.Contains(loc, "mal=hora") {
		t.Fatalf("hora mala: %q", loc)
	}
	if _, body := e.get("/datos", "ana"); !strings.Contains(body, "Cumplimiento") || !strings.Contains(body, "Beber agua") || !strings.Contains(body, "Faltan días") {
		t.Fatal("datos")
	}

	// Otra persona no ve ni toca nada.
	if _, body := e.get("/habitos", "bob"); strings.Contains(body, "Beber agua") {
		t.Fatal("bob ve los hábitos de ana")
	}
	if _, body := e.get("/diario", "bob"); strings.Contains(body, "nota inventada") {
		t.Fatal("bob ve el diario de ana")
	}
	if code, _ := e.get("/habito/"+water, "bob"); code != 404 {
		t.Errorf("bob: GET ficha = %d", code)
	}
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/habito/" + water, url.Values{"nombre": {"robado"}}},
		{"/habito/" + water + "/archivar", url.Values{"on": {"1"}}},
		{"/habito/" + water + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/habito/" + water + "/borrar", nil},
		{"/marcar", url.Values{"habito": {water}, "dia": {today}, "accion": {"deshacer"}}},
	} {
		if code, _, _ := e.post(p.path, "bob", p.form); code != 404 {
			t.Errorf("bob: POST %s = %d", p.path, code)
		}
	}
	e.post("/diario/borrar", "bob", url.Values{"dia": {today}, "confirmar": {"1"}})
	if _, ok, _ := e.svc.store.entry(ctx, "ana", dayOf(testNow())); !ok {
		t.Fatal("bob borra el diario de ana")
	}
	if m, _ := e.svc.store.marks(ctx, "ana"); m[mustID(water)][today] != 8 {
		t.Fatal("bob ha cambiado algo")
	}

	// La pasarela recoge los avisos (una vez); la Agenda, las líneas de hoy.
	e.c.set(at(10, 5))
	e.svc.setAmount(ctx, "ana", mustHabit(t, e.svc, water), dayOf(testNow()), 3, "web")
	e.svc.remind(ctx)
	code, _, body := e.do("GET", "/api/avisos", "ana", nil)
	var list []notice
	json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list) != 1 || list[0].Kind != "habitos" || !strings.Contains(list[0].Text, "Beber agua: llevas 3 de 8 vasos") {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, _, body = e.do("GET", "/api/avisos", "ana", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("avisos repetidos: %s", body)
	}
	if _, _, body = e.do("GET", "/api/avisos", "bob", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("bob recibe avisos de ana: %s", body)
	}
	call := func(gateway bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/interno/hoy", strings.NewReader(`{"usuario":"ana"}`))
		req.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			req.Header.Set("X-Forwarded-Prefix", "/habitos")
		}
		rec := httptest.NewRecorder()
		(&web{svc: e.svc}).internalToday(rec, req)
		return rec
	}
	if rec := call(true); rec.Code != http.StatusForbidden {
		t.Fatalf("interno por la pasarela: %d", rec.Code)
	}
	if rec := call(false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lineas":["Hoy: 💧 Beber agua (8 vasos)`) {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}

	// Borrar el diario y el hábito, con confirmación.
	if _, loc, _ := e.post("/diario/borrar", "ana", url.Values{"dia": {today}}); loc != "/diario?dia="+today+"&borrar=1#borrar" {
		t.Fatalf("borrar diario sin confirmar: %q", loc)
	}
	if _, body := e.get("/diario?dia="+today+"&borrar=1", "ana"); !strings.Contains(body, "¿Borrar lo de este día?") {
		t.Fatal("no pide confirmación")
	}
	if _, loc, _ := e.post("/diario/borrar", "ana", url.Values{"dia": {today}, "confirmar": {"1"}}); !strings.Contains(loc, "ok=diario-borrado") {
		t.Fatalf("borrar diario: %q", loc)
	}
	if _, loc, _ := e.post("/habito/"+water+"/borrar", "ana", nil); loc != "/habito/"+water+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}
	if _, loc, _ := e.post("/habito/"+water+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/habitos?ok=borrado" {
		t.Fatalf("borrar: %q", loc)
	}
	if code, _ := e.get("/habito/"+water, "ana"); code != 404 {
		t.Fatal("sigue existiendo")
	}
}

func mustID(s string) int64 {
	var id int64
	for _, r := range s {
		id = id*10 + int64(r-'0')
	}
	return id
}

func mustHabit(t *testing.T, svc *service, id string) habit {
	t.Helper()
	h, err := svc.store.habit(context.Background(), "ana", mustID(id))
	if err != nil {
		t.Fatal(err)
	}
	return h
}
