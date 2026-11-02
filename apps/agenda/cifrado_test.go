package main

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"testing"
	"testing/fstest"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/db"
	"homeserver/platform/dbtest"
	sealpkg "homeserver/platform/seal"
)

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	if err := pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, s := range texts {
		if bytes.Contains([]byte(dump), []byte(s)) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}

// Lo guardado antes del cifrado (migración 001) se cifra al actualizar y se
// sigue encontrando.
func TestSealExistingData(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	only001 := fstest.MapFS{}
	all, _ := fs.Glob(migrations, "migrations/*.sql")
	for _, p := range all {
		if b, _ := fs.ReadFile(migrations, p); p == "migrations/001_agenda.sql" {
			only001[p] = &fstest.MapFile{Data: b}
		}
	}
	if err := db.Migrate(ctx, pool, schema, only001, "migrations"); err != nil {
		t.Fatal(err)
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//ES\r\nBEGIN:VEVENT\r\nUID:den-1\r\nDTSTAMP:20261101T000000Z\r\n" +
		"DTSTART:20261105T160000Z\r\nDTEND:20261105T170000Z\r\nSUMMARY:Dentista Ortiz\r\nLOCATION:Calle Pez 3\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c-1\r\nFN:Lucía Pérez\r\nTEL:+34 600 111 222\r\nBDAY:1990-11-05\r\nEND:VCARD\r\n"
	for _, q := range []string{
		`INSERT INTO users (username, prefs) VALUES ('ana', '{"home": "Calle Mayor 3", "summary_on": true}')`,
		`INSERT INTO collections (owner, kind, slug, name) VALUES ('ana', 'cal', 'personal', 'Personal'), ('ana', 'card', 'contactos', 'Contactos')`,
		`INSERT INTO objects (collection_id, name, uid, comp, data, etag, summary, location, starts_at, ends_at, search)
			SELECT id, 'den-1.ics', 'den-1', 'VEVENT', '` + ics + `', 'e1', 'Dentista Ortiz', 'Calle Pez 3',
			'2026-11-05 16:00Z', '2026-11-05 17:00Z', 'dentista ortiz calle pez 3' FROM collections WHERE kind = 'cal'`,
		`INSERT INTO contacts (collection_id, name, uid, data, etag, fn, birthday, search)
			SELECT id, 'c-1.vcf', 'c-1', '` + vcf + `', 'e2', 'Lucía Pérez', '1990-11-05', 'lucia perez +34 600 111 222 34600111222'
			FROM collections WHERE kind = 'card'`,
		`INSERT INTO notices (owner, kind, text) VALUES ('ana', 'aviso', 'Recordatorio: Dentista Ortiz')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	box, _ := sealpkg.Load("")
	if err := db.MigrateSteps(ctx, pool, schema, migrations, "migrations", steps(box)); err != nil {
		t.Fatal(err)
	}
	st := store{pool, box}
	if p, err := st.prefs(ctx, "ana"); err != nil || p.Home != "Calle Mayor 3" || !p.SummaryOn {
		t.Fatalf("ajustes: %+v %v", p, err)
	}
	if l, err := st.searchObjects(ctx, "ana", "dentis", 10); err != nil || len(l) != 1 || !bytes.Contains([]byte(l[0].Data), []byte("Calle Pez 3")) {
		t.Fatalf("evento: %+v %v", l, err)
	}
	if l, err := st.searchCards(ctx, "ana", "lucia", 10); err != nil || len(l) != 1 {
		t.Fatalf("contacto: %+v %v", l, err)
	}
	if l, err := st.searchCards(ctx, "ana", "", 10); err != nil || len(l) != 1 {
		t.Fatalf("todos los contactos: %+v %v", l, err)
	}
	if l, err := st.birthdays(ctx, "ana", "11-05"); err != nil || len(l) != 1 {
		t.Fatalf("cumpleaños: %+v %v", l, err)
	}
	if n, err := st.takeNotices(ctx, "ana", time.Now()); err != nil || len(n) != 1 || n[0].Text != "Recordatorio: Dentista Ortiz" {
		t.Fatalf("avisos: %+v %v", n, err)
	}
	noPlain(t, pool, "objects", "Dentista", "Pez")
	noPlain(t, pool, "contacts", "Lucía", "Lucia", "600 111 222", "34600111222")
	noPlain(t, pool, "users", "Mayor")
	noPlain(t, pool, "notices", "Dentista")
}

func TestHeaderSignalsAndClassify(t *testing.T) {
	p := defaultPrefs()
	cases := []struct {
		raw, from, subject, want, reason string
	}{
		{"From: luis@example.com\r\nSubject: Fotos\r\n\r\nhola", "luis@example.com", "Fotos", catPersonal, ""},
		{"List-Unsubscribe: <mailto:baja@tienda.es>\r\n\r\n", "ofertas@tienda.es", "50% en todo", catBulk, ""},
		{"List-Unsubscribe: <https://x>\r\nPrecedence: bulk\r\n\r\n", "news@app.com", "Actualización de nuestros Términos de servicio", catImportant, "condiciones"},
		{"\r\n", "no-reply@accounts.google.com", "Alerta de seguridad", catImportant, "seguridad"},
		{"Auto-Submitted: auto-generated\r\n\r\n", "robot@red.com", "A Juan le gusta tu foto", catNotice, ""},
		{"X-Mailer: Mailchimp Mailer\r\n\r\n", "hola@club.es", "Boletín de noviembre", catBulk, ""},
		{"\r\n", "notificaciones@banco.es", "Tu recibo de noviembre", catImportant, "pagos"},
		{"\r\n", "noreply@apagon.es", "Apagón previsto", catNotice, ""}, // «pago» solo al principio de palabra
	}
	for _, c := range cases {
		m := message{FromAddr: c.from, Subject: c.subject, Signals: headerSignals([]byte(c.raw), c.from)}
		classify(&m, p)
		if m.Category != c.want || m.Reason != c.reason {
			t.Errorf("%s / %q: %s (%s), quiero %s (%s)", c.from, c.subject, m.Category, m.Reason, c.want, c.reason)
		}
	}
	p.MailImportant, p.MailIgnore = []string{"club.es"}, []string{"luis@example.com"}
	m := message{FromAddr: "hola@socios.club.es", Signals: mailSignals{Bulk: true}}
	if classify(&m, p); m.Category != catImportant {
		t.Errorf("dominio importante: %s", m.Category)
	}
	m = message{FromAddr: "LUIS@example.com"}
	if classify(&m, p); m.Category != catBulk {
		t.Errorf("remitente ignorado: %s", m.Category)
	}
}

func TestMailClassifiedAndSealed(t *testing.T) {
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
	appendMail(t, admin, "INBOX", "From: Luis <luis@example.com>\nSubject: Cena el viernes\nDate: "+now+"\nMessage-ID: <a@x>\n\nReservo en Casa Pepe.\n")
	appendMail(t, admin, "INBOX", "From: Tienda <ofertas@tienda.es>\nSubject: Rebajas secretas\nList-Unsubscribe: <mailto:b@tienda.es>\nDate: "+now+"\nMessage-ID: <b@x>\n\nSolo hoy.\n")
	appendMail(t, admin, "INBOX", "From: Google <no-reply@accounts.google.com>\nSubject: Alerta de seguridad\nDate: "+now+"\nMessage-ID: <c@x>\n\nNuevo inicio de sesión.\n")
	a := mailAccount{Owner: "ana", Kind: "imap", Address: "ana@example.com", Host: host, Port: port, Username: "ana@example.com",
		Secret: svc.mail.seal.seal("clave-app")}
	if a.ID, err = svc.store.addAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.mail.syncAccount(ctx, a); err != nil || n != 3 {
		t.Fatalf("sincronizar: %d %v", n, err)
	}
	// La siguiente vuelta solo trae lo nuevo (la cuenta recuerda hasta dónde llegó).
	stored, err := svc.store.account(ctx, "ana", a.ID)
	if err != nil || stored.LastUID == 0 || stored.Error != "" {
		t.Fatalf("la cuenta no guarda hasta dónde ha leído: %+v %v", stored, err)
	}
	if n, err := svc.mail.syncAccount(ctx, stored); err != nil || n != 0 {
		t.Fatalf("segunda vuelta: %d %v", n, err)
	}
	q := mailQuery{UnreadOnly: true, Categories: []string{catPersonal, catImportant}}
	l, err := svc.store.searchMessages(ctx, "ana", q)
	if err != nil || len(l) != 2 {
		t.Fatalf("personal e importante: %+v %v", l, err)
	}
	counts, _ := svc.store.countMessages(ctx, "ana", q)
	if counts[catBulk] != 1 || counts[catPersonal] != 1 || counts[catImportant] != 1 {
		t.Fatalf("recuento: %v", counts)
	}
	if l, _ := svc.store.searchMessages(ctx, "ana", mailQuery{From: "luis"}); len(l) != 1 || l[0].Subject != "Cena el viernes" {
		t.Fatalf("por remitente: %+v", l)
	}
	if l, _ := svc.store.searchMessages(ctx, "ana", mailQuery{Text: "casa pep"}); len(l) != 1 {
		t.Fatalf("por el texto: %+v", l)
	}
	noPlain(t, svc.store.db, "messages", "Casa Pepe", "luis", "Rebajas", "Alerta")

	// El resumen solo nombra lo personal e importante; lo comercial, contado.
	text, err := svc.summary(ctx, "ana", time.Now(), false)
	if err != nil || !bytes.Contains([]byte(text), []byte("Cena el viernes")) || bytes.Contains([]byte(text), []byte("Rebajas")) ||
		!bytes.Contains([]byte(text), []byte("1 comercial")) {
		t.Fatalf("resumen:\n%s\n%v", text, err)
	}

	// Ignorar a Luis lo reclasifica.
	if err := svc.markSender(ctx, "ana", "luis@example.com", "ignorar"); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.store.searchMessages(ctx, "ana", q); len(l) != 1 || l[0].Category != catImportant {
		t.Fatalf("tras ignorar: %+v", l)
	}
	if err := svc.setMailRules(ctx, "ana", []string{"no es un dominio"}, nil); err == nil {
		t.Fatal("acepta una regla que no es dirección ni dominio")
	}
}
