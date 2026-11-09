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
	t    *testing.T
	svc  *service
	site *site
	srv  *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	svc, s, _ := newTestService(t)
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, site: s, srv: srv}
}

// do hace una petición como user ("" sin identidad) y devuelve el estado,
// la redirección y el cuerpo.
func (e *webEnv) do(method, path, user string, form url.Values, header ...string) (int, string, string) {
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
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") && resp.Header.Get("Content-Security-Policy") != csp {
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
	if code, _ := e.get("/", "token:talia"); code != http.StatusUnauthorized {
		t.Fatalf("token sin persona: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no has guardado nada") {
		t.Fatalf("vacío: %d", code)
	}
	if code, body := e.get("/static/app.css", ""); code != 200 || !strings.Contains(body, "--paper") {
		t.Fatalf("estático: %d", code)
	}

	// Guardar.
	code, loc, _ := e.post("/guardar", "ana", url.Values{"url": {e.site.url("/articulo/tomates")}, "etiquetas": {"huerto, verano"}})
	if code != http.StatusSeeOther || loc != "/?ok=guardado" {
		t.Fatalf("guardar: %d %s", code, loc)
	}
	if _, loc, _ := e.post("/guardar", "ana", url.Values{"url": {e.site.url("/articulo/tomates")}}); loc != "/?ok=ya-estaba" {
		t.Fatalf("repetido: %s", loc)
	}
	if _, loc, _ := e.post("/guardar", "ana", url.Values{"url": {e.site.url("/no-existe")}}); loc != "/?ok=sin-texto" {
		t.Fatalf("no se puede leer: %s", loc)
	}
	code, _, body := e.post("/guardar", "ana", url.Values{"url": {"https://192.168.1.1/admin"}, "etiquetas": {"casa"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Esa dirección es de la red local") || !strings.Contains(body, `value="https://192.168.1.1/admin"`) {
		t.Fatalf("dirección local: %d", code)
	}
	code, body = e.get("/?ok=guardado", "ana")
	if code != 200 || !strings.Contains(body, "Guardado.") || !strings.Contains(body, "Cómo cultivar tomates en el balcón") ||
		!strings.Contains(body, "No se ha podido leer la página") || !strings.Contains(body, `name="etiqueta" value="huerto"`) {
		t.Fatalf("pendientes: %d", code)
	}
	list, _, _ := e.svc.store.list(ctx, "ana", filter{List: listAll})
	var tom, bad article
	for _, a := range list {
		if a.State == stateOK {
			tom = a
		} else {
			bad = a
		}
	}
	id := strconv.FormatInt(tom.ID, 10)

	// Leer.
	code, body = e.get("/articulo/"+id, "ana")
	if code != 200 || !strings.Contains(body, "<h1 class=\"reader-title\" lang=\"es\">Cómo cultivar tomates en el balcón</h1>") || !strings.Contains(body, "Lo básico de los tomates") ||
		strings.Contains(body, "<script") || strings.Contains(body, "rastreo") {
		t.Fatalf("lector: %d", code)
	}
	if code, body := e.get("/articulo/"+strconv.FormatInt(bad.ID, 10), "ana"); code != 200 || !strings.Contains(body, "Reintentar") || !strings.Contains(body, "(404)") {
		t.Fatalf("lector con error: %d", code)
	}
	if code, _, _ := e.post("/articulo/"+strconv.FormatInt(bad.ID, 10)+"/reintentar", "ana", nil); code != http.StatusSeeOther {
		t.Fatalf("reintentar: %d", code)
	}

	// Marcar.
	for _, c := range []struct{ campo, valor, volver, want string }{
		{"leido", "1", "articulo", "/articulo/" + id + "?ok=leido"},
		{"favorito", "1", "favoritos", "/favoritos?ok=favorito"},
		{"archivado", "1", "pendientes", "/?ok=archivado"},
		{"archivado", "0", "archivo", "/archivo?ok=devuelto"},
		{"leido", "0", "https://otro.ejemplo.es/", "/?ok=no-leido"},
	} {
		code, loc, _ := e.post("/articulo/"+id+"/marcar", "ana", url.Values{"campo": {c.campo}, "valor": {c.valor}, "volver": {c.volver}})
		if code != http.StatusSeeOther || loc != c.want {
			t.Errorf("marcar %+v: %d %s", c, code, loc)
		}
	}
	if code, _, _ := e.post("/articulo/"+id+"/marcar", "ana", url.Values{"campo": {"borrado"}, "valor": {"1"}}); code != http.StatusBadRequest {
		t.Fatalf("campo raro: %d", code)
	}
	if a, _ := e.svc.store.get(ctx, "ana", tom.ID); a.Read || !a.Favorite || a.Archived {
		t.Fatalf("tras marcar: %+v", a)
	}
	if _, body := e.get("/favoritos", "ana"); !strings.Contains(body, "Cómo cultivar tomates") {
		t.Fatal("favoritos")
	}

	// Etiquetas.
	if _, loc, _ := e.post("/articulo/"+id+"/etiquetas", "ana", url.Values{"etiquetas": {"jardín, balcón"}}); loc != "/articulo/"+id+"?ok=etiquetas" {
		t.Fatalf("etiquetas: %s", loc)
	}
	if code, _, body := e.post("/articulo/"+id+"/etiquetas", "ana", url.Values{"etiquetas": {"a,b,c,d,e,f,g,h,i,j,k"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Como mucho 10") ||
		!strings.Contains(body, `value="a,b,c,d,e,f,g,h,i,j,k"`) {
		t.Fatalf("demasiadas etiquetas: %d", code)
	}

	// Buscar, por POST.
	code, _, body = e.post("/buscar", "ana", url.Values{"q": {"tomates"}, "lista": {"archivo"}})
	if code != 200 || !strings.Contains(body, "Resultados") || !strings.Contains(body, "Cómo cultivar tomates") || !strings.Contains(body, `aria-current="page"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 5.5h16v4H4z"/>`) {
		t.Fatalf("buscar: %d", code)
	}
	if _, _, body := e.post("/buscar", "ana", url.Values{"etiqueta": {"JARDÍN"}}); !strings.Contains(body, "Cómo cultivar tomates") || !strings.Contains(body, `class="chip on"`) {
		t.Fatal("por etiqueta")
	}
	if _, _, body := e.post("/buscar", "ana", url.Values{"q": {"inexistente"}}); !strings.Contains(body, "No ha salido nada") {
		t.Fatal("sin resultados")
	}

	// Tamaño de letra y ajustes.
	if _, loc, _ := e.post("/tamano", "ana", url.Values{"tamano": {"xl"}, "id": {id}}); loc != "/articulo/"+id {
		t.Fatalf("tamaño: %s", loc)
	}
	if _, body := e.get("/articulo/"+id, "ana"); !strings.Contains(body, "size-xl") {
		t.Fatal("no aplica el tamaño")
	}
	if code, _, _ := e.post("/tamano", "ana", url.Values{"tamano": {"enorme"}}); code != http.StatusBadRequest {
		t.Fatalf("tamaño raro: %d", code)
	}
	if _, loc, _ := e.post("/ajustes", "ana", url.Values{"tamano": {"s"}, "semanal": {"1"}}); loc != "/ajustes?ok=ajustes" {
		t.Fatalf("ajustes: %s", loc)
	}
	if p, _ := e.svc.store.prefs(ctx, "ana"); p != (prefs{Size: "s", Summary: false, Weekly: true}) {
		t.Fatalf("ajustes guardados: %+v", p)
	}
	if _, body := e.get("/ajustes", "ana"); !strings.Contains(body, `name="tamano" value="s" checked`) || !strings.Contains(body, `name="semanal" value="1" checked`) {
		t.Fatal("página de ajustes")
	}

	// Lo de otro no se ve ni se toca.
	if code, _ := e.get("/articulo/"+id, "luis"); code != http.StatusNotFound {
		t.Fatalf("luis lee lo de ana: %d", code)
	}
	for _, p := range []string{"/marcar", "/etiquetas", "/reintentar", "/borrar"} {
		if code, _, _ := e.post("/articulo/"+id+p, "luis", url.Values{"campo": {"archivado"}, "valor": {"1"}, "etiquetas": {"x"}, "confirmar": {"1"}}); code != http.StatusNotFound {
			t.Errorf("luis %s: %d", p, code)
		}
	}
	if _, _, body := e.post("/buscar", "luis", url.Values{"q": {"tomates"}}); strings.Contains(body, "Cómo cultivar") {
		t.Fatal("luis encuentra lo de ana")
	}
	if code, _ := e.get("/articulo/abc", "ana"); code != http.StatusNotFound {
		t.Fatalf("id raro: %d", code)
	}

	// Borrar pide confirmación.
	if _, loc, _ := e.post("/articulo/"+id+"/borrar", "ana", nil); loc != "/articulo/"+id+"?borrar=1#borrar" {
		t.Fatalf("borrar sin confirmar: %s", loc)
	}
	if _, body := e.get("/articulo/"+id+"?borrar=1", "ana"); !strings.Contains(body, "¿Borrar este artículo?") {
		t.Fatal("confirmación")
	}
	if _, loc, _ := e.post("/articulo/"+id+"/borrar", "ana", url.Values{"confirmar": {"1"}}); loc != "/?ok=borrado" {
		t.Fatalf("borrar: %s", loc)
	}
	if code, _ := e.get("/articulo/"+id, "ana"); code != http.StatusNotFound {
		t.Fatalf("sigue tras borrarlo: %d", code)
	}

	// Formulario demasiado grande.
	if code, _, _ := e.post("/guardar", "ana", url.Values{"url": {strings.Repeat("a", maxForm)}}); code != http.StatusBadRequest {
		t.Fatalf("formulario grande: %d", code)
	}
}

func TestNoticesAndToday(t *testing.T) {
	e := newWebEnv(t)
	ctx := context.Background()
	e.svc.store.addNotice(ctx, "ana", "📚 Tienes 2 artículos pendientes en Leer después.", e.svc.clock())
	code, body := e.get("/api/avisos", "ana")
	var got []notice
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got) != 1 || got[0].Kind != "leer" {
		t.Fatalf("avisos: %d %s", code, body)
	}
	if _, body := e.get("/api/avisos", "ana"); strings.TrimSpace(body) != "[]" {
		t.Fatalf("se entregan dos veces: %s", body)
	}
	if code, _ := e.get("/api/avisos", ""); code != http.StatusUnauthorized {
		t.Fatalf("avisos sin identidad: %d", code)
	}

	e.svc.save(ctx, "ana", e.site.url("/articulo/tomates"), nil, "web")
	call := func(user string, header ...string) (int, string) {
		b, _ := json.Marshal(map[string]string{"usuario": user})
		req, _ := http.NewRequest("POST", e.srv.URL+"/interno/hoy", strings.NewReader(string(b)))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	code, body = call("ana")
	var res struct {
		Lines []string `json:"lineas"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &res) != nil || len(res.Lines) != 1 ||
		res.Lines[0] != "1 artículo pendiente · sugerencia: «Cómo cultivar tomates en el balcón» (2 min)" {
		t.Fatalf("hoy: %d %s", code, body)
	}
	if code, body := call("luis"); code != 200 || !strings.Contains(body, `"lineas":[]`) {
		t.Fatalf("hoy de luis: %d %s", code, body)
	}
	// Por la pasarela (con su cabecera), no.
	if code, _ := call("ana", "X-Forwarded-Prefix", "/leer"); code != http.StatusForbidden {
		t.Fatalf("desde fuera: %d", code)
	}
	if code, _ := call(""); code != http.StatusBadRequest {
		t.Fatalf("sin usuario: %d", code)
	}
}
