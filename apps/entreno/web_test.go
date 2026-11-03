package main

import (
	"bytes"
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
	"strings"
	"testing"
	"time"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

func rirP(n int) *int { return &n }

// TestWebRender ejecuta cada plantilla con datos realistas (y los casos
// límite) sin base de datos. Con ENTRENO_PREVIEW_DIR escribe las páginas y
// la hoja de estilos en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	now := time.Date(2026, 11, 3, 19, 30, 0, 0, madrid)
	w := &web{svc: &service{log: slog.New(slog.DiscardHandler), now: func() time.Time { return now }}}
	day := func(d int) time.Time { return time.Date(2026, 11, 3+d, 0, 0, 0, 0, time.UTC) }

	lat := exerciseLog{Key: "lat_pulldown", Name: "Lat machine", Sets: []setLog{{Reps: 10, Kg: 50, RIR: rirP(2)}, {Reps: 10, Kg: 50, RIR: rirP(2)}, {Reps: 8, Kg: 55, RIR: rirP(1)}}}
	farmer := exerciseLog{Key: "farmer_carry", Name: "Farmer carry", PerSide: true, Sets: []setLog{{Meters: 20, Kg: 22}, {Meters: 20, Kg: 22}, {Meters: 20, Kg: 22}}}
	hollow := exerciseLog{Key: "hollow_hold", Name: "Hollow hold", Sets: []setLog{{Seconds: 45}, {Seconds: 45}, {Seconds: 40}}, Note: "la última costó"}
	own := exerciseLog{Key: "u7", Name: "Blorpo", Sets: []setLog{{Reps: 12}}}
	custom := map[string]string{"u7": "Blorpo en polea con un nombre bastante largo para ver cómo se parte"}

	var cards []sessionCard
	for i, x := range []session{
		{ID: 41, Day: day(0), Source: "web", Exercises: []exerciseLog{lat, farmer, hollow, own}, Notes: []string{"partido", "poca energía"}},
		{ID: 40, Day: day(-2), Source: "whatsapp", Exercises: []exerciseLog{lat}},
		{ID: 12, Day: time.Date(2025, 12, 2, 0, 0, 0, 0, time.UTC), Source: "whatsapp"},
	} {
		c := sessionCard{ID: x.ID, Date: w.shortDate(x.Day), Source: x.Source, Notes: x.Notes}
		for _, e := range x.Exercises {
			c.Lines = append(c.Lines, exLine{Key: e.Key, Name: exerciseName(e.Key, e.Name, custom), Summary: summarize(e)})
		}
		cards = append(cards, c)
		if i == 0 && c.Date != "mar 3 nov" {
			t.Errorf("fecha corta = %q", c.Date)
		}
	}
	if got := w.shortDate(day(-337)); !strings.HasSuffix(got, " 2025") {
		t.Errorf("fecha de otro año sin año: %q", got)
	}

	sess := sessionDetail{ID: 41, Date: w.longDate(day(0)), Time: "19:30", Source: sources["web"], Notes: []string{"partido"}}
	for _, e := range []exerciseLog{lat, farmer, hollow, own} {
		sess.Exercises = append(sess.Exercises, detail(e, custom))
	}

	hist := historyView{Key: "lat_pulldown", Name: "Jalón al pecho", Muscles: "espalda, bíceps"}
	var list []done
	for i := 0; i < 8; i++ {
		kg := 40 + float64(i)*2.5
		list = append([]done{{SessionID: int64(30 + i), Day: day(-7 * (8 - i)), Entry: exerciseLog{Key: "lat_pulldown", Name: "Lat machine",
			Sets: []setLog{{Reps: 10, Kg: kg, RIR: rirP(2)}, {Reps: 10, Kg: kg, RIR: rirP(2)}}}}}, list...)
	}
	w.fillHistory(&hist, list)
	if hist.Chart == nil || len(hist.Chart.Dots) != 8 || hist.Metric != "1RM estimado" || hist.Record == "" {
		t.Fatalf("historial: %+v", hist)
	}
	hollowHist := historyView{Key: "hollow_hold", Name: "Hollow hold", Muscles: "abdomen"}
	w.fillHistory(&hollowHist, []done{{SessionID: 41, Day: day(0), Entry: hollow}})
	if hollowHist.Metric != "Segundos de la mejor serie" || len(hollowHist.Chart.Dots) != 1 {
		t.Fatalf("historial por tiempo: %+v", hollowHist)
	}
	farmerHist := historyView{Key: "farmer_carry", Name: "Paseo del granjero"}
	w.fillHistory(&farmerHist, []done{{SessionID: 41, Day: day(0), Entry: farmer}, {SessionID: 40, Day: day(-2), Entry: farmer}})
	if farmerHist.Metric != "Carga máxima" {
		t.Errorf("métrica con distancia y carga = %q", farmerHist.Metric)
	}

	groups := append(customGroup(custom), catalogGroups()...)
	unknown := []unknownRow{{Index: 0, Name: "Blorpo"}, {Index: 1, Name: "Pressa con nombre largo de verdad", Sel: "nuevo"}}
	preview := &previewView{ID: "0123456789abcdef", Count: 143, From: "2 dic 2025", To: "3 nov", Already: 12, More: 131,
		Sample:  []sampleRow{{Date: "mar 3 nov", Names: []nameTag{{Name: "Jalón al pecho"}, {Name: "Glorbix", Unknown: true}}}, {Date: "dom 1 nov", Names: []nameTag{{Name: "Sentadilla trasera con barra"}}}},
		Unknown: []unknownRow{{Index: 0, Name: "Glorbix", Count: 14}, {Index: 1, Name: "Pressa", Count: 1}}}

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"inicio", "inicio.html", pageData{Tab: "inicio", Name: "Olga", Sessions: cards, Flash: "Sesión borrada."}},
		{"inicio-vacio", "inicio.html", pageData{Tab: "inicio"}},
		{"apuntar", "apuntar.html", pageData{Tab: "apuntar", Date: "2026-11-03"}},
		{"apuntar-error", "apuntar.html", pageData{Tab: "apuntar", Date: "2026-11-03", Text: "hoy nada", Error: "no he encontrado ningún ejercicio"}},
		{"apuntar-desconocidos", "apuntar.html", pageData{Tab: "apuntar", Date: "2026-11-03", Text: "Lat machine 3x10 50kg\nBlorpo 3x12", Unknown: unknown, Groups: groups}},
		{"apuntar-parcial", "apuntar.html", pageData{Tab: "apuntar", Date: "2026-11-03", Text: "Lat machine 3x10 50kg\nBlorpo 3x12", Unknown: unknown[:1], Groups: groups,
			Partial: true, SessionID: 41, Saved: []string{"Jalón al pecho — 3×10 · 50 kg · RIR 2"}, Flash: "Apuntado lo que reconozco."}},
		{"sesion", "sesion.html", pageData{Tab: "inicio", Session: sess, Flash: "Apuntado."}},
		{"sesion-borrar", "sesion.html", pageData{Tab: "inicio", Session: sess, Confirm: true}},
		{"sesion-vacia", "sesion.html", pageData{Tab: "inicio", Session: sessionDetail{ID: 3, Date: "lunes, 2 de noviembre"}}},
		{"ejercicios", "ejercicios.html", pageData{Tab: "ejercicios", Stats: []statRow{{Key: "lat_pulldown", Name: "Jalón al pecho", Times: 31, Last: "mar 3 nov"}, {Key: "u7", Name: custom["u7"], Times: 1, Last: "dom 1 nov"}}}},
		{"ejercicios-busqueda", "ejercicios.html", pageData{Tab: "ejercicios", Query: "sentadilla", Searched: true, Results: searchCatalog("sentadilla", 40)}},
		{"ejercicios-sin-resultados", "ejercicios.html", pageData{Tab: "ejercicios", Query: "zzz", Searched: true}},
		{"ejercicios-vacio", "ejercicios.html", pageData{Tab: "ejercicios"}},
		{"ejercicio", "ejercicio.html", pageData{Tab: "ejercicios", Exercise: hist}},
		{"ejercicio-tiempo", "ejercicio.html", pageData{Tab: "ejercicios", Exercise: hollowHist}},
		{"ejercicio-carga", "ejercicio.html", pageData{Tab: "ejercicios", Exercise: farmerHist}},
		{"ejercicio-nuevo", "ejercicio.html", pageData{Tab: "ejercicios", Exercise: historyView{Key: "deadlift", Name: "Peso muerto", Muscles: "isquiotibiales, glúteo"}}},
		{"importar", "importar.html", pageData{Tab: "importar"}},
		{"importar-hecho", "importar.html", pageData{Tab: "importar", Done: &importDone{Saved: 131, Skipped: 12}}},
		{"importar-error", "importar.html", pageData{Tab: "importar", Error: "El fichero pasa de 5 MB: exporta el chat «Sin archivos»."}},
		{"importar-vista", "importar.html", pageData{Tab: "importar", Preview: preview, Groups: catalogGroups()}},
		{"importar-vista-sin-desconocidos", "importar.html", pageData{Tab: "importar", Preview: &previewView{ID: "0123456789abcdef", Count: 1, From: "3 nov", To: "3 nov",
			Sample: []sampleRow{{Date: "mar 3 nov", Names: []nameTag{{Name: "Jalón al pecho"}}}}}}},
	}

	dir := os.Getenv("ENTRENO_PREVIEW_DIR")
	base := "/entreno"
	if dir != "" {
		base = "."
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base, d.Today = "v0.0.0-test", base, "2026-11-03"
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script", " on"} {
				if bad == " on" {
					if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
						t.Errorf("%s tiene un manejador de eventos", p.file)
					}
					continue
				}
				if strings.Contains(out, bad) {
					t.Errorf("%s contiene %q", p.file, bad)
				}
			}
			if !strings.Contains(out, `aria-current="page"`) {
				t.Errorf("%s: falta la pestaña activa", p.file)
			}
			if dir != "" {
				if err := os.WriteFile(filepath.Join(dir, p.file+".html"), buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
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
	}
}

// ---- Flujos por HTTP con la base de datos de pruebas ----

type webEnv struct {
	t   *testing.T
	svc *service
	srv *httptest.Server
}

func newWebEnv(t *testing.T) *webEnv {
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	now := time.Date(2026, 11, 3, 19, 30, 0, 0, madrid)
	svc := &service{store: store{pool, box}, log: slog.New(slog.DiscardHandler), imports: newImports(), now: func() time.Time { return now }}
	mux := http.NewServeMux()
	(&web{svc: svc}).routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &webEnv{t: t, svc: svc, srv: srv}
}

// do hace una petición como user ("" sin identidad) y devuelve el estado,
// la redirección y el cuerpo.
func (e *webEnv) do(method, path, user string, body io.Reader, ctype string) (int, string, string) {
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
		e.t.Errorf("%s %s sin CSP", method, path)
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

func (e *webEnv) upload(path, user, name, content string) (int, string, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("fichero", name)
	fw.Write([]byte(content))
	mw.Close()
	return e.do("POST", path, user, &buf, mw.FormDataContentType())
}

var (
	reSessionLink = regexp.MustCompile(`/sesion/(\d+)`)
	reImportForm  = regexp.MustCompile(`/importar/([0-9a-f]{16})`)
)

func TestWebFlows(t *testing.T) {
	e := newWebEnv(t)
	latName := func() string { x, _ := byKey("lat_pulldown"); return x.Name }()

	if code, _ := e.get("/", ""); code != http.StatusUnauthorized {
		t.Fatalf("sin identidad: %d", code)
	}
	if code, body := e.get("/", "ana"); code != 200 || !strings.Contains(body, "Aún no hay entrenos") {
		t.Fatalf("inicio vacío: %d", code)
	}
	if code, _ := e.get("/static/app.css", "ana"); code != 200 {
		t.Fatalf("css: %d", code)
	}

	// Apuntar con un nombre que no reconoce: lo demás se apunta y pregunta.
	text := "Lat machine 3x10 50kg rir 2\nFarmer carry 3x20m 22kg\nHollow hold 3x45s\nBlorpo 3x12 20kg\nRir 1\npartido"
	code, _, body := e.post("/apuntar", "ana", url.Values{"texto": {text}, "fecha": {"2026-11-03"}})
	if code != 200 || !strings.Contains(body, "«Blorpo»") || !strings.Contains(body, "Apuntado lo que reconozco") || !strings.Contains(body, `name="parcial"`) {
		t.Fatalf("apuntar con desconocido: %d\n%s", code, body)
	}
	m := reSessionLink.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("falta el enlace a la sesión")
	}
	sid := m[1]
	code, body = e.get("/sesion/"+sid, "ana")
	if code != 200 || !strings.Contains(body, latName) || !strings.Contains(body, "3×10 · 50 kg · RIR 2") || !strings.Contains(body, "partido") || strings.Contains(body, "Blorpo") {
		t.Fatalf("sesión: %d\n%s", code, body)
	}

	// Lo asigna como ejercicio propio: se apunta solo ese, sin repetir lo demás.
	code, loc, _ := e.post("/alias", "ana", url.Values{"texto": {text}, "fecha": {"2026-11-03"}, "parcial": {"1"}, "sesion": {sid},
		"n_0": {"Blorpo"}, "m_0": {"nuevo"}})
	if code != http.StatusSeeOther || loc != "/sesion/"+sid+"?ok=apuntado" {
		t.Fatalf("alias: %d %q", code, loc)
	}
	ctx := t.Context()
	x, err := e.svc.store.session(ctx, "ana", mustID(t, sid))
	if err != nil || len(x.Exercises) != 4 || len(x.Notes) != 1 {
		t.Fatalf("tras el alias: %v %+v", err, x)
	}
	if b := x.Exercises[3]; b.Name != "Blorpo" || !strings.HasPrefix(b.Key, "u") || b.Sets[0].RIR == nil || *b.Sets[0].RIR != 1 {
		t.Fatalf("ejercicio propio: %+v", b)
	}
	if _, body = e.get(loc, "ana"); !strings.Contains(body, "Blorpo") || !strings.Contains(body, "Apuntado.") {
		t.Fatal("la sesión no enseña el ejercicio propio")
	}

	// Todo desconocido y otro día: no se apunta nada hasta asignarlo.
	code, _, body = e.post("/apuntar", "ana", url.Values{"texto": {"Quixtal 3x5 60kg"}, "fecha": {"2026-11-01"}})
	if code != 200 || !strings.Contains(body, "Aún no se ha apuntado nada") || strings.Contains(body, `name="parcial"`) {
		t.Fatalf("todo desconocido: %d\n%s", code, body)
	}
	code, loc, _ = e.post("/alias", "ana", url.Values{"texto": {"Quixtal 3x5 60kg"}, "fecha": {"2026-11-01"}, "n_0": {"Quixtal"}, "m_0": {"bench_press"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sesion/") {
		t.Fatalf("alias del catálogo: %d %q", code, loc)
	}
	if _, body = e.get(strings.TrimSuffix(loc, "?ok=apuntado"), "ana"); !strings.Contains(body, "Press de banca con barra") || !strings.Contains(body, "domingo, 1 de noviembre") {
		t.Fatalf("sesión del día 1:\n%s", body)
	}
	if code, loc, _ = e.post("/alias", "ana", url.Values{"texto": {"Otro 3x5"}, "fecha": {"2026-11-01"}, "n_0": {"Otro"}, "m_0": {"no_existe"}}); code != 200 {
		t.Fatalf("clave inventada: %d %q", code, loc)
	}
	if code, _, body = e.post("/apuntar", "ana", url.Values{"texto": {"Lat 3x10"}, "fecha": {"2026-12-01"}}); code != 200 || !strings.Contains(body, "futura") {
		t.Fatalf("fecha futura: %d", code)
	}
	if code, _, body = e.post("/apuntar", "ana", url.Values{"texto": {"hoy nada"}, "fecha": {"2026-11-03"}}); code != 200 || !strings.Contains(body, "ningún ejercicio") {
		t.Fatalf("sin ejercicios: %d", code)
	}

	// Inicio, ejercicios, historial.
	if _, body = e.get("/", "ana"); !strings.Contains(body, latName+"</span> <span class=\"ex-sum\">— 3×10 · 50 kg · RIR 2") || !strings.Contains(body, "mar 3 nov") {
		t.Fatalf("inicio:\n%s", body)
	}
	if _, body = e.get("/ejercicios", "ana"); !strings.Contains(body, latName) || !strings.Contains(body, "/ejercicio/lat_pulldown") {
		t.Fatal("ejercicios sin el jalón")
	}
	if _, body = e.get("/ejercicios?q=sentadilla", "ana"); !strings.Contains(body, "/ejercicio/back_squat") {
		t.Fatal("búsqueda en el catálogo")
	}
	if code, body = e.get("/ejercicio/lat_pulldown", "ana"); code != 200 || !strings.Contains(body, "<polyline") || !strings.Contains(body, "1RM estimado") {
		t.Fatalf("historial: %d\n%s", code, body)
	}
	if code, _ = e.get("/ejercicio/no_existe", "ana"); code != 404 {
		t.Fatalf("clave inventada: %d", code)
	}
	if code, body = e.get("/api/hoy/texto", "ana"); code != 200 || body != "Última sesión: mar 3 nov — 4 ejercicios" {
		t.Fatalf("texto de hoy: %d %q", code, body)
	}

	// Otra persona no ve ni borra nada de ana.
	ownKey := x.Exercises[3].Key
	for _, p := range []string{"/sesion/" + sid, "/ejercicio/" + ownKey} {
		if code, _ = e.get(p, "bob"); code != 404 {
			t.Errorf("bob ve %s: %d", p, code)
		}
	}
	if code, _, _ = e.post("/sesion/"+sid+"/borrar", "bob", nil); code != 404 {
		t.Errorf("bob borra: %d", code)
	}
	if _, body = e.get("/", "bob"); !strings.Contains(body, "Aún no hay entrenos") {
		t.Error("bob ve sesiones de ana")
	}
	if _, body = e.get("/api/hoy/texto", "bob"); body != "" {
		t.Errorf("texto de hoy de bob: %q", body)
	}
	if _, err := e.svc.store.session(ctx, "ana", mustID(t, sid)); err != nil {
		t.Fatal("la sesión de ana ya no está")
	}

	// Importar del Markdown de un exportador.
	chat := "# WhatsApp\n\n## 20 January 2026\n\n[20:40] **You:** Squat 3x6 30kg\n[20:41] **You:** Lat machine 3x10 45kg\nRir 2\n[20:45] **You:** Glorbix 3x8 12kg\n\n" +
		"## 21 January 2026\n\n[19:10] **You:** Bench 3x8 50kg\n"
	code, _, body = e.upload("/importar", "ana", "chat.md", chat)
	if code != 200 || !strings.Contains(body, "2 sesiones") || !strings.Contains(body, "«Glorbix»") || !strings.Contains(body, "Glorbix (?)") {
		t.Fatalf("vista previa: %d\n%s", code, body)
	}
	m = reImportForm.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("falta el formulario de la importación")
	}
	if code, _, body = e.post("/importar/"+m[1], "bob", url.Values{"m_0": {""}}); code != 200 || !strings.Contains(body, "caducado") {
		t.Fatalf("bob confirma la importación de ana: %d", code)
	}
	code, loc, _ = e.post("/importar/"+m[1], "ana", url.Values{"m_0": {"nuevo"}})
	if code != http.StatusSeeOther || loc != "/importar?hechas=2&ya=0" {
		t.Fatalf("confirmar: %d %q", code, loc)
	}
	if _, body = e.get(loc, "ana"); !strings.Contains(body, "Importadas 2 sesiones") {
		t.Fatalf("resultado:\n%s", body)
	}
	if _, body = e.get("/ejercicio/back_squat", "ana"); !strings.Contains(body, "3×6 · 30 kg") {
		t.Fatal("historial importado")
	}
	// Otra vez el mismo fichero: los días ya están.
	_, _, body = e.upload("/importar", "ana", "chat.md", chat)
	if !strings.Contains(body, "2 días ya estaban importados") || strings.Contains(body, "«Glorbix»") {
		t.Fatalf("reimportar:\n%s", body)
	}
	m = reImportForm.FindStringSubmatch(body)
	if _, loc, _ = e.post("/importar/"+m[1], "ana", nil); loc != "/importar?hechas=0&ya=2" {
		t.Fatalf("reimportar: %q", loc)
	}
	if _, _, body = e.upload("/importar", "ana", "foto.jpg", "\xff\xd8\xff\xe0 no es texto"); !strings.Contains(body, "no es texto") {
		t.Fatal("fichero binario aceptado")
	}

	// Borrar, con confirmación.
	if _, body = e.get("/sesion/"+sid+"?borrar=1", "ana"); !strings.Contains(body, "Sí, borrar la sesión") {
		t.Fatal("falta la confirmación")
	}
	if code, loc, _ = e.post("/sesion/"+sid+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/?ok=borrada" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if code, _ = e.get("/sesion/"+sid, "ana"); code != 404 {
		t.Fatalf("sesión borrada: %d", code)
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	var id int64
	for _, c := range s {
		id = id*10 + int64(c-'0')
	}
	return id
}
