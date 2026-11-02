package main

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"homeserver/platform/dbtest"
	sealpkg "homeserver/platform/seal"
)

func ics(lines ...string) string {
	return strings.Join(append(append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//test//ES"}, lines...), "END:VCALENDAR"), "\r\n") + "\r\n"
}

func mustCal(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := decodeCal(s)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, madrid)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRecurrence(t *testing.T) {
	// Cada lunes a las 9:00 (hora de Madrid), sin el 10 de noviembre y con el
	// 17 movido a las 11:00; aviso 15 minutos antes.
	cal := mustCal(t, ics(
		"BEGIN:VEVENT", "UID:serie", "DTSTAMP:20261101T000000Z", "SUMMARY:Reunión",
		"DTSTART;TZID=Europe/Madrid:20261102T090000", "DTEND;TZID=Europe/Madrid:20261102T100000",
		"RRULE:FREQ=WEEKLY;COUNT=5", "EXDATE;TZID=Europe/Madrid:20261109T090000",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT15M", "END:VALARM", "END:VEVENT",
		"BEGIN:VEVENT", "UID:serie", "DTSTAMP:20261101T000000Z", "SUMMARY:Reunión (movida)",
		"RECURRENCE-ID;TZID=Europe/Madrid:20261116T090000",
		"DTSTART;TZID=Europe/Madrid:20261116T110000", "DTEND;TZID=Europe/Madrid:20261116T120000", "END:VEVENT"))
	d, err := derive(cal)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Recurring || d.End == nil || !d.End.Equal(at("2026-11-30 10:00")) {
		t.Fatalf("derive: %+v", d)
	}
	occ := occurrences(cal, at("2026-11-01 00:00"), at("2026-12-31 00:00"))
	var got []string
	for _, o := range occ {
		got = append(got, o.Start.In(madrid).Format("02 15:04")+" "+text(o.Comp, ical.PropSummary))
	}
	want := "02 09:00 Reunión|16 11:00 Reunión (movida)|23 09:00 Reunión|30 09:00 Reunión"
	if strings.Join(got, "|") != want {
		t.Fatalf("veces:\n%s\nquería\n%s", strings.Join(got, "|"), want)
	}
	if n := nextAlarm(cal, at("2026-11-02 09:00")); !n.Equal(at("2026-11-16 10:45")) {
		// La del 9 no existe (EXDATE) y la del 16 va según la excepción.
		t.Fatalf("próximo aviso %v", n)
	}
	if f := firing(cal, at("2026-11-23 08:44"), at("2026-11-23 08:46")); len(f) != 1 {
		t.Fatalf("avisos que saltan: %d", len(f))
	}
	// Un evento de día completo de varios días cuenta en todos ellos.
	all := mustCal(t, ics("BEGIN:VEVENT", "UID:viaje", "DTSTAMP:20261101T000000Z", "SUMMARY:Viaje",
		"DTSTART;VALUE=DATE:20261105", "DTEND;VALUE=DATE:20261108", "END:VEVENT"))
	if o := occurrences(all, at("2026-11-07 00:00"), at("2026-11-08 00:00")); len(o) != 1 || !o[0].AllDay {
		t.Fatalf("día completo: %+v", o)
	}
	if o := occurrences(all, at("2026-11-08 00:00"), at("2026-11-09 00:00")); len(o) != 0 {
		t.Fatalf("el día siguiente al fin no cuenta: %+v", o)
	}
	if s := describeRule("FREQ=WEEKLY;BYDAY=MO,WE;COUNT=4"); s != "cada semana (lunes, miércoles), 4 veces" {
		t.Fatal(s)
	}
}

func newTestService(t *testing.T) *service {
	box, err := sealpkg.Load("")
	if err != nil {
		t.Fatal(err)
	}
	pool := dbtest.MigratedSteps(t, migrations, steps(box))
	st := store{pool, box}
	seal, err := loadSealer("")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	return &service{store: st, mail: newMailer(st, seal, log), log: log, now: time.Now}
}

func TestEventsTasksAndReminders(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if err := svc.store.ensureUser(ctx, "ana"); err != nil {
		t.Fatal(err)
	}
	now := at("2026-11-03 08:00")
	svc.now = func() time.Time { return now }

	start := at("2026-11-04 17:30")
	place := "Calle Mayor 3, Madrid"
	e, err := svc.create(ctx, "ana", "", ical.CompEvent, eventInput{Title: ptr("Dentista"), Start: &start, Location: &place})
	if err != nil {
		t.Fatal(err)
	}
	if e.End == nil || !e.End.Equal(at("2026-11-04 18:30")) || len(e.Alarms) != 1 || e.Alarms[0] != 15 {
		t.Fatalf("evento: %+v", e)
	}
	if !strings.Contains(e.Route, "destination=Calle+Mayor+3") {
		t.Fatalf("ruta: %s", e.Route)
	}
	due := at("2026-11-03 10:00")
	task, err := svc.create(ctx, "ana", "", ical.CompToDo, eventInput{Title: ptr("Llamar a Juan"), Due: &due, AllDay: ptr(false), Alarms: &[]int{0}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.agenda(ctx, "ana", at("2026-11-03 00:00"), at("2026-11-05 00:00"))
	if err != nil || len(items) != 2 || items[0].Title != "Llamar a Juan" || items[1].Title != "Dentista" {
		t.Fatalf("agenda: %+v %v", items, err)
	}

	// La tarea avisa a su hora; el evento, 15 minutos antes.
	now = at("2026-11-03 10:00")
	svc.fireAlarms(ctx)
	got, err := svc.store.takeNotices(ctx, "ana", now)
	if err != nil || len(got) != 1 {
		t.Fatalf("aviso de la tarea: %+v %v", got, err)
	}
	now = at("2026-11-04 17:15")
	svc.fireAlarms(ctx)
	svc.fireAlarms(ctx) // no repite
	more, err := svc.store.takeNotices(ctx, "ana", now)
	if got = append(got, more...); err != nil || len(got) != 2 {
		t.Fatalf("avisos: %+v %v", got, err)
	}
	if !strings.Contains(got[0].Text, "Llamar a Juan") || !strings.Contains(got[1].Text, "Hoy a las 17:30") || !strings.Contains(got[1].Text, "Cómo llegar") {
		t.Fatalf("textos: %q", got)
	}
	if again, _ := svc.store.takeNotices(ctx, "ana", now); len(again) != 0 {
		t.Fatalf("se entregan dos veces: %+v", again)
	}

	// Mover el evento conserva la duración que se le dé y recalcula el aviso.
	moved := at("2026-11-06 09:00")
	end := at("2026-11-06 09:30")
	if e, err = svc.update(ctx, "ana", e.ID, eventInput{Start: &moved, End: &end}); err != nil || e.Location != place {
		t.Fatalf("mover: %+v %v", e, err)
	}
	now = at("2026-11-06 08:45")
	svc.fireAlarms(ctx)
	if got, _ := svc.store.takeNotices(ctx, "ana", now); len(got) != 1 || !strings.Contains(got[0].Text, "09:00") {
		t.Fatalf("aviso del evento movido: %+v", got)
	}

	done := true
	if _, err := svc.update(ctx, "ana", task.ID, eventInput{Done: &done}); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.tasks(ctx, "ana"); len(l) != 0 {
		t.Fatalf("la tarea hecha sigue pendiente: %+v", l)
	}

	// Resumen del día con un cumpleaños.
	if _, err := svc.saveContact(ctx, "ana", 0, contactInput{Name: ptr("Luis Gómez"), Birthday: ptr("1990-11-06")}); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.summary(ctx, "ana", at("2026-11-06 07:45"), false)
	if err != nil || !strings.Contains(sum, "09:00–09:30 Dentista (Calle Mayor 3, Madrid)") || !strings.Contains(sum, "Luis Gómez (36 años)") {
		t.Fatalf("resumen:\n%s\n%v", sum, err)
	}
}

// gatewayLike imita a la pasarela: Basic con un token, cabeceras de
// identidad y el prefijo /agenda.
func gatewayLike(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || pass != "token-"+user {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		r.Header.Set("X-Auth-User", user)
		r.Header.Set("X-Auth-Role", "usuario")
		r.Header.Set("X-Forwarded-Prefix", "/agenda")
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/agenda")
		h.ServeHTTP(w, r)
	})
}

func TestDAV(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	srv := httptest.NewServer(gatewayLike(newDAV(svc.store)))
	defer srv.Close()
	hc := webdav.HTTPClientWithBasicAuth(nil, "ana", "token-ana")

	cc, err := caldav.NewClient(hc, srv.URL+"/agenda/dav/")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := cc.FindCurrentUserPrincipal(ctx)
	if err != nil || principal != "/agenda/dav/ana/" {
		t.Fatalf("principal %q %v", principal, err)
	}
	home, err := cc.FindCalendarHomeSet(ctx, principal)
	if err != nil || home != "/agenda/dav/ana/calendarios/" {
		t.Fatalf("calendarios %q %v", home, err)
	}
	cals, err := cc.FindCalendars(ctx, home)
	if err != nil || len(cals) != 1 || cals[0].Name != "Personal" {
		t.Fatalf("calendarios %+v %v", cals, err)
	}
	cal := mustCal(t, ics("BEGIN:VEVENT", "UID:movil-1", "DTSTAMP:20261101T000000Z", "SUMMARY:Pádel",
		"DTSTART;TZID=Europe/Madrid:20261105T190000", "DTEND;TZID=Europe/Madrid:20261105T203000", "LOCATION:Club",
		"RRULE:FREQ=WEEKLY", "END:VEVENT"))
	obj, err := cc.PutCalendarObject(ctx, cals[0].Path+"movil-1.ics", cal)
	if err != nil {
		t.Fatal(err)
	}
	if obj.ETag == "" {
		t.Fatal("sin etag")
	}
	q := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true}}},
		CompFilter:  caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT", Start: at("2026-11-19 00:00"), End: at("2026-11-20 00:00")}}},
	}
	found, err := cc.QueryCalendar(ctx, cals[0].Path, q)
	if err != nil || len(found) != 1 {
		t.Fatalf("consulta por fechas: %d %v", len(found), err)
	}
	// Lo creado desde el móvil se ve en la agenda (cada semana) y al revés.
	items, _ := svc.agenda(ctx, "ana", at("2026-11-05 00:00"), at("2026-11-20 00:00"))
	if len(items) != 3 || items[0].Location != "Club" || items[0].Repeat != "cada semana" {
		t.Fatalf("agenda: %+v", items)
	}
	start := at("2026-11-07 12:00")
	e, err := svc.create(ctx, "ana", "personal", ical.CompEvent, eventInput{Title: ptr("Comida"), Start: &start})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cc.GetCalendarObject(ctx, cals[0].Path+e.uidFile(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	if ev := got.Data.Events(); len(ev) != 1 || text(ev[0].Component, ical.PropSummary) != "Comida" {
		t.Fatalf("evento creado en la app: %+v", got.Data)
	}

	// Contactos por CardDAV: la cuenta sirve a los dos.
	kc, err := carddav.NewClient(hc, srv.URL+"/agenda/dav/")
	if err != nil {
		t.Fatal(err)
	}
	khome, err := kc.FindAddressBookHomeSet(ctx, principal)
	if err != nil || khome != "/agenda/dav/ana/contactos/" {
		t.Fatalf("libretas %q %v", khome, err)
	}
	books, err := kc.FindAddressBooks(ctx, khome)
	if err != nil || len(books) != 1 {
		t.Fatalf("libretas %+v %v", books, err)
	}
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldUID, "c-1")
	card.SetValue(vcard.FieldFormattedName, "Lucía Pérez")
	card.Add(vcard.FieldTelephone, &vcard.Field{Value: "+34 600 111 222", Params: vcard.Params{vcard.ParamType: {"cell"}}})
	card.AddAddress(&vcard.Address{Field: &vcard.Field{Params: vcard.Params{}}, StreetAddress: "Gran Vía 1", Locality: "Madrid", PostalCode: "28013"})
	if _, err := kc.PutAddressObject(ctx, books[0].Path+"c-1.vcf", card); err != nil {
		t.Fatal(err)
	}
	people, err := svc.contacts(ctx, "ana", "lucia", 10)
	if err != nil || len(people) != 1 || people[0].Phones[0].Label != "móvil" || people[0].Addresses[0].Value != "Gran Vía 1, 28013 Madrid" {
		t.Fatalf("contacto: %+v %v", people, err)
	}
	if people, _ := svc.contacts(ctx, "ana", "600111222", 10); len(people) != 1 {
		t.Fatal("no encuentra por teléfono sin espacios")
	}

	// Nadie ve lo de otro.
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/agenda/dav/ana/calendarios/", nil)
	req.SetBasicAuth("eva", "token-eva")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("otra cuenta: %v %v", resp.Status, err)
	}
	resp.Body.Close()
}

// uidFile es el fichero DAV de un evento creado en la app.
func (e entry) uidFile(t *testing.T, svc *service) string {
	o, err := svc.store.objectByID(context.Background(), "ana", e.ID)
	if err != nil {
		t.Fatal(err)
	}
	return o.Name
}

func TestConflicts(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	c, _ := svc.store.collection(ctx, "ana", "cal", "personal")
	cal := mustCal(t, ics("BEGIN:VEVENT", "UID:x", "DTSTAMP:20261101T000000Z", "SUMMARY:A", "DTSTART:20261105T090000Z", "END:VEVENT"))
	o, err := svc.store.putObject(ctx, c.ID, "x.ics", cal, "", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.putObject(ctx, c.ID, "x.ics", cal, "", true, time.Now()); err == nil {
		t.Fatal("If-None-Match: * no impide crear encima")
	}
	if _, err := svc.store.putObject(ctx, c.ID, "x.ics", cal, "otro", false, time.Now()); err == nil {
		t.Fatal("If-Match con otra etag no falla")
	}
	if _, err := svc.store.putObject(ctx, c.ID, "x.ics", cal, o.ETag, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.putObject(ctx, c.ID, "y.ics", cal, "", false, time.Now()); err == nil {
		t.Fatal("dos ficheros con el mismo UID")
	}
}

// ---- Correo ----

func imapServer(t *testing.T) (string, int, *imapmemserver.User) {
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("ana@example.com", "clave-app")
	user.Create("INBOX", nil)
	user.Create("Drafts", nil)
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, user
}

func appendMail(t *testing.T, c *imapclient.Client, mailbox, raw string, flags ...imap.Flag) {
	t.Helper()
	raw = strings.ReplaceAll(raw, "\n", "\r\n")
	cmd := c.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: time.Now()})
	io.WriteString(cmd, raw)
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestMailSyncAndDraft(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	host, port, _ := imapServer(t)
	svc.mail.dial = func(a mailAccount) (*imapclient.Client, error) {
		return imapclient.DialInsecure(net.JoinHostPort(a.Host, itoa(a.Port)), nil)
	}
	admin, err := imapclient.DialInsecure(net.JoinHostPort(host, itoa(port)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := admin.Login("ana@example.com", "clave-app").Wait(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC1123Z)
	appendMail(t, admin, "INBOX", "From: Gestoría López <gestoria@example.com>\nTo: ana@example.com\nSubject: Cita para la renta\nDate: "+now+
		"\nMessage-ID: <renta-1@example.com>\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=X\n\n--X\nContent-Type: text/html; charset=iso-8859-1\nContent-Transfer-Encoding: quoted-printable\n\n<p>Le esperamos el <b>jueves</b> a las 10:00.</p><p>Un saludo,<br>Gestor=EDa</p>\n--X\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=\"borrador.pdf\"\n\nJVBERi0=\n--X--\n")
	appendMail(t, admin, "INBOX", "From: luis@example.com\nTo: ana@example.com\nSubject: Fotos\nDate: "+now+"\nMessage-ID: <fotos@example.com>\n\nAhí van.\n", imap.FlagSeen)

	a := mailAccount{Owner: "ana", Kind: "imap", Address: "ana@example.com", Host: host, Port: port, Username: "ana@example.com",
		Secret: svc.mail.seal.seal("clave-app")}
	if err := svc.mail.testLogin(ctx, a); err != nil {
		t.Fatal(err)
	}
	bad := a
	bad.Secret = svc.mail.seal.seal("mala")
	if err := svc.mail.testLogin(ctx, bad); err == nil {
		t.Fatal("entra con una clave mala")
	}
	if a.ID, err = svc.store.addAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.mail.syncAccount(ctx, a); err != nil || n != 2 {
		t.Fatalf("sincronizar: %d %v", n, err)
	}
	unread, err := svc.store.searchMessages(ctx, "ana", mailQuery{UnreadOnly: true})
	if err != nil || len(unread) != 1 {
		t.Fatalf("sin leer: %+v %v", unread, err)
	}
	m := unread[0]
	if m.FromName != "Gestoría López" || !strings.Contains(m.Body, "Le esperamos el jueves a las 10:00.") || !strings.Contains(m.Body, "Gestoría") ||
		len(m.Attachments) != 1 || m.Attachments[0] != "borrador.pdf" {
		t.Fatalf("correo: %+v", m)
	}
	if l, _ := svc.store.searchMessages(ctx, "ana", mailQuery{Text: "gestoria renta"}); len(l) != 1 {
		t.Fatal("la búsqueda sin tildes no lo encuentra")
	}

	// Lo leído en el móvil y lo borrado se reflejan; lo nuevo llega.
	if _, err := admin.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := admin.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := admin.Store(imap.SeqSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := admin.Expunge().Close(); err != nil {
		t.Fatal(err)
	}
	appendMail(t, admin, "INBOX", "From: banco@example.com\nSubject: Recibo\nDate: "+now+"\n\nCargo de 10 euros.\n")
	accts, _ := svc.store.accounts(ctx, "ana")
	if n, err := svc.mail.syncAccount(ctx, accts[0]); err != nil || n != 1 {
		t.Fatalf("segunda sincronización: %d %v", n, err)
	}
	all, _ := svc.store.searchMessages(ctx, "ana", mailQuery{})
	if len(all) != 2 || all[0].Subject != "Recibo" && all[1].Subject != "Recibo" {
		t.Fatalf("tras borrar uno y llegar otro: %+v", all)
	}
	for _, x := range all {
		if x.Subject == "Cita para la renta" && !x.Seen {
			t.Fatal("no refleja que se ha leído")
		}
	}

	// Borrador de respuesta: en Borradores, en el hilo y sin enviar.
	folder, err := svc.mail.saveDraft(ctx, accts[0], draftInput{FromName: "Ana", To: "gestoria@example.com", Body: "Allí estaré.\nGracias.", ReplyTo: &m})
	if err != nil || folder != "Drafts" {
		t.Fatalf("borrador: %q %v", folder, err)
	}
	if _, err := admin.Select("Drafts", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	msgs, err := admin.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true, BodySection: []*imap.FetchItemBodySection{{}}}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("borradores: %v", err)
	}
	raw := string(msgs[0].BodySection[0].Bytes)
	for _, want := range []string{"In-Reply-To: <renta-1@example.com>", "Subject: Re: Cita para la renta", "Allí estaré."} {
		if !strings.Contains(raw, want) && !strings.Contains(decodeQP(raw), want) {
			t.Fatalf("falta %q en el borrador:\n%s", want, raw)
		}
	}
	if !contains(msgs[0].Flags, imap.FlagDraft) {
		t.Fatalf("sin marca de borrador: %v", msgs[0].Flags)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decodeQP(s string) string {
	_, body, _ := strings.Cut(s, string([]byte{13, 10, 13, 10}))
	b, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	return string(b)
}

func contains(flags []imap.Flag, f imap.Flag) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func TestHelpers(t *testing.T) {
	if got := htmlToText("<style>x{}</style><div>Hola&nbsp;<b>Ana</b></div><p>Adiós</p>"); got != "Hola Ana\nAdiós\n" && strings.TrimSpace(got) != "Hola Ana\nAdiós" {
		t.Fatalf("%q", got)
	}
	tok := "x." + b64(`{"email":"Ana@Outlook.com"}`) + ".y"
	if idTokenEmail(tok) != "ana@outlook.com" {
		t.Fatal(idTokenEmail(tok))
	}
	for in, want := range map[string]string{"--0517": "--05-17", "19900517": "1990-05-17", "1990-05-17T00:00:00Z": "1990-05-17", "x": ""} {
		if got := normalBirthday(in); got != want {
			t.Fatalf("%s → %q", in, got)
		}
	}
	if s, ok := parseWhenOK("2026-11-05 17:30"); !ok || s != "2026-11-05T17:30:00+01:00" {
		t.Fatal(s)
	}
}

func parseWhenOK(s string) (string, bool) {
	t, _, err := parseWhen(s)
	return t.Format(time.RFC3339), err == nil
}
