package main

import (
	"bytes"
	"encoding/json"
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

	// Rutinas y modo gimnasio.
	push := routine{ID: 5, Name: "Empuje", Days: []int{4, 1}, Items: []routineItem{
		{Key: "bench_press", Sets: 4, Min: 6, Max: 8, RIR: 2, RestSec: 150},
		{Key: "hollow_hold", Sets: 3, Min: 30, Max: 45, RIR: 1, RestSec: 60, Note: "lumbar pegada al suelo"},
		{Key: "u7", Sets: 3, Min: 12, Max: 15, RIR: 2, RestSec: 75}}}
	legs := routine{ID: 6, Name: "Pierna (cuádriceps)", Items: []routineItem{{Key: "back_squat", Sets: 3, Min: 5, Max: 7, RIR: 2, RestSec: 180}}}
	routines := []routineView{viewRoutine(push, custom), viewRoutine(legs, custom)}
	if v := routines[0]; v.Days != "lunes y jueves" || v.Items[0].Text != "4 × 6–8 · RIR 2 · descanso 2 min 30 s" || v.Items[1].Text != "3 × 30–45 s · RIR 1 · descanso 1 min" {
		t.Errorf("rutina: %+v", v)
	}
	if got := daysText([]int{0, 3, 1}); got != "lunes, miércoles y domingo" {
		t.Errorf("días = %q", got)
	}
	suggest := []routineView{routines[1], routines[0]}
	suggest[1].Index = 1
	newForm := formOf(routine{})
	editForm := formOf(push)
	editForm.Confirm = true

	bench := done{SessionID: 40, Day: day(-7), Entry: exerciseLog{Key: "bench_press", Sets: []setLog{{Reps: 8, Kg: 60, RIR: rirP(2)}, {Reps: 8, Kg: 60, RIR: rirP(2)}}}}
	plan := []planItem{
		{Key: "bench_press", Name: "Press de banca con barra", Kind: "reps", Last: &bench, Sets: 3, Goal: "3×6 · 55 kg · RIR 2", Stalled: true, RestSec: 150,
			Advice: "Llevas 3 sesiones sin progresar con 60 kg. Toca una semana de descarga.", Target: setLog{Reps: 6, Kg: 55, RIR: rirP(2)},
			DoneToday: []setLog{{Reps: 6, Kg: 55, RIR: rirP(2)}}},
		{Key: "hollow_hold", Name: "Hollow hold", Kind: "time", Sets: 3, Goal: "3×30 s · RIR 1", RestSec: 60, Target: setLog{Seconds: 30, RIR: rirP(1)},
			Advice: "Primera vez: elige una carga con la que llegues a 30–45 s dejando 1 en reserva."},
		{Key: "farmer_carry", Name: "Paseo del granjero", Kind: "distance", Sets: 2, Goal: "2×25 m · 22 kg", RestSec: 90, Target: setLog{Meters: 25, Kg: 22},
			LastText: "2026-10-27: 2×20 m · 22 kg", Advice: "Misma carga y un poco más que la última vez.",
			DoneToday: []setLog{{Meters: 25, Kg: 22}, {Meters: 25, Kg: 22}}},
	}
	gym := &gymView{RoutineID: 5, Name: "Empuje", Days: "lunes y jueves", Others: []routineLink{{ID: 5, Name: "Empuje", Current: true}, {ID: 6, Name: "Pierna"}}}
	for _, p := range plan {
		gym.Items = append(gym.Items, w.gymItemOf(p))
	}
	if b := gym.Items[0]; b.Last != "mar 27 oct: 2×8 · 60 kg · RIR 2" || len(b.Sets) != 3 || !b.Sets[0].Done || b.Sets[0].Text != "6 reps · 55 kg · RIR 2" ||
		b.Sets[1].Done || b.Sets[1].Amount != "6" || b.Sets[1].Kg != "55" || b.Sets[1].RIR != "2" || !reSetID.MatchString(b.Sets[1].ID) || b.Sets[1].ID == b.Sets[2].ID {
		t.Errorf("gimnasio, press: %+v", b)
	}
	if h := gym.Items[1]; h.Field != "seg" || h.Sets[0].Amount != "30" || h.Sets[0].Kg != "" {
		t.Errorf("gimnasio, por tiempo: %+v", h)
	}
	if f := gym.Items[2]; f.Field != "m" || f.Done != 2 || f.Sets[1].Text != "25 m · 22 kg" {
		t.Errorf("gimnasio, por distancia: %+v", f)
	}

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"inicio", "inicio.html", pageData{Tab: "inicio", Name: "Olga", Sessions: cards, Flash: "Sesión borrada.",
			Hoy: &todayCard{ID: 5, Name: "Empuje", Count: 3, Stalled: []string{"Press de banca con barra", custom["u7"]}}}},
		{"inicio-sin-rutinas", "inicio.html", pageData{Tab: "inicio", Sessions: cards[:1], Hoy: &todayCard{None: true}}},
		{"inicio-vacio", "inicio.html", pageData{Tab: "inicio", Hoy: &todayCard{None: true}}},
		{"rutinas", "rutinas.html", pageData{Tab: "rutinas", Routines: routines, Flash: "Rutina guardada."}},
		{"rutinas-vacio", "rutinas.html", pageData{Tab: "rutinas"}},
		{"detectar", "detectar.html", pageData{Tab: "rutinas", Suggest: suggest}},
		{"detectar-una", "detectar.html", pageData{Tab: "rutinas", Suggest: suggest[:1], Error: "Esa sugerencia ya no está: mira las de ahora."}},
		{"detectar-vacio", "detectar.html", pageData{Tab: "rutinas"}},
		{"rutina-nueva", "rutina.html", pageData{Tab: "rutinas", Form: &newForm, Groups: groups}},
		{"rutina-editar", "rutina.html", pageData{Tab: "rutinas", Form: &editForm, Groups: groups, Error: "Ponle un nombre a la rutina."}},
		{"gimnasio", "gimnasio.html", pageData{Tab: "gimnasio", Script: true, Gym: gym}},
		{"gimnasio-elegida", "gimnasio.html", pageData{Tab: "gimnasio", Script: true, Error: "Revisa la serie: los números no son válidos.",
			Gym: &gymView{RoutineID: 6, Name: "Pierna", Chosen: true, Others: gym.Others[:1], Items: gym.Items[2:]}}},
		{"gimnasio-vacio", "gimnasio.html", pageData{Tab: "gimnasio", Script: true, Gym: &gymView{Empty: true}}},
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

	// Lo que tiene que salir en cada página nueva.
	wants := map[string][]string{
		"inicio":             {"Hoy toca", "Empuje", "2 estancados", "Press de banca con barra", "Ir al gimnasio", "Importar de WhatsApp"},
		"inicio-sin-rutinas": {"Aún no tienes rutinas", "/rutinas/detectar"},
		"rutinas":            {"Empuje", "lunes y jueves", "Sin día fijo", "4 × 6–8 · RIR 2 · descanso 2 min 30 s", "lumbar pegada al suelo", "/gimnasio?rutina=5", "Nueva rutina", "Detectar desde tu historial"},
		"rutinas-vacio":      {"Aún no tienes rutinas"},
		"detectar":           {"2 rutinas en tu historial", `name="n" value="1"`, `name="todas" value="1"`, "Guardar las 2"},
		"detectar-vacio":     {"No encuentro rutinas"},
		"rutina-nueva":       {"Nueva rutina", `action="/entreno/rutina/0"`, `name="filas" value="3"`, `value="1"`, "domingo", "Guardar y añadir filas"},
		"rutina-editar":      {`name="filas" value="6"`, `value="bench_press" selected`, `value="u7" selected`, `name="quitar_2"`, "Sí, borrar la rutina", `name="dia" value="1" checked`, `name="dia" value="4" checked`, "Ponle un nombre"},
		"gimnasio":           {"Hoy toca", "La última vez", "mar 27 oct: 2×8 · 60 kg · RIR 2", "<strong>3×6 · 55 kg · RIR 2</strong>", `advice stalled`, "Estancado.", "2 min 30 s", "6 reps · 55 kg · RIR 2", `name="reps" value="6"`, `name="seg" value="30"`, `data-api="/entreno/api/serie"`, `id="ex-bench_press"`, `data-rest="150"`, "+15 s", "Saltar", "Sesión libre", `aria-current="true"`},
		"gimnasio-elegida":   {"Rutina", "2026-10-27: 2×20 m · 22 kg", "25 m · 22 kg", "Revisa la serie"},
		"gimnasio-vacio":     {"Aún no tienes rutinas", "/rutinas/detectar", "/apuntar"},
	}
	// Cuerpo, fotos y cardio (web_cuerpo_test.go).
	extra, extraWants := bodyRenderCases(t, w)
	for _, c := range extra {
		cases = append(cases, page{c.file, c.tmpl, c.data})
	}
	for k, v := range extraWants {
		wants[k] = v
	}
	// Comida (web_nutricion_test.go).
	extra, extraWants = nutriRenderCases(t, w)
	for _, c := range extra {
		cases = append(cases, page{c.file, c.tmpl, c.data})
	}
	for k, v := range extraWants {
		wants[k] = v
	}

	dir := os.Getenv("ENTRENO_PREVIEW_DIR")
	base := "/entreno"
	if dir != "" {
		base = "."
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			if d.Form != nil { // renderRoutineForm añade las filas vacías
				f := *d.Form
				f.Rows = append(append([]itemRow{}, f.Rows...), make([]itemRow, emptyRows)...)
				for i := range f.Rows {
					f.Rows[i].I = i
				}
				d.Form = &f
			}
			d.Version, d.Base, d.Today, d.Tomorrow = "v0.0.0-test", base, "2026-11-03", "2026-11-04"
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			// El único script: el del modo gimnasio, como fichero.
			script := `<script src="` + base + `/static/gimnasio.js" defer></script>`
			if d.Script != strings.Contains(out, script) {
				t.Errorf("%s: script = %v", p.file, !d.Script)
			}
			out = strings.Replace(out, script, "", 1)
			if d.Form != nil && strings.Count(out, "Añadir ejercicio") != emptyRows {
				t.Errorf("%s: %d filas vacías", p.file, strings.Count(out, "Añadir ejercicio"))
			}
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
			if dir == "" {
				for _, want := range wants[p.file] {
					if !strings.Contains(out, want) {
						t.Errorf("%s: falta %q", p.file, want)
					}
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
		for _, n := range []string{"app.css", "icon.svg", "gimnasio.js"} {
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
	svc := &service{store: store{pool, box}, log: slog.New(slog.DiscardHandler), imports: newImports(), now: func() time.Time { return now },
		photos: newPhotoFiles(t.TempDir(), box)}
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
	want := csp
	if method == "GET" && strings.HasPrefix(path, "/gimnasio") {
		want = cspGym
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

var reRoutineLink = regexp.MustCompile(`^/rutina/(\d+)\?ok=guardada$`)

// TestWebRoutines: crear una rutina con el formulario, cambiarla, que otra
// persona no la vea ni la toque, y borrarla.
func TestWebRoutines(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	if code, body := e.get("/rutinas", "ana"); code != 200 || !strings.Contains(body, "Aún no tienes rutinas") {
		t.Fatalf("rutinas vacío: %d", code)
	}
	if code, body := e.get("/rutinas/nueva", "ana"); code != 200 || !strings.Contains(body, `name="filas" value="3"`) || strings.Count(body, "Añadir ejercicio") != 3 {
		t.Fatalf("formulario nuevo: %d", code)
	}

	form := url.Values{"nombre": {"Empuje"}, "dia": {"1", "4", "9"}, "filas": {"3"}, "accion": {"filas"},
		"k_0": {"bench_press"}, "s_0": {"4"}, "mn_0": {"6"}, "mx_0": {"8"}, "r_0": {"2"}, "d_0": {"150"},
		"k_1": {"lat_pulldown"}, "s_1": {"3"}, "mn_1": {"10"}, "mx_1": {"12"}, "r_1": {"1"}, "d_1": {"90"}, "n_1": {"agarre neutro"}}
	code, loc, _ := e.post("/rutina/0", "ana", form)
	m := reRoutineLink.FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil {
		t.Fatalf("crear: %d %q", code, loc)
	}
	id := m[1]
	code, body := e.get(loc, "ana")
	if code != 200 || !strings.Contains(body, `value="Empuje"`) || !strings.Contains(body, `name="dia" value="1" checked`) || !strings.Contains(body, `name="dia" value="4" checked`) ||
		!strings.Contains(body, `name="filas" value="5"`) || !strings.Contains(body, `value="agarre neutro"`) || !strings.Contains(body, "Rutina guardada.") {
		t.Fatalf("editar: %d\n%s", code, body)
	}
	noPlain(t, e.svc.store.db, "routines", "Empuje", "agarre neutro", "bench_press")

	// Errores: se vuelve a enseñar lo escrito.
	bad := url.Values{"nombre": {""}, "filas": {"1"}, "k_0": {"bench_press"}, "s_0": {"4"}}
	if code, _, body = e.post("/rutina/"+id, "ana", bad); code != 200 || !strings.Contains(body, "Ponle un nombre") || !strings.Contains(body, `value="bench_press" selected`) {
		t.Fatalf("sin nombre: %d", code)
	}
	bad = url.Values{"nombre": {"X"}, "filas": {"1"}, "k_0": {"no_existe"}}
	if code, _, body = e.post("/rutina/0", "ana", bad); code != 200 || !strings.Contains(body, "no existe") {
		t.Fatalf("ejercicio inventado: %d", code)
	}
	bad = url.Values{"nombre": {"X"}, "filas": {"1"}, "k_0": {"bench_press"}, "s_0": {"tres"}}
	if code, _, body = e.post("/rutina/0", "ana", bad); code != 200 || !strings.Contains(body, "Revisa los números") {
		t.Fatalf("números: %d", code)
	}

	// Cambiarla: otro nombre y quitar el jalón.
	form.Set("nombre", "Empuje A")
	form.Set("quitar_1", "1")
	form.Set("accion", "guardar")
	if code, loc, _ = e.post("/rutina/"+id, "ana", form); code != http.StatusSeeOther || loc != "/rutinas?ok=guardada" {
		t.Fatalf("guardar: %d %q", code, loc)
	}
	if _, body = e.get("/rutinas", "ana"); !strings.Contains(body, "Empuje A") || !strings.Contains(body, "lunes y jueves") ||
		!strings.Contains(body, "4 × 6–8 · RIR 2 · descanso 2 min 30 s") || strings.Contains(body, "agarre neutro") {
		t.Fatalf("lista:\n%s", body)
	}
	list, err := e.svc.store.routines(ctx, "ana")
	if err != nil || len(list) != 1 || len(list[0].Items) != 1 || list[0].Name != "Empuje A" {
		t.Fatalf("guardada: %v %+v", err, list)
	}

	// Otra persona: 404 en todo.
	for _, p := range []string{"/rutina/" + id, "/rutina/" + id + "?borrar=1", "/gimnasio?rutina=" + id} {
		if code, _ = e.get(p, "bob"); code != 404 {
			t.Errorf("bob ve %s: %d", p, code)
		}
	}
	if code, _, _ = e.post("/rutina/"+id, "bob", form); code != 404 {
		t.Errorf("bob cambia la rutina: %d", code)
	}
	if code, _, _ = e.post("/rutina/"+id+"/borrar", "bob", nil); code != 404 {
		t.Errorf("bob borra la rutina: %d", code)
	}
	if _, body = e.get("/rutinas", "bob"); strings.Contains(body, "Empuje") {
		t.Error("bob ve las rutinas de ana")
	}
	if list, _ = e.svc.store.routines(ctx, "ana"); len(list) != 1 || list[0].Name != "Empuje A" {
		t.Fatalf("bob ha tocado la rutina: %+v", list)
	}

	// Borrar, con confirmación.
	if _, body = e.get("/rutina/"+id+"?borrar=1", "ana"); !strings.Contains(body, "Sí, borrar la rutina") {
		t.Fatal("falta la confirmación")
	}
	if code, loc, _ = e.post("/rutina/"+id+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/rutinas?ok=rutina-borrada" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if code, _ = e.get("/rutina/"+id, "ana"); code != 404 {
		t.Fatalf("rutina borrada: %d", code)
	}
	if code, _ = e.get("/rutina/abc", "ana"); code != 404 {
		t.Fatalf("id no numérico: %d", code)
	}
}

// api manda una serie a /api/serie.
func (e *webEnv) api(user, body, ctype, site string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/serie", strings.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	if user != "" {
		req.Header.Set("X-Auth-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestWebGym: detectar la rutina del historial, el modo gimnasio y apuntar
// series por formulario y por la API (sin duplicar al reenviar).
func TestWebGym(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	benchName := func() string { x, _ := byKey("bench_press"); return x.Name }()

	code, body := e.get("/gimnasio", "ana")
	if code != 200 || !strings.Contains(body, "Aún no tienes rutinas") || !strings.Contains(body, "/static/gimnasio.js") {
		t.Fatalf("gimnasio vacío: %d", code)
	}
	if _, body = e.get("/", "ana"); strings.Contains(body, "/static/gimnasio.js") {
		t.Fatal("el script sale fuera del gimnasio")
	}
	if code, body = e.get("/rutinas/detectar", "ana"); code != 200 || !strings.Contains(body, "No encuentro rutinas") {
		t.Fatalf("detectar sin historial: %d", code)
	}

	// Tres martes con los mismos ejercicios.
	text := "Bench 3x8 50kg rir 2\nLat machine 3x10 45kg rir 2\nSquat 3x6 60kg rir 2"
	for _, d := range []int{13, 20, 27} {
		if _, err := e.svc.record(ctx, "ana", text, time.Date(2026, 10, d, 19, 0, 0, 0, madrid), "web"); err != nil {
			t.Fatal(err)
		}
	}
	if code, body = e.get("/rutinas/detectar", "ana"); code != 200 || !strings.Contains(body, "Guardar esta") || !strings.Contains(body, "martes") || !strings.Contains(body, benchName) {
		t.Fatalf("detectar: %d\n%s", code, body)
	}
	if code, _, body = e.post("/rutinas/detectar", "ana", url.Values{"n": {"7"}}); code != 200 || !strings.Contains(body, "ya no está") {
		t.Fatalf("sugerencia que no existe: %d", code)
	}
	code, loc, _ := e.post("/rutinas/detectar", "ana", url.Values{"n": {"0"}})
	m := reRoutineLink.FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil {
		t.Fatalf("guardar la sugerencia: %d %q", code, loc)
	}
	rid := m[1]
	if code, loc, _ = e.post("/rutinas/detectar", "bob", url.Values{"todas": {"1"}}); code != http.StatusSeeOther || loc != "/rutinas?ok=detectadas" {
		t.Fatalf("bob guarda todas: %d %q", code, loc)
	}
	if list, _ := e.svc.store.routines(ctx, "bob"); len(list) != 0 {
		t.Fatalf("bob tiene las rutinas de ana: %+v", list)
	}

	// Hoy (martes) toca esa rutina.
	code, body = e.get("/gimnasio", "ana")
	if code != 200 || !strings.Contains(body, "Hoy toca") || !strings.Contains(body, benchName) || !strings.Contains(body, "La última vez") ||
		!strings.Contains(body, "mar 27 oct: 3×8 · 50 kg · RIR 2") || strings.Count(body, `class="serie-form"`) != 9 || !strings.Contains(body, `id="ex-bench_press"`) {
		t.Fatalf("gimnasio: %d\n%s", code, body)
	}
	if _, body = e.get("/", "ana"); !strings.Contains(body, "Hoy toca") || !strings.Contains(body, `href="/gimnasio"`) {
		t.Fatalf("inicio sin «Hoy toca»:\n%s", body)
	}
	if code, _ = e.get("/gimnasio?rutina="+rid, "ana"); code != 200 {
		t.Fatalf("gimnasio con la rutina elegida: %d", code)
	}
	if code, _ = e.get("/gimnasio?rutina=999999", "ana"); code != 404 {
		t.Fatalf("rutina que no existe: %d", code)
	}

	todaySets := func(key string) []setLog {
		t.Helper()
		today := dayOf(e.svc.clock())
		list, err := e.svc.store.sessions(ctx, "ana", today, today, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []setLog
		for _, x := range list {
			for _, ex := range x.Exercises {
				if ex.Key == key {
					out = append(out, ex.Sets...)
				}
			}
		}
		return out
	}

	// Sin JavaScript: el formulario de la serie.
	serie := url.Values{"key": {"bench_press"}, "reps": {"8"}, "kg": {"52,5"}, "rir": {"2"}, "rutina": {rid}, "id": {"abcdef0123456789"}}
	if code, loc, _ = e.post("/gimnasio/serie", "ana", serie); code != http.StatusSeeOther || loc != "/gimnasio?rutina="+rid+"#ex-bench_press" {
		t.Fatalf("serie: %d %q", code, loc)
	}
	if code, loc, _ = e.post("/gimnasio/serie", "ana", serie); code != http.StatusSeeOther {
		t.Fatalf("serie repetida: %d", code)
	}
	if sets := todaySets("bench_press"); len(sets) != 1 || sets[0].Reps != 8 || sets[0].Kg != 52.5 || sets[0].RIR == nil || *sets[0].RIR != 2 {
		t.Fatalf("serie apuntada: %+v", sets)
	}
	if _, body = e.get("/gimnasio", "ana"); !strings.Contains(body, "8 reps · 52.5 kg · RIR 2") || strings.Count(body, `class="serie-form"`) != 8 {
		t.Fatalf("serie hecha:\n%s", body)
	}
	serie.Set("reps", "ocho")
	if code, loc, _ = e.post("/gimnasio/serie", "ana", serie); code != http.StatusSeeOther || !strings.Contains(loc, "error=serie") {
		t.Fatalf("serie mal escrita: %d %q", code, loc)
	}

	// Con JavaScript: JSON, y el mismo id dos veces no duplica.
	js := `{"key":"bench_press","reps":8,"kg":52.5,"rir":1,"id":"00112233445566aa"}`
	for i := range 2 {
		code, out := e.api("ana", js, "application/json", "same-origin")
		if code != 200 || out["ok"] != true || out["sesion"] == nil {
			t.Fatalf("api (%d): %d %v", i, code, out)
		}
	}
	if sets := todaySets("bench_press"); len(sets) != 2 || *sets[1].RIR != 1 {
		t.Fatalf("api: %+v", sets)
	}
	if code, out := e.api("ana", `{"key":"hollow_hold","seg":45,"id":"99887766554433ff"}`, "application/json; charset=utf-8", ""); code != 200 || out["ok"] != true {
		t.Fatalf("api por tiempo: %d %v", code, out)
	}
	if sets := todaySets("hollow_hold"); len(sets) != 1 || sets[0].Seconds != 45 {
		t.Fatalf("api por tiempo: %+v", sets)
	}
	for _, c := range []struct {
		name, user, body, ctype, site string
		want                          int
	}{
		{"otro sitio", "ana", js, "application/json", "cross-site", http.StatusForbidden},
		{"mismo sitio, otro origen", "ana", js, "application/json", "same-site", http.StatusForbidden},
		{"no es JSON", "ana", "key=bench_press&reps=8", "application/x-www-form-urlencoded", "same-origin", http.StatusUnsupportedMediaType},
		{"texto", "ana", js, "text/plain", "same-origin", http.StatusUnsupportedMediaType},
		{"sin identidad", "", js, "application/json", "same-origin", http.StatusUnauthorized},
		{"ejercicio inventado", "ana", `{"key":"no_existe","reps":8}`, "application/json", "same-origin", http.StatusBadRequest},
		{"ejercicio de otro", "bob", `{"key":"u1","reps":8}`, "application/json", "same-origin", http.StatusBadRequest},
		{"sin cantidad", "ana", `{"key":"bench_press","kg":50}`, "application/json", "same-origin", http.StatusBadRequest},
		{"negativo", "ana", `{"key":"bench_press","reps":-3}`, "application/json", "same-origin", http.StatusBadRequest},
		{"id raro", "ana", `{"key":"bench_press","reps":3,"id":"<x>"}`, "application/json", "same-origin", http.StatusBadRequest},
		{"demasiado grande", "ana", `{"key":"bench_press","reps":3,"id":"` + strings.Repeat("a", 5000) + `"}`, "application/json", "same-origin", http.StatusBadRequest},
		{"roto", "ana", `{"key":`, "application/json", "same-origin", http.StatusBadRequest},
	} {
		if code, _ := e.api(c.user, c.body, c.ctype, c.site); code != c.want {
			t.Errorf("api, %s: %d, quiero %d", c.name, code, c.want)
		}
	}
	if sets := todaySets("bench_press"); len(sets) != 2 {
		t.Fatalf("se han colado series: %+v", sets)
	}
}
