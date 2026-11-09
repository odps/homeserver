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
	tv  *fakeTV
	c   *clock
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	svc, f, c := newTestService(t)
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, tv: f, c: c, srv: srv}
}

// do hace una petición como user ("" sin identidad) y devuelve el estado,
// la redirección y el cuerpo.
func (e *webEnv) do(method, path, user string, form url.Values) (int, string, string, http.Header) {
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
	return resp.StatusCode, resp.Header.Get("Location"), string(b), resp.Header
}

func (e *webEnv) get(path, user string) (int, string) {
	code, _, body, _ := e.do("GET", path, user, nil)
	return code, body
}

func (e *webEnv) post(path, user string, form url.Values) (int, string, string) {
	if form == nil {
		form = url.Values{}
	}
	code, loc, body, _ := e.do("POST", path, user, form)
	return code, loc, body
}

func TestWebFlows(t *testing.T) {
	e := newWebEnv(t)
	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no sigues nada") {
		t.Fatalf("ahora vacío: %d", code)
	}
	for _, f := range []string{"app.css", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}

	// Buscar (por POST: lo buscado no va en la URL) y seguir.
	code, _, body := e.post("/buscar", "ana", url.Values{"q": {"ejemplo"}})
	if code != 200 || !strings.Contains(body, `name="tvmaze" value="1001"`) || !strings.Contains(body, "Canal de Ejemplo") {
		t.Fatalf("buscar: %d", code)
	}
	if code, _, body = e.post("/buscar", "ana", url.Values{"q": {"  "}}); code != 200 || !strings.Contains(body, "Escribe el nombre") {
		t.Fatalf("búsqueda vacía: %d", code)
	}
	code, loc, _ := e.post("/seguir", "ana", url.Values{"tvmaze": {"1001"}, "estado": {stateWatching}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/serie/") || !strings.HasSuffix(loc, "?ok=siguiendo") {
		t.Fatalf("seguir: %d %q", code, loc)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(loc, "/serie/"), "?ok=siguiendo")
	if _, loc, _ = e.post("/seguir", "ana", url.Values{"tvmaze": {"1001"}}); loc != "/serie/"+id+"?ok=ya-la-seguias" {
		t.Fatalf("seguirla dos veces: %q", loc)
	}
	if code, _, body = e.post("/seguir", "ana", url.Values{"tvmaze": {"4040"}}); code != 200 || !strings.Contains(body, "TVmaze no tiene esa serie") {
		t.Fatalf("la que no existe: %d", code)
	}
	if code, _, _ = e.post("/seguir", "ana", url.Values{"tvmaze": {"../x"}}); code != 200 {
		t.Fatalf("id raro: %d", code)
	}
	if code, body = e.get("/serie/"+id, "ana"); code != 200 || !strings.Contains(body, "Serie de Ejemplo") || !strings.Contains(body, "Temporada 2") {
		t.Fatalf("ficha: %d", code)
	}

	// El cartel lo sirve la app.
	code, _, img, h := e.do("GET", "/cartel/"+id, "ana", nil)
	if code != 200 || h.Get("Content-Type") != "image/jpeg" || h.Get("Cache-Control") != "private, no-store" || h.Get("X-Content-Type-Options") != "nosniff" || len(img) < 100 {
		t.Fatalf("cartel: %d %v", code, h)
	}

	// Marcar: hasta el 1x03, luego el siguiente desde Ahora.
	if code, loc, _ = e.post("/serie/"+id+"/visto", "ana", url.Values{"accion": {"hasta:50003"}}); code != http.StatusSeeOther || loc != "/serie/"+id+"?ok=visto#t1" {
		t.Fatalf("hasta aquí: %d %q", code, loc)
	}
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Te toca ver") || !strings.Contains(body, `value="uno:50101"`) {
		t.Fatal("ahora no dice lo que toca")
	}
	if _, loc, _ = e.post("/serie/"+id+"/visto", "ana", url.Values{"accion": {"uno:50101"}, "desde": {"ahora"}}); loc != "/?ok=visto" {
		t.Fatalf("visto desde ahora: %q", loc)
	}
	if _, loc, _ = e.post("/serie/"+id+"/visto", "ana", url.Values{"accion": {"sin-temporada:1"}}); loc != "/serie/"+id+"?ok=no-visto#t1" {
		t.Fatalf("desmarcar temporada: %q", loc)
	}
	for _, bad := range []string{"", "uno", "uno:x", "borrar:1", "temporada:99999"} {
		if code, _, _ = e.post("/serie/"+id+"/visto", "ana", url.Values{"accion": {bad}}); code != http.StatusBadRequest {
			t.Errorf("acción %q: %d", bad, code)
		}
	}
	if code, _, body = e.post("/serie/"+id+"/visto", "ana", url.Values{"accion": {"uno:70001"}}); code != 200 || !strings.Contains(body, "no es de esta serie") {
		t.Fatalf("capítulo de otra: %d", code)
	}
	f, _ := e.svc.store.follow(context.Background(), "ana", mustID(id))
	if len(f.Watched) != 1 || f.Watched[0] != 50101 {
		t.Fatalf("vistos: %v", f.Watched)
	}

	// Estado, valoración y nota.
	if _, loc, _ = e.post("/serie/"+id, "ana", url.Values{"estado": {statePaused}, "valoracion": {"4"}, "nota": {"muy buena"}}); loc != "/serie/"+id+"?ok=guardado" {
		t.Fatalf("guardar: %q", loc)
	}
	if code, _, body = e.post("/serie/"+id, "ana", url.Values{"estado": {"raro"}}); code != 200 || !strings.Contains(body, "estado no es válido") {
		t.Fatalf("estado raro: %d", code)
	}
	if _, body = e.get("/series", "ana"); !strings.Contains(body, "En pausa") || !strings.Contains(body, "★★★★☆") {
		t.Fatal("la lista no lo refleja")
	}

	// Pelis y juegos.
	if code, loc, _ = e.post("/pelis", "ana", url.Values{"titulo": {"Peli de Prueba"}, "anio": {"2024"}, "estado": {stateDone}, "valoracion": {"5"}, "plataforma": {"Cine"}}); code != http.StatusSeeOther || loc != "/pelis?ok=anadido" {
		t.Fatalf("apuntar peli: %d %q", code, loc)
	}
	if code, _, body = e.post("/pelis", "ana", url.Values{"titulo": {"X"}, "anio": {"dos mil"}}); code != 200 || !strings.Contains(body, "cuatro cifras") || !strings.Contains(body, `value="dos mil"`) {
		t.Fatalf("año malo: %d", code)
	}
	if _, loc, _ = e.post("/juegos", "ana", url.Values{"titulo": {"Juego de Prueba"}, "estado": {stateWatching}}); loc != "/juegos?ok=anadido" {
		t.Fatalf("apuntar juego: %q", loc)
	}
	pelis, _ := e.svc.store.items(context.Background(), "ana", kindFilm)
	juegos, _ := e.svc.store.items(context.Background(), "ana", kindGame)
	if len(pelis) != 1 || len(juegos) != 1 || pelis[0].Day != "2026-11-09" || pelis[0].Origin != "web" {
		t.Fatalf("pelis %+v juegos %+v", pelis, juegos)
	}
	peli, juego := idStr(pelis[0].ID), idStr(juegos[0].ID)
	if code, body = e.get("/pelis/"+peli, "ana"); code != 200 || !strings.Contains(body, "Peli de Prueba") {
		t.Fatalf("ficha de peli: %d", code)
	}
	if code, _ = e.get("/juegos/"+peli, "ana"); code != 404 {
		t.Fatalf("una peli por la ruta de juegos: %d", code)
	}
	if _, loc, _ = e.post("/pelis/"+peli, "ana", url.Values{"titulo": {"Peli de Prueba"}, "estado": {stateDone}, "valoracion": {"3"}, "fecha": {"2026-11-01"}}); loc != "/pelis/"+peli+"?ok=guardado" {
		t.Fatalf("editar peli: %q", loc)
	}
	if code, _, body = e.post("/pelis/"+peli, "ana", url.Values{"titulo": {""}}); code != 200 || !strings.Contains(body, "Falta el título") {
		t.Fatalf("sin título: %d", code)
	}
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Juego de Prueba") {
		t.Fatal("ahora no enseña el juego en curso")
	}

	// Otra persona no ve ni toca nada.
	if _, body = e.get("/series", "bob"); strings.Contains(body, "Serie de Ejemplo") {
		t.Fatal("bob ve las series de ana")
	}
	for _, p := range []string{"/serie/" + id, "/cartel/" + id, "/pelis/" + peli, "/juegos/" + juego} {
		if code, _ = e.get(p, "bob"); code != 404 {
			t.Errorf("bob: GET %s = %d", p, code)
		}
	}
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/serie/" + id, url.Values{"estado": {stateAbandoned}}},
		{"/serie/" + id + "/visto", url.Values{"accion": {"uno:50102"}}},
		{"/serie/" + id + "/borrar", url.Values{"confirmar": {"1"}}},
		{"/pelis/" + peli, url.Values{"titulo": {"robada"}}},
		{"/pelis/" + peli + "/borrar", url.Values{"confirmar": {"1"}}},
	} {
		if code, _, _ = e.post(p.path, "bob", p.form); code != 404 {
			t.Errorf("bob: POST %s = %d", p.path, code)
		}
	}
	if code, _, body, _ := e.do("GET", "/api/avisos", "bob", nil); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("bob recibe avisos: %s", body)
	}

	// La pasarela recoge los avisos (una vez); la Agenda, las líneas de hoy.
	e.c.set(at(9, 10))
	if n, err := e.svc.checkNotices(context.Background()); err != nil || n != 1 {
		t.Fatalf("avisos: %d %v", n, err)
	}
	code, _, body, _ = e.do("GET", "/api/avisos", "ana", nil)
	var list []notice
	json.Unmarshal([]byte(body), &list)
	if code != 200 || len(list) != 1 || list[0].Kind != "series" || !strings.Contains(list[0].Text, "2x02 de «Serie de Ejemplo»") {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, _, body, _ = e.do("GET", "/api/avisos", "ana", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("avisos repetidos: %s", body)
	}
	call := func(gateway bool, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/interno/hoy", strings.NewReader(`{"usuario":"`+user+`"}`))
		req.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			req.Header.Set("X-Forwarded-Prefix", "/series")
		}
		rec := httptest.NewRecorder()
		(&web{svc: e.svc}).internalToday(rec, req)
		return rec
	}
	if rec := call(true, "ana"); rec.Code != http.StatusForbidden {
		t.Fatalf("interno por la pasarela: %d", rec.Code)
	}
	if rec := call(false, "ana"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lineas":["Serie de Ejemplo 2x02 «El domingo», 03:00"]`) {
		t.Fatalf("interno: %d %s", rec.Code, rec.Body)
	}
	if rec := call(false, "bob"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"lineas":[]`) {
		t.Fatalf("interno sin series: %d %s", rec.Code, rec.Body)
	}

	// Borrar, con confirmación.
	if _, loc, _ = e.post("/pelis/"+peli+"/borrar", "ana", nil); loc != "/pelis/"+peli+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %q", loc)
	}
	if _, body = e.get("/pelis/"+peli+"?borrar=1", "ana"); !strings.Contains(body, "¿Borrar «Peli de Prueba»?") {
		t.Fatal("no pide confirmación")
	}
	if _, loc, _ = e.post("/pelis/"+peli+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/pelis?ok=borrado" {
		t.Fatalf("borrar peli: %q", loc)
	}
	if _, loc, _ = e.post("/serie/"+id+"/borrar", "ana", nil); loc != "/serie/"+id+"?borrar=1#borrar" {
		t.Fatalf("dejar de seguir sin confirmar: %q", loc)
	}
	if _, loc, _ = e.post("/serie/"+id+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/series?ok=borrada" {
		t.Fatalf("dejar de seguir: %q", loc)
	}
	if code, _ = e.get("/serie/"+id, "ana"); code != 404 {
		t.Fatalf("sigue la ficha: %d", code)
	}
}

func mustID(s string) int64 {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(err)
	}
	return id
}
