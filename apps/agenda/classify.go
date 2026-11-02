package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/textproto"
	"regexp"
	"strings"

	sealpkg "homeserver/platform/seal"
)

// Clasificación del correo por sus metadatos (cabeceras, remitente y asunto),
// sin IA: decide qué merece gastar tokens. Solo «personal» e «importante»
// pasan por la IA sin que la persona lo pida.
//
//   - personal: lo ha escrito una persona (sin marcas de envío masivo ni
//     automático).
//   - importante: masivo o automático, pero de los que conviene leer:
//     seguridad, cambios de condiciones, pagos, citas, la cuenta, trámites.
//   - notificacion: automático sin más (avisos de apps, códigos, envíos).
//   - comercial: boletines y publicidad (con baja de la lista o de una
//     plataforma de envíos masivos).
//
// La persona puede fijar remitentes como importantes o a ignorar (ajustes).

const (
	catPersonal  = "personal"
	catImportant = "importante"
	catNotice    = "notificacion"
	catBulk      = "comercial"
)

// toAI dice si un correo de esa categoría va a la IA sin pedirlo.
func toAI(category string) bool { return category == catPersonal || category == catImportant }

var allCategories = []string{catPersonal, catImportant, catNotice, catBulk}

// mailSignals son las marcas de las cabeceras.
type mailSignals struct {
	Bulk bool // boletín o publicidad: baja de la lista, Precedence: bulk, plataforma de envíos
	Auto bool // automático: no-reply, Auto-Submitted, lista de distribución…
}

var (
	autoLocal  = regexp.MustCompile(`^(no-?reply|do-?not-?reply|donotreply|noresponder|no-?responder|notifications?|notificacion(es)?|alerts?|alertas?|avisos?|news|newsletters?|boletin(es)?|marketing|promo(cion(es)?)?|ofertas|mailer-daemon|postmaster|bounces?|automated|automatico|security|seguridad|billing|facturacion|invoices?|receipts?|orders?|pedidos|account(s)?|updates?|digest|calendar-notification)([.+_-].*)?$`)
	bulkMailer = regexp.MustCompile(`(?i)mailchimp|sendinblue|brevo|hubspot|salesforce|marketo|klaviyo|mailjet|acumbamail|mailrelay|campaign|newsletter|acoustic|emarsys|braze|iterable|customer\.io`)
)

// headerSignals lee las marcas de envío masivo o automático de las cabeceras
// del correo en bruto.
func headerSignals(raw []byte, from string) mailSignals {
	var s mailSignals
	if local, _, ok := strings.Cut(strings.ToLower(from), "@"); ok && autoLocal.MatchString(local) {
		s.Auto = true
	}
	if len(raw) == 0 {
		return s
	}
	h, _ := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw))).ReadMIMEHeader()
	get := func(k string) string { return strings.ToLower(strings.TrimSpace(h.Get(k))) }
	if v := get("Auto-Submitted"); v != "" && v != "no" {
		s.Auto = true
	}
	switch get("Precedence") {
	case "bulk", "junk":
		s.Bulk = true
	case "list":
		s.Auto = true
	}
	if get("List-Id") != "" || get("X-Auto-Response-Suppress") != "" || get("Feedback-Id") != "" || get("Return-Path") == "<>" {
		s.Auto = true
	}
	if get("List-Unsubscribe") != "" || bulkMailer.MatchString(get("X-Mailer")) {
		s.Bulk = true
	}
	for k := range h {
		k = strings.ToLower(k)
		if strings.HasPrefix(k, "x-campaign") || strings.HasPrefix(k, "x-mailchimp") || k == "x-mc-user" ||
			k == "x-sg-eid" || k == "x-csa-complaints" || strings.HasPrefix(k, "x-marketing") || k == "x-mailgun-tag" {
			s.Bulk = true
		}
	}
	return s
}

// importantTopics son los asuntos que merecen leerse aunque lleguen de una
// lista o de una máquina. Cada clave se busca al principio de una palabra del
// asunto (sin tildes ni mayúsculas).
var importantTopics = []struct {
	reason string
	keys   []string
}{
	{"seguridad", []string{"alerta de seguridad", "aviso de seguridad", "security alert", "security notice", "seguridad de tu cuenta",
		"seguridad de su cuenta", "inicio de sesion", "nuevo inicio", "sign in", "signin", "new login", "new sign", "login attempt",
		"intento de acceso", "intento de inicio", "nuevo dispositivo", "new device", "contrasena", "password", "verificacion en dos",
		"two factor", "2fa", "actividad sospechosa", "actividad inusual", "suspicious", "unusual", "cuenta bloqueada",
		"account locked", "comprometid", "compromised", "brecha", "breach", "vulnerab", "phishing", "fraude", "fraud"}},
	{"condiciones", []string{"terminos", "condiciones", "terms of", "terms and", "updated terms", "updates to our terms",
		"politica de privacidad", "privacy policy", "privacy notice", "aviso de privacidad", "aviso legal", "acuerdo de usuario",
		"user agreement", "contrato", "cambio de precio", "subida de precio", "price change", "price increase"}},
	{"pagos", []string{"factura", "invoice", "recibo", "receipt", "pago", "payment", "cargo", "cobro", "reembolso", "refund",
		"renovacion", "renewal", "se renovara", "will renew", "suscripcion", "subscription", "vencimiento", "vence", "caduca",
		"expira", "expire", "impago", "deuda", "domiciliacion", "transferencia", "devolucion"}},
	{"citas", []string{"cita", "appointment", "reserva", "booking", "reservation", "convocatoria", "invitacion", "invitation",
		"cancelad", "cancelacion", "cancelled", "canceled", "aplazad", "reprogramad", "rescheduled"}},
	{"cuenta", []string{"suspend", "desactiv", "deactivat", "cierre de", "account closure", "eliminacion de", "sera eliminad",
		"will be deleted", "datos personales", "personal data", "acceso a tu cuenta", "access to your account", "action required",
		"accion requerida", "se requiere", "urgente", "urgent"}},
	{"trámites", []string{"agencia tributaria", "hacienda", "aeat", "seguridad social", "dgt", "notificacion electronica",
		"notificaciones electronicas", "sede electronica", "multa", "sancion", "requerimiento", "embargo", "ayuntamiento", "juzgado",
		"tribunal", "carpeta ciudadana", "dehu", "declaracion de la renta", "campana de la renta"}},
}

// importantReason devuelve por qué un asunto es importante ("" si no lo es).
func importantReason(subject string) string {
	text := " " + strings.Join(sealpkg.Words(subject), " ")
	for _, t := range importantTopics {
		for _, k := range t.keys {
			if strings.Contains(text, " "+k) {
				return t.reason
			}
		}
	}
	return ""
}

// senderIn dice si la dirección está en la lista: direcciones completas o
// dominios ("empresa.es" o "@empresa.es", que incluye sus subdominios).
func senderIn(addr string, list []string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	_, domain, _ := strings.Cut(addr, "@")
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case strings.Contains(strings.TrimPrefix(e, "@"), "@"):
			if addr == e {
				return true
			}
		default:
			d := strings.TrimPrefix(e, "@")
			if domain == d || strings.HasSuffix(domain, "."+d) {
				return true
			}
		}
	}
	return false
}

// classify pone la categoría y el motivo de un correo.
func classify(m *message, p prefs) {
	switch {
	case senderIn(m.FromAddr, p.MailIgnore):
		m.Category, m.Reason = catBulk, "remitente que has marcado para ignorar"
	case senderIn(m.FromAddr, p.MailImportant):
		m.Category, m.Reason = catImportant, "remitente que has marcado como importante"
	case !m.Signals.Bulk && !m.Signals.Auto:
		m.Category, m.Reason = catPersonal, ""
	default:
		if r := importantReason(m.Subject); r != "" {
			m.Category, m.Reason = catImportant, r
		} else if m.Signals.Bulk {
			m.Category, m.Reason = catBulk, ""
		} else {
			m.Category, m.Reason = catNotice, ""
		}
	}
}

// categoryName es el nombre para la persona.
func categoryName(c string) string {
	switch c {
	case catImportant:
		return "Importante"
	case catNotice:
		return "Notificación"
	case catBulk:
		return "Comercial"
	}
	return "Personal"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
