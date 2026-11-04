package main

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/seal"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor, sin JavaScript: el inicio (lo caducado,
// lo que caduca pronto y todo por tipo, con buscador), la ficha de cada
// documento con sus archivos, el formulario y la subida de archivos.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"lines": func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	},
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxForm = 64 << 10

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /papeles detrás de la pasarela
	Name    string
	Title   string // de la página, antes de «Papeles»
	Flash   string
	Error   string
	Today   string

	// Inicio
	Query   string
	Total   int // documentos de la persona (sin filtrar)
	Expired []docRow
	Soon    []docRow
	Groups  []docGroup

	// Ficha
	Doc         *docView
	Files       []fileView
	Confirm     bool      // pide confirmar el borrado del documento
	ConfirmFile *fileView // pide confirmar el borrado de un archivo

	// Formulario
	Form  *docForm
	Types []typeOpt
}

// docRow es un documento en una lista.
type docRow struct {
	ID        int64
	Title     string
	Holder    string
	Type      string
	TypeLabel string
	Issuer    string
	Expires   string // «1 mar 2027»
	State     string // caducado, caduca_pronto, vigente, sin_caducidad
	Days      int
	Big       string // «23», «Hoy»
	Unit      string // «días»
	Lead      string // «Faltan», «Hace»
	When      string // «faltan 23 días», «caducó hace 5 días»
	Files     int

	expiresISO string
}

type docGroup struct {
	Label string
	Docs  []docRow
}

// docView es la ficha de un documento.
type docView struct {
	docRow
	Number    string
	Issued    string // «12 ene 2022»
	Months    int
	Every     string // «cada año», «cada 6 meses»
	Alerts    string // «60, 30 y 7 días antes»
	Amount    string
	Notes     string
	Next      string // caducidad si se renueva por periodo («1 mar 2028»)
	CanRenew  bool
	ExpiresTx string // fecha larga
}

type fileView struct {
	ID    int64
	Name  string
	Kind  string // PDF o Imagen
	Size  string
	Mini  bool
	W, H  int
	Added string
}

// docForm son los campos del formulario tal como se escribieron.
type docForm struct {
	ID                                    int64
	Type, Title, Holder, Number, Issuer   string
	Issued, Expires, Months, Alerts, Note string
	Amount                                string
}

type typeOpt struct {
	Key, Label string
	Alerts     string
}

func typeOpts() []typeOpt {
	out := make([]typeOpt, len(docTypes))
	for i, t := range docTypes {
		a := alertsText(t.Alerts)
		if a == "" {
			a = "sin avisos"
		}
		out[i] = typeOpt{Key: t.Key, Label: t.Label, Alerts: a}
	}
	return out
}

type web struct {
	svc *service
}

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.HandleFunc("GET /{$}", w.user(w.home))
	mux.HandleFunc("GET /doc/nuevo", w.user(w.newDoc))
	mux.HandleFunc("GET /doc/{id}", w.user(w.docPage))
	mux.HandleFunc("GET /doc/{id}/editar", w.user(w.editDoc))
	mux.HandleFunc("POST /doc/{id}", w.user(w.saveDoc))
	mux.HandleFunc("POST /doc/{id}/borrar", w.user(w.deleteDoc))
	mux.HandleFunc("POST /doc/{id}/renovar", w.user(w.renewDoc))
	mux.HandleFunc("POST /doc/{id}/archivo", w.user(w.uploadFile))
	mux.HandleFunc("GET /archivo/{id}", w.user(w.serveFile(false)))
	mux.HandleFunc("GET /archivo/{id}/mini", w.user(w.serveFile(true)))
	mux.HandleFunc("POST /archivo/{id}/borrar", w.user(w.deleteFile))
	mux.HandleFunc("POST /agenda", w.user(w.agendaPost))
}

// user exige la identidad que pone la pasarela.
func (w *web) user(h func(http.ResponseWriter, *http.Request, app.Identity)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		who := app.User(r)
		if !who.IsUser() {
			http.Error(rw, "entra desde la pasarela", http.StatusUnauthorized)
			return
		}
		h(rw, r, who)
	}
}

func base(r *http.Request) string { return strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/") }

func (w *web) render(rw http.ResponseWriter, r *http.Request, name string, d pageData) {
	d.Version, d.Base = version.Version, base(r)
	d.Today = w.svc.today().Format(dateFmt)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Security-Policy", csp)
	rw.Header().Set("Cache-Control", "no-store")
	if err := pages.ExecuteTemplate(rw, name, d); err != nil {
		w.svc.log.Error("plantilla", "pagina", name, "err", err)
	}
}

// fail registra un error interno (sin datos de la persona) y lo dice.
func (w *web) fail(rw http.ResponseWriter, op string, err error) {
	w.svc.log.Error("papeles web", "op", op, "err", err)
	http.Error(rw, "Algo ha fallado. Prueba otra vez.", http.StatusInternalServerError)
}

var flashes = map[string]string{
	"guardado": "Guardado.", "borrado": "Documento borrado.", "renovado": "Renovado: la caducidad ya es la nueva.",
	"archivo": "Archivo guardado.", "archivo-borrado": "Archivo borrado.",
	"agenda":       "Vencimientos al día en la Agenda.",
	"agenda-no":    "No usas la Agenda: pide que te la asignen en Usuarios para recibir los avisos.",
	"agenda-error": "La Agenda no responde ahora; se intentará otra vez con el próximo cambio.",
}

func flash(r *http.Request) string { return flashes[r.URL.Query().Get("ok")] }

// back vuelve a una página de la app con un aviso (la pasarela pone el
// prefijo en la redirección).
func back(rw http.ResponseWriter, r *http.Request, to, ok string) {
	http.Redirect(rw, r, to+"?ok="+ok, http.StatusSeeOther)
}

func notFound(rw http.ResponseWriter) { http.Error(rw, "No existe.", http.StatusNotFound) }

func pathID(r *http.Request) int64 {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 0 {
		return -1
	}
	return id
}

// ---- Fechas y textos ----

var (
	months     = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
	monthShort = []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sept", "oct", "nov", "dic"}
)

// shortDate es «1 mar 2027».
func shortDate(iso string) string {
	t, err := time.Parse(dateFmt, iso)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d %s %d", t.Day(), monthShort[t.Month()-1], t.Year())
}

// longDate es «1 de marzo de 2027».
func longDate(iso string) string {
	t, err := time.Parse(dateFmt, iso)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d de %s de %d", t.Day(), months[t.Month()-1], t.Year())
}

func plural(n int, one, many string) string {
	if n == 1 || n == -1 {
		return one
	}
	return many
}

// whenText es «faltan 23 días», «caduca hoy», «caducó hace 5 días».
func whenText(days int) string {
	switch {
	case days == 0:
		return "caduca hoy"
	case days == 1:
		return "caduca mañana"
	case days == -1:
		return "caducó ayer"
	case days < 0:
		return fmt.Sprintf("caducó hace %d días", -days)
	}
	return fmt.Sprintf("faltan %d días", days)
}

func everyText(m int) string {
	switch {
	case m == 0:
		return ""
	case m == 12:
		return "cada año"
	case m%12 == 0:
		return fmt.Sprintf("cada %d años", m/12)
	case m == 1:
		return "cada mes"
	}
	return fmt.Sprintf("cada %d meses", m)
}

// alertsLong es «60, 30 y 7 días antes».
func alertsLong(a []int) string {
	if len(a) == 0 {
		return "Sin avisos"
	}
	parts := make([]string, len(a))
	for i, n := range a {
		parts[i] = strconv.Itoa(n)
	}
	s := parts[0]
	if len(parts) > 1 {
		s = strings.Join(parts[:len(parts)-1], ", ") + " y " + parts[len(parts)-1]
	}
	return s + plural(a[len(a)-1], " día antes", " días antes")
}

func sizeText(n int) string {
	switch {
	case n >= 1<<20:
		return strings.Replace(strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64), ".", ",", 1) + " MB"
	case n >= 1<<10:
		return strconv.Itoa(n/(1<<10)) + " KB"
	}
	return strconv.Itoa(n) + " bytes"
}

func (w *web) row(d doc) docRow {
	state, days := d.status(w.svc.today())
	r := docRow{ID: d.ID, Title: d.Title, Holder: d.Holder, Type: d.Type, TypeLabel: typeLabel(d.Type), Issuer: d.Issuer,
		Expires: shortDate(d.Expires), State: state, Days: days, Files: d.Files, expiresISO: d.Expires}
	if state != stNone {
		r.When = whenText(days)
		switch {
		case days == 0:
			r.Big = "Hoy"
		case days < 0:
			r.Lead, r.Big, r.Unit = "Hace", strconv.Itoa(-days), plural(days, "día", "días")
		default:
			r.Lead, r.Big, r.Unit = "Faltan", strconv.Itoa(days), plural(days, "día", "días")
		}
		if days == 1 {
			r.Lead = "Falta"
		}
	}
	return r
}

// ---- Inicio ----

func (w *web) home(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	list, err := w.svc.store.docs(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "inicio", err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 100 {
		q = string([]rune(q)[:100])
	}
	d := w.homeData(list, q)
	d.Name, d.Flash = who.Name, flash(r)
	w.render(rw, r, "inicio.html", d)
}

// homeData monta el inicio: lo caducado y lo que caduca pronto (sin
// búsqueda) y todo, o lo encontrado, agrupado por tipo.
func (w *web) homeData(list []doc, q string) pageData {
	d := pageData{Query: q, Total: len(list)}
	words := seal.Words(q)
	var rows []docRow
	for _, x := range list {
		if len(words) > 0 && !x.matches(words) {
			continue
		}
		rows = append(rows, w.row(x))
	}
	// Por tipo; dentro, primero lo que caduca antes y luego por título.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ti, tj := typeIndex(a.Type), typeIndex(b.Type); ti != tj {
			return ti < tj
		}
		if (a.expiresISO == "") != (b.expiresISO == "") {
			return b.expiresISO == ""
		}
		if a.expiresISO != b.expiresISO {
			return a.expiresISO < b.expiresISO
		}
		return fold(a.Title) < fold(b.Title)
	})
	for _, x := range rows {
		if n := len(d.Groups); n == 0 || d.Groups[n-1].Label != x.TypeLabel {
			d.Groups = append(d.Groups, docGroup{Label: x.TypeLabel})
		}
		g := &d.Groups[len(d.Groups)-1]
		g.Docs = append(g.Docs, x)
		if q == "" {
			switch x.State {
			case stExpired:
				d.Expired = append(d.Expired, x)
			case stSoon:
				d.Soon = append(d.Soon, x)
			}
		}
	}
	// Lo caducado, de lo más reciente a lo más antiguo; lo que caduca
	// pronto, de lo más cercano a lo más lejano.
	sort.SliceStable(d.Expired, func(i, j int) bool { return d.Expired[i].Days > d.Expired[j].Days })
	sort.SliceStable(d.Soon, func(i, j int) bool { return d.Soon[i].Days < d.Soon[j].Days })
	if q != "" {
		d.Title = "Buscar"
	}
	return d
}

// ---- Ficha ----

func (w *web) view(d doc) *docView {
	v := &docView{docRow: w.row(d), Number: d.Number, Issued: shortDate(d.Issued), Months: d.Months, Every: everyText(d.Months),
		Alerts: alertsLong(d.Alerts), Notes: d.Notes, ExpiresTx: longDate(d.Expires)}
	if d.Amount != nil {
		v.Amount = euros(*d.Amount)
	}
	if next := d.nextRenewal(); next != "" {
		v.Next = shortDate(next)
	}
	v.CanRenew = d.Expires != ""
	return v
}

func fileViews(list []fileMeta) []fileView {
	out := make([]fileView, len(list))
	for i, f := range list {
		kind := "Imagen"
		if f.Type == typePDF {
			kind = "PDF"
		}
		out[i] = fileView{ID: f.ID, Name: f.Name, Kind: kind, Size: sizeText(f.Size), Mini: f.Mini, W: f.W, H: f.H, Added: shortDate(f.Created)}
	}
	return out
}

// docData prepara la ficha (sin enviarla).
func (w *web) docData(r *http.Request, who app.Identity, id int64) (pageData, error) {
	x, err := w.svc.store.doc(r.Context(), who.User, id)
	if err != nil {
		return pageData{}, err
	}
	files, err := w.svc.store.files(r.Context(), who.User, id)
	if err != nil {
		return pageData{}, err
	}
	return pageData{Name: who.Name, Title: x.Title, Doc: w.view(x), Files: fileViews(files)}, nil
}

func (w *web) docPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	d, err := w.docData(r, who, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "ficha", err)
		return
	}
	d.Flash = flash(r)
	q := r.URL.Query()
	d.Confirm = q.Get("borrar") == "1"
	if fid, err := strconv.ParseInt(q.Get("quitar"), 10, 64); err == nil {
		for i := range d.Files {
			if d.Files[i].ID == fid {
				d.ConfirmFile = &d.Files[i]
			}
		}
	}
	w.render(rw, r, "doc.html", d)
}

// renderDoc vuelve a la ficha con un error (de la subida o de renovar).
func (w *web) renderDoc(rw http.ResponseWriter, r *http.Request, who app.Identity, id int64, msg string) {
	d, err := w.docData(r, who, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "ficha", err)
		return
	}
	d.Error = msg
	w.render(rw, r, "doc.html", d)
}

// ---- Formulario ----

func formOf(d doc) *docForm {
	f := &docForm{ID: d.ID, Type: d.Type, Title: d.Title, Holder: d.Holder, Number: d.Number, Issuer: d.Issuer,
		Issued: d.Issued, Expires: d.Expires, Note: d.Notes, Amount: amountText(d.Amount), Alerts: alertsText(d.Alerts)}
	if d.Months > 0 {
		f.Months = strconv.Itoa(d.Months)
	}
	if d.ID > 0 && f.Alerts == "" {
		f.Alerts = "0"
	}
	return f
}

func (w *web) renderForm(rw http.ResponseWriter, r *http.Request, who app.Identity, f *docForm, msg string) {
	title := "Nuevo documento"
	if f.ID > 0 {
		title = "Editar"
	}
	w.render(rw, r, "form.html", pageData{Name: who.Name, Title: title, Form: f, Types: typeOpts(), Error: msg})
}

func (w *web) newDoc(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	f := &docForm{Type: "dni"}
	if t := r.URL.Query().Get("tipo"); t != "" {
		if _, ok := typeOf(t); ok {
			f.Type = t
		}
	}
	w.renderForm(rw, r, who, f, "")
}

func (w *web) editDoc(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	x, err := w.svc.store.doc(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "editar", err)
		return
	}
	w.renderForm(rw, r, who, formOf(x), "")
}

// docFromForm lee el formulario (sin validar lo que valida doc.clean).
func docFromForm(f *docForm) (doc, error) {
	d := doc{ID: f.ID, Type: f.Type, Title: f.Title, Holder: f.Holder, Number: f.Number, Issuer: f.Issuer,
		Issued: f.Issued, Expires: f.Expires, Notes: f.Note}
	if m := strings.TrimSpace(f.Months); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 0 || n > maxMonths {
			return d, docErr(fmt.Sprintf("La renovación periódica va de 1 a %d meses.", maxMonths))
		}
		d.Months = n
	}
	if _, ok := typeOf(d.Type); !ok {
		return d, docErr("Elige el tipo de documento.")
	}
	var err error
	if d.Alerts, err = parseAlerts(f.Alerts, d.Type); err != nil {
		return d, err
	}
	if d.Amount, err = parseAmount(f.Amount); err != nil {
		return d, err
	}
	return d, nil
}

func (w *web) saveDoc(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id < 0 {
		notFound(rw)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "El formulario es demasiado largo.", http.StatusRequestEntityTooLarge)
		return
	}
	p := r.PostForm
	f := &docForm{ID: id, Type: p.Get("tipo"), Title: p.Get("titulo"), Holder: p.Get("titular"), Number: p.Get("numero"),
		Issuer: p.Get("emisor"), Issued: p.Get("emision"), Expires: p.Get("caducidad"), Months: p.Get("cada_meses"),
		Alerts: p.Get("avisos"), Amount: p.Get("importe"), Note: p.Get("notas")}
	if id > 0 {
		if _, err := w.svc.store.doc(r.Context(), who.User, id); errors.Is(err, errNotFound) {
			notFound(rw)
			return
		} else if err != nil {
			w.fail(rw, "guardar", err)
			return
		}
	}
	d, err := docFromForm(f)
	if err == nil {
		id, err = w.svc.saveDoc(r.Context(), who.User, d)
	}
	var de docErr
	switch {
	case errors.As(err, &de):
		w.renderForm(rw, r, who, f, err.Error())
	case errors.Is(err, errNotFound):
		notFound(rw)
	case err != nil:
		w.fail(rw, "guardar", err)
	default:
		back(rw, r, fmt.Sprintf("/doc/%d", id), "guardado")
	}
}

func (w *web) deleteDoc(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	err := w.svc.deleteDoc(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "borrar", err)
		return
	}
	back(rw, r, "/", "borrado")
}

func (w *web) renewDoc(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	_, err := w.svc.renewDoc(r.Context(), who.User, id, strings.TrimSpace(r.PostForm.Get("fecha")))
	var de docErr
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case errors.As(err, &de):
		w.renderDoc(rw, r, who, id, err.Error())
	case err != nil:
		w.fail(rw, "renovar", err)
	default:
		back(rw, r, fmt.Sprintf("/doc/%d", id), "renovado")
	}
}

// ---- Archivos ----

// readUpload lee el archivo subido en memoria, con límite. Nada pasa por el
// disco en claro.
func readUpload(r *http.Request) (string, []byte, error) {
	readErr := func(err error) error {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return errTooBig
		}
		return fileErr("No se ha podido leer el archivo. Prueba otra vez.")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, fileErr("Elige un archivo.")
	}
	for n := 0; ; n++ {
		part, err := mr.NextPart()
		if err == io.EOF {
			return "", nil, fileErr("Elige un archivo.")
		}
		if err != nil {
			return "", nil, readErr(err)
		}
		if n >= 4 {
			return "", nil, fileErr("El formulario no es válido.")
		}
		if part.FormName() != "archivo" {
			part.Close()
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, maxFile+1))
		if err != nil {
			return "", nil, readErr(err)
		}
		if len(b) > maxFile {
			return "", nil, errTooBig
		}
		name := part.FileName()
		part.Close()
		if len(b) == 0 && name == "" {
			return "", nil, fileErr("Elige un archivo.")
		}
		return name, b, nil
	}
}

func (w *web) uploadFile(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxFile+1<<20)
	name, raw, err := readUpload(r)
	if err == nil {
		_, err = w.svc.addFile(r.Context(), who.User, id, name, raw)
	}
	var fe fileErr
	switch {
	case errors.Is(err, errNotFound):
		notFound(rw)
	case errors.As(err, &fe):
		w.renderDoc(rw, r, who, id, err.Error())
	case err != nil:
		w.fail(rw, "subir", err)
	default:
		http.Redirect(rw, r, fmt.Sprintf("/doc/%d?ok=archivo#archivos", id), http.StatusSeeOther)
	}
}

// serveFile sirve el archivo (o su miniatura) solo a su dueño, sin caché.
func (w *web) serveFile(mini bool) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		id := pathID(r)
		if id <= 0 {
			notFound(rw)
			return
		}
		m, b, err := w.svc.fileContent(r.Context(), who.User, id, mini)
		if errors.Is(err, errNotFound) {
			notFound(rw)
			return
		}
		if err != nil {
			w.fail(rw, "archivo", err)
			return
		}
		h := rw.Header()
		ctype := m.Type
		if mini {
			ctype = typeJPEG
		}
		h.Set("Content-Type", ctype)
		h.Set("Content-Length", strconv.Itoa(len(b)))
		h.Set("Cache-Control", "private, no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if ctype != typePDF {
			// El visor de PDF del navegador no funciona con una CSP tan
			// cerrada; el tipo fijo y nosniff impiden leerlo como HTML.
			h.Set("Content-Security-Policy", "default-src 'none'")
		}
		if !mini {
			h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": m.Name}))
		}
		rw.Write(b)
	}
}

func (w *web) deleteFile(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	docID, err := w.svc.deleteFile(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "borrar archivo", err)
		return
	}
	http.Redirect(rw, r, fmt.Sprintf("/doc/%d?ok=archivo-borrado#archivos", docID), http.StatusSeeOther)
}

// ---- Agenda ----

// agendaPost es el botón «Actualizar la Agenda ahora».
func (w *web) agendaPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	res, err := w.svc.syncAgenda(r.Context(), who.User)
	switch {
	case err != nil:
		w.svc.log.Warn("vencimientos a la agenda", "err", err)
		back(rw, r, "/", "agenda-error")
	case !res.Agenda:
		back(rw, r, "/", "agenda-no")
	default:
		back(rw, r, "/", "agenda")
	}
}
