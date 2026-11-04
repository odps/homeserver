package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWebRender ejecuta cada plantilla con datos realistas (y los casos
// límite) sin base de datos. Con COMPRA_PREVIEW_DIR escribe las páginas y lo
// estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	now := time.Date(2026, 11, 3, 19, 30, 0, 0, madrid)
	w := &web{svc: &service{log: slog.New(slog.DiscardHandler), now: func() time.Time { return now }}}
	added := now.Add(-50 * time.Hour)
	mk := func(id int64, name, qty, note, sec string, done bool, src string) item {
		return item{ID: id, Done: done, Source: src, itemData: itemData{Name: name, Qty: qty, Note: note, Section: sec, Added: added}}
	}
	full := []item{
		mk(1, "tomates", "1 kg", "", "fruta", false, sourceWeb),
		mk(2, "plátanos", "", "de Canarias", "fruta", true, sourceWeb),
		mk(3, "lechuga", "", "", "fruta", false, sourceAssistant),
		mk(4, "Yogur griego", "x4", "natural, sin azúcar", "lacteos", false, sourceWeb),
		mk(5, "leche sin lactosa", "6 l", "", "lacteos", true, sourceAssistant),
		mk(6, "huevos", "12", "", "lacteos", false, sourceWeb),
		mk(7, "lejía", "", "", "limpieza", false, sourceWeb),
		mk(8, "pan de molde integral con semillas y nombre muy largo para ver cómo se parte en el móvil", "2 paquetes", "", "panaderia", false, sourceWeb),
		mk(9, "pilas", "", "AA", "otros", false, sourceAssistant),
		mk(10, "comida para gato", "3 latas", "", "mascotas", false, sourceWeb),
	}
	order := normalizeOrder([]string{"limpieza"}) // limpieza primero
	secs, total, done := groupItems(full, order)
	if len(secs) != 6 || secs[0].Key != "limpieza" || total != 10 || done != 2 {
		t.Fatalf("secciones: %d %v %d %d", len(secs), secs[0].Key, total, done)
	}
	var fr sectionView
	for _, s := range secs {
		if s.Key == "fruta" {
			fr = s
		}
	}
	if fr.Left != 2 || fr.Items[2].Name != "plátanos" || !fr.Items[2].Done || fr.Items[1].By != "Talia" {
		t.Fatalf("lo tachado al final: %+v", fr)
	}
	running, usual := suggViews(
		[]suggestion{{Name: "café", Times: 9, AvgDays: 6.6, Since: 8}, {Name: "papel higiénico", Times: 4, AvgDays: 14, Since: 15}},
		[]suggestion{{Name: "leche", Times: 31}, {Name: "plátanos", Times: 12}, {Name: "pan", Times: 2}, {Name: "aceite de oliva virgen extra", Times: 5}})
	if running[0].Meta != "Cada 7 días · la última, hace 8 días" || usual[0].Meta != "31 veces" {
		t.Fatalf("sugerencias: %+v %+v", running, usual)
	}
	listData := func(d pageData) pageData {
		d.Tab, d.Script = "lista", true
		return d
	}
	edit := w.itemFormOf(full[4])
	edit.SectionTxt = sectionLabel(edit.Section)
	if edit.Added != "domingo 1 de noviembre" || edit.Source != "Talia" {
		t.Fatalf("editar: %+v", edit)
	}
	plain := w.itemFormOf(mk(11, "atún", "3 latas", "", "conservas", false, sourceWeb))
	plain.SectionTxt = sectionLabel(plain.Section)

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"lista", "lista.html", listData(pageData{Name: "Olga", Sections: secs, Total: total, Done: done, Left: total - done,
			Flash: "Añadido: 3 cosas. 1 ya estaba en la lista.", Running: running, Usual: usual})},
		{"lista-sin-sugerencias", "lista.html", listData(pageData{Sections: secs[:2], Total: 3, Done: 0, Left: 3})},
		{"lista-vaciar", "lista.html", listData(pageData{Sections: secs, Total: total, Done: done, Left: total - done, ConfirmClear: true})},
		{"lista-vacia", "lista.html", listData(pageData{})},
		{"lista-sugerencias", "lista.html", listData(pageData{Running: running, Usual: usual, Flash: "Hecho: 7 cosas pasan al historial."})},
		{"lista-error", "lista.html", listData(pageData{Error: errorsText["vacio"]})},
		{"cosa", "cosa.html", pageData{Tab: "lista", Title: "Editar", Item: edit, Options: w.sectionOptions(edit.Section)}},
		{"cosa-error", "cosa.html", pageData{Tab: "lista", Title: "Editar", Item: plain, Options: w.sectionOptions(plain.Section), Error: "Ponle un nombre."}},
		{"secciones", "secciones.html", pageData{Tab: "secciones", Order: orderOptions(order), Flash: "Orden guardado."}},
	}
	wants := map[string][]string{
		"lista": {"La lista", `<span id="quedan" class="tnum">8</span> por comprar`, "Limpieza", "Fruta y verdura", "Lácteos y huevos",
			`id="i-2"`, `class="item hecho"`, `aria-pressed="true"`, `name="hecho" value="0"`, `name="hecho" value="1"`, "1 kg", "de Canarias",
			`<span class="by">Talia</span>`, `action="/compra/marcar"`, `href="/compra/cosa/4"`, `data-api="/compra/api/marcar"`,
			"He terminado", `<span id="tachadas" class="tnum">2</span> en el carro`, "Vaciar la lista…", "Se te estará acabando", "Lo de siempre",
			`name="nombre" value="café"`, "Cada 7 días", "31 veces", `action="/compra/anadir"`, `name="texto"`},
		"lista-vaciar":      {"¿Vaciar la lista entera?", `name="confirmar" value="1"`, `action="/compra/vaciar"`, "Sí, vaciar la lista"},
		"lista-vacia":       {"La lista está vacía", "Vacía"},
		"lista-sugerencias": {"La lista está vacía", "Lo de siempre", "Se te estará acabando", "papel higiénico"},
		"lista-error":       {"No he entendido nada que añadir"},
		"cosa":              {"leche sin lactosa", `value="6 l"`, `<option value="lacteos" selected>Lácteos y huevos</option>`, "Tachada", "Añadida el domingo 1 de noviembre por Talia", `action="/compra/cosa/5"`, `action="/compra/cosa/5/borrar"`, "se recuerda"},
		"cosa-error":        {"Ponle un nombre.", `<option value="conservas" selected>`},
		"secciones":         {"Secciones", `name="subir" value="limpieza"`, `aria-label="Subir Limpieza" disabled`, `aria-label="Bajar Otros" disabled`, `id="sec-fruta"`, "Orden de siempre"},
	}
	nots := map[string][]string{
		"lista-sin-sugerencias": {"Lo de siempre", "Se te estará acabando"},
		"lista-vacia":           {"He terminado", "Lo de siempre", `class="lista"`},
	}

	dir := os.Getenv("COMPRA_PREVIEW_DIR")
	base := "/compra"
	if dir != "" {
		base = "."
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base = "v0.0.0-test", base
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			// El único script: el de la lista, como fichero.
			script := `<script src="` + base + `/static/lista.js" defer></script>`
			if d.Script != strings.Contains(out, script) {
				t.Errorf("%s: script = %v", p.file, !d.Script)
			}
			out = strings.Replace(out, script, "", 1)
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s contiene %q", p.file, bad)
				}
			}
			if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
				t.Errorf("%s tiene un manejador de eventos", p.file)
			}
			if !strings.Contains(out, `aria-current="page"`) {
				t.Errorf("%s: falta la pestaña activa", p.file)
			}
			if dir == "" {
				for _, want := range wants[p.file] {
					if !strings.Contains(out, want) {
						t.Errorf("%s: falta %q", p.file, want)
					}
				}
				for _, not := range nots[p.file] {
					if strings.Contains(out, not) {
						t.Errorf("%s: sobra %q", p.file, not)
					}
				}
			} else if err := os.WriteFile(filepath.Join(dir, p.file+".html"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
	if dir != "" {
		static, _ := fs.Sub(webFS, "web/static")
		os.MkdirAll(filepath.Join(dir, "static"), 0o755)
		for _, n := range []string{"app.css", "icon.svg", "lista.js"} {
			b, _ := fs.ReadFile(static, n)
			os.WriteFile(filepath.Join(dir, "static", n), b, 0o644)
		}
	}
}

// ---- Flujos por HTTP con la base de datos de pruebas ----

type webEnv struct {
	t   *testing.T
	svc *service
	clk *clock
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	svc, c := newTestService(t)
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, clk: c, srv: srv}
}

// do hace una petición como user ("" sin identidad) y devuelve el estado,
// la redirección y el cuerpo.
func (e *webEnv) do(method, path, user string, body io.Reader, ctype string, hdr ...string) (int, string, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
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
	want := csp
	if method == "GET" && (path == "/" || strings.HasPrefix(path, "/?")) {
		want = cspLista
	}
	if resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") && resp.Header.Get("Content-Security-Policy") != want {
		e.t.Errorf("%s %s sin su CSP", method, path)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

func (e *webEnv) get(path, user string) (int, string) {
	code, _, body := e.do("GET", path, user, nil, "")
	return code, body
}

func (e *webEnv) post(path, user string, form url.Values) (int, string, string) {
	return e.do("POST", path, user, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

func (e *webEnv) api(user, body string, hdr ...string) (int, string) {
	code, _, b := e.do("POST", "/api/marcar", user, strings.NewReader(body), "application/json", hdr...)
	return code, b
}

func (e *webEnv) items(owner string) []item {
	list, err := e.svc.store.items(context.Background(), owner)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func id(it item) string { return strconv.FormatInt(it.ID, 10) }

func TestWebFlows(t *testing.T) {
	e := newWebEnv(t)
	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "La lista está vacía") || !strings.Contains(body, "/static/lista.js") {
		t.Fatalf("lista vacía: %d", code)
	}
	for _, f := range []string{"app.css", "lista.js", "icon.svg"} {
		if code, _ := e.get("/static/"+f, "ana"); code != 200 {
			t.Fatalf("%s: %d", f, code)
		}
	}

	// Añadir.
	code, loc, _ := e.post("/anadir", "ana", url.Values{"texto": {"Yogur griego, 2 kg de patatas, lejía y pan"}})
	if code != http.StatusSeeOther || loc != "/?ok=anadido&n=4&j=0&d=0" {
		t.Fatalf("añadir: %d %q", code, loc)
	}
	_, body := e.get(loc, "ana")
	for _, want := range []string{"Añadido: 4 cosas.", "Yogur griego", "2 kg", "Lácteos y huevos", "Fruta y verdura", "Limpieza", "Panadería", `<span id="quedan" class="tnum">4</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("lista: falta %q", want)
		}
	}
	if _, loc, _ = e.post("/anadir", "ana", url.Values{"texto": {"patatas 1 kg, lejía"}}); loc != "/?ok=anadido&n=0&j=1&d=1" {
		t.Fatalf("repetidas: %q", loc)
	}
	if _, body = e.get(loc, "ana"); !strings.Contains(body, "1 ya estaba y se ha juntado la cantidad. 1 ya estaba en la lista.") || !strings.Contains(body, "3 kg") {
		t.Fatal("aviso de repetidas")
	}
	if _, loc, _ = e.post("/anadir", "ana", url.Values{"texto": {" , y "}}); loc != "/?error=vacio" {
		t.Fatalf("nada: %q", loc)
	}
	if _, body = e.get(loc, "ana"); !strings.Contains(body, "No he entendido nada") {
		t.Fatal("aviso de nada")
	}
	if code, _, _ = e.post("/anadir", "ana", url.Values{"texto": {strings.Repeat("leche ", 4000)}}); code != http.StatusBadRequest {
		t.Fatalf("formulario enorme: %d", code)
	}
	list := e.items("ana")
	yogur, patatas, lejia := list[0], list[1], list[2]

	// Tachar sin JavaScript: el estado final, así que repetirlo da igual.
	for range 2 {
		if code, loc, _ = e.post("/marcar", "ana", url.Values{"id": {id(patatas)}, "hecho": {"1"}}); code != http.StatusSeeOther || loc != "/#i-"+id(patatas) {
			t.Fatalf("tachar: %d %q", code, loc)
		}
	}
	if _, body = e.get("/", "ana"); !strings.Contains(body, `<li class="item hecho" id="i-`+id(patatas)+`"`) || !strings.Contains(body, `<span id="tachadas" class="tnum">1</span>`) {
		t.Fatal("no sale tachada")
	}
	if code, _, _ = e.post("/marcar", "ana", url.Values{"id": {"x"}}); code != http.StatusBadRequest {
		t.Fatalf("id raro: %d", code)
	}

	// Otra persona no ve ni toca nada de ana.
	if _, body = e.get("/", "bob"); strings.Contains(body, "Yogur") || !strings.Contains(body, "La lista está vacía") {
		t.Fatal("bob ve la lista de ana")
	}
	if code, _ = e.get("/cosa/"+id(yogur), "bob"); code != 404 {
		t.Fatalf("bob ve una cosa: %d", code)
	}
	for _, p := range []struct {
		path string
		form url.Values
	}{
		{"/marcar", url.Values{"id": {id(yogur)}, "hecho": {"1"}}},
		{"/cosa/" + id(yogur), url.Values{"nombre": {"robado"}, "seccion": {"otros"}}},
		{"/cosa/" + id(yogur) + "/borrar", nil},
	} {
		if code, _, _ = e.post(p.path, "bob", p.form); code != 404 {
			t.Errorf("bob: POST %s = %d", p.path, code)
		}
	}
	e.post("/terminar", "bob", nil)
	e.post("/vaciar", "bob", url.Values{"confirmar": {"1"}})
	if l := e.items("ana"); len(l) != 4 || l[0].Done || l[0].Name != "Yogur griego" || !l[1].Done {
		t.Fatalf("bob ha cambiado algo: %q", names(l))
	}

	// Editar: la sección nueva se recuerda.
	code, body = e.get("/cosa/"+id(lejia), "ana")
	if code != 200 || !strings.Contains(body, `<option value="limpieza" selected>`) || !strings.Contains(body, "lejía") {
		t.Fatalf("editar: %d", code)
	}
	if code, loc, _ = e.post("/cosa/"+id(lejia), "ana", url.Values{"nombre": {"Lejía"}, "cantidad": {"2botellas"}, "nota": {"con detergente"}, "seccion": {"higiene"}}); code != http.StatusSeeOther || loc != "/?ok=guardado#i-"+id(lejia) {
		t.Fatalf("guardar: %d %q", code, loc)
	}
	if it, _ := e.svc.store.item(context.Background(), "ana", lejia.ID); it.Qty != "2 botellas" || it.Section != "higiene" || it.Note != "con detergente" {
		t.Fatalf("guardado: %+v", it)
	}
	if code, _, _ = e.post("/cosa/"+id(lejia), "ana", url.Values{"nombre": {"Lejía"}, "seccion": {"inventada"}}); code != http.StatusBadRequest {
		t.Fatalf("sección inventada: %d", code)
	}
	if code, _, body = e.post("/cosa/"+id(lejia), "ana", url.Values{"nombre": {"  "}, "seccion": {"higiene"}}); code != 200 || !strings.Contains(body, "Ponle un nombre") {
		t.Fatalf("sin nombre: %d", code)
	}
	if code, loc, _ = e.post("/cosa/"+id(lejia)+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/?ok=borrada" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	e.post("/anadir", "ana", url.Values{"texto": {"lejías"}})
	if l := e.items("ana"); l[len(l)-1].Section != "higiene" {
		t.Fatalf("no recuerda la sección: %+v", l[len(l)-1])
	}
	if code, _ = e.get("/cosa/999999", "ana"); code != 404 {
		t.Fatalf("cosa inventada: %d", code)
	}

	// He terminado: lo tachado pasa al historial.
	if _, loc, _ = e.post("/terminar", "ana", nil); loc != "/?ok=terminado&n=1" {
		t.Fatalf("terminar: %q", loc)
	}
	if _, body = e.get(loc, "ana"); !strings.Contains(body, "Hecho: 1 cosa pasa al historial.") || strings.Contains(body, `class="item-name">patatas<`) {
		t.Fatal("tras terminar")
	}
	if _, loc, _ = e.post("/terminar", "ana", nil); loc != "/?ok=nada-tachado" {
		t.Fatalf("terminar otra vez: %q", loc)
	}

	// Una semana después vuelve a comprar patatas: «lo de siempre».
	e.clk.days(7)
	e.post("/anadir", "ana", url.Values{"texto": {"patatas"}})
	l := e.items("ana")
	e.post("/marcar", "ana", url.Values{"id": {id(l[len(l)-1])}, "hecho": {"1"}})
	e.post("/terminar", "ana", nil)
	e.clk.days(1)
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Lo de siempre") || !strings.Contains(body, `name="nombre" value="patatas"`) || !strings.Contains(body, "2 veces") {
		t.Fatal("sin lo de siempre")
	}
	if _, body = e.get("/", "bob"); strings.Contains(body, "Lo de siempre") {
		t.Fatal("bob ve las sugerencias de ana")
	}
	e.clk.days(6)
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Se te estará acabando") || !strings.Contains(body, "Cada 7 días · la última, hace 7 días") {
		t.Fatal("sin se te estará acabando")
	}
	if _, loc, _ = e.post("/anadir", "ana", url.Values{"nombre": {"patatas"}}); loc != "/?ok=anadido&n=1&j=0&d=0" {
		t.Fatalf("añadir sugerencia: %q", loc)
	}
	if _, body = e.get("/", "ana"); strings.Contains(body, "Se te estará acabando") {
		t.Fatal("sugiere lo que ya está en la lista")
	}

	// Vaciar, con confirmación.
	if _, loc, _ = e.post("/vaciar", "ana", nil); loc != "/?vaciar=1#vaciar" {
		t.Fatalf("vaciar sin confirmar: %q", loc)
	}
	if len(e.items("ana")) == 0 {
		t.Fatal("ha vaciado sin confirmar")
	}
	if _, body = e.get("/?vaciar=1", "ana"); !strings.Contains(body, "Sí, vaciar la lista") {
		t.Fatal("no pide confirmación")
	}
	if _, loc, _ = e.post("/vaciar", "ana", url.Values{"confirmar": {"1"}}); loc != "/?ok=vaciada" {
		t.Fatalf("vaciar: %q", loc)
	}
	if len(e.items("ana")) != 0 {
		t.Fatal("no ha vaciado")
	}

	// Orden de las secciones.
	if code, body = e.get("/secciones", "ana"); code != 200 || !strings.Contains(body, "Fruta y verdura") {
		t.Fatalf("secciones: %d", code)
	}
	if _, loc, _ = e.post("/secciones", "ana", url.Values{"subir": {"carne"}}); loc != "/secciones?ok=orden#sec-carne" {
		t.Fatalf("subir: %q", loc)
	}
	e.post("/secciones", "ana", url.Values{"bajar": {"otros"}}) // ya es la última: nada
	if o, _ := e.svc.store.order(context.Background(), "ana"); o[0] != "carne" || o[1] != "fruta" || o[len(o)-1] != "otros" {
		t.Fatalf("orden: %v", o)
	}
	if o, _ := e.svc.store.order(context.Background(), "bob"); o[0] != "fruta" {
		t.Fatal("el orden de ana cambia el de bob")
	}
	if code, _, _ = e.post("/secciones", "ana", url.Values{"subir": {"inventada"}}); code != http.StatusBadRequest {
		t.Fatalf("sección inventada: %d", code)
	}
	e.post("/secciones", "ana", url.Values{"restablecer": {"1"}})
	if o, _ := e.svc.store.order(context.Background(), "ana"); o[0] != "fruta" {
		t.Fatalf("restablecer: %v", o)
	}
}

func TestAPI(t *testing.T) {
	e := newWebEnv(t)
	e.post("/anadir", "ana", url.Values{"texto": {"yogur, lejía"}})
	list := e.items("ana")
	body := func(it item, done bool) string {
		return `{"id":` + id(it) + `,"hecho":` + strconv.FormatBool(done) + `}`
	}
	done := func() bool {
		it, err := e.svc.store.item(context.Background(), "ana", list[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		return it.Done
	}

	if code, _ := e.api("", body(list[0], true)); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/marcar", "ana", strings.NewReader(body(list[0], true)), "application/x-www-form-urlencoded"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("sin JSON: %d", code)
	}
	if code, _, _ := e.do("POST", "/api/marcar", "ana", strings.NewReader(body(list[0], true)), "text/plain"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", code)
	}
	for _, site := range []string{"cross-site", "same-site", "none"} {
		if code, _ := e.api("ana", body(list[0], true), "Sec-Fetch-Site", site); code != http.StatusForbidden {
			t.Fatalf("Sec-Fetch-Site %s: %d", site, code)
		}
	}
	if done() {
		t.Fatal("se ha tachado con una petición rechazada")
	}
	big := `{"id":` + id(list[0]) + `,"hecho":true,"relleno":"` + strings.Repeat("y", 4000) + `"}`
	if code, _ := e.api("ana", big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("cuerpo enorme: %d", code)
	}
	for _, bad := range []string{`{"id":` + id(list[0]) + `}`, `{"id":0,"hecho":true}`, `{"id":"1","hecho":true}`, `nada`, `{"id":1,"hecho":true,"otro":1}`} {
		if code, _ := e.api("ana", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if done() {
		t.Fatal("se ha tachado con una petición mala")
	}

	// Idempotente: tachar dos veces es tachar.
	for range 2 {
		code, b := e.api("ana", body(list[0], true), "Sec-Fetch-Site", "same-origin")
		if code != 200 || !strings.Contains(b, `"ok":true`) || !strings.Contains(b, `"hecho":true`) {
			t.Fatalf("tachar: %d %s", code, b)
		}
		if !done() {
			t.Fatal("no se ha tachado")
		}
	}
	if code, _ := e.api("ana", body(list[0], false)); code != 200 || done() {
		t.Fatalf("destachar: %d", code)
	}

	// Otra persona: como si no existiera, y no cambia nada.
	if code, b := e.api("bob", body(list[0], true)); code != http.StatusNotFound || !strings.Contains(b, "Ya no está en la lista") {
		t.Fatalf("bob: %d %s", code, b)
	}
	if done() {
		t.Fatal("bob ha tachado lo de ana")
	}
	// Lo que ya se terminó tampoco existe (la cola lo descarta).
	e.api("ana", body(list[1], true))
	e.post("/terminar", "ana", nil)
	if code, _ := e.api("ana", body(list[1], true)); code != http.StatusNotFound {
		t.Fatalf("ya terminada: %d", code)
	}
}
