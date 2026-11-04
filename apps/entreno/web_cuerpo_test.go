package main

import (
	"bytes"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

type renderCase struct {
	file, tmpl string
	data       pageData
}

// bodyRenderCases son las páginas del cuerpo, las fotos y el cardio para
// TestWebRender (sin base de datos), con lo que tiene que salir en cada una.
func bodyRenderCases(t *testing.T, w *web) ([]renderCase, map[string][]string) {
	now := w.now()
	day := func(d int) time.Time { return time.Date(2026, 11, 3+d, 0, 0, 0, 0, time.UTC) }
	var logs []bodyLog // del más reciente al más antiguo
	for d := 0; d >= -40; d -= 2 {
		b := bodyLog{Day: day(d), Kg: kg(78 + float64(-d)*0.05)}
		if d%14 == 0 {
			b.Measures = map[string]float64{"cintura": 84 - float64(d)*0.1, "brazo": 35}
			b.Fat = kg(18)
		}
		logs = append(logs, b)
	}
	logs[0].Note = "Nota de prueba"
	id := func(c string) string { return strings.Repeat(c, 32) }
	photos := []photo{
		{ID: id("a"), Day: day(0), Pose: "frente"}, {ID: id("b"), Day: day(0), Pose: "perfil", Note: "Nota de prueba"},
		{ID: id("c"), Day: day(-28), Pose: "frente"}, {ID: id("d"), Day: day(-56), Pose: "espalda"},
	}
	body := w.bodyViewOf(logs, photos, now)
	if body.Trend == nil || body.Trend.Avg7 == "" || body.Trend.Week == "" || body.Chart == nil || !strings.HasPrefix(body.Chart.Label, "Peso de los últimos 21 pesajes") {
		t.Fatalf("cuerpo: %+v %+v", body.Trend, body.Chart)
	}
	if len(body.Compare) != 1 || body.Compare[0].Before.ID != id("c") || body.Compare[0].After.ID != id("a") || len(body.PhotoDays) != 3 || len(body.PhotoDays[0].Photos) != 2 {
		t.Fatalf("fotos: %+v", body)
	}
	if len(body.Measures) != 2 || body.Measures[0].Label != "Cintura" || body.Measures[0].First == "" || len(body.Logs) != 16 {
		t.Fatalf("medidas y registro: %+v %d", body.Measures, len(body.Logs))
	}
	body.Weight, body.Upload = weightForm{Date: "2026-11-03"}, photoForm{Date: "2026-11-03", Pose: "perfil"}
	empty := w.bodyViewOf(nil, nil, now)
	empty.Weight, empty.Upload = weightForm{Date: "2026-11-03", Kg: "abc"}, photoForm{Date: "2026-11-03", Pose: "frente"}

	dayForm := w.dayFormOf(day(0), logs[0], true)
	confirmForm := *dayForm
	confirmForm.Confirm = true

	at := time.Date(2026, 11, 3, 7, 15, 0, 0, madrid)
	cardio := []cardioLog{
		{ID: 9, Day: day(0), Start: at, Type: "correr", Seconds: 28*60 + 30, Meters: 5200, HR: 152, RPE: 7, Note: "Nota de prueba"},
		{ID: 8, Day: day(-2), Start: at.AddDate(0, 0, -2), Type: "bici_estatica", Seconds: 2700},
		{ID: 7, Day: day(-20), Start: at.AddDate(0, 0, -20), Type: "remo", Seconds: 600, Meters: 2400},
	}
	cp := w.cardioPageOf(cardio, cardio, now, cardioForm{Type: "correr", Date: "2026-11-03"})
	if cp.Week.Count != 1 || cp.Week.Time != "28:30" || cp.Week.Km != "5.2" || len(cp.Chart.Bars) != 8 || !cp.Chart.Bars[7].Now || cp.Chart.Bars[7].Alt || !cp.Chart.Bars[6].Alt {
		t.Fatalf("cardio: %+v", cp)
	}
	cpBad := w.cardioPageOf(nil, nil, now, cardioForm{Type: "remo", Date: "2026-11-03", Dur: "abc", Km: "5,2", RPE: "7"})
	row := w.cardioRowOf(cardio[0])
	cpBad.Confirm = &row

	cards := w.withCardio([]sessionCard{
		{ID: 41, Date: "lun 2 nov", day: "2026-11-02", at: at.AddDate(0, 0, -1), Lines: []exLine{{Name: "Jalón al pecho", Summary: "3×10 · 50 kg"}}},
	}, cardio[:2])
	if len(cards) != 3 || cards[0].Cardio == "" || cards[1].ID != 41 || cards[2].Cardio == "" {
		t.Fatalf("inicio con cardio: %+v", cards)
	}

	cases := []renderCase{
		{"cuerpo", "cuerpo.html", pageData{Tab: "cuerpo", Body: body, Flash: "Peso apuntado."}},
		{"cuerpo-vacio", "cuerpo.html", pageData{Tab: "cuerpo", Body: empty, Error: "Escribe el peso en kg, por ejemplo 78,4."}},
		{"cuerpo-dia", "cuerpo_dia.html", pageData{Tab: "cuerpo", Day: dayForm}},
		{"cuerpo-dia-borrar", "cuerpo_dia.html", pageData{Tab: "cuerpo", Day: &confirmForm}},
		{"cuerpo-dia-nuevo", "cuerpo_dia.html", pageData{Tab: "cuerpo", Day: w.dayFormOf(day(-1), bodyLog{}, false)}},
		{"foto", "foto.html", pageData{Tab: "cuerpo", Photo: &photoPage{ID: id("a"), Date: w.longDate(day(0)), Pose: "Frente", Note: "Nota de prueba", W: 1200, H: 1600}}},
		{"foto-borrar", "foto.html", pageData{Tab: "cuerpo", Photo: &photoPage{ID: id("a"), Date: w.longDate(day(0)), Pose: "Frente", Confirm: true}}},
		{"cardio", "cardio.html", pageData{Tab: "inicio", Title: "Cardio", Cardio: cp, Flash: "Cardio apuntado."}},
		{"cardio-vacio", "cardio.html", pageData{Tab: "inicio", Title: "Cardio", Cardio: cpBad, Error: "La duración no es válida."}},
		{"inicio-cardio", "inicio.html", pageData{Tab: "inicio", Sessions: cards, Hoy: &todayCard{None: true}, BodyCard: w.bodyCardOf(trend(logs, now))}},
	}
	wants := map[string][]string{
		"cuerpo": {"<title>Cuerpo · Entreno</title>", "Media 7 días", "<polyline", "Peso de los últimos 21 pesajes", "Antes y ahora", "/entreno/foto/" + id("c") + "/mini",
			"/entreno/fotos/" + id("b"), `enctype="multipart/form-data"`, `accept="image/jpeg,image/png"`, "cifradas en tu servidor", "nunca pasan por la IA", "Cintura",
			"desde el 6 oct", "/entreno/cuerpo/dia?fecha=2026-11-03", `value="perfil" selected`, "Nota de prueba", `max="2026-11-04"`, "comparan medias de 7 días"},
		"cuerpo-vacio":      {"Aún no hay pesajes", "Aún no hay fotos", "Aún no hay medidas", `value="abc"`, "Escribe el peso"},
		"cuerpo-dia":        {`name="m_cintura" value="84"`, "a la altura del ombligo", `name="fecha" value="2026-11-03"`, `name="peso" value="78"`, "Borrar el día…", "martes, 3 de noviembre", "sustituye"},
		"cuerpo-dia-borrar": {"Sí, borrar el día", `action="/entreno/cuerpo/dia/borrar"`},
		"cuerpo-dia-nuevo":  {"Aún no hay nada apuntado"},
		"foto":              {`src="/entreno/foto/` + id("a") + `"`, `width="1200" height="1600"`, "Borrar la foto…", "Nota de prueba"},
		"foto-borrar":       {"Sí, borrar la foto", `action="/entreno/fotos/` + id("a") + `/borrar"`},
		"cardio": {"<title>Cardio · Entreno</title>", "5:29 min/km", "Minutos por semana", `class="bar now"`, `value="correr" selected`, "/entreno/cardio?borrar=9",
			"Bici estática — 45 min", "esfuerzo 7/10", "28:30", "5.2 km", `class="tick alt"`},
		"cardio-vacio":  {"¿Borrar este cardio?", `action="/entreno/cardio/9/borrar"`, `value="abc"`, `value="5,2"`, `value="remo" selected`, `value="7" selected`, "Aún no hay cardio"},
		"inicio-cardio": {"Correr — 5.2 km en 28:30", `href="/entreno/cardio"`, "Último peso", "Media 7 días", "Esta semana", "Tu cuerpo", "+</span> Cardio"},
	}
	return cases, wants
}

// TestTemplatesNoInlineStyle: la CSP no deja estilos en línea, y el único
// script es el del modo gimnasio, en el layout.
func TestTemplatesNoInlineStyle(t *testing.T) {
	files, _ := fs.Glob(webFS, "web/*.html")
	if len(files) == 0 {
		t.Fatal("no hay plantillas")
	}
	for _, f := range files {
		b, _ := fs.ReadFile(webFS, f)
		if bytes.Contains(b, []byte("style=")) {
			t.Errorf("%s tiene un style=", f)
		}
		if f != "web/layout.html" && bytes.Contains(b, []byte("<script")) {
			t.Errorf("%s tiene un script", f)
		}
	}
}

// fetch hace un GET y devuelve la respuesta entera (para las cabeceras).
func (e *webEnv) fetch(path, user string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	if user != "" {
		req.Header.Set("X-Auth-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

type formPart struct{ name, value string }

// uploadPhoto sube una foto con los campos en el orden dado ("foto" marca
// dónde va el fichero; sin él, no se manda).
func (e *webEnv) uploadPhoto(user string, parts []formPart, raw []byte) (int, string, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.name == "foto" {
			fw, _ := mw.CreateFormFile("foto", "IMG_0001.jpg")
			fw.Write(raw)
			continue
		}
		mw.WriteField(p.name, p.value)
	}
	mw.Close()
	return e.do("POST", "/fotos", user, &buf, mw.FormDataContentType())
}

func noScript(t *testing.T, path, body string) {
	t.Helper()
	if strings.Contains(body, "<script") {
		t.Errorf("%s tiene un script", path)
	}
}

// TestWebBody: el peso rápido, el formulario del día (sustituye y, vacío,
// borra), el borrado con confirmación y que nadie más lo vea.
func TestWebBody(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	code, body := e.get("/cuerpo", "ana")
	if code != 200 || !strings.Contains(body, "Aún no hay pesajes") || !strings.Contains(body, `aria-current="page"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="4" y="4"`) {
		t.Fatalf("cuerpo vacío: %d\n%s", code, body)
	}
	noScript(t, "/cuerpo", body)

	for _, p := range []url.Values{{"fecha": {"2026-11-03"}, "peso": {"78,4"}}, {"fecha": {"2026-11-01"}, "peso": {"79"}}} {
		if code, loc, _ := e.post("/cuerpo/peso", "ana", p); code != http.StatusSeeOther || loc != "/cuerpo?ok=peso" {
			t.Fatalf("peso: %d %q", code, loc)
		}
	}
	// La media de 7 días incluye el pesaje de hoy.
	code, body = e.get("/cuerpo?ok=peso", "ana")
	if code != 200 || !strings.Contains(body, "Peso apuntado.") || !strings.Contains(body, "78.4 kg") || !strings.Contains(body, "78.7 kg") || !strings.Contains(body, "<polyline") {
		t.Fatalf("cuerpo con peso: %d\n%s", code, body)
	}
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"fecha": {"2026-11-03"}, "peso": {"abc"}}, "Escribe el peso"},
		{url.Values{"fecha": {"2026-11-03"}, "peso": {""}}, "Escribe el peso"},
		{url.Values{"fecha": {"2026-11-03"}, "peso": {"5"}}, "El peso tiene que estar entre 20 y 400 kg."},
		{url.Values{"fecha": {"2026-11-10"}, "peso": {"78"}}, "después de mañana"},
		{url.Values{"fecha": {"1999-01-01"}, "peso": {"78"}}, "La fecha no es válida."},
	} {
		if code, _, body := e.post("/cuerpo/peso", "ana", c.form); code != 200 || !strings.Contains(body, c.want) || !strings.Contains(body, `class="toast error"`) {
			t.Errorf("peso %v: %d, falta %q", c.form, code, c.want)
		}
	}

	// El formulario del día sustituye lo de ese día.
	if code, body = e.get("/cuerpo/dia?fecha=2026-11-03", "ana"); code != 200 || !strings.Contains(body, `name="peso" value="78.4"`) || !strings.Contains(body, `name="m_cintura"`) {
		t.Fatalf("día: %d\n%s", code, body)
	}
	day := time.Date(2026, 11, 3, 12, 0, 0, 0, madrid)
	form := url.Values{"fecha": {"2026-11-03"}, "peso": {"78,2"}, "grasa": {""}, "m_cintura": {"84,5"}, "nota": {"Nota de prueba"}}
	if code, loc, _ := e.post("/cuerpo/dia", "ana", form); code != http.StatusSeeOther || loc != "/cuerpo?ok=guardado" {
		t.Fatalf("guardar el día: %d %q", code, loc)
	}
	if b, ok, _ := e.svc.store.bodyOn(ctx, "ana", day); !ok || b.Kg == nil || *b.Kg != 78.2 || b.Fat != nil || b.Measures["cintura"] != 84.5 || b.Note != "Nota de prueba" {
		t.Fatalf("día guardado: %+v", b)
	}
	if code, loc, _ := e.post("/cuerpo/dia", "ana", url.Values{"fecha": {"2026-11-03"}, "m_cintura": {"84"}}); code != http.StatusSeeOther || loc != "/cuerpo?ok=guardado" {
		t.Fatalf("sustituir: %d %q", code, loc)
	}
	if b, _, _ := e.svc.store.bodyOn(ctx, "ana", day); b.Kg != nil || b.Note != "" || b.Measures["cintura"] != 84 {
		t.Fatalf("no sustituye: %+v", b)
	}
	if _, body = e.get("/cuerpo?ok=guardado", "ana"); !strings.Contains(body, "Guardado.") || !strings.Contains(body, "Cintura") || !strings.Contains(body, "84 cm") {
		t.Fatalf("medidas en la página:\n%s", body)
	}
	if code, _, body = e.post("/cuerpo/dia", "ana", url.Values{"fecha": {"2026-11-03"}, "m_cintura": {"900"}, "peso": {"77"}}); code != 200 ||
		!strings.Contains(body, "entre 5 y 300 cm") || !strings.Contains(body, `value="900"`) || !strings.Contains(body, `value="77"`) {
		t.Fatalf("medida imposible: %d\n%s", code, body)
	}
	if code, _, body = e.post("/cuerpo/dia", "ana", url.Values{"fecha": {"2026-11-03"}, "grasa": {"mucha"}}); code != 200 || !strings.Contains(body, "Revisa los números") {
		t.Fatalf("número mal escrito: %d", code)
	}
	if b, _, _ := e.svc.store.bodyOn(ctx, "ana", day); b.Measures["cintura"] != 84 {
		t.Fatalf("un error ha cambiado el día: %+v", b)
	}
	// Todo vacío: se borra el día.
	if code, loc, _ := e.post("/cuerpo/dia", "ana", url.Values{"fecha": {"2026-11-01"}}); code != http.StatusSeeOther || loc != "/cuerpo?ok=dia-borrado" {
		t.Fatalf("vaciar: %d %q", code, loc)
	}
	if _, ok, _ := e.svc.store.bodyOn(ctx, "ana", day.AddDate(0, 0, -2)); ok {
		t.Fatal("el día vacío no se borra")
	}
	for _, p := range []string{"/cuerpo/dia?fecha=nope", "/cuerpo/dia?fecha=2026-12-01"} {
		if code, _ = e.get(p, "ana"); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
	// Mañana sí se deja (como en el asistente).
	if code, loc, _ := e.post("/cuerpo/peso", "ana", url.Values{"fecha": {"2026-11-04"}, "peso": {"78"}}); code != http.StatusSeeOther {
		t.Fatalf("mañana: %d %q", code, loc)
	}
	e.svc.store.deleteBody(ctx, "ana", day.AddDate(0, 0, 1))

	// Otra persona no ve nada y su borrado no toca lo de ana.
	if _, body = e.get("/cuerpo", "bob"); strings.Contains(body, "84 cm") || !strings.Contains(body, "Aún no hay pesajes") {
		t.Fatal("bob ve el cuerpo de ana")
	}
	if _, body = e.get("/cuerpo/dia?fecha=2026-11-03", "bob"); strings.Contains(body, `value="84"`) {
		t.Fatal("bob ve el día de ana")
	}
	if code, _, _ := e.post("/cuerpo/dia/borrar", "bob", url.Values{"fecha": {"2026-11-03"}}); code != http.StatusSeeOther {
		t.Fatalf("bob borra su día: %d", code)
	}
	if _, ok, _ := e.svc.store.bodyOn(ctx, "ana", day); !ok {
		t.Fatal("bob ha borrado el día de ana")
	}

	// Borrar, con confirmación.
	if _, body = e.get("/cuerpo/dia?fecha=2026-11-03&borrar=1", "ana"); !strings.Contains(body, "Sí, borrar el día") {
		t.Fatal("falta la confirmación")
	}
	if code, _, _ := e.post("/cuerpo/dia/borrar", "ana", url.Values{"fecha": {""}}); code != 404 {
		t.Fatalf("borrar sin fecha: %d", code)
	}
	if code, loc, _ := e.post("/cuerpo/dia/borrar", "ana", url.Values{"fecha": {"2026-11-03"}}); code != http.StatusSeeOther || loc != "/cuerpo?ok=dia-borrado" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if _, ok, _ := e.svc.store.bodyOn(ctx, "ana", day); ok {
		t.Fatal("el día sigue ahí")
	}
}

var rePhotoLink = regexp.MustCompile(`/fotos/([0-9a-f]{32})`)

// TestWebPhotos: subir (en memoria, campos en cualquier orden), ver,
// servir solo a su dueño, con sus cabeceras, y borrar.
func TestWebPhotos(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	raw := jpegWithExif(t, 300, 400, 6)
	fields := func(date, pose, note string) []formPart {
		return []formPart{{"fecha", date}, {"pose", pose}, {"nota", note}, {"foto", ""}}
	}
	code, loc, _ := e.uploadPhoto("ana", fields("2026-11-03", "perfil", "Nota de prueba"), raw)
	if code != http.StatusSeeOther || loc != "/cuerpo?ok=foto#fotos" {
		t.Fatalf("subir: %d %q", code, loc)
	}
	// El fichero primero y los campos después.
	if code, loc, _ = e.uploadPhoto("ana", []formPart{{"foto", ""}, {"pose", "perfil"}, {"fecha", "2026-10-01"}}, raw); code != http.StatusSeeOther {
		t.Fatalf("subir con el fichero primero: %d %q", code, loc)
	}
	list, _ := e.svc.store.photoList(ctx, "ana")
	if len(list) != 2 || list[0].Note != "Nota de prueba" || list[0].Pose != "perfil" || list[1].Day.Format("2006-01-02") != "2026-10-01" || list[1].Pose != "perfil" {
		t.Fatalf("fotos: %+v", list)
	}
	newest, oldest := list[0].ID, list[1].ID

	code, body := e.get("/cuerpo?ok=foto", "ana")
	if code != 200 || !strings.Contains(body, "Foto guardada.") || !strings.Contains(body, "Antes y ahora") || !strings.Contains(body, "/foto/"+oldest+"/mini") ||
		!strings.Contains(body, "/fotos/"+newest) {
		t.Fatalf("cuerpo con fotos: %d\n%s", code, body)
	}
	noScript(t, "/cuerpo", body)
	if code, body = e.get("/fotos/"+newest, "ana"); code != 200 || !strings.Contains(body, "Perfil") || !strings.Contains(body, "Nota de prueba") || !strings.Contains(body, `src="/foto/`+newest+`"`) {
		t.Fatalf("página de la foto: %d\n%s", code, body)
	}
	noScript(t, "/fotos/", body)

	// La imagen: JPEG sin metadatos, sin caché y solo para ana.
	for _, p := range []string{"/foto/" + newest, "/foto/" + newest + "/mini"} {
		resp, img := e.fetch(p, "ana")
		h := resp.Header
		if resp.StatusCode != 200 || h.Get("Content-Type") != "image/jpeg" || h.Get("Cache-Control") != "private, no-store" || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Content-Security-Policy") != "default-src 'none'" || !bytes.HasPrefix(img, []byte{0xFF, 0xD8}) || bytes.Contains(img, []byte("secreto")) || bytes.Contains(img, []byte("Exif")) {
			t.Fatalf("%s: %d %v", p, resp.StatusCode, h)
		}
		if resp, b := e.fetch(p, "bob"); resp.StatusCode != 404 || bytes.HasPrefix(b, []byte{0xFF, 0xD8}) || resp.Header.Get("Content-Type") == "image/jpeg" {
			t.Fatalf("bob ve %s: %d", p, resp.StatusCode)
		}
		if resp, _ := e.fetch(p, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("sin identidad ve %s: %d", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/fotos/" + newest, "/fotos/" + newest + "?borrar=1", "/foto/clave-datos", "/foto/" + strings.Repeat("0", 32)} {
		if code, _ = e.get(p, "bob"); code != 404 {
			t.Errorf("bob ve %s: %d", p, code)
		}
	}
	if code, _, _ = e.post("/fotos/"+newest+"/borrar", "bob", nil); code != 404 {
		t.Fatalf("bob borra la foto: %d", code)
	}
	if _, body = e.get("/cuerpo", "bob"); strings.Contains(body, newest) || !strings.Contains(body, "Aún no hay fotos") {
		t.Fatal("bob ve las fotos de ana")
	}

	// Errores: se dicen en la página.
	for _, c := range []struct {
		name  string
		parts []formPart
		raw   []byte
		want  string
	}{
		{"no es una foto", fields("2026-11-03", "frente", ""), []byte("hola, esto no es una imagen"), "No es una foto JPEG o PNG válida."},
		{"sin fichero", []formPart{{"fecha", "2026-11-03"}, {"pose", "frente"}}, nil, "Elige una foto."},
		{"fichero vacío", fields("2026-11-03", "frente", ""), nil, "Elige una foto."},
		{"nota larga", fields("2026-11-03", "frente", strings.Repeat("a", 250)), raw, "La nota pasa de 200 caracteres."},
		{"campo enorme", fields("2026-11-03", "frente", strings.Repeat("a", 5000)), raw, "demasiado larga"},
		{"fecha futura", fields("2026-12-03", "frente", ""), raw, "después de mañana"},
	} {
		code, _, body := e.uploadPhoto("ana", c.parts, c.raw)
		if code != 200 || !strings.Contains(body, c.want) || !strings.Contains(body, `class="toast error"`) {
			t.Errorf("%s: %d, falta %q", c.name, code, c.want)
		}
	}
	if code, _, body = e.do("POST", "/fotos", "ana", strings.NewReader("foto=x"), "application/x-www-form-urlencoded"); code != 200 || !strings.Contains(body, "Elige una foto.") {
		t.Errorf("sin multipart: %d", code)
	}
	if list, _ = e.svc.store.photoList(ctx, "ana"); len(list) != 2 {
		t.Fatalf("se han guardado fotos con errores: %d", len(list))
	}

	// Borrar, con confirmación.
	if _, body = e.get("/fotos/"+newest+"?borrar=1", "ana"); !strings.Contains(body, "Sí, borrar la foto") {
		t.Fatal("falta la confirmación")
	}
	if code, loc, _ = e.post("/fotos/"+newest+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/cuerpo?ok=foto-borrada#fotos" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if resp, _ := e.fetch("/foto/"+newest, "ana"); resp.StatusCode != 404 {
		t.Fatalf("foto borrada: %d", resp.StatusCode)
	}
	if _, body = e.get("/cuerpo?ok=foto-borrada", "ana"); !strings.Contains(body, "Foto borrada.") || strings.Contains(body, "Antes y ahora") {
		t.Fatal("tras borrar")
	}
}

var reCardioDel = regexp.MustCompile(`/cardio\?borrar=(\d+)`)

// TestWebCardio: apuntar (con su ritmo), errores con lo escrito, el inicio
// con el cardio y el peso, y el borrado solo de lo propio.
func TestWebCardio(t *testing.T) {
	e := newWebEnv(t)
	ctx := t.Context()
	code, body := e.get("/cardio", "ana")
	if code != 200 || !strings.Contains(body, "Aún no hay cardio") || !strings.Contains(body, "<title>Cardio · Entreno</title>") || !strings.Contains(body, `href="/" aria-current="page"`) {
		t.Fatalf("cardio vacío: %d\n%s", code, body)
	}
	noScript(t, "/cardio", body)

	form := url.Values{"tipo": {"correr"}, "fecha": {"2026-11-03"}, "hora": {""}, "duracion": {"28:30"}, "distancia": {"5,2"}, "pulso": {"152"}, "kcal": {""},
		"esfuerzo": {"7"}, "nota": {"Nota de prueba"}}
	if code, loc, _ := e.post("/cardio", "ana", form); code != http.StatusSeeOther || loc != "/cardio?ok=cardio" {
		t.Fatalf("apuntar: %d %q", code, loc)
	}
	code, body = e.get("/cardio?ok=cardio", "ana")
	if code != 200 || !strings.Contains(body, "Cardio apuntado.") || !strings.Contains(body, "5:29 min/km") || !strings.Contains(body, "Correr — 5.2 km en 28:30") ||
		!strings.Contains(body, `class="bar now"`) || !strings.Contains(body, "19:30") {
		t.Fatalf("cardio: %d\n%s", code, body)
	}
	// Otro día sin hora: a mediodía; con hora, a esa hora.
	for _, c := range []struct{ date, hour, want string }{{"2026-11-01", "", "12:00"}, {"2026-11-02", "07:15", "07:15"}} {
		f := url.Values{"tipo": {"bici"}, "fecha": {c.date}, "hora": {c.hour}, "duracion": {"45"}}
		if code, loc, _ := e.post("/cardio", "ana", f); code != http.StatusSeeOther {
			t.Fatalf("cardio del %s: %d %q", c.date, code, loc)
		}
	}
	list, _ := e.svc.store.cardioList(ctx, "ana", time.Time{}, time.Time{}, 0)
	if len(list) != 3 || list[0].Meters != 5200 || list[0].Seconds != 1710 || list[0].HR != 152 || list[0].RPE != 7 ||
		list[1].Start.In(madrid).Format("15:04") != "07:15" || list[2].Start.In(madrid).Format("15:04") != "12:00" {
		t.Fatalf("guardado: %+v", list)
	}
	noPlain(t, e.svc.store.db, "cardio", "Nota de prueba", "correr", `"metros"`)

	// Errores: se vuelve a enseñar lo escrito.
	for _, c := range []struct {
		field, value, want string
	}{
		{"duracion", "abc", "La duración no es válida"},
		{"duracion", "", "Falta la duración."},
		{"tipo", "volar", "Elige el tipo de cardio."},
		{"fecha", "2026-11-20", "después de mañana"},
		{"hora", "25:99", "La hora no es válida."},
		{"pulso", "300", "El pulso medio tiene que estar entre 40 y 230."},
		{"pulso", "mucho", "pulsaciones por minuto"},
		{"distancia", "-3", "La distancia no es válida"},
		{"esfuerzo", "11", "El esfuerzo va del 1 al 10."},
		{"nota", strings.Repeat("a", 301), "La nota pasa de 300 caracteres."},
	} {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set(c.field, c.value)
		code, _, body := e.post("/cardio", "ana", f)
		kept := `value="5,2"`
		if c.field == "distancia" {
			kept = `value="-3"`
		}
		if code != 200 || !strings.Contains(body, c.want) || !strings.Contains(body, `class="toast error"`) || !strings.Contains(body, kept) {
			t.Errorf("%s=%q: %d, falta %q", c.field, c.value, code, c.want)
		}
	}
	if list, _ = e.svc.store.cardioList(ctx, "ana", time.Time{}, time.Time{}, 0); len(list) != 3 {
		t.Fatalf("se ha colado cardio con errores: %d", len(list))
	}

	// El inicio mezcla el cardio con las sesiones, y enseña el peso.
	if _, err := e.svc.record(ctx, "ana", "Bench 3x8 50kg", time.Date(2026, 11, 2, 19, 0, 0, 0, madrid), "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.store.saveBody(ctx, "ana", time.Date(2026, 11, 3, 12, 0, 0, 0, madrid), bodyLog{Kg: kg(78.4)}, true); err != nil {
		t.Fatal(err)
	}
	code, body = e.get("/", "ana")
	run, bench, bike := strings.Index(body, "Correr — 5.2 km"), strings.Index(body, "3×8 · 50 kg"), strings.Index(body, "Bici — 45 min")
	if code != 200 || run < 0 || bench < 0 || bike < 0 || run > bench || !strings.Contains(body, "Último peso") || !strings.Contains(body, "78.4 kg") ||
		!strings.Contains(body, `href="/cardio"`) || !strings.Contains(body, `href="/cuerpo"`) {
		t.Fatalf("inicio: %d (%d %d %d)\n%s", code, run, bench, bike, body)
	}
	// El 2 a las 07:15 (bici) va después de la sesión del 2 a las 19:00.
	if bike < bench {
		t.Errorf("orden del inicio: bici %d, sesión %d", bike, bench)
	}
	noScript(t, "/", body)
	if _, body = e.get("/", "bob"); strings.Contains(body, "Correr") || strings.Contains(body, "Último peso") {
		t.Fatal("bob ve el cardio o el peso de ana")
	}

	// Borrar: confirmación, y bob no puede.
	_, body = e.get("/cardio", "ana")
	m := reCardioDel.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("falta el enlace para borrar")
	}
	id := m[1]
	if _, body = e.get("/cardio?borrar="+id, "ana"); !strings.Contains(body, "¿Borrar este cardio?") || !strings.Contains(body, "/cardio/"+id+"/borrar") {
		t.Fatal("falta la confirmación")
	}
	if code, _ = e.get("/cardio?borrar="+id, "bob"); code != 404 {
		t.Fatalf("bob ve el cardio de ana: %d", code)
	}
	if code, _, _ = e.post("/cardio/"+id+"/borrar", "bob", nil); code != 404 {
		t.Fatalf("bob borra el cardio de ana: %d", code)
	}
	if code, _, _ = e.post("/cardio/abc/borrar", "ana", nil); code != 404 {
		t.Fatalf("id no numérico: %d", code)
	}
	if code, loc, _ := e.post("/cardio/"+id+"/borrar", "ana", nil); code != http.StatusSeeOther || loc != "/cardio?ok=cardio-borrado" {
		t.Fatalf("borrar: %d %q", code, loc)
	}
	if list, _ = e.svc.store.cardioList(ctx, "ana", time.Time{}, time.Time{}, 0); len(list) != 2 {
		t.Fatalf("tras borrar: %d", len(list))
	}
	if _, body = e.get("/cardio?ok=cardio-borrado", "ana"); !strings.Contains(body, "Cardio borrado.") {
		t.Fatal("falta el aviso")
	}
}
