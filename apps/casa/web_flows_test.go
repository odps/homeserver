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
	f   *fakeREE
	c   *clock
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	svc, f, c := newTestService(t)
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, f: f, c: c, srv: srv}
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
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no están los precios de hoy") {
		t.Fatalf("luz sin precios: %d", code)
	}
	for _, f := range []string{"app.css", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}

	// Con los precios de hoy.
	e.svc.refreshPrices(ctx)
	code, body := e.get("/", "ana")
	if code != 200 || !strings.Contains(body, "0,203 €/kWh") || !strings.Contains(body, "de 13 a 16 h") || !strings.Contains(body, `class="b barata`) ||
		!strings.Contains(body, "se publican hacia las 20:30") {
		t.Fatalf("luz: %d", code)
	}

	// Mantenimiento: una sugerencia con un clic y una propia.
	if code, body = e.get("/mantenimiento", "ana"); !strings.Contains(body, "Aún no hay tareas") || !strings.Contains(body, `name="clave" value="caldera"`) {
		t.Fatalf("mantenimiento vacío: %d", code)
	}
	if code, loc, _ := e.post("/mantenimiento/sugerencia", "ana", url.Values{"clave": {"caldera"}}); code != http.StatusSeeOther || loc != "/mantenimiento?ok=anadida" {
		t.Fatalf("sugerencia: %d %q", code, loc)
	}
	if _, loc, _ := e.post("/mantenimiento/sugerencia", "ana", url.Values{"clave": {"caldera"}}); !strings.HasPrefix(loc, "/tarea/") || !strings.HasSuffix(loc, "?ok=ya-estaba") {
		t.Fatalf("sugerencia repetida: %q", loc)
	}
	nueva := url.Values{"nombre": {"Limpiar los filtros del aire"}, "cada": {"tres"}, "unidad": {unitMonths}, "ultima": {"2026-08-20"}}
	if code, _, body = e.post("/mantenimiento/nueva", "ana", nueva); code != 200 || !strings.Contains(body, "número entero") || !strings.Contains(body, `value="Limpiar los filtros del aire"`) {
		t.Fatalf("periodo mal: %d", code)
	}
	nueva.Set("cada", "3")
	if code, loc, _ := e.post("/mantenimiento/nueva", "ana", nueva); code != http.StatusSeeOther || loc != "/mantenimiento?ok=anadida" {
		t.Fatalf("nueva: %d %q", code, loc)
	}
	_, body = e.get("/mantenimiento?ok=anadida", "ana")
	if !strings.Contains(body, "Revisión de la caldera") || !strings.Contains(body, "Toca hoy") || !strings.Contains(body, "toca en 11 días (20 nov)") ||
		strings.Contains(body, `name="clave" value="caldera"`) || !strings.Contains(body, "Tarea añadida.") {
		t.Fatal("lista de tareas")
	}
	list, _ := e.svc.store.tasks(ctx, "ana")
	if len(list) != 2 {
		t.Fatalf("tareas: %d", len(list))
	}
	caldera, aire := idStr(list[0].ID), idStr(list[1].ID)

	// Hecha desde la lista y desde la ficha.
	if _, loc, _ := e.post("/tarea/"+caldera+"/hecho", "ana", url.Values{"desde": {"lista"}}); loc != "/mantenimiento?ok=hecho" {
		t.Fatalf("hecha: %q", loc)
	}
	if code, body = e.get("/tarea/"+caldera, "ana"); code != 200 || !strings.Contains(body, "Al día") || !strings.Contains(body, "lunes, 9 de noviembre de 2026") {
		t.Fatalf("ficha: %d", code)
	}
	if code, _, body = e.post("/tarea/"+aire+"/hecho", "ana", url.Values{"fecha": {"2026-12-01"}}); code != 200 || !strings.Contains(body, "en el futuro") {
		t.Fatalf("hecha en el futuro: %d", code)
	}
	if _, loc, _ := e.post("/tarea/"+aire+"/hecho", "ana", url.Values{"fecha": {"2026-11-05"}}); loc != "/tarea/"+aire+"?ok=hecho" {
		t.Fatalf("hecha otro día: %q", loc)
	}
	if t2, _ := e.svc.store.task(ctx, "ana", list[1].ID); isoDay(t2.Due) != "2027-02-05" {
		t.Fatalf("próxima: %s", isoDay(t2.Due))
	}
	// Editar.
	edit := url.Values{"nombre": {"Limpiar los filtros del aire acondicionado"}, "cada": {"2"}, "unidad": {unitMonths}, "ultima": {"2026-11-05"}, "nota": {"los del salón"}}
	if _, loc, _ := e.post("/tarea/"+aire, "ana", edit); loc != "/tarea/"+aire+"?ok=guardado" {
		t.Fatalf("editar: %q", loc)
	}
	if _, body = e.get("/tarea/"+aire, "ana"); !strings.Contains(body, "cada 2 meses") || !strings.Contains(body, "los del salón") {
		t.Fatal("no se ha guardado")
	}

	// Lecturas.
	if _, body = e.get("/lecturas", "ana"); !strings.Contains(body, "Con dos lecturas de Luz") {
		t.Fatal("lecturas vacías")
	}
	rd := url.Values{"contador": {"luz"}, "fecha": {"2026-10-10"}, "valor": {"12.345,6"}}
	if _, loc, _ := e.post("/lecturas/nueva", "ana", rd); loc != "/lecturas?c=luz&ok=apuntada" {
		t.Fatalf("lectura: %q", loc)
	}
	rd.Set("fecha", "2026-11-09")
	rd.Set("valor", "12.645,6")
	e.post("/lecturas/nueva", "ana", rd)
	if _, loc, _ := e.post("/lecturas/nueva", "ana", rd); loc != "/lecturas?c=luz&ok=sustituida" {
		t.Fatalf("la del mismo día: %q", loc)
	}
	rd.Set("valor", "doce")
	if code, _, body = e.post("/lecturas/nueva", "ana", rd); code != 200 || !strings.Contains(body, "La lectura es un número") || !strings.Contains(body, `value="doce"`) {
		t.Fatalf("lectura mala: %d", code)
	}
	_, body = e.get("/lecturas?c=luz", "ana")
	if !strings.Contains(body, "12.645,6 kWh") || !strings.Contains(body, "&#43;300 kWh (10 al día)") || !strings.Contains(body, `<rect class="b use"`) {
		t.Fatal("consumo")
	}
	rs, _ := e.svc.store.readings(ctx, "ana")
	if len(rs) != 2 {
		t.Fatalf("lecturas: %d", len(rs))
	}
	rid := strconv.FormatInt(rs[0].ID, 10)

	// Ajustes.
	if _, body = e.get("/ajustes", "ana"); !strings.Contains(body, `name="mantenimiento" value="1" checked`) || !strings.Contains(body, `value="0,100"`) {
		t.Fatal("ajustes por defecto")
	}
	if code, _, body = e.post("/ajustes", "ana", url.Values{"barata": {"1"}, "barata_hasta": {"0,3"}, "cara_desde": {"0,2"}, "unidad_gas": {"m³"}}); code != 200 || !strings.Contains(body, "tiene que ser menor") {
		t.Fatalf("umbrales al revés: %d", code)
	}
	if _, loc, _ := e.post("/ajustes", "ana", url.Values{"barata": {"1"}, "barata_hasta": {"0,07"}, "cara_desde": {"0,30"}, "unidad_gas": {"kWh"}}); loc != "/ajustes?ok=guardado" {
		t.Fatalf("ajustes: %q", loc)
	}
	if st, _ := e.svc.store.settings(ctx, "ana"); !st.MaintOff || !st.PriceLow || st.PriceHigh || !near(st.LowBelow, 0.07) || st.GasUnit != "kWh" {
		t.Fatalf("guardados: %+v", st)
	}

	// Otra persona no ve ni toca nada.
	if _, body = e.get("/mantenimiento", "bob"); strings.Contains(body, "los del salón") || strings.Contains(body, "/tarea/"+caldera) || !strings.Contains(body, "Aún no hay tareas") {
		t.Fatal("bob ve las tareas de ana")
	}
	if _, body = e.get("/lecturas", "bob"); strings.Contains(body, "12.645") {
		t.Fatal("bob ve las lecturas de ana")
	}
	if code, _ = e.get("/tarea/"+caldera, "bob"); code != 404 {
		t.Errorf("bob: GET tarea = %d", code)
	}
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/tarea/" + caldera, edit},
		{"/tarea/" + caldera + "/hecho", nil},
		{"/tarea/" + caldera + "/borrar", nil},
		{"/tarea/" + caldera + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/lectura/" + rid + "/borrar", nil},
	} {
		if code, _, _ := e.post(p.path, "bob", p.form); code != 404 {
			t.Errorf("bob: POST %s = %d", p.path, code)
		}
	}
	if code, _, body := e.do("GET", "/api/avisos", "bob", nil); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("bob recibe avisos: %s", body)
	}

	// La pasarela recoge los avisos (una vez); la Agenda, las líneas de hoy.
	e.svc.store.saveSettings(ctx, "ana", settingsData{PriceLow: true, LowBelow: 0.07})
	e.f.set(func(f *fakeREE) { f.published["2026-11-10"] = true })
	e.c.set(at("2026-11-09", 20, 40))
	e.svc.round(ctx)
	code, _, body = e.do("GET", "/api/avisos", "ana", nil)
	var got []notice
	json.Unmarshal([]byte(body), &got)
	if code != 200 || len(got) != 1 || got[0].Kind != "casa" || !strings.Contains(got[0].Text, "muy barata") {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, _, body = e.do("GET", "/api/avisos", "ana", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("avisos repetidos: %s", body)
	}
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Mañana · martes, 10 de noviembre") || !strings.Contains(body, "de 12 a 15 h") {
		t.Fatal("no salen los precios de mañana")
	}
	call := func(gateway bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/interno/hoy", strings.NewReader(`{"usuario":"ana"}`))
		req.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			req.Header.Set("X-Forwarded-Prefix", "/casa")
		}
		rec := httptest.NewRecorder()
		(&web{svc: e.svc}).internalToday(rec, req)
		return rec
	}
	if rec := call(true); rec.Code != http.StatusForbidden {
		t.Fatalf("interno por la pasarela: %d", rec.Code)
	}
	e.c.set(at("2026-11-09", 8, 0))
	rec := call(false)
	var out struct {
		Lines []string `json:"lineas"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Lines) != 2 || !strings.HasPrefix(out.Lines[0], "Luz hoy:") {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}

	// Borrar una lectura y una tarea (con confirmación).
	if _, loc, _ := e.post("/lectura/"+rid+"/borrar", "ana", url.Values{"contador": {"luz"}}); loc != "/lecturas?c=luz&ok=borrada" {
		t.Fatalf("borrar lectura: %q", loc)
	}
	if _, loc, _ := e.post("/tarea/"+caldera+"/borrar", "ana", nil); loc != "/tarea/"+caldera+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}
	if _, body = e.get("/tarea/"+caldera+"?borrar=1", "ana"); !strings.Contains(body, "¿Borrar «Revisión de la caldera»?") {
		t.Fatal("no pide confirmación")
	}
	if _, loc, _ := e.post("/tarea/"+caldera+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/mantenimiento?ok=borrada" {
		t.Fatalf("borrar: %q", loc)
	}
	if l, _ := e.svc.store.tasks(ctx, "ana"); len(l) != 1 {
		t.Fatal("no se ha borrado")
	}
}
