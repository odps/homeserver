package main

import (
	"bytes"
	"image/jpeg"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
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
// límite) sin base de datos. Con PAPELES_PREVIEW_DIR escribe las páginas y
// la hoja de estilos en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	w := &web{svc: &service{log: slog.New(slog.DiscardHandler), now: func() time.Time { return testNow }}}
	docs := []doc{
		{ID: 1, Type: "dni", Title: "DNI", Number: "12345678Z", Issuer: "Policía Nacional", Issued: "2021-03-01", Expires: "2031-03-01", Alerts: []int{60, 30, 7}, Files: 2},
		{ID: 2, Type: "dni", Title: "DNI de Leo", Holder: "Leo Pérez", Expires: "2026-11-20", Alerts: []int{60, 30, 7}},
		{ID: 3, Type: "pasaporte", Title: "Pasaporte", Number: "PAX123456", Expires: "2026-10-01", Alerts: []int{60, 30, 7}, Files: 1},
		{ID: 4, Type: "itv", Title: "ITV del coche familiar con un título bastante largo para ver cómo se parte en el móvil", Issuer: "Estación ITV Ejemplo", Expires: "2026-11-05", Months: 12, Alerts: []int{30, 7}},
		{ID: 5, Type: "seguro_coche", Title: "Seguro del coche", Issuer: "Seguro Ejemplo", Number: "POL-998877", Expires: "2027-01-15", Months: 12, Alerts: []int{30, 7}, Amount: amount(45050)},
		{ID: 6, Type: "garantia", Title: "Garantía del portátil", Issuer: "Tienda Ejemplo", Issued: "2025-12-01", Expires: "2028-12-01", Alerts: []int{30}},
		{ID: 7, Type: "factura", Title: "Factura de la nevera", Issuer: "Tienda Ejemplo", Amount: amount(64900), Alerts: []int{}},
		{ID: 8, Type: "carne", Title: "Carné de conducir", Expires: "2026-11-04", Alerts: []int{60, 30, 7}},
		{ID: 9, Type: "seguro_hogar", Title: "Seguro de la casa", Expires: "2025-03-01", Months: 12, Alerts: []int{30, 7}},
	}
	byID := map[int64]doc{}
	for _, d := range docs {
		byID[d.ID] = d
	}
	homeData := func(q string) pageData { return w.homeData(docs, q) }
	if d := homeData(""); len(d.Expired) != 2 || d.Expired[0].ID != 3 || len(d.Soon) != 4 || d.Soon[0].ID != 8 || d.Soon[1].ID != 4 ||
		d.Groups[0].Label != "DNI" || d.Groups[0].Docs[0].ID != 2 || d.Groups[1].Label != "Pasaporte" || d.Groups[2].Label != "Carné de conducir" ||
		d.Groups[len(d.Groups)-1].Label != "Factura" {
		t.Fatalf("inicio: %+v", d)
	}
	if d := homeData("seguro"); len(d.Groups) != 2 || d.Expired != nil || d.Title != "Buscar" {
		t.Fatalf("buscar: %+v", d)
	}
	files := []fileView{
		{ID: 11, Name: "dni-delante.jpg", Kind: "Imagen", Size: "412 KB", Mini: true, W: 1600, H: 1000, Added: "4 nov 2026"},
		{ID: 12, Name: "dni-detras-con-un-nombre-de-archivo-largo-de-verdad.jpg", Kind: "Imagen", Size: "388 KB", Mini: true},
		{ID: 13, Name: "escritura.pdf", Kind: "PDF", Size: "2,3 MB"},
	}
	ten := make([]fileView, 10)
	for i := range ten {
		ten[i] = fileView{ID: int64(20 + i), Name: "pagina-" + strconv.Itoa(i+1) + ".pdf", Kind: "PDF", Size: "120 KB"}
	}
	full := func(id int64, fs []fileView) pageData {
		return pageData{Title: byID[id].Title, Doc: w.view(byID[id]), Files: fs}
	}
	docConfirm := full(1, files)
	docConfirm.Confirm = true
	docQuitar := full(1, files)
	docQuitar.ConfirmFile = &docQuitar.Files[2]
	docErrPage := full(4, nil)
	docErrPage.Error = string(errHEIC)
	editForm := formOf(byID[5])
	badForm := formOf(byID[1])
	badForm.Expires = "2020-01-01"
	empty := pageData{}

	type page struct {
		file, tmpl string
		data       pageData
		want       []string
	}
	cases := []page{
		{"inicio", "inicio.html", withFlash(homeData(""), "Guardado."), []string{"Caducados", "Caducan pronto", "Pasaporte", "Hace", ">34<", "Hoy",
			"Falta", ">1<", "DNI de Leo", "Leo Pérez", "Seguro Ejemplo", "Sin caducidad", "Actualizar la Agenda ahora", `action="/papeles/agenda"`,
			`name="q"`, "2 archivos", "Garantía", "pill vigente", "1 mar 2031"}},
		{"inicio-busqueda", "inicio.html", homeData("seguro"), []string{"Resultados para «seguro»", "Seguro del coche", "Seguro de la casa", "Ver todo"}},
		{"inicio-sin-resultados", "inicio.html", homeData("zzz"), []string{"No hay nada con «zzz»"}},
		{"inicio-vacio", "inicio.html", empty, []string{"Aún no hay documentos", "Añadir el primero"}},
		{"doc", "doc.html", withFlash(full(1, files), "Archivo guardado."), []string{"12345678Z", "Policía Nacional", "1 mar 2021", "Vigente", "60, 30 y 7 días antes",
			"/papeles/archivo/11/mini", "/papeles/archivo/13", ">PDF<", `accept="image/*,application/pdf"`, `enctype="multipart/form-data"`, "Borrar el documento…", "Renovar", "required"}},
		{"doc-renovar-periodo", "doc.html", full(5, nil), []string{"Caduca pronto", "Faltan", ">72<", "cada año", "15 ene 2028", "450,50 €", "Sin archivos"}},
		{"doc-caducado", "doc.html", full(3, files[2:]), []string{"Caducado", "Hace", "Caducó el 1 de octubre de 2026"}},
		{"doc-sin-caducidad", "doc.html", full(7, nil), []string{"Sin caducidad", "Sin avisos", "649 €"}},
		{"doc-borrar", "doc.html", docConfirm, []string{"¿Borrar este documento?", "sus 3 archivos", `action="/papeles/doc/1/borrar"`}},
		{"doc-quitar", "doc.html", docQuitar, []string{"¿Quitar «escritura.pdf»?", `action="/papeles/archivo/13/borrar"`}},
		{"doc-lleno", "doc.html", full(6, ten), []string{"Ya tiene 10 archivos"}},
		{"doc-error", "doc.html", docErrPage, []string{"HEIC", "Falta", ">1<", "día<"}},
		{"nuevo", "form.html", pageData{Title: "Nuevo documento", Form: &docForm{Type: "dni"}, Types: typeOpts()}, []string{"Nuevo documento", `action="/papeles/doc/0"`, `value="dni" selected`, "Título o certificado académico", "sin avisos"}},
		{"editar", "form.html", pageData{Title: "Editar", Form: editForm, Types: typeOpts()}, []string{`action="/papeles/doc/5"`, `value="seguro_coche" selected`, `value="POL-998877"`, `value="450,50"`, `value="30, 7"`, `value="12"`}},
		{"editar-error", "form.html", pageData{Title: "Editar", Form: badForm, Types: typeOpts(), Error: "La caducidad no puede ser anterior a la emisión."}, []string{"anterior a la emisión", `value="2020-01-01"`}},
	}

	dir := os.Getenv("PAPELES_PREVIEW_DIR")
	base := "/papeles"
	if dir != "" {
		base = "."
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base, d.Today = "v0.0.0-test", base, "2026-11-04"
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script"} {
				if strings.Contains(out, bad) {
					t.Errorf("contiene %q", bad)
				}
			}
			if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
				t.Error("tiene un manejador de eventos")
			}
			if dir == "" {
				for _, want := range p.want {
					if !strings.Contains(out, want) {
						t.Errorf("falta %q", want)
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
		for _, n := range []string{"app.css", "icon.svg"} {
			b, _ := fs.ReadFile(static, n)
			os.WriteFile(filepath.Join(dir, "static", n), b, 0o644)
		}
		// Miniaturas de muestra para que la ficha se vea como en el móvil.
		_, _, thumb, _ := prepare("muestra.jpg", testJPEG(t, 320, 200))
		for _, id := range []string{"11", "12"} {
			os.MkdirAll(filepath.Join(dir, "archivo", id), 0o755)
			os.WriteFile(filepath.Join(dir, "archivo", id, "mini"), thumb, 0o644)
		}
	}
}

func withFlash(d pageData, msg string) pageData {
	d.Flash = msg
	return d
}

// ---- Flujos por HTTP con la base de datos de pruebas ----

type webEnv struct {
	t   *testing.T
	svc *service
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv { return newWebEnvWith(t, newTestService(t)) }

func newWebEnvWith(t *testing.T, svc *service) *webEnv {
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, srv: srv}
}

// do hace una petición como user ("" sin identidad) y devuelve el estado,
// la redirección, las cabeceras y el cuerpo.
func (e *webEnv) doH(method, path, user string, body io.Reader, ctype string) (int, string, http.Header, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
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
	return resp.StatusCode, resp.Header.Get("Location"), resp.Header, string(b)
}

func (e *webEnv) do(method, path, user string, body io.Reader, ctype string) (int, string, string) {
	e.t.Helper()
	code, loc, _, b := e.doH(method, path, user, body, ctype)
	return code, loc, b
}

func (e *webEnv) get(path, user string) (int, string) {
	e.t.Helper()
	code, _, body := e.do("GET", path, user, nil, "")
	return code, body
}

func (e *webEnv) post(path, user string, form url.Values) (int, string, string) {
	e.t.Helper()
	return e.do("POST", path, user, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

func (e *webEnv) upload(path, user, name string, content []byte) (int, string, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("archivo", name)
	fw.Write(content)
	mw.Close()
	return e.do("POST", path, user, &buf, mw.FormDataContentType())
}

var (
	reDocLoc  = regexp.MustCompile(`^/doc/(\d+)\?ok=guardado$`)
	reMiniSrc = regexp.MustCompile(`/archivo/(\d+)/mini`)
	reFileSrc = regexp.MustCompile(`/archivo/(\d+)"`)
)

func TestWebFlows(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()

	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, _, _ := e.post("/doc/0", "", url.Values{"tipo": {"dni"}, "titulo": {"x"}}); code != http.StatusUnauthorized {
		t.Fatalf("guardar sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no hay documentos") {
		t.Fatalf("inicio vacío: %d", code)
	}
	if code, _ := e.get("/static/app.css", "ana"); code != 200 {
		t.Fatalf("css: %d", code)
	}
	if code, body := e.get("/doc/nuevo?tipo=itv", "ana"); code != 200 || !strings.Contains(body, `value="itv" selected`) {
		t.Fatalf("nuevo: %d", code)
	}

	// Crear: los avisos vacíos son los del tipo.
	form := url.Values{"tipo": {"dni"}, "titulo": {"DNI de Ana"}, "titular": {"Ana Pérez"}, "numero": {"12345678Z"}, "emisor": {"Policía Nacional"},
		"emision": {"2021-03-01"}, "caducidad": {"2026-12-01"}, "avisos": {""}, "importe": {""}, "notas": {"Renovar en comisaría"}}
	code, loc, _ := e.post("/doc/0", "ana", form)
	m := reDocLoc.FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil {
		t.Fatalf("crear: %d %q", code, loc)
	}
	id := m[1]
	code, body := e.get(loc, "ana")
	if code != 200 || !strings.Contains(body, "12345678Z") || !strings.Contains(body, "Guardado.") || !strings.Contains(body, "60, 30 y 7 días antes") ||
		!strings.Contains(body, "Caduca pronto") || !strings.Contains(body, ">27<") {
		t.Fatalf("ficha: %d\n%s", code, body)
	}
	// Un error vuelve al formulario con lo escrito.
	bad := url.Values{"tipo": {"dni"}, "titulo": {"DNI de Ana"}, "numero": {"12345678Z"}, "emision": {"2021-03-01"}, "caducidad": {"2020-03-01"}}
	if code, _, body := e.post("/doc/"+id, "ana", bad); code != 200 || !strings.Contains(body, "anterior a la emisión") || !strings.Contains(body, `value="2020-03-01"`) {
		t.Fatalf("error: %d", code)
	}
	if code, _, body := e.post("/doc/0", "ana", url.Values{"tipo": {"dni"}, "titulo": {"X"}, "avisos": {"mañana"}}); code != 200 || !strings.Contains(body, "separados por comas") {
		t.Fatalf("avisos malos: %d", code)
	}
	// Editar.
	if code, body := e.get("/doc/"+id+"/editar", "ana"); code != 200 || !strings.Contains(body, `value="12345678Z"`) || !strings.Contains(body, `value="60, 30, 7"`) {
		t.Fatalf("editar: %d", code)
	}
	form.Set("cada_meses", "120")
	form.Set("avisos", "15")
	form.Set("importe", "12,5")
	if code, loc, _ := e.post("/doc/"+id, "ana", form); code != http.StatusSeeOther || loc != "/doc/"+id+"?ok=guardado" {
		t.Fatalf("guardar cambios: %d %q", code, loc)
	}
	if _, body := e.get("/doc/"+id, "ana"); !strings.Contains(body, "15 días antes") || !strings.Contains(body, "12,50 €") || !strings.Contains(body, "cada 10 años") {
		t.Fatal("no se ven los cambios")
	}

	// Inicio y buscador.
	e.post("/doc/0", "ana", url.Values{"tipo": {"seguro_hogar"}, "titulo": {"Póliza de la casa"}, "emisor": {"Seguro Ejemplo"}})
	_, body = e.get("/", "ana")
	if !strings.Contains(body, "Caducan pronto") || !strings.Contains(body, "Póliza de la casa") || !strings.Contains(body, "Seguro del hogar") {
		t.Fatalf("inicio:\n%s", body)
	}
	if _, body = e.get("/?q=POLIZA", "ana"); !strings.Contains(body, "Póliza de la casa") || strings.Contains(body, "DNI de Ana") {
		t.Fatal("buscar sin tildes")
	}
	if _, body = e.get("/?q=perez", "ana"); !strings.Contains(body, "DNI de Ana") || strings.Contains(body, "Póliza") {
		t.Fatal("buscar por titular")
	}

	// Renovar: con cada_meses, avanza; con fecha, esa.
	if code, loc, _ := e.post("/doc/"+id+"/renovar", "ana", url.Values{"fecha": {""}}); code != http.StatusSeeOther || loc != "/doc/"+id+"?ok=renovado" {
		t.Fatalf("renovar: %d %q", code, loc)
	}
	d, _ := e.svc.store.doc(ctx, "ana", mustID(t, id))
	if d.Expires != "2036-12-01" {
		t.Fatalf("renovado: %s", d.Expires)
	}
	e.post("/doc/"+id+"/renovar", "ana", url.Values{"fecha": {"2037-01-15"}})
	if d, _ = e.svc.store.doc(ctx, "ana", mustID(t, id)); d.Expires != "2037-01-15" {
		t.Fatalf("renovado con fecha: %s", d.Expires)
	}
	if code, _, body := e.post("/doc/"+id+"/renovar", "ana", url.Values{"fecha": {"2001-01-01"}}); code != 200 || !strings.Contains(body, "anterior a la emisión") {
		t.Fatalf("renovar mal: %d", code)
	}

	// Archivos: una foto con EXIF y un PDF.
	code, loc, _ = e.upload("/doc/"+id+"/archivo", "ana", "dni-delante.jpg", testJPEG(t, 90, 60))
	if code != http.StatusSeeOther || loc != "/doc/"+id+"?ok=archivo#archivos" {
		t.Fatalf("subir: %d %q", code, loc)
	}
	e.upload("/doc/"+id+"/archivo", "ana", "escritura.pdf", []byte(testPDF))
	_, body = e.get("/doc/"+id, "ana")
	mini := reMiniSrc.FindStringSubmatch(body)
	if mini == nil || !strings.Contains(body, "dni-delante.jpg") || !strings.Contains(body, "escritura.pdf") {
		t.Fatalf("ficha con archivos:\n%s", body)
	}
	jpgID := mini[1]
	var pdfID string
	for _, m := range reFileSrc.FindAllStringSubmatch(body, -1) {
		if m[1] != jpgID {
			pdfID = m[1]
		}
	}
	code, _, h, img := e.doH("GET", "/archivo/"+jpgID, "ana", nil, "")
	if code != 200 || h.Get("Content-Type") != "image/jpeg" || h.Get("Cache-Control") != "private, no-store" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "inline") || strings.Contains(img, "Exif") || strings.Contains(img, "GPS") {
		t.Fatalf("imagen: %d %v", code, h)
	}
	if cfg, err := jpeg.DecodeConfig(strings.NewReader(img)); err != nil || cfg.Width != 60 || cfg.Height != 90 {
		t.Fatalf("imagen girada: %+v %v", cfg, err)
	}
	if code, _, h, _ := e.doH("GET", "/archivo/"+jpgID+"/mini", "ana", nil, ""); code != 200 || h.Get("Cache-Control") != "private, no-store" || h.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("miniatura: %d %v", code, h)
	}
	code, _, h, pdf := e.doH("GET", "/archivo/"+pdfID, "ana", nil, "")
	if code != 200 || pdf != testPDF || h.Get("Content-Type") != "application/pdf" || h.Get("Cache-Control") != "private, no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("pdf: %d %v", code, h)
	}
	if code, _ := e.get("/archivo/"+pdfID+"/mini", "ana"); code != http.StatusNotFound {
		t.Fatalf("miniatura de un pdf: %d", code)
	}
	// Lo que no se admite, con un mensaje claro.
	for name, c := range map[string]struct {
		raw  []byte
		want string
	}{
		"foto.heic":  {testHEIC, "HEIC"},
		"nota.txt":   {[]byte("texto"), "Solo se admiten PDF"},
		"enorme.pdf": {append([]byte("%PDF-"), make([]byte, maxFile+10)...), "15 MB"},
	} {
		if code, _, body := e.upload("/doc/"+id+"/archivo", "ana", name, c.raw); code != 200 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d sin «%s»", name, code, c.want)
		}
	}
	if d, _ := e.svc.store.doc(ctx, "ana", mustID(t, id)); d.Files != 2 {
		t.Fatalf("archivos: %d", d.Files)
	}

	// Otro no ve ni toca nada de ana.
	for _, p := range []string{"/doc/" + id, "/doc/" + id + "/editar", "/archivo/" + jpgID, "/archivo/" + jpgID + "/mini", "/archivo/" + pdfID} {
		if code, body := e.get(p, "luis"); code != http.StatusNotFound || strings.Contains(body, "12345678Z") {
			t.Errorf("luis GET %s: %d", p, code)
		}
	}
	for _, p := range []string{"/doc/" + id, "/doc/" + id + "/borrar", "/doc/" + id + "/renovar", "/archivo/" + jpgID + "/borrar"} {
		if code, _, _ := e.post(p, "luis", form); code != http.StatusNotFound {
			t.Errorf("luis POST %s: %d", p, code)
		}
	}
	if code, _, _ := e.upload("/doc/"+id+"/archivo", "luis", "x.pdf", []byte(testPDF)); code != http.StatusNotFound {
		t.Errorf("luis sube: %d", code)
	}
	if _, body := e.get("/?q=dni", "luis"); strings.Contains(body, "DNI de Ana") {
		t.Error("luis ve el DNI de ana")
	}
	if d, _ := e.svc.store.doc(ctx, "ana", mustID(t, id)); d.Title != "DNI de Ana" || d.Files != 2 {
		t.Fatalf("luis ha cambiado algo: %+v", d)
	}

	// Quitar un archivo (con confirmación) y borrar el documento.
	if _, body := e.get("/doc/"+id+"?quitar="+pdfID, "ana"); !strings.Contains(body, "¿Quitar «escritura.pdf»?") {
		t.Fatal("falta la confirmación del archivo")
	}
	if code, loc, _ := e.post("/archivo/"+pdfID+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/doc/"+id+"?ok=archivo-borrado#archivos" {
		t.Fatalf("quitar: %d %q", code, loc)
	}
	if _, err := os.Stat(e.svc.files.path(mustID(t, pdfID), false)); !os.IsNotExist(err) {
		t.Fatal("el pdf sigue en el disco")
	}
	if _, body := e.get("/doc/"+id+"?borrar=1", "ana"); !strings.Contains(body, "¿Borrar este documento?") {
		t.Fatal("falta la confirmación")
	}
	if code, loc, _ := e.post("/doc/"+id+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/?ok=borrado" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if code, _ := e.get("/doc/"+id, "ana"); code != http.StatusNotFound {
		t.Fatal("sigue tras borrarlo")
	}
	if left, _ := os.ReadDir(e.svc.files.dir); len(left) != 0 {
		t.Fatalf("quedan ficheros: %v", left)
	}
	for _, p := range []string{"/doc/abc", "/doc/-1", "/archivo/x"} {
		if code, _ := e.get(p, "ana"); code != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, code)
		}
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
