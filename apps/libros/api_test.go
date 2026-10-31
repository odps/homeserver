package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"homeserver/platform/dbtest"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := z.Create(name)
		io.WriteString(w, content)
	}
	z.Close()
	return buf.Bytes()
}

func mobiBytes() []byte {
	b := make([]byte, 100)
	copy(b[60:], "BOOKMOBI")
	return b
}

func TestDetectFormat(t *testing.T) {
	f, err := newFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	epub := zipBytes(t, map[string]string{"mimetype": "application/epub+zip", "META-INF/container.xml": "<container/>"})
	cases := []struct {
		name, file string
		data       []byte
		want       string
	}{
		{"epub", "libro.epub", epub, "epub"},
		{"pdf", "doc.pdf", []byte("%PDF-1.7\n..."), "pdf"},
		{"mobi", "a.mobi", mobiBytes(), "mobi"},
		{"azw3", "a.azw3", mobiBytes(), "azw3"},
		{"fb2", "a.fb2", []byte(`<?xml version="1.0"?><FictionBook xmlns="x">`), "fb2"},
		{"fbz", "a.fbz", zipBytes(t, map[string]string{"libro.fb2": "<FictionBook/>"}), "fbz"},
		{"cbz", "a.cbz", zipBytes(t, map[string]string{"001.jpg": "x", "002.jpg": "y"}), "cbz"},
		{"zip sin libro", "a.zip", zipBytes(t, map[string]string{"notas.txt": "x"}), ""},
		{"texto", "a.txt", []byte("hola"), ""},
	}
	for _, c := range cases {
		up, err := f.receive(bytes.NewReader(c.data), maxBookBytes)
		if err != nil {
			t.Fatal(err)
		}
		got, err := detectFormat(up, c.file)
		up.discard()
		if got != c.want || (c.want == "" && err == nil) {
			t.Errorf("%s: formato %q (%v), quiero %q", c.name, got, err, c.want)
		}
	}
	if _, err := f.receive(bytes.NewReader(make([]byte, 11)), 10); err == nil {
		t.Error("acepta un fichero mayor que el límite")
	}
}

func TestClean(t *testing.T) {
	if got := clean("  Cien   años\nde soledad ", 500); got != "Cien años de soledad" {
		t.Errorf("clean = %q", got)
	}
	if got := clean("ñandúñandú", 4); got != "ñand" {
		t.Errorf("recorte por runas = %q", got)
	}
	if got := cleanList([]string{"García Márquez", " garcía márquez ", "", "Otro"}, 20, 200); len(got) != 2 {
		t.Errorf("cleanList = %v", got)
	}
}

// pngPixel es un PNG válido de 1×1.
var pngPixel = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

// harness: la API con su base de pruebas. user y role son la identidad que
// la pasarela pondría en las cabeceras (se cambian para simular a otra persona).
type harness struct {
	srv   *httptest.Server
	files files
	api   *api
	user  string
	role  string
}

func (h *harness) as(user, role string) { h.user, h.role = user, role }

func (h *harness) identify(req *http.Request) {
	req.Header.Set("X-Auth-User", h.user)
	req.Header.Set("X-Auth-Name", h.user)
	req.Header.Set("X-Auth-Role", h.role)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := dbtest.Migrated(t, migrations)
	f, err := newFiles(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &api{store: store{pool}, files: f, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	a.routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, files: f, api: a, user: "ana", role: "admin"}
}

func (h *harness) upload(t *testing.T, name string, data []byte, meta map[string]any) (int, map[string]json.RawMessage) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	m, _ := json.Marshal(meta)
	mw.WriteField("meta", string(m))
	cw, _ := mw.CreateFormFile("cover", "cover.png")
	cw.Write(pngPixel)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/books", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	h.identify(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]json.RawMessage
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, r)
	h.identify(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUploadListAndDedupe(t *testing.T) {
	h := newHarness(t)
	epub := zipBytes(t, map[string]string{"mimetype": "application/epub+zip", "META-INF/container.xml": "<c/>"})
	status, out := h.upload(t, "cien.epub", epub, map[string]any{
		"title": "Cien años de soledad", "authors": []string{"Gabriel García Márquez"}, "genres": []string{"Novela"},
		"language": "es", "description": "Macondo",
	})
	if status != http.StatusCreated {
		t.Fatalf("subida: %d %s", status, out["error"])
	}
	var b Book
	json.Unmarshal(out["book"], &b)
	if b.Format != "epub" || !b.HasCover || b.Status != "pendiente" || b.Authors[0] != "Gabriel García Márquez" {
		t.Fatalf("libro guardado: %+v", b)
	}
	if _, err := os.Stat(h.files.bookPath(b)); err != nil {
		t.Fatal("el fichero no está en disco")
	}

	status, out = h.upload(t, "copia.epub", epub, map[string]any{"title": "Otro título"})
	if status != http.StatusOK || string(out["duplicate"]) != "true" {
		t.Fatalf("duplicado: %d %s", status, out["duplicate"])
	}

	status, _ = h.upload(t, "virus.exe", []byte("MZ\x90\x00"), map[string]any{})
	if status != http.StatusBadRequest {
		t.Fatalf("formato no válido aceptado: %d", status)
	}

	books := decode[[]Book](t, h.do(t, "GET", "/api/books?q=garcía", nil))
	if len(books) != 1 || books[0].Title != "Cien años de soledad" {
		t.Fatalf("búsqueda: %+v", books)
	}
	if books := decode[[]Book](t, h.do(t, "GET", "/api/books?q=nada", nil)); len(books) != 0 {
		t.Fatalf("búsqueda sin resultados: %+v", books)
	}

	resp := h.do(t, "GET", "/api/books/"+itoa(b.ID)+"/file", nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(data, epub) || resp.Header.Get("ETag") == "" {
		t.Fatal("el fichero descargado no coincide")
	}
	if resp := h.do(t, "GET", "/api/books/"+itoa(b.ID)+"/cover", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("portada: %d", resp.StatusCode)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestProgressSync(t *testing.T) {
	h := newHarness(t)
	_, out := h.upload(t, "a.pdf", []byte("%PDF-1.7 libro"), map[string]any{"title": "Un PDF"})
	var b Book
	json.Unmarshal(out["book"], &b)
	path := "/api/books/" + itoa(b.ID)
	now := time.Now().UTC()

	save := func(dev string, frac float32, at time.Time) Progress {
		resp := h.do(t, "PUT", path+"/progress", map[string]any{
			"device_id": dev, "device_name": "Disp " + dev, "location": "epubcfi(/6/4)", "fraction": frac,
			"label": "Cap. 1", "updated_at": at,
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("progreso: %d", resp.StatusCode)
		}
		return *decode[struct{ Last *Progress }](t, resp).Last
	}
	save("movil", 0.10, now.Add(-time.Hour))
	last := save("pc", 0.40, now)
	if last.DeviceID != "pc" || last.Fraction != 0.40 {
		t.Fatalf("última posición: %+v", last)
	}
	// Una cola sin conexión que llega tarde con una lectura antigua no pisa nada.
	last = save("movil", 0.05, now.Add(-2*time.Hour))
	if last.DeviceID != "pc" {
		t.Fatalf("una lectura antigua ganó: %+v", last)
	}
	detail := decode[struct {
		Book    Book
		Devices []Progress
	}](t, h.do(t, "GET", path, nil))
	if detail.Book.Status != "leyendo" || len(detail.Devices) != 2 || detail.Devices[1].Fraction != 0.10 {
		t.Fatalf("detalle: %+v", detail)
	}
	// Un reloj adelantado se recorta a la hora del servidor.
	last = save("tablet", 0.5, now.Add(24*time.Hour))
	if last.UpdatedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("acepta fechas futuras: %v", last.UpdatedAt)
	}
	if resp := h.do(t, "PUT", path+"/progress", map[string]any{"device_id": "x", "device_name": "x", "location": "a", "fraction": 2}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("fracción fuera de rango: %d", resp.StatusCode)
	}
	if resp := h.do(t, "PUT", "/api/books/999999/progress", map[string]any{"device_id": "x", "device_name": "x", "location": "a", "fraction": 0.1}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("libro inexistente: %d", resp.StatusCode)
	}
}

func TestEditAndDelete(t *testing.T) {
	h := newHarness(t)
	_, out := h.upload(t, "a.fb2", []byte(`<?xml version="1.0"?><FictionBook>`), map[string]any{})
	var b Book
	json.Unmarshal(out["book"], &b)
	if b.Title != "a" {
		t.Fatalf("título por defecto = %q", b.Title)
	}
	path := "/api/books/" + itoa(b.ID)
	edited := decode[Book](t, h.do(t, "PATCH", path, map[string]any{"title": "Título nuevo", "authors": []string{"Yo"}, "series": "Saga", "series_index": 2}))
	read := decode[Book](t, h.do(t, "PUT", path+"/status", map[string]any{"status": "leido"}))
	if edited.Title != "Título nuevo" || read.Status != "leido" || read.FinishedAt == nil || *edited.Series != "Saga" {
		t.Fatalf("edición: %+v", edited)
	}
	if resp := h.do(t, "PATCH", path, map[string]any{"title": " "}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("título vacío: %d", resp.StatusCode)
	}
	if resp := h.do(t, "PUT", path+"/status", map[string]any{"status": "otro"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("estado no válido: %d", resp.StatusCode)
	}
	if resp := h.do(t, "DELETE", path, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("borrar: %d", resp.StatusCode)
	}
	if _, err := os.Stat(h.files.bookPath(b)); !os.IsNotExist(err) {
		t.Fatal("el fichero sigue en disco")
	}
	if resp := h.do(t, "GET", path, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("libro borrado: %d", resp.StatusCode)
	}
}

func TestPrivacyAndPermissions(t *testing.T) {
	h := newHarness(t)
	idOf := func(out map[string]json.RawMessage) string {
		var b Book
		json.Unmarshal(out["book"], &b)
		return itoa(b.ID)
	}
	visibleTo := func(user, role string) []Book {
		h.as(user, role)
		return decode[[]Book](t, h.do(t, "GET", "/api/books", nil))
	}

	// Ana (admin) sube un libro privado: Luis no lo ve de ninguna forma.
	h.as("ana", "admin")
	_, out := h.upload(t, "diario.pdf", []byte("%PDF-1.7 diario"), map[string]any{"title": "Diario", "visibility": "privado"})
	diario := idOf(out)
	if books := visibleTo("luis", "usuario"); len(books) != 0 {
		t.Fatalf("Luis ve un libro privado de Ana: %+v", books)
	}
	for _, p := range []string{"", "/file", "/cover"} {
		if resp := h.do(t, "GET", "/api/books/"+diario+p, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Luis accede a %s: %d", p, resp.StatusCode)
		}
	}
	if resp := h.do(t, "PUT", "/api/books/"+diario+"/progress", map[string]any{"device_id": "d", "device_name": "d", "location": "x", "fraction": 0.1}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Luis guarda progreso en un libro privado ajeno: %d", resp.StatusCode)
	}

	// Ana lo comparte: Luis lo ve, no lo puede editar, y su estado es suyo.
	h.as("ana", "admin")
	h.do(t, "PATCH", "/api/books/"+diario, map[string]any{"title": "Diario", "visibility": "compartido"})
	h.do(t, "PUT", "/api/books/"+diario+"/status", map[string]any{"status": "leido"})
	books := visibleTo("luis", "usuario")
	if len(books) != 1 || books[0].CanEdit || books[0].Status != "pendiente" || books[0].Owner != "ana" {
		t.Fatalf("libro compartido visto por Luis: %+v", books)
	}
	if resp := h.do(t, "PATCH", "/api/books/"+diario, map[string]any{"title": "Mío"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Luis edita un libro ajeno: %d", resp.StatusCode)
	}
	if resp := h.do(t, "DELETE", "/api/books/"+diario, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Luis borra un libro ajeno: %d", resp.StatusCode)
	}
	// Si Luis sube el mismo fichero, se le devuelve el compartido.
	status, out := h.upload(t, "otro.pdf", []byte("%PDF-1.7 diario"), map[string]any{})
	if status != http.StatusOK || string(out["duplicate"]) != "true" {
		t.Fatalf("duplicado de un compartido: %d", status)
	}

	// Luis sube un libro privado; Ana sube el mismo fichero: son dos filas y el
	// fichero sigue en disco hasta que nadie lo usa.
	_, out = h.upload(t, "notas.epub", zipBytes(t, map[string]string{"mimetype": "application/epub+zip"}), map[string]any{"title": "Notas de Luis"})
	notasLuis := idOf(out)
	var b Book
	json.Unmarshal(out["book"], &b)
	if b.Visibility != "privado" {
		t.Fatalf("visibilidad por defecto = %q", b.Visibility)
	}
	h.as("ana", "admin")
	if resp := h.do(t, "GET", "/api/books/"+notasLuis, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("una admin ve un libro privado ajeno: %d", resp.StatusCode)
	}
	status, out = h.upload(t, "notas.epub", zipBytes(t, map[string]string{"mimetype": "application/epub+zip"}), map[string]any{"title": "Notas de Ana"})
	if status != http.StatusCreated || idOf(out) == notasLuis {
		t.Fatalf("copia propia de un fichero privado ajeno: %d", status)
	}
	h.as("luis", "usuario")
	if resp := h.do(t, "DELETE", "/api/books/"+notasLuis, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Luis borra su libro: %d", resp.StatusCode)
	}
	if _, err := os.Stat(h.files.bookPath(b)); err != nil {
		t.Fatal("se borró un fichero que Ana sigue usando")
	}

	// Un libro compartido de Luis: la admin lo puede editar, pero no hacerlo privado.
	_, out = h.upload(t, "receta.fb2", []byte(`<?xml version="1.0"?><FictionBook>`), map[string]any{"title": "Recetas", "visibility": "compartido"})
	recetas := idOf(out)
	h.as("ana", "admin")
	if resp := h.do(t, "PATCH", "/api/books/"+recetas, map[string]any{"title": "Recetas de Luis"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("una admin edita un compartido: %d", resp.StatusCode)
	}
	if resp := h.do(t, "PATCH", "/api/books/"+recetas, map[string]any{"title": "x", "visibility": "privado"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("una admin se queda un libro ajeno: %d", resp.StatusCode)
	}

	// Los tokens sin persona no tienen biblioteca.
	h.as("token:agente", "servicio")
	if resp := h.do(t, "GET", "/api/books", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token de servicio: %d", resp.StatusCode)
	}
}
