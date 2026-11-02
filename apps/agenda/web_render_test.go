package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRenderPages ejecuta cada plantilla con datos de ejemplo realistas (y
// los casos límite) sin base de datos. Con AGENDA_PREVIEW_DIR escribe las
// páginas y la hoja de estilos en esa carpeta para verlas en un navegador.
func TestRenderPages(t *testing.T) {
	now := time.Now().In(madrid)
	today := dayStart(now)
	at := func(days, h, m int) *time.Time {
		v := today.AddDate(0, 0, days).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		return &v
	}
	ymd := func(days int) string { return today.AddDate(0, 0, days).Format("2006-01-02") }

	dentist := entry{ID: 11, Kind: "evento", Title: "Dentista", Start: at(0, 17, 30), End: at(0, 18, 15),
		When: "hoy, 17:30–18:15", Location: "Calle Mayor 3, Madrid", Maps: "https://www.google.com/maps/search/?api=1&query=Calle+Mayor+3",
		Route: "https://www.google.com/maps/dir/?api=1&destination=Calle+Mayor+3", Alarms: []int{30}}
	standup := entry{ID: 12, Kind: "evento", Title: "Reunión de equipo semanal con un título bastante largo para ver cómo se parte en el móvil",
		Start: at(0, 9, 0), End: at(0, 9, 30), When: "09:00–09:30", Repeat: "cada semana", RRule: "FREQ=WEEKLY;BYDAY=MO,WE"}
	trip := entry{ID: 13, Kind: "evento", Title: "Viaje a Lisboa", Start: at(1, 0, 0), End: at(4, 0, 0), AllDay: true,
		When: dayLabel(*at(1, 0, 0)) + " – " + dayLabel(*at(3, 0, 0))}
	call := entry{ID: 14, Kind: "tarea", Title: "Llamar a Juan", Start: at(0, 10, 0), When: "vence el hoy a las 10:00", Alarms: []int{0}}
	doneTask := entry{ID: 15, Kind: "tarea", Title: "Pagar el IBI", Start: at(0, 0, 0), AllDay: true, When: "vence hoy", Done: true}
	overdue := entry{ID: 16, Kind: "tarea", Title: "Renovar el DNI", Start: at(-3, 0, 0), AllDay: true, When: "vence el " + dayLabel(*at(-3, 0, 0)),
		Overdue: true, Notes: "Pedir cita previa en la web de la Policía Nacional. Llevar foto reciente y el DNI caducado; si es por pérdida, la denuncia."}
	undated := entry{ID: 17, Kind: "tarea", Title: "Comprar bombillas"}
	dated := entry{ID: 18, Kind: "tarea", Title: "Enviar facturas a la gestoría", Start: at(5, 0, 0), AllDay: true, When: "vence el " + dayLabel(*at(5, 0, 0)), Repeat: "cada mes"}

	var days []dayView
	for i := 0; i < 14; i++ {
		d := dayView{Date: ymd(i), Label: longDate(today.AddDate(0, 0, i)), Today: i == 0}
		switch {
		case i == 0:
			d.Items = []entry{standup, call, dentist, doneTask}
		case i >= 1 && i <= 3:
			d.Items = []entry{trip}
		case i == 5:
			d.Items = []entry{dated}
		}
		days = append(days, d)
	}

	mails := []message{
		{ID: 101, Account: "olga@gmail.com", FromName: "Gestoría López", FromAddr: "info@gestorialopez.es", Subject: "Modelo 303 del tercer trimestre",
			Date: now.Add(-25 * time.Minute), Attachments: []string{"303-T3.pdf"}, Flagged: true},
		{ID: 102, Account: "olga@gmail.com", FromAddr: "no-reply@notificaciones-muy-largas-de-un-banco.example.com", Subject: "", Date: now.Add(-5 * time.Hour)},
		{ID: 103, Account: "olga@outlook.es", FromName: "Juan Pérez", FromAddr: "juan@example.com", Subject: "Re: cena del sábado", Date: now.Add(-50 * time.Hour), Seen: true},
	}
	longBody := "Hola, Olga:\n\nTe adjunto el modelo 303 del tercer trimestre para que lo revises antes del día 20.\n\n" +
		"Puedes consultarlo también aquí: https://sede.agenciatributaria.gob.es/Sede/procedimientoini/G414.shtml?parametro_muy_largo=abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789\n\n" +
		strings.Repeat("Línea de texto citado del mensaje anterior, bastante larga para comprobar el ajuste. ", 8) + "\n\n> respuesta citada\n> otra línea\n\nUn saludo,\nGestoría López"
	msg := mails[0]
	msg.Body, msg.To, msg.Cc = longBody, "Olga Pereira <olga@gmail.com>", "Contabilidad <conta@gestorialopez.es>, otra.persona.con.direccion.larga@example.com"
	msg.Attachments = []string{"303-T3.pdf", "justificante-de-presentacion-con-nombre-muy-largo-2026.pdf", "datos.xlsx"}

	ana := contact{ID: 21, Name: "Ana García Fernández", Org: "Clínica Dental Sonrisas", Role: "Recepción",
		Emails:    []labeled{{Label: "trabajo", Value: "ana@clinicasonrisas.es"}, {Value: "ana.garcia.fernandez.1990@gmail.com"}},
		Phones:    []labeled{{Label: "móvil", Value: "+34 600 11 22 33"}, {Label: "trabajo", Value: "91 123 45 67"}},
		Addresses: []labeled{{Label: "trabajo", Value: "Calle Mayor 3, 28013 Madrid", Maps: "https://www.google.com/maps/search/?api=1&query=Calle+Mayor+3"}, {Label: "casa", Value: "Avenida de la Constitución 145, 4.º B, 41001 Sevilla", Maps: "https://www.google.com/maps/search/?api=1&query=Avenida+Constitucion"}},
		Birthday:  "--05-17", Notes: "Prefiere WhatsApp.\nNo llamar antes de las 10."}

	shipments := []shipmentView{
		{ID: 1, Carrier: "SEUR", Number: "08123456789", Merchant: "PcComponentes", Status: stOut, State: "en reparto", Expected: ymd(0),
			When: dayLabel(today), Link: "https://www.seur.com/livetracking/?segOnlineIdentificador=08123456789", Updated: now.Add(-2 * time.Hour),
			Text: "Tu envío sale hoy en reparto"},
		{ID: 2, Carrier: "GLS", Number: "61234567890", Status: stProblem, State: "incidencia", Updated: now.Add(-26 * time.Hour),
			Text: "Incidencia en la entrega de tu envío: no había nadie en el domicilio y haremos un nuevo intento mañana por la mañana"},
		{ID: 3, Carrier: "Amazon", Merchant: "Amazon", Status: stDelivered, State: "entregado", Updated: now.Add(-50 * time.Hour)},
	}
	cals := []collection{{Slug: "personal", Name: "Personal"}, {Slug: "trabajo", Name: "Trabajo"}}
	accts := []accountView{
		{ID: 1, Kind: "gmail", Address: "olga@gmail.com", Synced: "hace 3 min", Drafts: "[Gmail]/Borradores"},
		{ID: 2, Kind: "outlook", Address: "olga@outlook.es", Error: "AUTHENTICATE failed: token caducado", Drafts: "Drafts"},
		{ID: 3, Kind: "imap", Address: "olga@midominio.es"},
	}
	settings := pageData{Tab: "ajustes", User: "olga", Prefs: defaultPrefs(), Accounts: accts, DAVURL: "https://casa.example.net/agenda/dav/"}

	type page struct {
		file, tmpl string
		data       pageData
	}
	with := func(d pageData, f func(*pageData)) pageData { f(&d); return d }
	cases := []page{
		{"agenda", "agenda.html", pageData{Tab: "agenda", Name: "Olga", Days: days, From: ymd(0), Prev: ymd(-14), Next: ymd(14),
			Tasks: []entry{overdue, undated}, Unread: 7, Mails: mails[:2], Flash: "Evento guardado."}},
		{"agenda-vacia", "agenda.html", pageData{Tab: "agenda", Days: func() []dayView {
			var out []dayView
			for i := 20; i < 34; i++ {
				out = append(out, dayView{Date: ymd(i), Label: longDate(today.AddDate(0, 0, i))})
			}
			return out
		}(), From: ymd(20), Prev: ymd(6), Next: ymd(34), Error: "no se pudo leer el calendario"}},
		{"evento-nuevo", "evento.html", pageData{Tab: "agenda", Form: formView{Kind: "evento", Date: ymd(0), EndDate: ymd(0), Start: "10:00", End: "11:00", Alarm: "15"}, Calendars: cals}},
		{"evento-editar", "evento.html", pageData{Tab: "agenda", Form: formView{ID: 12, Kind: "evento", Title: standup.Title, Date: ymd(0), Start: "09:00", EndDate: ymd(0), End: "09:30",
			Location: dentist.Location, Maps: dentist.Maps, Route: dentist.Route, Repeat: "keep", RepeatText: "cada semana el lunes y el miércoles", Alarm: "45", Notes: "Orden del día en la carpeta compartida."}, Calendars: cals}},
		{"evento-todo-el-dia", "evento.html", pageData{Tab: "agenda", Form: formView{ID: 13, Kind: "evento", Title: trip.Title, Date: ymd(1), EndDate: ymd(3), AllDay: true, Repeat: ""}}},
		{"tarea-nueva", "evento.html", pageData{Tab: "tareas", Form: formView{Kind: "tarea"}, Calendars: cals[:1], Error: "fecha no válida"}},
		{"tarea-editar", "evento.html", pageData{Tab: "tareas", Form: formView{ID: 14, Kind: "tarea", Title: "Llamar a Juan", Date: ymd(0), Start: "10:00", Alarm: "0", Repeat: "MONTHLY", RepeatText: "cada mes"}}},
		{"tareas", "tareas.html", pageData{Tab: "tareas", Tasks: []entry{overdue, call, dated, undated}, Flash: "Tarea marcada."}},
		{"tareas-vacia", "tareas.html", pageData{Tab: "tareas"}},
		{"contactos", "contactos.html", pageData{Tab: "contactos", Contacts: []contact{ana, {ID: 22, Name: "Juan Pérez", Phones: []labeled{{Value: "600 99 88 77"}}}, {ID: 23, Name: "Fontanero"}}}},
		{"contactos-vacia", "contactos.html", pageData{Tab: "contactos"}},
		{"contactos-busqueda", "contactos.html", pageData{Tab: "contactos", Query: "zzz"}},
		{"contacto", "contacto.html", pageData{Tab: "contactos", Contact: ana, Mails: mails, Flash: "Contacto guardado."}},
		{"contacto-minimo", "contacto.html", pageData{Tab: "contactos", Contact: contact{ID: 23, Name: "Fontanero"}}},
		{"contacto-nuevo", "contacto.html", pageData{Tab: "contactos"}},
		{"correo", "correo.html", pageData{Tab: "correo", Mails: mails, Accounts: accts}},
		{"correo-busqueda", "correo.html", pageData{Tab: "correo", Query: "gestoría", UnreadOnly: true, Accounts: accts[:1]}},
		{"correo-sin-cuentas", "correo.html", pageData{Tab: "correo"}},
		{"correo-con-envios", "correo.html", pageData{Tab: "correo", Mails: mails, Accounts: accts, Shipments: shipments}},
		{"envios", "envios.html", pageData{Tab: "correo", Shipments: shipments, Flash: "Envío quitado de la lista."}},
		{"envios-entregados", "envios.html", pageData{Tab: "correo", Shipments: shipments[2:], Delivered: true}},
		{"envios-vacia", "envios.html", pageData{Tab: "correo"}},
		{"mensaje", "mensaje.html", pageData{Tab: "correo", Mail: msg, Flash: "Borrador guardado en tu correo. No se ha enviado."}},
		{"mensaje-vacio", "mensaje.html", pageData{Tab: "correo", Mail: message{ID: 104, FromAddr: "x@example.com", Date: now}}},
		{"correo-categorias", "correo.html", pageData{Tab: "correo", Accounts: accts, Category: catImportant,
			Counts: map[string]int{catPersonal: 3, catImportant: 1, catNotice: 7, catBulk: 12},
			Mails: []message{{ID: 105, FromName: "Google", FromAddr: "no-reply@accounts.google.com", Subject: "Alerta de seguridad", Date: now,
				Category: catImportant, Reason: "seguridad", Account: "ana@gmail.com"}}}},
		{"mensaje-comercial", "mensaje.html", pageData{Tab: "correo", Mail: message{ID: 106, FromAddr: "ofertas@tienda.es", Subject: "Rebajas",
			Date: now, Category: catBulk, Body: "Solo hoy."}}},
		{"rutinas", "rutinas.html", pageData{Tab: "agenda", From: ymd(0), Flash: "Rutina creada.", Tasks: []entry{{ID: 30, Kind: "evento",
			Title: "Trabajo", Routine: true, Start: at(0, 9, 0), End: at(0, 17, 0), Repeat: "cada semana (lunes, martes, miércoles, jueves, viernes)",
			Location: "Oficina"}}}},
		{"rutinas-vacia", "rutinas.html", pageData{Tab: "agenda", From: ymd(0), Error: "faltan los días de la semana"}},
		{"agenda-con-rutina", "agenda.html", pageData{Tab: "agenda", From: ymd(0), Days: []dayView{{Date: ymd(0), Label: "hoy", Today: true,
			Items: []entry{{ID: 30, Kind: "evento", Title: "Trabajo", Routine: true, Start: at(0, 9, 0), End: at(0, 17, 0)}, dentist}}}}},
		{"ajustes", "ajustes.html", settings},
		{"ajustes-sin-cuentas", "ajustes.html", with(settings, func(d *pageData) { d.Accounts = nil; d.Prefs.DefaultMin = 45 })},
		{"ajustes-outlook", "ajustes.html", with(settings, func(d *pageData) {
			d.Flow = &deviceFlow{UserCode: "F7KQ-2MXP", URI: "https://microsoft.com/devicelogin", Expires: now.Add(10 * time.Minute)}
			d.Refresh = 5
		})},
		{"ajustes-outlook-error", "ajustes.html", with(settings, func(d *pageData) {
			d.Flow = &deviceFlow{Err: "AADSTS7000218: la app no permite flujos de cliente público", Expires: now}
		})},
		{"ajustes-outlook-hecho", "ajustes.html", with(settings, func(d *pageData) {
			d.Flow = &deviceFlow{Done: true, Address: "olga@outlook.es", Expires: now}
		})},
	}

	dir := os.Getenv("AGENDA_PREVIEW_DIR")
	base := "/agenda"
	if dir != "" {
		base = "."
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base, d.Today = "v0.0.0-test", base, ymd(0)
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "stub ", "<no value>", "style=", "<script"} {
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
		err := fs.WalkDir(static, ".", func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			b, err := fs.ReadFile(static, path)
			if err != nil {
				return err
			}
			dst := filepath.Join(dir, "static", filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dst, b, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
