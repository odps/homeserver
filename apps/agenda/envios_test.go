package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/db"
	"homeserver/platform/dbtest"
	sealpkg "homeserver/platform/seal"
)

// sampleMail es un correo de ejemplo, como los reales.
type sampleMail struct {
	name, from, subject, body string
}

// El lunes 2 de noviembre de 2026 a las 10:00.
var monday = time.Date(2026, 11, 2, 10, 0, 0, 0, madrid)

var (
	mailCorreos = sampleMail{"Correos", "notificaciones@correos.es", "Tu envío PQ4C9H0712345670128001K está en camino",
		"Hola:\n\nTu envío de Zalando ya está en camino.\nFecha prevista de entrega: 05/11/2026.\n\n" +
			"Puedes seguirlo aquí: https://www.correos.es/es/es/herramientas/localizador/envios/detalle?tracking-number=PQ4C9H0712345670128001K\n\n" +
			"Si no estás en casa, te dejaremos un aviso. Una vez entregado, recibirás un correo.\nCorreos"}
	mailSEUR = sampleMail{"SEUR", "notificaciones@seur.com", "Tu envío sale hoy en reparto",
		"Hola Olga,\nTu pedido de PcComponentes sale hoy en reparto.\nNº de envío: 08123456789\n" +
			"Lo recibirás hoy entre las 9:00 y las 14:00.\nSigue tu envío en https://www.seur.com/livetracking/?segOnlineIdentificador=08123456789\n" +
			"Darse de baja: https://www.seur.com/baja?u=1"}
	mailDHL = sampleMail{"DHL", "noreply@dhl.com", "Su envío de MediaMarkt está en camino",
		"Estimado cliente:\nNúmero de seguimiento: JJD0003900123456789\nEntrega estimada: viernes 6 de noviembre\n" +
			"Seguimiento: https://www.dhl.com/es-es/home/seguimiento.html?tracking-id=JJD0003900123456789"}
	mailGLS = sampleMail{"GLS Spain", "noreply@gls-spain.es", "Incidencia en la entrega de tu envío",
		"No hemos podido entregar tu paquete de Decathlon porque no había nadie en el domicilio.\n" +
			"Número de expedición: 61234567890\nHaremos un nuevo intento mañana."}
	mailInPost = sampleMail{"InPost", "no-reply@inpost.es", "Tu paquete te espera en el Locker",
		"¡Hola!\nTu paquete de Shein ya está disponible para recoger en el InPost Locker CAL01M (Calle Mayor 5, Madrid).\n" +
			"Código de recogida: 123456\nNúmero de envío: 520113014230722029585646\nTienes 3 días para recogerlo."}
	// Amazon: el pedido, el envío (con el número de SEUR), el reparto de SEUR y la entrega.
	mailAmazonOrder = sampleMail{"Amazon.es", "confirmar-pedido@amazon.es", `Tu pedido de Amazon.es de "Auriculares inalámbricos..."`,
		"Hola Olga:\nGracias por tu pedido. Te enviaremos un correo cuando se hayan enviado los artículos.\n" +
			"Llegada: jueves, 5 de noviembre\nPedido nº 405-1234567-7654321\n" +
			"Ver o gestionar el pedido: https://www.amazon.es/gp/css/your-orders?ref=x"}
	mailAmazonShipped = sampleMail{"Amazon.es", "confirmar-envio@amazon.es", `Enviado: "Auriculares inalámbricos..."`,
		"Tu paquete se ha enviado con SEUR.\nLlegará el jueves.\nPedido nº 405-1234567-7654321\nNúmero de seguimiento: 08765432109\n" +
			"Sigue tu paquete: https://www.amazon.es/progress-tracker/package/ref=pe_shipconfirm?orderId=405-1234567-7654321&shipmentId=Abc123"}
	mailSEURAmazon = sampleMail{"SEUR", "notificaciones@seur.com", "Tu envío está en reparto",
		"Tu pedido de Amazon llega hoy.\nNº de envío: 08765432109\n"}
	mailAmazonDelivered = sampleMail{"Amazon.es", "confirmar-envio@amazon.es", `Entregado: "Auriculares inalámbricos..."`,
		"Tu paquete se ha entregado.\nPedido nº 405-1234567-7654321"}
)

func (s sampleMail) msg(at time.Time) message {
	return message{FromName: s.name, FromAddr: s.from, Subject: s.subject, Body: s.body, Date: at, Category: catNotice, Attachments: []string{}}
}

func TestDetectShipments(t *testing.T) {
	cases := []struct {
		mail                                                   sampleMail
		carrier, number, order, merchant, status, expect, link string
	}{
		{mailCorreos, "Correos", "PQ4C9H0712345670128001K", "", "Zalando", stShipped, "2026-11-05", "https://www.correos.es/"},
		{mailSEUR, "SEUR", "08123456789", "", "PcComponentes", stOut, "2026-11-02", "https://www.seur.com/livetracking/"},
		{mailDHL, "DHL", "JJD0003900123456789", "", "MediaMarkt", stShipped, "2026-11-06", "https://www.dhl.com/"},
		{mailGLS, "GLS", "61234567890", "", "Decathlon", stProblem, "2026-11-03", ""},
		{mailInPost, "InPost", "520113014230722029585646", "", "Shein", stPickup, "", ""},
		{mailAmazonOrder, "Amazon", "", "405-1234567-7654321", "Amazon", stOrdered, "2026-11-05", ""},
		{mailAmazonShipped, "SEUR", "08765432109", "405-1234567-7654321", "Amazon", stShipped, "2026-11-05", "https://www.amazon.es/progress-tracker/"},
		{mailSEURAmazon, "SEUR", "08765432109", "", "Amazon", stOut, "2026-11-02", ""},
		{mailAmazonDelivered, "Amazon", "", "405-1234567-7654321", "Amazon", stDelivered, "", ""},
		{sampleMail{"Tienda Pepa", "pedidos@tiendapepa.es", "Tu pedido 10234 ha sido enviado",
			"Hola, tu pedido ya va de camino con UPS. Tracking: 1Z999AA10123456784"}, "UPS", "1Z999AA10123456784", "10234", "Tienda Pepa", stShipped, "", ""},
		{sampleMail{"", "no-reply@mail.zalando.es", "Pedido confirmado", "Número de pedido: 10203040506"}, "", "", "10203040506", "Zalando", stOrdered, "", ""},
	}
	for _, c := range cases {
		d, ok := detectShipment(c.mail.msg(monday))
		if !ok {
			t.Errorf("%s / %q: no se detecta", c.mail.from, c.mail.subject)
			continue
		}
		exp := ""
		if !d.Expected.IsZero() {
			exp = d.Expected.Format("2006-01-02")
		}
		if d.Carrier != c.carrier || d.Number != c.number || d.Order != c.order || d.Merchant != c.merchant || d.Status != c.status ||
			exp != c.expect || !strings.HasPrefix(d.Link, c.link) || (c.link == "") != (d.Link == "") {
			t.Errorf("%s / %q:\n %q %q %q %q %q %q %q\nquiero\n %q %q %q %q %q %q %q…", c.mail.from, c.mail.subject,
				d.Carrier, d.Number, d.Order, d.Merchant, d.Status, exp, d.Link, c.carrier, c.number, c.order, c.merchant, c.status, c.expect, c.link)
		}
	}
	// No son envíos.
	for _, m := range []sampleMail{
		{"Tienda", "ofertas@tienda.es", "Envío gratis en todos los pedidos", "Solo este fin de semana. Pedidos desde 20 €."},
		{"Glovo", "pedidos@glovoapp.com", "Tu pedido está en camino", "Pedido nº 123456. Llega en 20 minutos."},
		{"Luis", "luis@example.com", "Fotos", "Te las he enviado por WhatsApp.\n\nEnviado desde mi iPhone"},
		{"Banco", "avisos@banco.es", "Tu recibo de noviembre", "Recibo nº 2026-11. Importe: 40 €."},
		{"Gestoría", "info@gestoria.es", "Factura del pedido 4567", "Adjuntamos la factura. El pedido se envió ayer."},
	} {
		if d, ok := detectShipment(m.msg(monday)); ok {
			t.Errorf("%s / %q: no es un envío, pero sale %+v", m.from, m.subject, d)
		}
	}
	// Lo que nombra un estado futuro no lo es.
	for _, c := range []struct{ subject, body, want string }{
		{"Tu pedido será entregado el jueves", "", ""},
		{`Entregado: "Libro"`, "", stDelivered},
		{"Aviso", "No hemos podido entregar tu paquete. Una vez entregado te avisaremos.", stProblem},
		{"Tu pedido", "Te avisaremos cuando haya sido enviado. Gracias por tu compra.", stOrdered},
		{"Out for delivery: your package", "", stOut},
	} {
		if got, _ := shipmentStatus(c.subject, c.body); got != c.want {
			t.Errorf("estado de %q / %q: %q, quiero %q", c.subject, c.body, got, c.want)
		}
	}
}

func TestDeliveryDate(t *testing.T) {
	cases := []struct {
		subject, body string
		sent          time.Time
		want          string
	}{
		{"", "Llegará el jueves 5 de noviembre", monday, "2026-11-05"},
		{"", "Entrega estimada: 05/11/2026", monday, "2026-11-05"},
		{"", "Entrega prevista: 2026-11-04", monday, "2026-11-04"},
		{"Arriving Thursday", "", monday, "2026-11-05"},
		{"", "Delivery date: November 5th", monday, "2026-11-05"},
		{"Tu paquete llega mañana", "", monday, "2026-11-03"},
		{"", "Llegará pasado mañana por la mañana", monday, "2026-11-04"},
		{"", "Llegará el jueves por la mañana", monday, "2026-11-05"},
		{"", "Llegará el lunes", monday, "2026-11-09"}, // el mismo día se diría «hoy»
		{"", "Llega hoy", monday, "2026-11-02"},
		{"", "Fecha de entrega: 5 dic.", time.Date(2026, 11, 30, 9, 0, 0, 0, madrid), "2026-12-05"},
		{"", "Llegará el 3 de enero", time.Date(2026, 12, 28, 9, 0, 0, 0, madrid), "2027-01-03"},
		{"", "Lo recibirás entre el 5 y el 7 de noviembre", monday, "2026-11-07"},
		{"", "Factura del 01/01/2020. Entrega estimada: 5 de noviembre", monday, "2026-11-05"},
		{"", "Llegará el 31/02", monday, ""},
		{"", "Pedido del 5-7 nov", monday, ""},
		{"", "Llegará el 05/11/2019", monday, ""}, // fuera de plazo
	}
	for _, c := range cases {
		got := ""
		if d := deliveryDate(c.subject, c.body, c.sent); !d.IsZero() {
			got = d.Format("2006-01-02")
		}
		if got != c.want {
			t.Errorf("%q %q: %q, quiero %q", c.subject, c.body, got, c.want)
		}
	}
}

func TestShipmentStatusOnlyMovesForward(t *testing.T) {
	var r shipmentRow
	h := func(n int) time.Time { return monday.Add(time.Duration(n) * time.Hour) }
	steps := []struct {
		status string
		at     time.Time
		want   string
	}{
		{stOut, h(1), stOut},
		{stShipped, h(2), stOut},         // un correo de «enviado» que llega tarde no lo devuelve atrás
		{stProblem, h(0), stOut},         // una incidencia más antigua que el reparto no cuenta
		{stProblem, h(3), stProblem},     // nuevo intento fallido
		{stOut, h(4), stOut},             // y se vuelve a repartir
		{stPickup, h(5), stPickup},       // al punto de recogida
		{stDelivered, h(6), stDelivered}, // recogido
		{stOut, h(7), stDelivered},       // entregado gana
		{stProblem, h(8), stDelivered},
	}
	for i, s := range steps {
		r.apply(shipmentInfo{Status: s.status, Text: s.status}, int64(i+1), s.at)
		if r.Status != s.want {
			t.Fatalf("paso %d (%s): %s, quiero %s", i, s.status, r.Status, s.want)
		}
	}
	if len(r.Messages) != len(steps) || !r.Updated.Equal(h(8)) {
		t.Fatalf("correos %v, actualizado %v", r.Messages, r.Updated)
	}
}

// shipmentsSetup da de alta a ana con una cuenta y le guarda los correos de
// ejemplo, como si llegaran por IMAP.
func shipmentsSetup(t *testing.T, svc *service, owner string, mails ...sampleMail) int64 {
	t.Helper()
	ctx := context.Background()
	if err := svc.store.ensureUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	acct, err := svc.store.addAccount(ctx, mailAccount{Owner: owner, Kind: "imap", Address: owner + "@example.com", Host: "imap.example.com",
		Port: 993, Username: owner, Secret: svc.mail.seal.seal("x")})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range mails {
		m := s.msg(monday.Add(time.Duration(i) * time.Hour))
		m.AccountID, m.UID = acct, int64(i+1)
		if m.ID, err = svc.store.saveMessage(ctx, m); err != nil || m.ID == 0 {
			t.Fatalf("guardar: %d %v", m.ID, err)
		}
		if err := svc.store.trackShipment(ctx, owner, m); err != nil {
			t.Fatal(err)
		}
	}
	return acct
}

func TestShipmentsStoredSealedAndJoined(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	shipmentsSetup(t, svc, "ana", mailAmazonOrder, mailCorreos, mailAmazonShipped, mailSEURAmazon, mailInPost, mailAmazonDelivered)

	// Los cuatro correos de Amazon y SEUR son un solo envío, ya entregado.
	all, err := svc.shipments(ctx, "ana", true)
	if err != nil || len(all) != 3 {
		t.Fatalf("envíos: %+v %v", all, err)
	}
	amazon := all[2]
	if amazon.Status != stDelivered || amazon.State != "entregado" || amazon.Carrier != "SEUR" || amazon.Merchant != "Amazon" ||
		amazon.Number != "08765432109" || amazon.Order != "405-1234567-7654321" || amazon.Expected != "2026-11-02" || len(amazon.Messages) != 4 ||
		!strings.Contains(amazon.Link, "progress-tracker") || !strings.HasPrefix(amazon.Text, "Entregado") {
		t.Fatalf("el de Amazon: %+v", amazon)
	}
	pending, _ := svc.shipments(ctx, "ana", false)
	if len(pending) != 2 || pending[0].Merchant != "Zalando" || pending[1].Merchant != "Shein" {
		t.Fatalf("pendientes (primero los que tienen fecha): %+v", pending)
	}
	// Un correo repetido no cuenta dos veces.
	rows, _ := svc.store.shipments(ctx, "ana", false)
	again := mailCorreos.msg(monday)
	again.ID, again.AccountID = pending[0].Messages[0], rows[0].AccountID
	if err := svc.store.trackShipment(ctx, "ana", again); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.shipments(ctx, "ana", false); len(l[0].Messages) != 1 {
		t.Fatalf("correo contado dos veces: %+v", l[0])
	}

	noPlain(t, svc.store.db, "shipments", "Zalando", "Amazon", "SEUR", "Correos", "InPost", "Shein", "PQ4C9H0712345670128001K",
		"08765432109", "405-1234567", "520113014230722029585646", "correos.es", "progress-tracker", "Auriculares")

	// Cada uno ve los suyos: el mismo número en otra cuenta es otro envío.
	shipmentsSetup(t, svc, "luis", mailSEURAmazon)
	if l, _ := svc.shipments(ctx, "luis", true); len(l) != 1 || l[0].Status != stOut || l[0].ID == amazon.ID {
		t.Fatalf("los de luis: %+v", l)
	}
	if _, err := svc.hideShipment(ctx, "luis", amazon.ID); !notFound(err) {
		t.Fatalf("luis quita un envío de ana: %v", err)
	}
	if l, _ := svc.shipments(ctx, "ana", true); len(l) != 3 {
		t.Fatalf("a ana le han quitado un envío: %+v", l)
	}

	// Quitar de la lista: no vuelve aunque llegue otro correo suyo.
	if v, err := svc.hideShipment(ctx, "ana", pending[1].ID); err != nil || v.Merchant != "Shein" {
		t.Fatalf("quitar: %+v %v", v, err)
	}
	m := mailInPost.msg(monday.Add(48 * time.Hour))
	m.AccountID, m.UID, m.Subject = rows[0].AccountID, 99, "Recuerda: tu paquete te espera en el Locker"
	m.ID, _ = svc.store.saveMessage(ctx, m)
	svc.store.trackShipment(ctx, "ana", m)
	if l, _ := svc.shipments(ctx, "ana", false); len(l) != 1 || l[0].Merchant != "Zalando" {
		t.Fatalf("tras quitar: %+v", l)
	}

	// Plazos: el entregado se va a los 30 días; el resto, a los 90.
	if err := svc.store.pruneShipments(ctx, monday.AddDate(0, 0, 35)); err != nil {
		t.Fatal(err)
	}
	if l, _ := svc.shipments(ctx, "ana", true); len(l) != 1 || l[0].Merchant != "Zalando" {
		t.Fatalf("a los 35 días: %+v", l)
	}
	svc.store.pruneShipments(ctx, monday.AddDate(0, 0, 95))
	var n int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM shipments`).Scan(&n)
	if n != 0 {
		t.Fatalf("a los 95 días quedan %d", n)
	}
}

func TestShipmentsSummaryAndTools(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	shipmentsSetup(t, svc, "ana", mailCorreos, mailSEUR, mailDHL, mailInPost, mailAmazonOrder, mailAmazonShipped, mailSEURAmazon, mailAmazonDelivered)

	// El jueves 5 llega el de Zalando y el de InPost espera; el de SEUR estuvo
	// en reparto el lunes (ya no cuenta) y el de DHL llega el viernes.
	thursday := time.Date(2026, 11, 5, 7, 45, 0, 0, madrid)
	text, err := svc.summary(ctx, "ana", thursday, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"📦 ENVÍOS\n• Hoy llega: Zalando (Correos)", "• Para recoger: Shein (InPost)"} {
		if !strings.Contains(text, want) {
			t.Errorf("falta %q en el resumen:\n%s", want, text)
		}
	}
	for _, not := range []string{"PcComponentes", "MediaMarkt", "Amazon"} {
		if strings.Contains(text, not) {
			t.Errorf("sobra %q en el resumen:\n%s", not, text)
		}
	}
	if lines := svc.shipmentLines(ctx, "ana", monday.Add(5*time.Hour)); strings.Join(lines, "|") != "📦 Hoy llega: PcComponentes (SEUR), en reparto|📦 Para recoger: Shein (InPost)" {
		t.Fatalf("el lunes: %q", lines)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "agenda", Version: "test"}, nil)
	addTools(server, svc)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer srv.Close()
	connect := func(user string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
		sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: asUser{user}}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}
	call := func(sess *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var out map[string]any
		json.Unmarshal(b, &out)
		return out, res.IsError
	}
	ana := connect("ana")
	out, isErr := call(ana, "envios_ver", nil)
	list, _ := out["envios"].([]any)
	if isErr || len(list) != 4 {
		t.Fatalf("envios_ver: %v", out)
	}
	first := list[0].(map[string]any) // el que antes llega: SEUR, hoy lunes
	for k, want := range map[string]string{"transportista": "SEUR", "numero": "08123456789", "tienda": "PcComponentes", "estado": "en reparto",
		"entrega_prevista": "2026-11-02", "enlace": "https://www.seur.com/livetracking/?segOnlineIdentificador=08123456789"} {
		if first[k] != want {
			t.Errorf("envios_ver %s: %v, quiero %q", k, first[k], want)
		}
	}
	if s, _ := first["actualizado"].(string); !strings.HasPrefix(s, "2026-11-02T11:00:00+01:00") {
		t.Errorf("actualizado: %v", first["actualizado"])
	}
	if out, _ := call(ana, "envios_ver", map[string]any{"incluir_entregados": true}); len(out["envios"].([]any)) != 5 {
		t.Fatalf("con los entregados: %v", out)
	}
	id := first["id"]
	if out, isErr := call(connect("luis"), "envios_borrar", map[string]any{"id": id}); !isErr {
		t.Fatalf("luis borra un envío de ana: %v", out)
	}
	if out, isErr := call(ana, "envios_borrar", map[string]any{"id": id}); isErr || out["tienda"] != "PcComponentes" {
		t.Fatalf("envios_borrar: %v", out)
	}
	if out, _ := call(ana, "envios_ver", nil); len(out["envios"].([]any)) != 3 {
		t.Fatalf("tras borrar: %v", out)
	}
	if out, _ := call(connect("luis"), "envios_ver", nil); len(out["envios"].([]any)) != 0 {
		t.Fatalf("luis ve envíos ajenos: %v", out)
	}
}

// asUser añade la identidad que pondría la pasarela.
type asUser struct{ user string }

func (u asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Auth-User", u.user)
	return http.DefaultTransport.RoundTrip(r)
}

// Al sincronizar se detectan los envíos, con el enlace del HTML (que el
// texto guardado pierde).
func TestSyncDetectsShipments(t *testing.T) {
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
	appendMail(t, admin, "INBOX", "From: Correos <notificaciones@correos.es>\nSubject: Tu paquete va en camino\nDate: "+now+
		"\nMessage-ID: <e1@x>\nMIME-Version: 1.0\nContent-Type: text/html; charset=utf-8\n\n"+
		`<html><body><p>Tu env&iacute;o de Zalando ya est&aacute; en camino (PQ4C9H0712345670128001K).</p>`+
		`<p><a href="https://www.correos.es/es/es/herramientas/localizador/envios/detalle?tracking-number=PQ4C9H0712345670128001K&amp;utm=x">Sigue tu env&iacute;o</a>`+
		` <a href="https://www.correos.es/baja">Baja</a></p></body></html>`+"\n")
	appendMail(t, admin, "INBOX", "From: Luis <luis@example.com>\nSubject: Cena\nDate: "+now+"\nMessage-ID: <e2@x>\n\nEl viernes.\n")
	a := mailAccount{Owner: "ana", Kind: "imap", Address: "ana@example.com", Host: host, Port: port, Username: "ana@example.com",
		Secret: svc.mail.seal.seal("clave-app")}
	if a.ID, err = svc.store.addAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.mail.syncAccount(ctx, a); err != nil || n != 2 {
		t.Fatalf("sincronizar: %d %v", n, err)
	}
	l, err := svc.shipments(ctx, "ana", false)
	if err != nil || len(l) != 1 {
		t.Fatalf("envíos: %+v %v", l, err)
	}
	if v := l[0]; v.Carrier != "Correos" || v.Merchant != "Zalando" || v.Status != stShipped ||
		v.Link != "https://www.correos.es/es/es/herramientas/localizador/envios/detalle?tracking-number=PQ4C9H0712345670128001K&utm=x" {
		t.Fatalf("envío: %+v", v)
	}
	m, err := svc.store.messageByID(ctx, "ana", l[0].Messages[0])
	if err != nil || len(m.Links) != 1 {
		t.Fatalf("enlaces del correo: %+v %v", m.Links, err)
	}
}

// El paso 004 busca envíos en el correo que ya estaba guardado.
func TestBackfillShipments(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	box, _ := sealpkg.Load("")
	upTo3 := fstest.MapFS{}
	all, _ := fs.Glob(migrations, "migrations/*.sql")
	for _, p := range all {
		if !strings.HasPrefix(p, "migrations/004") {
			b, _ := fs.ReadFile(migrations, p)
			upTo3[p] = &fstest.MapFile{Data: b}
		}
	}
	if err := db.MigrateSteps(ctx, pool, schema, upTo3, "migrations", map[int]db.Step{2: steps(box)[2]}); err != nil {
		t.Fatal(err)
	}
	st := store{pool, box}
	st.ensureUser(ctx, "ana")
	acct, err := st.addAccount(ctx, mailAccount{Owner: "ana", Kind: "imap", Address: "ana@example.com", Host: "h", Port: 993, Username: "ana", Secret: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []sampleMail{mailAmazonOrder, mailAmazonShipped, mailSEURAmazon, mailGLS, {"Luis", "luis@example.com", "Fotos", "Mira."}} {
		m := s.msg(monday.Add(time.Duration(i) * time.Hour))
		m.AccountID, m.UID = acct, int64(i+1)
		if _, err := st.saveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateSteps(ctx, pool, schema, migrations, "migrations", steps(box)); err != nil {
		t.Fatal(err)
	}
	svc := &service{store: st}
	l, err := svc.shipments(ctx, "ana", true)
	if err != nil || len(l) != 2 {
		t.Fatalf("envíos: %+v %v", l, err)
	}
	if l[0].Merchant != "Amazon" || l[0].Status != stOut || len(l[0].Messages) != 3 || l[1].Merchant != "Decathlon" || l[1].Status != stProblem {
		t.Fatalf("envíos: %+v", l)
	}
	noPlain(t, pool, "shipments", "Decathlon", "Amazon", "61234567890")
}
