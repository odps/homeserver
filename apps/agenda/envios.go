package main

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	sealpkg "homeserver/platform/seal"
)

// Envíos: los correos de tiendas y transportistas se leen con código (sin IA
// y sin consultar las webs de las empresas) para saber qué paquetes vienen
// de camino: transportista, número de seguimiento, tienda, estado, entrega
// prevista y el enlace de seguimiento que traiga el propio correo. Un envío
// junta todos sus correos (por el número de seguimiento o el del pedido) y su
// estado solo avanza. Los entregados se borran a los 30 días; los demás, a
// los 90.

const (
	sealShipment = "agenda/shipments"

	keepDelivered = 30 // días
	keepShipments = 90
)

// Estados, de menos a más avanzado.
const (
	stOrdered   = "pedido"
	stShipped   = "enviado"
	stOut       = "reparto"
	stProblem   = "incidencia"
	stPickup    = "recogida"
	stDelivered = "entregado"
)

// statusRank ordena los estados: uno nuevo solo cuenta si es igual o más
// avanzado. Reparto e incidencia van a la par (tras un intento fallido se
// vuelve a repartir), y manda el correo más reciente.
var statusRank = map[string]int{stOrdered: 1, stShipped: 2, stOut: 3, stProblem: 3, stPickup: 4, stDelivered: 5}

var statusNames = map[string]string{stOrdered: "pedido confirmado", stShipped: "enviado", stOut: "en reparto",
	stProblem: "incidencia", stPickup: "disponible para recoger", stDelivered: "entregado"}

// ---- Detección ----

type carrierDef struct {
	Name   string
	Labels []string       // partes del dominio del remitente o del enlace (correos en notificaciones.correos.es)
	Word   *regexp.Regexp // nombre en el texto del correo
}

// carriers: el orden decide los empates (Correos Express antes que Correos).
var carriers = []carrierDef{
	{"Correos Express", []string{"correosexpress"}, regexp.MustCompile(`\bCorreos ?Express\b|(?i:\bcorreos ?express\b)`)},
	{"Correos", []string{"correos"}, regexp.MustCompile(`\bCorreos\b|\bCORREOS\b`)}, // «correos» en minúscula es «emails»
	{"SEUR", []string{"seur"}, regexp.MustCompile(`(?i)\bseur\b`)},
	{"MRW", []string{"mrw"}, regexp.MustCompile(`(?i)\bmrw\b`)},
	{"GLS", []string{"gls-spain", "gls-group", "gls-es", "gls"}, regexp.MustCompile(`\bGLS\b`)},
	{"DHL", []string{"dhl", "dhlparcel"}, regexp.MustCompile(`(?i)\bdhl\b`)},
	{"UPS", []string{"ups"}, regexp.MustCompile(`\bUPS\b`)}, // «ups» es también una interjección
	{"FedEx", []string{"fedex"}, regexp.MustCompile(`(?i)\bfedex\b`)},
	{"TNT", []string{"tnt"}, regexp.MustCompile(`\bTNT\b`)},
	{"Nacex", []string{"nacex"}, regexp.MustCompile(`(?i)\bnacex\b`)},
	{"InPost", []string{"inpost"}, regexp.MustCompile(`(?i)\binpost\b`)},
	{"CTT", []string{"ctt", "cttexpress"}, regexp.MustCompile(`\bCTT\b`)},
	{"Zeleris", []string{"zeleris"}, regexp.MustCompile(`(?i)\bzeleris\b`)},
	{"Envialia", []string{"envialia"}, regexp.MustCompile(`(?i)\benvialia\b`)},
	{"Paack", []string{"paack"}, regexp.MustCompile(`(?i)\bpaack\b`)},
	{"Celeritas", []string{"celeritas", "celeritastransporte"}, regexp.MustCompile(`(?i)\bceleritas\b`)},
	{"Amazon", []string{"amazon"}, regexp.MustCompile(`(?i)\bamazon logistics\b`)},
}

func carrierNamed(name string) *carrierDef {
	for i := range carriers {
		if carriers[i].Name == name {
			return &carriers[i]
		}
	}
	return nil
}

// noShipments son remitentes de comida a domicilio: sus «pedidos» no son
// paquetes.
var noShipments = []string{"glovoapp", "glovo", "just-eat", "justeat", "uber", "ubereats", "deliveroo", "telepizza", "dominos", "dominospizza"}

// domainLabels son las partes de un dominio sin la terminación (es, com,
// co.uk…).
func domainLabels(domain string) []string {
	l := strings.Split(strings.ToLower(strings.Trim(domain, ".")), ".")
	if len(l) < 2 {
		return nil
	}
	l = l[:len(l)-1]
	if n := len(l); n >= 2 && slices.Contains([]string{"co", "com", "org", "net", "gob", "edu"}, l[n-1]) {
		l = l[:n-1]
	}
	return l
}

// baseLabel es el nombre del dominio: «mail.zalando.es» → «zalando».
func baseLabel(domain string) string {
	l := domainLabels(domain)
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1]
}

func carrierByDomain(domain string) *carrierDef {
	labels := domainLabels(domain)
	for i := range carriers {
		for _, want := range carriers[i].Labels {
			if slices.Contains(labels, want) {
				return &carriers[i]
			}
		}
	}
	return nil
}

func domainOf(addr string) string {
	_, d, _ := strings.Cut(strings.ToLower(strings.TrimSpace(addr)), "@")
	return d
}

// carrierInText es el transportista que antes se nombra en el texto.
func carrierInText(text string) string {
	best, at := "", -1
	for _, c := range carriers {
		if loc := c.Word.FindStringIndex(text); loc != nil && (at < 0 || loc[0] < at) {
			best, at = c.Name, loc[0]
		}
	}
	return best
}

// numberFormats son los números de seguimiento con un formato propio, que se
// reconocen sin etiqueta. only: solo si el transportista ya se sabe.
var numberFormats = []struct {
	carrier   string // "" según el sufijo (UPU)
	only      string
	re        *regexp.Regexp
	minDigits int
}{
	{"UPS", "", regexp.MustCompile(`\b1Z[0-9A-Z]{16}\b`), 6},
	{"Amazon", "", regexp.MustCompile(`\bTBA\d{12}\b`), 12},
	{"DHL", "", regexp.MustCompile(`\b(?:JJD\d{16,20}|JVGL\d{8,16})\b`), 8},
	{"Correos", "", regexp.MustCompile(`\bP[A-Z0-9]{20,22}\b`), 10}, // paquetería nacional: PQ…, PK…
	{"", "", regexp.MustCompile(`\b[A-Z]{2}\d{9}[A-Z]{2}\b`), 9},    // UPU S10: RR123456789ES
	{"InPost", "InPost", regexp.MustCompile(`\b\d{24}\b`), 24},
}

// s10Carrier: el país del número internacional dice quién lo reparte aquí.
func s10Carrier(n string) string {
	switch n[len(n)-2:] {
	case "ES":
		return "Correos"
	case "PT":
		return "CTT"
	case "WW":
		return "TNT"
	}
	return ""
}

// labeledNumber: el número que sigue a «número de seguimiento», «nº de
// envío», «localizador», «tracking number»… (sobre el texto plegado).
var labeledNumber = regexp.MustCompile(`\b(?:(?:numero|num\.?|n[o°º]\.?|codigo|referencia|id|identificador) (?:de |del )?(?:seguimiento|envio|expedicion|tracking|bulto|paquete|albaran)|localizador(?: del envio| de envio)?|tracking(?: number| no\.?| id| code| #)?|seguimiento(?: del envio| de envio)?|expedicion|albaran|envio (?:n[o°º]\.?|numero)|shipment (?:number|id)|waybill(?: number| no\.?)?|awb)\s*(?:\([^)\n]{0,20}\))?\s*(?:n[o°º]\.?)?\s*[:#.-]?\s*([a-z0-9][a-z0-9/-]{5,34}[a-z0-9])\b`)

var (
	amazonOrder  = regexp.MustCompile(`\b\d{3}-\d{7}-\d{7}\b`)
	labeledOrder = regexp.MustCompile(`(?:numero de pedido|n[o°º]\.? de pedido|pedido n[o°º]\.?|pedido numero|referencia del pedido|referencia de pedido|pedido|order number|order no\.?|order)\s*[:#]?\s*#?\s*([a-z0-9][a-z0-9-]{2,24}[a-z0-9])\b`)
	dateLike     = regexp.MustCompile(`^\d{1,4}[/.-]\d{1,2}[/.-]\d{1,4}$`)
)

func digitCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// trackingNumber busca el número de seguimiento y, si lo delata su formato,
// el transportista.
func trackingNumber(text, folded, carrier string) (string, string) {
	for _, f := range numberFormats {
		if f.only != "" && f.only != carrier {
			continue
		}
		for _, n := range f.re.FindAllString(text, -1) {
			if digitCount(n) < f.minDigits {
				continue
			}
			c := f.carrier
			if c == "" {
				c = s10Carrier(n)
			}
			return n, c
		}
	}
	for _, sm := range labeledNumber.FindAllStringSubmatch(folded, -1) {
		n := strings.ToUpper(sm[1])
		if digitCount(n) >= 5 && !dateLike.MatchString(n) && !amazonOrder.MatchString(n) {
			return n, ""
		}
	}
	return "", ""
}

func orderNumber(folded string) string {
	if n := amazonOrder.FindString(folded); n != "" {
		return n
	}
	for _, sm := range labeledOrder.FindAllStringSubmatch(folded, -1) {
		n := strings.ToUpper(sm[1])
		if digitCount(n) < 4 || dateLike.MatchString(n) {
			continue
		}
		if y, err := strconv.Atoi(n); err == nil && y >= 1990 && y <= 2100 {
			continue // un año, no un pedido
		}
		return n
	}
	return ""
}

// ---- Enlaces ----

var (
	urlRe     = regexp.MustCompile(`https://[^\s<>"'()\[\]{}]+`)
	htmlHref  = regexp.MustCompile(`(?i)<a\s[^>]*?href\s*=\s*["']([^"']+)["']`)
	trackPath = regexp.MustCompile(`track|seguimiento|localiza|rastre|livetracking|shiptrack|ship-track|progress-tracker|trazabilidad|envio|shipment|parcel|sendung`)
	notTrack  = regexp.MustCompile(`unsubscribe|baja|preferenc|privacy|privacidad|legal|cookie|facebook|twitter|instagram|linkedin|youtube|tiktok|app-store|play\.google|apps\.apple`)
)

// linkScore puntúa un enlace como enlace de seguimiento (0: no lo es).
func linkScore(u, number string) int {
	if len(u) > 600 {
		return 0
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "https" || pu.Host == "" {
		return 0
	}
	low := strings.ToLower(u)
	if notTrack.MatchString(low) {
		return 0
	}
	score := 0
	if number != "" && strings.Contains(strings.ToUpper(u), number) {
		score += 2
	}
	if trackPath.MatchString(strings.ToLower(pu.Path + "?" + pu.RawQuery)) {
		score++
	}
	if score == 0 {
		return 0
	}
	if carrierByDomain(pu.Hostname()) != nil {
		score += 2
	}
	return score
}

// trackingLinks son los enlaces de un HTML que parecen de seguimiento (el
// texto que se guarda pierde los enlaces del HTML).
func trackingLinks(htmlText string) []string {
	var out []string
	for _, sm := range htmlHref.FindAllStringSubmatch(htmlText, -1) {
		u := strings.TrimSpace(unescapeHTML(sm[1]))
		if linkScore(u, "") > 0 && !slices.Contains(out, u) {
			out = append(out, u)
			if len(out) == 10 {
				break
			}
		}
	}
	return out
}

func unescapeHTML(s string) string { return strings.ReplaceAll(s, "&amp;", "&") }

func bestLink(m message, number string) string {
	cands := append([]string{}, m.Links...)
	for _, u := range urlRe.FindAllString(m.Body, 50) {
		cands = append(cands, strings.TrimRight(u, ".,;:!?>"))
	}
	best, top := "", 0
	for _, u := range cands {
		if s := linkScore(u, number); s > top {
			best, top = u, s
		}
	}
	return best
}

// ---- Estado ----

// statusKeys: cada clave se busca al principio de una palabra del texto
// plegado. El orden es la prioridad en el asunto.
var statusKeys = []struct {
	status string
	keys   []string
}{
	{stProblem, []string{"incidencia", "no hemos podido entregar", "no se ha podido entregar", "no ha podido ser entregad",
		"no pudimos entregar", "no se pudo entregar", "intento de entrega", "intentamos entregar", "intento fallido", "no entregad",
		"devuelto al remitente", "en devolucion al remitente", "retenido en aduana", "pendiente de pago de aranceles",
		"direccion incorrecta", "delivery attempt", "could not be delivered", "unable to deliver", "we missed you",
		"delivery exception", "returned to sender", "held at customs"}},
	{stPickup, []string{"disponible para recoger", "disponible para su recogida", "listo para recoger", "listo para su recogida",
		"lista para recoger", "ya puedes recoger", "puedes recogerlo", "puedes recogerla", "recogelo", "recogela", "te espera en",
		"te esta esperando", "ha llegado a tu locker", "ha llegado al locker", "ha llegado al punto", "ha llegado a la oficina",
		"depositado en", "ready for pickup", "ready to collect", "ready for collection", "available for pickup", "available for collection"}},
	{stDelivered, []string{"entregado", "entregada", "se ha entregado", "hemos entregado", "entrega realizada", "entrega completada",
		"delivered"}},
	{stOut, []string{"en reparto", "sale hoy para reparto", "salido para reparto", "salido a reparto", "en ruta de entrega", "llega hoy",
		"llegara hoy", "lo entregaremos hoy", "te lo entregamos hoy", "se entregara hoy", "entrega hoy", "reparto hoy",
		"out for delivery", "arriving today", "arrives today"}},
	{stShipped, []string{"enviado", "enviada", "ha salido", "en camino", "en transito", "esta de camino", "va de camino", "admitido",
		"recogido por", "ha sido recogido", "shipped", "on its way", "on the way", "dispatched", "in transit", "despachado"}},
	{stOrdered, []string{"pedido confirmado", "confirmacion de pedido", "confirmacion de tu pedido", "confirmacion de su pedido",
		"confirmacion del pedido", "confirmacion de compra", "hemos recibido tu pedido", "hemos recibido su pedido", "gracias por tu pedido",
		"gracias por tu compra", "gracias por su pedido", "gracias por su compra", "pedido realizado", "pedido recibido", "tu pedido de",
		"su pedido de", "estamos preparando tu pedido", "order confirmation", "order confirmed", "thanks for your order",
		"thank you for your order", "order received"}},
}

// futureStatus: frases que nombran un estado sin que haya pasado («será
// entregado», «una vez enviado»).
var futureStatus = []string{"sera entregad", "seran entregad", "sea entregad", "haya sido entregad", "pueda ser entregad",
	"puede ser entregad", "antes de ser entregad", "una vez entregad", "will be delivered", "to be delivered", "once delivered",
	"sera enviad", "seran enviad", "sea enviad", "sean enviad", "hayan enviad", "haya enviad", "haya sido enviad",
	"hayan sido enviad", "una vez enviad", "will be shipped", "once shipped", "when it ships", "cuando se entregue"}

func statusIn(text string, byPriority bool) string {
	text = " " + strings.Join(sealpkg.Words(text), " ") + " "
	for _, f := range futureStatus {
		text = strings.ReplaceAll(text, " "+f, " ·")
	}
	best, at := "", -1
	for _, s := range statusKeys {
		for _, k := range s.keys {
			i := strings.Index(text, " "+k)
			if i < 0 {
				continue
			}
			if byPriority {
				return s.status
			}
			if at < 0 || i < at {
				best, at = s.status, i
			}
		}
	}
	return best
}

// shipmentStatus mira el asunto y, si no dice nada, el principio del texto
// (lo primero que se nombra: lo de más abajo suele ser letra pequeña).
func shipmentStatus(subject, body string) (string, bool) {
	if st := statusIn(subject, true); st != "" {
		return st, true
	}
	if r := []rune(body); len(r) > 1500 {
		body = string(r[:1500])
	}
	return statusIn(body, false), false
}

// ---- Entrega prevista ----

const monthAlt = `enero|febrero|marzo|abril|mayo|junio|julio|agosto|septiembre|setiembre|octubre|noviembre|diciembre|` +
	`january|february|march|april|june|july|august|september|october|november|december|` +
	`ene|feb|mar|abr|may|jun|jul|ago|sept|sep|oct|nov|dic|jan|apr|aug|dec`

var monthNum = map[string]int{"enero": 1, "ene": 1, "january": 1, "jan": 1, "febrero": 2, "feb": 2, "february": 2,
	"marzo": 3, "mar": 3, "march": 3, "abril": 4, "abr": 4, "april": 4, "apr": 4, "mayo": 5, "may": 5,
	"junio": 6, "jun": 6, "june": 6, "julio": 7, "jul": 7, "july": 7, "agosto": 8, "ago": 8, "august": 8, "aug": 8,
	"septiembre": 9, "setiembre": 9, "sept": 9, "sep": 9, "september": 9, "octubre": 10, "oct": 10, "october": 10,
	"noviembre": 11, "nov": 11, "november": 11, "diciembre": 12, "dic": 12, "december": 12, "dec": 12}

var weekdayNum = map[string]time.Weekday{"domingo": 0, "lunes": 1, "martes": 2, "miercoles": 3, "jueves": 4, "viernes": 5,
	"sabado": 6, "sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}

var (
	deliveryAnchor = regexp.MustCompile(`\b(?:entrega (?:estimada|prevista|programada)|fecha (?:estimada |prevista )?de (?:entrega|llegada)|fecha prevista|llegada (?:estimada|prevista)|llegada|llegaran?|llega|(?:lo|la|los|las) recibiras|recibiras|recibira|se entregara|sera entregad[oa]|te lo entregaremos|lo entregaremos|entregaremos|previst[oa] para|nuevo intento|estimated delivery|expected delivery|delivery date|delivery estimate|estimated arrival|arriving|arrives|will arrive|will be delivered|get it|entrega)\b`)
	reISO          = regexp.MustCompile(`\b(\d{4})-(\d{1,2})-(\d{1,2})\b`)
	reSlash        = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?\b`)
	reDashYear     = regexp.MustCompile(`\b(\d{1,2})[.-](\d{1,2})[.-](\d{2,4})\b`)
	reDayMonth     = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th|º|°)? (?:de )?(` + monthAlt + `)\b\.?(?:,? (?:de |del )?(\d{4})\b)?`)
	reMonthDay     = regexp.MustCompile(`\b(` + monthAlt + `)\.? (\d{1,2})(?:st|nd|rd|th)?\b(?:,? (\d{4})\b)?`)
	reRelative     = regexp.MustCompile(`\b(?:pasado manana|manana|hoy|tomorrow|today)\b`)
	reWeekday      = regexp.MustCompile(`\b(?:lunes|martes|miercoles|jueves|viernes|sabado|domingo|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)
)

// deliveryDate busca la entrega prevista: tras «llegará», «entrega
// estimada», «arriving»…, una fecha («5 de noviembre», «05/11/2026», «Nov 5»)
// o, si no hay, «hoy», «mañana» o un día de la semana, contados desde la
// fecha del correo. Cero si no la dice.
func deliveryDate(subject, body string, sent time.Time) time.Time {
	text := subject + "\n" + body
	if r := []rune(text); len(r) > 6000 {
		text = string(r[:6000])
	}
	text = fold(text)
	for _, loc := range deliveryAnchor.FindAllStringIndex(text, -1) {
		end := min(loc[1]+90, len(text))
		window := strings.ReplaceAll(text[loc[1]:end], "la manana", "la ·") // «por la mañana» no es «mañana»
		if d := dateInWindow(window, sent); !d.IsZero() {
			return d
		}
	}
	return time.Time{}
}

func dateInWindow(w string, sent time.Time) time.Time {
	sent = sent.In(madrid)
	day0 := dayStart(sent)
	valid := func(d time.Time) bool {
		return !d.Before(day0.AddDate(0, 0, -2)) && !d.After(day0.AddDate(0, 0, 90))
	}
	mk := func(y, m, d int, yearGiven bool) time.Time {
		if m < 1 || m > 12 || d < 1 || d > 31 {
			return time.Time{}
		}
		if !yearGiven {
			y = sent.Year()
		} else if y < 100 {
			y += 2000
		}
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, madrid)
		if t.Day() != d {
			return time.Time{}
		}
		if !yearGiven && t.Before(day0.AddDate(0, 0, -30)) {
			t = t.AddDate(1, 0, 0) // en diciembre, «el 3 de enero» es del año siguiente
		}
		if !valid(t) {
			return time.Time{}
		}
		return t
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }

	type cand struct {
		pos int
		t   time.Time
	}
	var found []cand
	add := func(pos int, t time.Time) {
		if !t.IsZero() {
			found = append(found, cand{pos, t})
		}
	}
	if m := reISO.FindStringSubmatchIndex(w); m != nil {
		add(m[0], mk(atoi(w[m[2]:m[3]]), atoi(w[m[4]:m[5]]), atoi(w[m[6]:m[7]]), true))
	}
	if m := reSlash.FindStringSubmatchIndex(w); m != nil {
		add(m[0], mk(atoi(sub(w, m, 3)), atoi(sub(w, m, 2)), atoi(sub(w, m, 1)), m[6] >= 0))
	}
	if m := reDashYear.FindStringSubmatchIndex(w); m != nil {
		add(m[0], mk(atoi(sub(w, m, 3)), atoi(sub(w, m, 2)), atoi(sub(w, m, 1)), true))
	}
	if m := reDayMonth.FindStringSubmatchIndex(w); m != nil {
		add(m[0], mk(atoi(sub(w, m, 3)), monthNum[sub(w, m, 2)], atoi(sub(w, m, 1)), m[6] >= 0))
	}
	if m := reMonthDay.FindStringSubmatchIndex(w); m != nil {
		add(m[0], mk(atoi(sub(w, m, 3)), monthNum[sub(w, m, 1)], atoi(sub(w, m, 2)), m[6] >= 0))
	}
	if len(found) == 0 {
		if m := reRelative.FindStringIndex(w); m != nil {
			n := map[string]int{"hoy": 0, "today": 0, "manana": 1, "tomorrow": 1, "pasado manana": 2}[w[m[0]:m[1]]]
			add(m[0], day0.AddDate(0, 0, n))
		}
		if m := reWeekday.FindStringIndex(w); m != nil {
			n := (int(weekdayNum[w[m[0]:m[1]]]) - int(day0.Weekday()) + 7) % 7
			if n == 0 {
				n = 7 // el mismo día se dice «hoy»
			}
			add(m[0], day0.AddDate(0, 0, n))
		}
	}
	if len(found) == 0 {
		return time.Time{}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	return found[0].t
}

// sub es el grupo i de una coincidencia ("" si no participa).
func sub(s string, m []int, i int) string {
	if m[2*i] < 0 {
		return ""
	}
	return s[m[2*i]:m[2*i+1]]
}

// ---- Tienda ----

var (
	shopPhrase  = regexp.MustCompile(`(?:[Pp]edido|[Pp]aquete|[Ee]nv[ií]o|[Cc]ompra|[Oo]rder|[Pp]arcel|[Pp]ackage) (?:de|from) ([A-ZÁÉÍÓÚÑ0-9][\p{L}\d&'’.-]*(?: [A-ZÁÉÍÓÚÑ][\p{L}\d&'’.-]*){0,2})`)
	senderLabel = regexp.MustCompile(`(?i:remitente|enviado por)\s*:\s*([^\n,;]{2,40})`)
	genericName = map[string]bool{"no": true, "reply": true, "noreply": true, "notificaciones": true, "notificacion": true, "info": true,
		"pedidos": true, "pedido": true, "orders": true, "order": true, "envios": true, "envio": true, "shipping": true, "confirmacion": true,
		"atencion": true, "al": true, "cliente": true, "clientes": true, "customer": true, "service": true, "servicio": true, "tienda": true,
		"shop": true, "store": true, "ventas": true, "team": true, "equipo": true, "de": true, "online": true}
)

// notShop son palabras que siguen a «pedido de…» sin ser una tienda.
func notShop(name string) bool {
	f := fold(name)
	if _, ok := monthNum[f]; ok {
		return true
	}
	if _, ok := weekdayNum[f]; ok {
		return true
	}
	if c := carrierNamed(name); carrierInText(name) != "" || (c != nil && c.Name != "Amazon") {
		return true
	}
	return slices.Contains([]string{"hoy", "manana", "seguimiento", "entrega", "recogida", "devolucion", "tu", "su", "la", "el", "los", "las",
		"un", "una", "nuevo", "nueva", "importacion"}, strings.Fields(f)[0]) || digitCount(name) == len([]rune(name))
}

func shopInText(text string) string {
	for _, re := range []*regexp.Regexp{shopPhrase, senderLabel} {
		for _, sm := range re.FindAllStringSubmatch(text, 5) {
			name := strings.TrimRight(strings.TrimSpace(sm[1]), ".-’'")
			if name != "" && !notShop(name) {
				return truncate(name, 60)
			}
		}
	}
	return ""
}

// merchantName es la tienda: el nombre del remitente o su dominio; si
// escribe el transportista, la que nombre el texto.
func merchantName(m message, sender *carrierDef, domain string) string {
	if sender != nil && sender.Name == "Amazon" {
		return "Amazon"
	}
	if sender != nil {
		return shopInText(m.Subject + "\n" + m.Body)
	}
	name := m.FromName
	for _, sep := range []string{" - ", " | ", " · ", " via ", " vía "} {
		name, _, _ = strings.Cut(name, sep)
	}
	name = strings.Trim(strings.TrimSpace(name), `"'`)
	generic := true
	for _, w := range sealpkg.Words(name) {
		if !genericName[w] {
			generic = false
		}
	}
	if name == "" || generic || strings.Contains(name, "@") {
		label := baseLabel(domain)
		if label == "" {
			return ""
		}
		r := []rune(label)
		name = strings.ToUpper(string(r[:1])) + string(r[1:])
	}
	return truncate(name, 60)
}

// ---- Todo junto ----

// shipmentInfo es lo que dice un correo de un envío.
type shipmentInfo struct {
	Carrier, Number, Order, Merchant, Link, Status, Text string
	Expected                                             time.Time // cero si no lo dice
	shop                                                 string    // dominio de la tienda (huella del pedido)
}

// detectShipment lee un correo y dice si habla de un envío o un pedido.
func detectShipment(m message) (shipmentInfo, bool) {
	domain := domainOf(m.FromAddr)
	var d shipmentInfo
	for _, l := range domainLabels(domain) {
		if slices.Contains(noShipments, l) {
			return d, false
		}
	}
	text := m.Subject + "\n" + m.Body
	if r := []rune(text); len(r) > 20000 {
		text = string(r[:20000])
	}
	folded := fold(text)
	d.shop = baseLabel(domain)
	sender := carrierByDomain(domain)
	if sender != nil && sender.Name != "Amazon" {
		d.Carrier = sender.Name
	} else {
		d.Carrier = carrierInText(text)
	}
	var byNumber string
	d.Number, byNumber = trackingNumber(text, folded, d.Carrier)
	if byNumber != "" && (d.Carrier == "" || d.Carrier == "Amazon") {
		d.Carrier = byNumber
	}
	d.Order = orderNumber(folded)
	if d.Order == d.Number {
		d.Order = ""
	}
	d.Link = bestLink(m, d.Number)
	if d.Carrier == "" && d.Link != "" {
		if u, err := url.Parse(d.Link); err == nil {
			if c := carrierByDomain(u.Hostname()); c != nil {
				d.Carrier = c.Name
			}
		}
	}
	if d.Carrier == "" && sender != nil {
		d.Carrier = sender.Name
	}
	if d.Number == "" && d.Order == "" && d.Link == "" {
		return d, false
	}
	status, inSubject := shipmentStatus(m.Subject, m.Body)
	switch {
	case status == "" && sender != nil && (d.Number != "" || d.Link != ""):
		status = stShipped // escribe el transportista con un número: va de camino
	case status == "":
		return d, false
	case !inSubject && sender == nil && d.Number == "" && d.Link == "":
		return d, false // «enviado» en el texto de una factura con un número de pedido: poco fiable
	}
	d.Status = status
	d.Merchant = merchantName(m, sender, domain)
	d.Expected = deliveryDate(m.Subject, m.Body, m.Date)
	d.Text = truncate(strings.TrimSpace(m.Subject), 160)
	return d, true
}

// shipmentTokens son las huellas para juntar los correos de un envío: el
// número de seguimiento (sin el transportista: la tienda y el transportista
// lo nombran distinto), el pedido en esa tienda o, si no hay más, el enlace.
func shipmentTokens(box *sealpkg.Box, d shipmentInfo) []int64 {
	var out []int64
	if d.Number != "" {
		out = append(out, box.Token("envio", d.Number))
	}
	if d.Order != "" {
		out = append(out, box.Token("pedido", d.shop+"|"+d.Order))
	}
	if len(out) == 0 && d.Link != "" {
		out = append(out, box.Token("enlace", d.Link))
	}
	return out
}

// ---- Base de datos ----

// shipmentBlob es lo que va cifrado de cada envío.
type shipmentBlob struct {
	Carrier  string    `json:"carrier,omitempty"`
	Number   string    `json:"number,omitempty"`
	Order    string    `json:"order,omitempty"`
	Merchant string    `json:"merchant,omitempty"`
	Link     string    `json:"link,omitempty"`
	Text     string    `json:"text,omitempty"` // asunto del correo que dio el estado
	StatusAt time.Time `json:"status_at"`
	Messages []int64   `json:"messages,omitempty"`
}

type shipmentRow struct {
	ID        int64
	AccountID int64
	Tokens    []int64
	Status    string
	Expected  string // AAAA-MM-DD o ""
	Hidden    bool
	Updated   time.Time
	shipmentBlob
}

// apply suma lo que dice un correo: el estado solo avanza (a igual nivel,
// manda el correo más reciente) y la entrega prevista es la del último correo
// que la diga.
func (r *shipmentRow) apply(d shipmentInfo, msgID int64, at time.Time) {
	if r.Status == "" || statusRank[d.Status] > statusRank[r.Status] ||
		(statusRank[d.Status] == statusRank[r.Status] && !at.Before(r.StatusAt)) {
		r.Status, r.StatusAt, r.Text = d.Status, at, d.Text
	}
	if !d.Expected.IsZero() && !at.Before(r.Updated) {
		r.Expected = d.Expected.Format("2006-01-02")
	}
	if d.Carrier != "" && (r.Carrier == "" || r.Carrier == "Amazon") {
		r.Carrier = d.Carrier
	}
	for dst, src := range map[*string]string{&r.Number: d.Number, &r.Order: d.Order, &r.Merchant: d.Merchant, &r.Link: d.Link} {
		if *dst == "" {
			*dst = src
		}
	}
	if msgID != 0 && !slices.Contains(r.Messages, msgID) && len(r.Messages) < 30 {
		r.Messages = append(r.Messages, msgID)
	}
	if at.After(r.Updated) {
		r.Updated = at
	}
}

// merge junta otro envío que ha resultado ser el mismo (el pedido de la
// tienda y el número del transportista).
func (r *shipmentRow) merge(o shipmentRow) {
	exp := o.Expected
	if r.Updated.After(o.Updated) || exp == "" {
		exp = r.Expected
	}
	r.apply(shipmentInfo{Carrier: o.Carrier, Number: o.Number, Order: o.Order, Merchant: o.Merchant, Link: o.Link,
		Status: o.Status, Text: o.Text}, 0, o.StatusAt)
	if exp != "" {
		r.Expected = exp
	}
	for _, id := range o.Messages {
		if !slices.Contains(r.Messages, id) && len(r.Messages) < 30 {
			r.Messages = append(r.Messages, id)
		}
	}
	if o.Updated.After(r.Updated) {
		r.Updated = o.Updated
	}
	r.Hidden = r.Hidden || o.Hidden
	r.Tokens = union(r.Tokens, o.Tokens)
}

func union(a, b []int64) []int64 {
	out := append([]int64{}, a...)
	for _, t := range b {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

const shipmentCols = `s.id, s.account_id, s.tokens, s.status, coalesce(to_char(s.expected_on, 'YYYY-MM-DD'), ''), s.hidden, s.updated_at, s.sealed`

func scanShipments(box *sealpkg.Box, rows pgx.Rows) ([]shipmentRow, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (shipmentRow, error) {
		var x shipmentRow
		var sealed []byte
		if err := r.Scan(&x.ID, &x.AccountID, &x.Tokens, &x.Status, &x.Expected, &x.Hidden, &x.Updated, &sealed); err != nil {
			return x, err
		}
		return x, box.OpenJSON(sealShipment, sealed, &x.shipmentBlob)
	})
}

// beginner es la base o una transacción (en la migración).
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// saveShipment suma un correo a su envío (o crea uno). Si el correo une dos
// envíos que se tenían por distintos, quedan en uno.
func saveShipment(ctx context.Context, db beginner, box *sealpkg.Box, owner string, accountID, msgID int64, at time.Time, d shipmentInfo) error {
	tokens := shipmentTokens(box, d)
	if len(tokens) == 0 {
		return nil
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, _ := tx.Query(ctx, `SELECT `+shipmentCols+` FROM shipments s JOIN mail_accounts a ON a.id = s.account_id
		WHERE a.owner = $1 AND s.tokens && $2 ORDER BY s.id FOR UPDATE OF s`, owner, tokens)
	list, err := scanShipments(box, rows)
	if err != nil {
		return err
	}
	cur := shipmentRow{AccountID: accountID}
	if len(list) > 0 {
		cur = list[0]
		if msgID != 0 && slices.Contains(cur.Messages, msgID) && len(list) == 1 {
			return nil // ya contado
		}
		for _, o := range list[1:] {
			cur.merge(o)
		}
	}
	cur.apply(d, msgID, at)
	cur.Tokens = union(cur.Tokens, tokens)
	sealed, err := box.SealJSON(sealShipment, cur.shipmentBlob)
	if err != nil {
		return err
	}
	var expected *string
	if cur.Expected != "" {
		expected = &cur.Expected
	}
	if cur.ID == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO shipments (account_id, tokens, status, expected_on, hidden, updated_at, sealed)
			VALUES ($1, $2, $3, $4::text::date, false, $5, $6)`, cur.AccountID, cur.Tokens, cur.Status, expected, cur.Updated, sealed)
	} else {
		_, err = tx.Exec(ctx, `UPDATE shipments SET tokens = $2, status = $3, expected_on = $4::text::date, hidden = $5,
			updated_at = $6, sealed = $7 WHERE id = $1`, cur.ID, cur.Tokens, cur.Status, expected, cur.Hidden, cur.Updated, sealed)
	}
	if err != nil {
		return err
	}
	if len(list) > 1 {
		var gone []int64
		for _, o := range list[1:] {
			gone = append(gone, o.ID)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM shipments WHERE id = ANY($1)`, gone); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// trackShipment mira si un correo recién guardado (con su id) es de un envío.
func (s store) trackShipment(ctx context.Context, owner string, m message) error {
	d, ok := detectShipment(m)
	if !ok {
		return nil
	}
	return saveShipment(ctx, s.db, s.box, owner, m.AccountID, m.ID, m.Date, d)
}

func (s store) shipments(ctx context.Context, owner string, delivered bool) ([]shipmentRow, error) {
	q := `SELECT ` + shipmentCols + ` FROM shipments s JOIN mail_accounts a ON a.id = s.account_id WHERE a.owner = $1 AND NOT s.hidden`
	if !delivered {
		q += ` AND s.status <> 'entregado'`
	}
	rows, _ := s.db.Query(ctx, q, owner)
	return scanShipments(s.box, rows)
}

func (s store) shipment(ctx context.Context, owner string, id int64) (shipmentRow, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+shipmentCols+` FROM shipments s JOIN mail_accounts a ON a.id = s.account_id
		WHERE a.owner = $1 AND s.id = $2`, owner, id)
	list, err := scanShipments(s.box, rows)
	if err == nil && len(list) == 0 {
		err = errNotFound
	}
	if err != nil {
		return shipmentRow{}, err
	}
	return list[0], nil
}

func (s store) hideShipment(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE shipments s SET hidden = true FROM mail_accounts a
		WHERE a.id = s.account_id AND a.owner = $1 AND s.id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// pruneShipments borra los entregados de hace más de 30 días y el resto a los
// 90 (contados desde el último correo).
func (s store) pruneShipments(ctx context.Context, now time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM shipments WHERE (status = 'entregado' AND updated_at < $1) OR updated_at < $2`,
		now.AddDate(0, 0, -keepDelivered), now.AddDate(0, 0, -keepShipments))
	return err
}

// backfillShipments busca envíos en el correo ya guardado (paso 004).
func backfillShipments(ctx context.Context, tx pgx.Tx, box *sealpkg.Box) error {
	type hit struct {
		owner       string
		account, id int64
		at          time.Time
		info        shipmentInfo
	}
	var hits []hit
	rows, _ := tx.Query(ctx, `SELECT m.id, m.account_id, a.owner, m.sent_at, m.sealed FROM messages m
		JOIN mail_accounts a ON a.id = m.account_id ORDER BY m.sent_at, m.id`)
	var h hit
	var sealed []byte
	if _, err := pgx.ForEachRow(rows, []any{&h.id, &h.account, &h.owner, &h.at, &sealed}, func() error {
		var b mailBlob
		if box.OpenJSON(sealMessage, sealed, &b) != nil {
			return nil // ilegible: se salta
		}
		m := message{ID: h.id, AccountID: h.account, Date: h.at, FromName: b.FromName, FromAddr: b.FromAddr,
			Subject: b.Subject, Body: b.Body, Links: b.Links}
		if d, ok := detectShipment(m); ok {
			x := h
			x.info = d
			hits = append(hits, x)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, x := range hits {
		if err := saveShipment(ctx, tx, box, x.owner, x.account, x.id, x.at, x.info); err != nil {
			return err
		}
	}
	return nil
}

// ---- Para el asistente, la web y el resumen ----

// shipmentView es un envío para mostrar.
type shipmentView struct {
	ID       int64     `json:"id"`
	Carrier  string    `json:"transportista"`
	Number   string    `json:"numero"`
	Merchant string    `json:"tienda"`
	State    string    `json:"estado"`
	Expected string    `json:"entrega_prevista"` // AAAA-MM-DD o ""
	Link     string    `json:"enlace"`
	Updated  time.Time `json:"actualizado"`
	Order    string    `json:"pedido,omitempty"`
	Text     string    `json:"ultimo_aviso,omitempty" jsonschema:"asunto del correo que dio el estado"`
	Messages []int64   `json:"correos,omitempty" jsonschema:"ids de los correos del envío (para correo_leer)"`
	Status   string    `json:"-"` // código del estado
	When     string    `json:"-"` // entrega prevista en palabras
}

func viewShipment(r shipmentRow) shipmentView {
	v := shipmentView{ID: r.ID, Carrier: r.Carrier, Number: r.Number, Merchant: r.Merchant, State: statusNames[r.Status],
		Expected: r.Expected, Link: r.Link, Updated: r.Updated.In(madrid), Order: r.Order, Text: r.Text,
		Messages: r.Messages, Status: r.Status}
	if t, err := time.ParseInLocation("2006-01-02", r.Expected, madrid); err == nil {
		v.When = dayLabel(t)
	}
	return v
}

// shipments son los envíos de la persona: primero los que llegan antes.
func (s *service) shipments(ctx context.Context, owner string, delivered bool) ([]shipmentView, error) {
	rows, err := s.store.shipments(ctx, owner, delivered)
	if err != nil {
		return nil, err
	}
	out := []shipmentView{}
	for _, r := range rows {
		out = append(out, viewShipment(r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Status == stDelivered) != (b.Status == stDelivered) {
			return b.Status == stDelivered
		}
		if a.Expected != b.Expected {
			return b.Expected == "" || (a.Expected != "" && a.Expected < b.Expected)
		}
		return a.Updated.After(b.Updated)
	})
	return out, nil
}

func (s *service) hideShipment(ctx context.Context, owner string, id int64) (shipmentView, error) {
	r, err := s.store.shipment(ctx, owner, id)
	if err != nil {
		return shipmentView{}, err
	}
	if err := s.store.hideShipment(ctx, owner, id); err != nil {
		return shipmentView{}, err
	}
	return viewShipment(r), nil
}

// shipmentLines son las líneas del resumen de la mañana: lo que llega hoy, lo
// que está en reparto y lo que espera en un punto de recogida.
func (s *service) shipmentLines(ctx context.Context, owner string, now time.Time) []string {
	list, err := s.shipments(ctx, owner, false)
	if err != nil {
		s.log.Warn("envíos", "usuario", owner, "err", err)
		return nil
	}
	today := now.In(madrid).Format("2006-01-02")
	var out []string
	for _, v := range list {
		name := v.Merchant
		switch {
		case name == "":
			name = v.Carrier
		case v.Carrier != "" && !strings.EqualFold(v.Carrier, name):
			name += " (" + v.Carrier + ")"
		}
		if name == "" {
			name = "un paquete"
		}
		recent := now.Sub(v.Updated) < 36*time.Hour
		switch {
		case v.Status == stPickup:
			out = append(out, "📦 Para recoger: "+name)
		case v.Expected == today || (v.Status == stOut && recent):
			line := "📦 Hoy llega: " + name
			if v.Status == stOut || v.Status == stProblem {
				line += ", " + v.State
			}
			out = append(out, line)
		}
		if len(out) == 6 {
			break
		}
	}
	return out
}
