package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Documentos: el modelo, los tipos con sus avisos por defecto, la validación,
// el estado (caducado, caduca pronto, vigente) y la renovación. Todo lo de un
// documento va cifrado en una sola columna (store.go); aquí no hay base de
// datos.

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

const dateFmt = "2006-01-02"

// Límites de los campos (en caracteres).
const (
	maxTitle  = 120
	maxHolder = 80
	maxNumber = 60
	maxIssuer = 120
	maxNotes  = 4000
	maxAlerts = 5
	maxMonths = 120
	maxAmount = 10_000_000_00 // céntimos: diez millones de euros
	soonDays  = 90            // «caduca pronto»
)

// docType es un tipo de documento con su etiqueta y sus avisos por defecto.
type docType struct {
	Key    string
	Label  string
	Alerts []int
}

var (
	alerts60 = []int{60, 30, 7}
	alerts30 = []int{30, 7}
)

// docTypes en el orden en que salen en la web.
var docTypes = []docType{
	{"dni", "DNI", alerts60},
	{"pasaporte", "Pasaporte", alerts60},
	{"carne", "Carné de conducir", alerts60},
	{"nie", "NIE/TIE", alerts60},
	{"sanitaria", "Tarjeta sanitaria", alerts60},
	{"certificado", "Certificado digital", alerts60},
	{"itv", "ITV", alerts30},
	{"seguro_coche", "Seguro del coche", alerts30},
	{"seguro_hogar", "Seguro del hogar", alerts30},
	{"seguro_salud", "Seguro de salud", alerts30},
	{"seguro_vida", "Seguro de vida", alerts30},
	{"garantia", "Garantía", []int{30}},
	{"contrato", "Contrato", alerts60},
	{"factura", "Factura", nil},
	{"titulo", "Título o certificado académico", nil},
	{"otro", "Otro", alerts60},
}

func typeOf(key string) (docType, bool) {
	for _, t := range docTypes {
		if t.Key == key {
			return t, true
		}
	}
	return docType{}, false
}

func typeLabel(key string) string {
	if t, ok := typeOf(key); ok {
		return t.Label
	}
	return "Otro"
}

func typeIndex(key string) int {
	for i, t := range docTypes {
		if t.Key == key {
			return i
		}
	}
	return len(docTypes)
}

// defaultAlerts son los avisos del tipo (una copia).
func defaultAlerts(key string) []int {
	t, _ := typeOf(key)
	return append([]int{}, t.Alerts...)
}

// doc es un documento. Lo que lleva json va cifrado; ID y las fechas de la
// fila van en claro.
type doc struct {
	ID      int64     `json:"-"`
	Created time.Time `json:"-"`
	Updated time.Time `json:"-"`
	Files   int       `json:"-"` // cuántos archivos tiene

	Type    string `json:"tipo"`
	Title   string `json:"titulo"`
	Holder  string `json:"titular,omitempty"`
	Number  string `json:"numero,omitempty"`
	Issuer  string `json:"emisor,omitempty"`
	Issued  string `json:"emision,omitempty"`   // AAAA-MM-DD
	Expires string `json:"caducidad,omitempty"` // AAAA-MM-DD
	Months  int    `json:"cada_meses,omitempty"`
	Alerts  []int  `json:"avisos"`
	Amount  *int64 `json:"importe,omitempty"` // céntimos
	Notes   string `json:"notas,omitempty"`
}

// docErr es un error de los datos que se puede enseñar tal cual.
type docErr string

func (e docErr) Error() string { return string(e) }

// clean recorta los espacios y valida el documento. Los avisos quedan
// ordenados de más a menos días y sin repetir.
func (d *doc) clean() error {
	d.Type = strings.TrimSpace(d.Type)
	if _, ok := typeOf(d.Type); !ok {
		return docErr("Elige el tipo de documento.")
	}
	d.Title = oneLine(d.Title)
	d.Holder = oneLine(d.Holder)
	d.Number = oneLine(d.Number)
	d.Issuer = oneLine(d.Issuer)
	d.Notes = strings.TrimSpace(strings.ReplaceAll(d.Notes, "\r\n", "\n"))
	switch {
	case d.Title == "":
		return docErr("Ponle un título (por ejemplo, «DNI de Ana»).")
	case utf8.RuneCountInString(d.Title) > maxTitle:
		return docErr(fmt.Sprintf("El título pasa de %d caracteres.", maxTitle))
	case utf8.RuneCountInString(d.Holder) > maxHolder:
		return docErr(fmt.Sprintf("El titular pasa de %d caracteres.", maxHolder))
	case utf8.RuneCountInString(d.Number) > maxNumber:
		return docErr(fmt.Sprintf("El número pasa de %d caracteres.", maxNumber))
	case utf8.RuneCountInString(d.Issuer) > maxIssuer:
		return docErr(fmt.Sprintf("El emisor pasa de %d caracteres.", maxIssuer))
	case utf8.RuneCountInString(d.Notes) > maxNotes:
		return docErr(fmt.Sprintf("Las notas pasan de %d caracteres.", maxNotes))
	}
	issued, err := parseDate(d.Issued, "de emisión")
	if err != nil {
		return err
	}
	expires, err := parseDate(d.Expires, "de caducidad")
	if err != nil {
		return err
	}
	if !issued.IsZero() && !expires.IsZero() && expires.Before(issued) {
		return docErr("La caducidad no puede ser anterior a la emisión.")
	}
	if d.Months < 0 || d.Months > maxMonths {
		return docErr(fmt.Sprintf("La renovación periódica va de 1 a %d meses.", maxMonths))
	}
	if d.Amount != nil && (*d.Amount < 0 || *d.Amount > maxAmount) {
		return docErr("El importe no es válido.")
	}
	if len(d.Alerts) > maxAlerts {
		return docErr(fmt.Sprintf("Como mucho %d avisos.", maxAlerts))
	}
	for _, a := range d.Alerts {
		if a < 1 || a > 365 {
			return docErr("Cada aviso va de 1 a 365 días antes.")
		}
	}
	d.Alerts = normAlerts(d.Alerts)
	return nil
}

// oneLine quita los saltos de línea y los caracteres de control.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func normAlerts(a []int) []int {
	out := append([]int{}, a...)
	slices.Sort(out)
	out = slices.Compact(out)
	slices.Reverse(out)
	return out
}

// parseDate lee AAAA-MM-DD (vacío: sin fecha).
func parseDate(s, what string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation(dateFmt, s, madrid)
	if err != nil || t.Year() < 1900 || t.Year() > 2200 {
		return time.Time{}, docErr("La fecha " + what + " no es válida.")
	}
	return t, nil
}

// parseAlerts lee «60, 30, 7». Vacío: los del tipo; «0» o «no»: ninguno.
func parseAlerts(s, typ string) ([]int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return defaultAlerts(typ), nil
	case "0", "no", "ninguno", "sin avisos":
		return []int{}, nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '/' })
	var out []int
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSuffix(p, "d"))
		if err != nil {
			return nil, docErr("Escribe los avisos como días separados por comas: 60, 30, 7.")
		}
		out = append(out, n)
	}
	if len(out) > maxAlerts {
		return nil, docErr(fmt.Sprintf("Como mucho %d avisos.", maxAlerts))
	}
	for _, n := range out {
		if n < 1 || n > 365 {
			return nil, docErr("Cada aviso va de 1 a 365 días antes.")
		}
	}
	return normAlerts(out), nil
}

func alertsText(a []int) string {
	if len(a) == 0 {
		return ""
	}
	parts := make([]string, len(a))
	for i, n := range a {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// parseAmount lee un importe en euros («45,50», «1.200», «1200.5»).
func parseAmount(s string) (*int64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "€"))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return nil, nil
	}
	bad := docErr("El importe no es válido: escribe, por ejemplo, 45,50.")
	// Con coma, la coma es el decimal y los puntos son miles. Sin coma, un
	// punto seguido de tres cifras son miles; si no, decimales.
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else if i := strings.LastIndex(s, "."); i >= 0 && len(s)-i-1 == 3 {
		s = strings.ReplaceAll(s, ".", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) || f*100 > maxAmount {
		return nil, bad
	}
	c := int64(math.Round(f * 100))
	return &c, nil
}

// euros es «1.234,50 €» (sin céntimos si son cero).
func euros(c int64) string {
	whole, cents := c/100, c%100
	s := strconv.FormatInt(whole, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if cents != 0 {
		fmt.Fprintf(&b, ",%02d", cents)
	}
	return b.String() + " €"
}

// amountText es el importe para un campo del formulario («45,50»).
func amountText(c *int64) string {
	if c == nil {
		return ""
	}
	if *c%100 == 0 {
		return strconv.FormatInt(*c/100, 10)
	}
	return fmt.Sprintf("%d,%02d", *c/100, *c%100)
}

// ---- Estado ----

const (
	stExpired = "caducado"
	stSoon    = "caduca_pronto"
	stValid   = "vigente"
	stNone    = "sin_caducidad"
)

// status es el estado de un documento en un día y los días que faltan
// (negativos si ya caducó; 0 sin caducidad).
func (d doc) status(today time.Time) (string, int) {
	exp, err := parseDate(d.Expires, "")
	if err != nil || exp.IsZero() {
		return stNone, 0
	}
	days := daysBetween(today, exp)
	switch {
	case days < 0:
		return stExpired, days
	case days <= soonDays:
		return stSoon, days
	}
	return stValid, days
}

// daysBetween cuenta los días de calendario de a a b (en Madrid).
func daysBetween(a, b time.Time) int {
	a, b = a.In(madrid), b.In(madrid)
	da := time.Date(a.Year(), a.Month(), a.Day(), 12, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 12, 0, 0, 0, time.UTC)
	return int(math.Round(db.Sub(da).Hours() / 24))
}

// addMonths suma meses sin pasarse de mes: 31 de enero + 1 = 28 (o 29) de
// febrero.
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m, 1, 0, 0, 0, 0, t.Location()).AddDate(0, n, 0)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(d, last), 0, 0, 0, 0, t.Location())
}

var errNoNewDate = docErr("Pon la fecha de caducidad nueva.")

// renew da la caducidad nueva: la fecha indicada o, sin ella, la de ahora
// más cada_meses.
func (d *doc) renew(newDate string) error {
	if newDate != "" {
		t, err := parseDate(newDate, "nueva")
		if err != nil {
			return err
		}
		if iss, _ := parseDate(d.Issued, ""); !iss.IsZero() && t.Before(iss) {
			return docErr("La caducidad no puede ser anterior a la emisión.")
		}
		d.Expires = t.Format(dateFmt)
		return nil
	}
	if d.Months == 0 {
		return errNoNewDate
	}
	exp, err := parseDate(d.Expires, "de caducidad")
	if err != nil {
		return err
	}
	if exp.IsZero() {
		return docErr("No tiene fecha de caducidad: ponla al editar el documento.")
	}
	d.Expires = addMonths(exp, d.Months).Format(dateFmt)
	return nil
}

// nextRenewal es la caducidad que daría renovar por periodo ("" si no hay).
func (d doc) nextRenewal() string {
	if d.Months == 0 {
		return ""
	}
	exp, err := parseDate(d.Expires, "")
	if err != nil || exp.IsZero() {
		return ""
	}
	return addMonths(exp, d.Months).Format(dateFmt)
}

// matches dice si el documento tiene todas las palabras de q (sin tildes ni
// mayúsculas) en el título, el titular, el emisor o el tipo.
func (d doc) matches(words []string) bool {
	hay := fold(strings.Join([]string{d.Title, d.Holder, d.Issuer, typeLabel(d.Type), d.Type}, " "))
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

var errNotFound = errors.New("no existe")
