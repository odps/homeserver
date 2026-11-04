package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

// Páginas servidas desde el servidor, sin JavaScript: la agenda, las
// tareas, los contactos, el correo y los ajustes. El uso principal es con el
// asistente; esto es para ver y retocar.

//go:embed web
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"ago":      ago,
	"catName":  categoryName,
	"cats":     func() []string { return allCategories },
	"hm":       func(t *time.Time) string { return t.In(madrid).Format("15:04") },
	"clock":    func(t time.Time) string { return t.In(madrid).Format("15:04") },
	"day":      func(t time.Time) string { return dayLabel(t) },
	"longDate": longDate,
	"join":     strings.Join,
}).ParseFS(webFS, "web/*.html"))

const csp = "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// pageData es lo que reciben las plantillas. Cada página usa su parte.
type pageData struct {
	Version string
	Base    string // /agenda detrás de la pasarela
	Name    string // nombre visible de la persona
	User    string // usuario (para la conexión del móvil)
	Tab     string // agenda, tareas, contactos, correo, ajustes
	Flash   string
	Error   string
	Refresh int // segundos: recarga sola (conexión de Outlook en curso)

	// Agenda
	Days       []dayView
	From       string // AAAA-MM-DD del primer día mostrado
	Prev, Next string // AAAA-MM-DD para ir 14 días atrás o adelante
	Today      string

	// Tareas (también en la agenda: las vencidas y las de hoy)
	Tasks []entry

	// Formulario de evento o tarea
	Form      formView
	Calendars []collection

	// Contactos
	Query    string
	Contacts []contact
	Contact  contact

	// Correo
	Mails      []message
	Mail       message
	Category   string         // filtro de categoría ("" todas)
	Counts     map[string]int // por categoría, con los filtros de la página
	UnreadOnly bool
	Unread     int // sin leer en la última semana (agenda)

	// Envíos (sacados del correo)
	Shipments []shipmentView
	Delivered bool // también los entregados

	// Ajustes
	Prefs    prefs
	Accounts []accountView
	Flow     *deviceFlow
	DAVURL   string
}

type dayView struct {
	Date  string // AAAA-MM-DD
	Label string // «jueves, 5 de noviembre»
	Today bool
	Items []entry
}

// formView es el formulario de un evento o una tarea.
type formView struct {
	ID         int64
	Kind       string // evento o tarea
	Title      string
	Date       string // AAAA-MM-DD (en tareas, el vencimiento; puede ir vacío)
	Start      string // HH:MM
	EndDate    string // AAAA-MM-DD
	End        string // HH:MM
	AllDay     bool
	Location   string
	Notes      string
	Repeat     string // "", DAILY, WEEKLY, MONTHLY, YEARLY o "keep" (una regla a medida que se conserva)
	RepeatText string // la regla actual en palabras
	Alarm      string // minutos de antelación, o "" sin aviso
	Calendar   string // slug
	Done       bool
	Maps       string
	Route      string
}

type accountView struct {
	ID      int64
	Kind    string // gmail, outlook, imap
	Address string
	Synced  string // «hace 3 min» o ""
	Error   string
	Drafts  string
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "ahora"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	case d < 6*24*time.Hour:
		return shortDays[t.In(madrid).Weekday()] + " " + t.In(madrid).Format("15:04")
	}
	return t.In(madrid).Format("02/01/06")
}

type web struct {
	svc *service
	dav *davServer
}

func (w *web) routes(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web/static")
	files := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(rw, r)
	})))
	mux.Handle("/dav/", w.dav)
	mux.HandleFunc("POST /interno/entreno/rutinas", w.gymSyncPost)
	mux.HandleFunc("POST /interno/papeles/vencimientos", w.deadlinesPost)
	mux.HandleFunc("GET /{$}", w.user(w.home))
	mux.HandleFunc("GET /evento/nuevo", w.user(w.newEntry))
	mux.HandleFunc("POST /evento", w.user(w.createEntry))
	mux.HandleFunc("GET /evento/{id}", w.user(w.editEntry))
	mux.HandleFunc("POST /evento/{id}", w.user(w.saveEntry))
	mux.HandleFunc("POST /evento/{id}/borrar", w.user(w.deleteEntry))
	mux.HandleFunc("GET /tareas", w.user(w.tasksPage))
	mux.HandleFunc("GET /rutinas", w.user(w.routinesPage))
	mux.HandleFunc("POST /rutinas", w.user(w.createRoutine))
	mux.HandleFunc("POST /tareas/{id}/hecha", w.user(w.toggleTask))
	mux.HandleFunc("GET /contactos", w.user(w.contactsPage))
	mux.HandleFunc("GET /contactos/nuevo", w.user(w.newContact))
	mux.HandleFunc("POST /contactos", w.user(w.saveContact))
	mux.HandleFunc("GET /contactos/{id}", w.user(w.contactPage))
	mux.HandleFunc("POST /contactos/{id}", w.user(w.saveContact))
	mux.HandleFunc("POST /contactos/{id}/borrar", w.user(w.deleteContact))
	mux.HandleFunc("GET /correo", w.user(w.mailPage))
	mux.HandleFunc("GET /correo/{id}", w.user(w.mailMessage))
	mux.HandleFunc("POST /correo/{id}/borrador", w.user(w.mailDraft))
	mux.HandleFunc("POST /correo/{id}/remitente", w.user(w.mailSender))
	mux.HandleFunc("POST /ajustes/correo", w.user(w.saveMailRules))
	mux.HandleFunc("POST /correo/sincronizar", w.user(w.mailSync))
	mux.HandleFunc("GET /envios", w.user(w.shipmentsPage))
	mux.HandleFunc("POST /envios/{id}/quitar", w.user(w.hideShipment))
	mux.HandleFunc("GET /ajustes", w.user(w.settings))
	mux.HandleFunc("POST /ajustes", w.user(w.savePrefs))
	mux.HandleFunc("POST /cuentas", w.user(w.addAccount))
	mux.HandleFunc("POST /cuentas/outlook", w.user(w.startOutlook))
	mux.HandleFunc("POST /cuentas/{id}/borrar", w.user(w.deleteAccount))
	mux.HandleFunc("GET /api/avisos", w.user(w.notices))
	mux.HandleFunc("GET /api/resumen/texto", w.user(w.summaryText))
}

// user exige la identidad que pone la pasarela.
func (w *web) user(h func(http.ResponseWriter, *http.Request, app.Identity)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		who := app.User(r)
		if !who.IsUser() {
			http.Error(rw, "entra desde la pasarela", http.StatusUnauthorized)
			return
		}
		if err := w.svc.store.ensureUser(r.Context(), who.User); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		h(rw, r, who)
	}
}

func base(r *http.Request) string { return strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/") }

func (w *web) render(rw http.ResponseWriter, r *http.Request, name string, d pageData) {
	d.Version, d.Base = version.Version, base(r)
	d.Today = time.Now().In(madrid).Format("2006-01-02")
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Content-Security-Policy", csp)
	rw.Header().Set("Cache-Control", "no-store")
	if err := pages.ExecuteTemplate(rw, name, d); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}
}

var flashes = map[string]string{
	"evento": "Evento guardado.", "tarea": "Tarea guardada.", "borrado": "Borrado.", "contacto": "Contacto guardado.",
	"ajustes": "Ajustes guardados.", "cuenta": "Cuenta conectada: el correo llegará en unos minutos.",
	"borrador": "Borrador guardado en tu correo. No se ha enviado.", "sincronizado": "Correo al día.",
	"hecha": "Tarea marcada.", "rutina": "Rutina creada.", "clasificacion": "Clasificación guardada: el correo ya se ha reordenado.",
	"envio": "Envío quitado de la lista.",
}

func flash(r *http.Request) string { return flashes[r.URL.Query().Get("ok")] }

// back vuelve a una página de la app con un aviso.
func back(rw http.ResponseWriter, r *http.Request, to, ok string) {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(rw, r, to+sep+"ok="+ok, http.StatusSeeOther)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func notFound(err error) bool { return errors.Is(err, errNotFound) }

// ---- Agenda ----

func (w *web) home(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	from := dayStart(time.Now())
	if t, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("desde"), madrid); err == nil {
		from = t
	}
	to := from.AddDate(0, 0, 14)
	data := pageData{Name: who.Name, Tab: "agenda", Flash: flash(r), From: from.Format("2006-01-02"),
		Prev: from.AddDate(0, 0, -14).Format("2006-01-02"), Next: to.Format("2006-01-02")}
	items, err := w.svc.agenda(ctx, who.User, from, to)
	if err != nil {
		data.Error = err.Error()
	}
	today := time.Now().In(madrid).Format("2006-01-02")
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		day := dayView{Date: d.Format("2006-01-02"), Label: longDate(d)}
		day.Today = day.Date == today
		next := d.AddDate(0, 0, 1)
		for _, e := range items {
			if e.Start != nil && e.Start.Before(next) && (e.End != nil && e.End.After(d) || !e.Start.Before(d)) {
				if e.Kind == "tarea" && e.Start.Before(d) {
					continue
				}
				day.Items = append(day.Items, e)
			}
		}
		data.Days = append(data.Days, day)
	}
	if tasks, err := w.svc.tasks(ctx, who.User); err == nil {
		for _, t := range tasks {
			if t.Overdue || t.Start == nil {
				data.Tasks = append(data.Tasks, t)
			}
		}
	}
	if mails, err := w.svc.store.searchMessages(ctx, who.User, mailQuery{UnreadOnly: true, Since: time.Now().AddDate(0, 0, -7), Limit: 200}); err == nil {
		data.Unread = len(mails)
		data.Mails = mails[:min(len(mails), 5)]
	}
	w.render(rw, r, "agenda.html", data)
}

func (w *web) formPage(rw http.ResponseWriter, r *http.Request, who app.Identity, f formView, errMsg string) {
	cals, _ := w.svc.store.collections(r.Context(), who.User, "cal")
	tab := "agenda"
	if f.Kind == "tarea" {
		tab = "tareas"
	}
	w.render(rw, r, "evento.html", pageData{Name: who.Name, Tab: tab, Form: f, Calendars: cals, Error: errMsg, Flash: flash(r)})
}

func (w *web) newEntry(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	f := formView{Kind: "evento", Date: q.Get("fecha"), Start: "10:00", End: "11:00"}
	if q.Get("tipo") == "tarea" {
		f = formView{Kind: "tarea", Date: q.Get("fecha"), Alarm: "0"} // avisa al vencer, si tiene hora
	}
	if f.Kind == "evento" {
		if f.Date == "" {
			f.Date = time.Now().In(madrid).Format("2006-01-02")
		}
		f.EndDate = f.Date
		if p, err := w.svc.store.prefs(r.Context(), who.User); err == nil && p.DefaultMin > 0 {
			f.Alarm = strconv.Itoa(p.DefaultMin)
		}
	}
	w.formPage(rw, r, who, f, "")
}

func formOf(e entry) formView {
	f := formView{ID: e.ID, Kind: e.Kind, Title: e.Title, Location: e.Location, Notes: e.Notes, AllDay: e.AllDay,
		Calendar: e.Calendar, Done: e.Done, Maps: e.Maps, Route: e.Route, RepeatText: e.Repeat}
	if e.Start != nil {
		f.Date = e.Start.Format("2006-01-02")
		if !e.AllDay {
			f.Start = e.Start.Format("15:04")
		}
	}
	if e.End != nil {
		end := *e.End
		if e.AllDay {
			end = end.AddDate(0, 0, -1)
		}
		f.EndDate, f.End = end.Format("2006-01-02"), end.Format("15:04")
	}
	if e.RRule != "" {
		f.Repeat = "keep"
		for _, fr := range []string{"DAILY", "WEEKLY", "MONTHLY", "YEARLY"} {
			if strings.EqualFold(e.RRule, "FREQ="+fr) {
				f.Repeat = fr
			}
		}
	}
	if len(e.Alarms) > 0 {
		f.Alarm = strconv.Itoa(e.Alarms[0])
	}
	return f
}

func (w *web) editEntry(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	e, err := w.svc.entry(r.Context(), who.User, pathID(r))
	if notFound(err) {
		http.NotFound(rw, r)
		return
	}
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.formPage(rw, r, who, formOf(e), "")
}

// readForm convierte el formulario en un cambio (todo lo que trae el
// formulario se aplica).
func readForm(r *http.Request) (formView, eventInput, error) {
	f := formView{Kind: r.FormValue("tipo"), Title: r.FormValue("titulo"), Date: r.FormValue("fecha"), Start: r.FormValue("hora"),
		EndDate: r.FormValue("fecha_fin"), End: r.FormValue("hora_fin"), AllDay: r.FormValue("todo_el_dia") != "",
		Location: r.FormValue("lugar"), Notes: r.FormValue("notas"), Repeat: r.FormValue("repetir"), Alarm: r.FormValue("aviso"),
		Calendar: r.FormValue("calendario")}
	if f.Kind != "tarea" {
		f.Kind = "evento"
	}
	in := eventInput{Title: &f.Title, Location: &f.Location, Notes: &f.Notes}
	alarms := []int{}
	if n, err := strconv.Atoi(f.Alarm); err == nil && n >= 0 && f.Alarm != "" {
		alarms = []int{n}
	}
	in.Alarms = &alarms
	switch f.Repeat {
	case "keep":
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
		rule := "FREQ=" + f.Repeat
		in.Repeat = &rule
	default:
		empty := ""
		in.Repeat = &empty
	}
	at := func(date, hm string) (time.Time, error) {
		if f.AllDay || hm == "" {
			return time.ParseInLocation("2006-01-02", date, madrid)
		}
		return time.ParseInLocation("2006-01-02 15:04", date+" "+hm, madrid)
	}
	if f.Kind == "tarea" {
		if f.Date == "" {
			in.NoDue = true
			return f, in, nil
		}
		due, err := at(f.Date, f.Start)
		if err != nil {
			return f, in, errors.New("fecha no válida")
		}
		allDay := f.AllDay || f.Start == ""
		in.Due, in.AllDay = &due, &allDay
		return f, in, nil
	}
	start, err := at(f.Date, f.Start)
	if err != nil {
		return f, in, errors.New("fecha u hora de comienzo no válida")
	}
	if f.EndDate == "" {
		f.EndDate = f.Date
	}
	end, err := at(f.EndDate, f.End)
	if err != nil || (!f.AllDay && f.End == "") {
		end = start.Add(time.Hour)
	}
	if f.AllDay {
		end = end.AddDate(0, 0, 1)
	}
	in.Start, in.End, in.AllDay = &start, &end, &f.AllDay
	return f, in, nil
}

func (w *web) createEntry(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	f, in, err := readForm(r)
	if err != nil {
		w.formPage(rw, r, who, f, err.Error())
		return
	}
	comp := ical.CompEvent
	if f.Kind == "tarea" {
		comp = ical.CompToDo
	}
	e, err := w.svc.create(r.Context(), who.User, f.Calendar, comp, in)
	if err != nil {
		w.formPage(rw, r, who, f, err.Error())
		return
	}
	if e.Kind == "tarea" {
		back(rw, r, "/tareas", "tarea")
		return
	}
	back(rw, r, "/?desde="+dayStart(*e.Start).Format("2006-01-02"), "evento")
}

func (w *web) saveEntry(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	cur, err := w.svc.entry(r.Context(), who.User, pathID(r))
	if notFound(err) {
		http.NotFound(rw, r)
		return
	}
	r.ParseForm()
	r.Form.Set("tipo", cur.Kind)
	f, in, err := readForm(r)
	f.ID, f.RepeatText = cur.ID, cur.Repeat
	if err == nil {
		_, err = w.svc.update(r.Context(), who.User, cur.ID, in)
	}
	if err != nil {
		w.formPage(rw, r, who, f, err.Error())
		return
	}
	if cur.Kind == "tarea" {
		back(rw, r, "/tareas", "tarea")
		return
	}
	back(rw, r, fmt.Sprintf("/evento/%d", cur.ID), "evento")
}

func (w *web) deleteEntry(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	e, err := w.svc.remove(r.Context(), who.User, pathID(r))
	if err != nil && !notFound(err) {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if e.Kind == "tarea" {
		back(rw, r, "/tareas", "borrado")
		return
	}
	back(rw, r, "/", "borrado")
}

func (w *web) tasksPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	tasks, err := w.svc.tasks(r.Context(), who.User)
	data := pageData{Name: who.Name, Tab: "tareas", Tasks: tasks, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "tareas.html", data)
}

func (w *web) routinesPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.routinesWith(rw, r, who, "")
}

func (w *web) routinesWith(rw http.ResponseWriter, r *http.Request, who app.Identity, errMsg string) {
	list, err := w.svc.routines(r.Context(), who.User)
	data := pageData{Name: who.Name, Tab: "agenda", Tasks: list, Flash: flash(r), Error: errMsg,
		From: time.Now().In(madrid).Format("2006-01-02")}
	if err != nil && errMsg == "" {
		data.Error = err.Error()
	}
	w.render(rw, r, "rutinas.html", data)
}

func (w *web) createRoutine(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	r.ParseForm()
	in := routineInput{Title: strings.TrimSpace(r.FormValue("titulo")), Days: r.Form["dias"], Start: r.FormValue("inicio"),
		End: r.FormValue("fin"), Location: strings.TrimSpace(r.FormValue("lugar"))}
	if t, err := time.ParseInLocation("2006-01-02", r.FormValue("desde"), madrid); err == nil {
		in.From = t
	}
	if t, err := time.ParseInLocation("2006-01-02", r.FormValue("hasta"), madrid); err == nil {
		in.Until = &t
	}
	if in.Title == "" {
		w.routinesWith(rw, r, who, "Falta qué es.")
		return
	}
	if _, err := w.svc.createRoutine(r.Context(), who.User, in); err != nil {
		w.routinesWith(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/rutinas", "rutina")
}

func (w *web) toggleTask(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	done := r.FormValue("hecha") != "0"
	if _, err := w.svc.update(r.Context(), who.User, pathID(r), eventInput{Done: &done}); err != nil && !notFound(err) {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	to := r.FormValue("volver")
	if to != "/" {
		to = "/tareas"
	}
	back(rw, r, to, "hecha")
}

// ---- Contactos ----

func (w *web) contactsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	list, err := w.svc.contacts(r.Context(), who.User, q, 500)
	data := pageData{Name: who.Name, Tab: "contactos", Query: q, Contacts: list, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "contactos.html", data)
}

func (w *web) newContact(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.render(rw, r, "contacto.html", pageData{Name: who.Name, Tab: "contactos"})
}

func (w *web) contactPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	c, err := w.svc.contact(r.Context(), who.User, pathID(r))
	if notFound(err) {
		http.NotFound(rw, r)
		return
	}
	data := pageData{Name: who.Name, Tab: "contactos", Contact: c, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	if mails, err := w.svc.store.searchMessages(r.Context(), who.User, mailQuery{From: firstOr(c.Emails), Limit: 5}); err == nil && len(c.Emails) > 0 {
		data.Mails = mails
	}
	w.render(rw, r, "contacto.html", data)
}

func firstOr(l []labeled) string {
	if len(l) == 0 {
		return ""
	}
	return l[0].Value
}

// lines separa un campo de varias líneas (correos, teléfonos, direcciones).
func lines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (w *web) saveContact(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	in := contactInput{Emails: lines(r.FormValue("correos")), Phones: lines(r.FormValue("telefonos")), Addresses: lines(r.FormValue("direcciones"))}
	for field, dst := range map[string]**string{"nombre": &in.Name, "empresa": &in.Org, "cargo": &in.Role, "cumpleanos": &in.Birthday, "notas": &in.Notes} {
		v := r.FormValue(field)
		*dst = &v
	}
	id := pathID(r)
	c, err := w.svc.saveContact(r.Context(), who.User, id, in)
	if err != nil {
		cur := contact{ID: id, Name: *in.Name, Org: *in.Org, Role: *in.Role, Birthday: *in.Birthday, Notes: *in.Notes}
		for _, v := range in.Emails {
			cur.Emails = append(cur.Emails, labeled{Value: v})
		}
		for _, v := range in.Phones {
			cur.Phones = append(cur.Phones, labeled{Value: v})
		}
		for _, v := range in.Addresses {
			cur.Addresses = append(cur.Addresses, labeled{Value: v})
		}
		w.render(rw, r, "contacto.html", pageData{Name: who.Name, Tab: "contactos", Contact: cur, Error: err.Error()})
		return
	}
	back(rw, r, fmt.Sprintf("/contactos/%d", c.ID), "contacto")
}

func (w *web) deleteContact(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if _, err := w.svc.removeContact(r.Context(), who.User, pathID(r)); err != nil && !notFound(err) {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	back(rw, r, "/contactos", "borrado")
}

// ---- Correo ----

func (w *web) mailPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	unread := r.URL.Query().Get("no_leidos") != ""
	cat := r.URL.Query().Get("cat")
	if !slices.Contains(allCategories, cat) {
		cat = ""
	}
	mq := mailQuery{Text: q, UnreadOnly: unread, Limit: 100}
	counts, _ := w.svc.store.countMessages(r.Context(), who.User, mq)
	if cat != "" {
		mq.Categories = []string{cat}
	}
	list, err := w.svc.store.searchMessages(r.Context(), who.User, mq)
	accts, _ := w.accounts(r.Context(), who.User)
	data := pageData{Name: who.Name, Tab: "correo", Query: q, UnreadOnly: unread, Category: cat, Counts: counts,
		Mails: list, Accounts: accts, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	data.Shipments, _ = w.svc.shipments(r.Context(), who.User, false)
	w.render(rw, r, "correo.html", data)
}

// ---- Envíos ----

func (w *web) shipmentsPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	all := r.URL.Query().Get("entregados") != ""
	list, err := w.svc.shipments(r.Context(), who.User, all)
	data := pageData{Name: who.Name, Tab: "correo", Shipments: list, Delivered: all, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "envios.html", data)
}

func (w *web) hideShipment(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if _, err := w.svc.hideShipment(r.Context(), who.User, pathID(r)); err != nil && !notFound(err) {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	back(rw, r, "/envios", "envio")
}

func (w *web) mailMessage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	m, err := w.svc.store.messageByID(r.Context(), who.User, pathID(r))
	if notFound(err) {
		http.NotFound(rw, r)
		return
	}
	data := pageData{Name: who.Name, Tab: "correo", Mail: m, Flash: flash(r)}
	if err != nil {
		data.Error = err.Error()
	}
	w.render(rw, r, "mensaje.html", data)
}

// mailSender fija el remitente de un correo como importante o a ignorar (o lo
// quita de ambas listas).
func (w *web) mailSender(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	m, err := w.svc.store.messageByID(r.Context(), who.User, pathID(r))
	if notFound(err) {
		http.NotFound(rw, r)
		return
	}
	if err == nil {
		err = w.svc.markSender(r.Context(), who.User, m.FromAddr, r.FormValue("como"))
	}
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	back(rw, r, fmt.Sprintf("/correo/%d", m.ID), "clasificacion")
}

func (w *web) saveMailRules(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if err := w.svc.setMailRules(r.Context(), who.User, senderList(r.FormValue("importantes")), senderList(r.FormValue("ignorar"))); err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes#clasificacion", "clasificacion")
}

func senderList(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func (w *web) mailDraft(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	m, err := w.svc.store.messageByID(ctx, who.User, pathID(r))
	if notFound(err) {
		http.NotFound(rw, r)
		return
	}
	if err == nil {
		var a mailAccount
		a, err = w.svc.pickAccount(ctx, who.User, m.Account)
		if err == nil {
			to := m.FromAddr
			if m.FromName != "" {
				to = fmt.Sprintf("%q <%s>", m.FromName, m.FromAddr)
			}
			if strings.TrimSpace(r.FormValue("texto")) == "" {
				err = errors.New("escribe el texto de la respuesta")
			} else {
				_, err = w.svc.mail.saveDraft(ctx, a, draftInput{FromName: who.Name, To: to, Body: r.FormValue("texto"), ReplyTo: &m})
			}
		}
	}
	if err != nil {
		w.render(rw, r, "mensaje.html", pageData{Name: who.Name, Tab: "correo", Mail: m, Query: r.FormValue("texto"), Error: err.Error()})
		return
	}
	back(rw, r, fmt.Sprintf("/correo/%d", m.ID), "borrador")
}

func (w *web) mailSync(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	list, err := w.svc.store.accounts(ctx, who.User)
	var errs []error
	for _, a := range list {
		if _, err := w.svc.mail.syncAccount(ctx, a); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Address, err))
		}
	}
	if err = errors.Join(append(errs, err)...); err != nil {
		accts, _ := w.accounts(ctx, who.User)
		w.render(rw, r, "correo.html", pageData{Name: who.Name, Tab: "correo", Accounts: accts, Error: err.Error()})
		return
	}
	back(rw, r, "/correo", "sincronizado")
}

// ---- Ajustes ----

func (w *web) accounts(ctx context.Context, owner string) ([]accountView, error) {
	list, err := w.svc.store.accounts(ctx, owner)
	out := []accountView{}
	for _, a := range list {
		v := accountView{ID: a.ID, Kind: a.Kind, Address: a.Address, Error: a.Error, Drafts: a.Drafts}
		if a.SyncedAt != nil {
			v.Synced = ago(*a.SyncedAt)
		}
		out = append(out, v)
	}
	return out, err
}

func davURL(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host + base(r) + "/dav/"
}

func (w *web) settingsPage(rw http.ResponseWriter, r *http.Request, who app.Identity, errMsg string) {
	p, err := w.svc.store.prefs(r.Context(), who.User)
	accts, err2 := w.accounts(r.Context(), who.User)
	data := pageData{Name: who.Name, User: who.User, Tab: "ajustes", Prefs: p, Accounts: accts, DAVURL: davURL(r), Flash: flash(r), Error: errMsg}
	if err := errors.Join(err, err2); err != nil && errMsg == "" {
		data.Error = err.Error()
	}
	if f, ok := w.svc.mail.flow(who.User); ok && time.Now().Before(f.Expires.Add(10*time.Minute)) {
		data.Flow = &f
		if !f.Done && f.Err == "" {
			data.Refresh = 5
		}
	}
	w.render(rw, r, "ajustes.html", data)
}

func (w *web) settings(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.settingsPage(rw, r, who, "")
}

func (w *web) savePrefs(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	p, err := w.svc.store.prefs(r.Context(), who.User)
	if err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	p.SummaryOn = r.FormValue("resumen") != ""
	p.SummaryAI = r.FormValue("resumen_ia") != ""
	p.RemindersOn = r.FormValue("avisos") != ""
	if at := r.FormValue("resumen_hora"); at != "" {
		if _, err := time.Parse("15:04", at); err != nil {
			w.settingsPage(rw, r, who, "Hora del resumen no válida.")
			return
		}
		p.SummaryAt = at
	}
	if n, err := strconv.Atoi(r.FormValue("aviso_min")); err == nil && n >= 0 && n <= 7*24*60 {
		p.DefaultMin = n
	}
	p.Home = strings.TrimSpace(r.FormValue("casa"))
	p.WeatherCity = strings.TrimSpace(r.FormValue("ciudad_tiempo"))
	if err := w.svc.store.savePrefs(r.Context(), who.User, p); err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "ajustes")
}

func (w *web) addAccount(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	kind := r.FormValue("tipo")
	address := strings.ToLower(strings.TrimSpace(r.FormValue("direccion")))
	password := strings.ReplaceAll(r.FormValue("clave"), " ", "") // Google la muestra en grupos de 4
	a := mailAccount{Owner: who.User, Kind: kind, Address: address, Username: address}
	if p, ok := imapPresets[kind]; ok {
		a.Host, a.Port = p.Host, p.Port
	} else {
		a.Kind = "imap"
		a.Host = strings.TrimSpace(r.FormValue("servidor"))
		a.Port, _ = strconv.Atoi(r.FormValue("puerto"))
		if a.Port == 0 {
			a.Port = 993
		}
		if u := strings.TrimSpace(r.FormValue("usuario")); u != "" {
			a.Username = u
		}
	}
	if !strings.Contains(address, "@") || password == "" || a.Host == "" {
		w.settingsPage(rw, r, who, "Faltan la dirección, la contraseña de aplicación o el servidor.")
		return
	}
	a.Secret = w.svc.mail.seal.seal(password)
	if err := w.svc.mail.testLogin(r.Context(), a); err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	id, err := w.svc.store.addAccount(r.Context(), a)
	if err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	a.ID = id
	go w.svc.mail.syncAccount(context.Background(), a)
	back(rw, r, "/ajustes", "cuenta")
}

func (w *web) startOutlook(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	p, err := w.svc.store.prefs(r.Context(), who.User)
	if err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	if c := strings.TrimSpace(r.FormValue("app_id")); c != "" {
		p.OutlookApp = c
		w.svc.store.savePrefs(r.Context(), who.User, p)
	}
	if err := w.svc.mail.startOutlook(r.Context(), who.User, p.OutlookApp); err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	http.Redirect(rw, r, "/ajustes#correo", http.StatusSeeOther)
}

func (w *web) deleteAccount(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if err := w.svc.store.deleteAccount(r.Context(), who.User, pathID(r)); err != nil {
		w.settingsPage(rw, r, who, err.Error())
		return
	}
	back(rw, r, "/ajustes", "borrado")
}

// ---- Para la pasarela ----

// notices entrega a la pasarela los avisos pendientes (y prepara el resumen
// de la mañana cuando toca: llegará en la siguiente pasada).
func (w *web) notices(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.svc.dueSummary(r.Context(), who.User)
	list, err := w.svc.store.takeNotices(r.Context(), who.User, time.Now())
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(list)
}

func (w *web) summaryText(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	text, err := w.svc.summary(ctx, who.User, time.Now(), r.URL.Query().Get("ia") != "")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(rw, text)
}
