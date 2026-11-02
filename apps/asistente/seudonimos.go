package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// Seudónimos: los datos personales no llegan al modelo. Teléfonos,
// direcciones, correos, IBAN, tarjetas, DNI/NIE, matrículas, cumpleaños y
// enlaces de Maps se cambian por etiquetas («TEL_1», «DIR_2:Madrid»,
// «EMAIL_3»…) en lo que escribe la persona y en lo que devuelven las
// herramientas. El valor queda en la tabla pseudonyms, cifrado. Al llamar a
// una herramienta y al contestar por Telegram se pone de vuelta el dato real;
// en el historial se guardan las etiquetas.
//
// Los nombres de personas no se tocan (decisión del 02-11-2026): sin ellos el
// asistente escribe mucho peor. Tampoco se detectan direcciones dentro de un
// texto libre (un correo que diga «en la calle Mayor 3»): solo en los campos
// de las herramientas que lo son.

const sealPseudonym = "asistente/pseudonyms"

// Campos de las herramientas que son datos personales, por su nombre JSON.
// «valor» hereda el tipo de su lista (contactos: telefonos[].valor…).
var piiFields = map[string]string{
	"lugar": "DIR", "direccion": "DIR", "direcciones": "DIR", "casa": "DIR", "origen": "DIR", "destino": "DIR",
	"telefono": "TEL", "telefonos": "TEL", "correos": "EMAIL", "correo": "EMAIL", "email": "EMAIL",
	"mapa": "ENLACE", "ruta": "ENLACE", "cumpleanos": "CUMPLE",
}

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reMaps  = regexp.MustCompile(`https?://(?:www\.)?(?:google\.[a-z.]+/maps|maps\.google\.[a-z.]+|maps\.app\.goo\.gl)[^\s"<>»]*`)
	reIBAN  = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]{4}){3,7}(?:[ ]?[A-Z0-9]{1,3})?\b`)
	reCard  = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
	reDNI   = regexp.MustCompile(`\b(?:\d{8}|[XYZ]\d{7})-?[A-HJ-NP-TV-Z]\b`)
	rePlate = regexp.MustCompile(`\b\d{4}[ -]?[BCDFGHJKLMNPRSTVWXYZ]{3}\b`)
	rePhone = regexp.MustCompile(`(?:(?:\+|00)\d{1,3}[ .-]?)?\(?\d(?:[ .()-]?\d){7,13}`)
	reLabel = regexp.MustCompile(`«((?:TEL|DIR|EMAIL|IBAN|TARJETA|DNI|MATRICULA|CUMPLE|ENLACE)_\d+)(?::[^»\n]*)?»|\b((?:TEL|DIR|EMAIL|IBAN|TARJETA|DNI|MATRICULA|CUMPLE|ENLACE)_\d+)\b`)
)

// pseudonyms cambia datos por etiquetas y al revés, para una conversación.
// Sin base de datos (resúmenes internos), las etiquetas viven en memoria.
type pseudonyms struct {
	st      store
	chat    int64
	memory  bool
	byLabel map[string]string // etiqueta → valor
	byValue map[string]string // tipo + valor normalizado → etiqueta
}

func newPseudonyms(st store, chat int64, memory bool) *pseudonyms {
	return &pseudonyms{st: st, chat: chat, memory: memory, byLabel: map[string]string{}, byValue: map[string]string{}}
}

func normalize(kind, value string) string {
	switch kind {
	case "TEL", "TARJETA", "IBAN":
		return strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) || unicode.IsUpper(r) {
				return r
			}
			return -1
		}, strings.ToUpper(value))
	case "EMAIL", "DNI", "MATRICULA":
		return strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(value))
	}
	return strings.TrimSpace(value)
}

// label devuelve la etiqueta de un dato (la misma para el mismo dato).
func (p *pseudonyms) label(ctx context.Context, kind, value string) (string, error) {
	key := kind + "\x00" + normalize(kind, value)
	if l, ok := p.byValue[key]; ok {
		return l, nil
	}
	var l string
	if p.memory {
		n := 1
		for _, have := range p.byValue {
			if strings.HasPrefix(have, kind+"_") {
				n++
			}
		}
		l = fmt.Sprintf("%s_%d", kind, n)
	} else {
		vk := p.st.box.Token("seudonimo", key)
		var err error
		for try := 0; try < 3; try++ {
			err = p.st.db.QueryRow(ctx, `INSERT INTO pseudonyms (chat_id, label, kind, value_key, sealed)
				VALUES ($1, $2 || '_' || (SELECT coalesce(max(split_part(label, '_', 2)::int), 0) + 1 FROM pseudonyms
					WHERE chat_id = $1 AND kind = $2), $2, $3, $4)
				ON CONFLICT (chat_id, value_key) DO UPDATE SET used_at = now()
				RETURNING label`, p.chat, kind, vk, p.st.box.SealString(sealPseudonym, value)).Scan(&l)
			if err == nil {
				break
			}
		}
		if err != nil {
			return "", err
		}
	}
	p.byValue[key], p.byLabel[l] = l, value
	return l, nil
}

// value devuelve el dato de una etiqueta.
func (p *pseudonyms) value(ctx context.Context, label string) (string, bool) {
	if v, ok := p.byLabel[label]; ok {
		return v, true
	}
	if p.memory {
		return "", false
	}
	var sealed []byte
	err := p.st.db.QueryRow(ctx, `UPDATE pseudonyms SET used_at = now() WHERE chat_id = $1 AND label = $2 RETURNING sealed`,
		p.chat, label).Scan(&sealed)
	if err != nil {
		return "", false
	}
	v, err := p.st.box.OpenString(sealPseudonym, sealed)
	if err != nil {
		return "", false
	}
	p.byLabel[label] = v
	return v, true
}

// tag es la etiqueta tal como la ve el modelo, con una pista si la hay.
func (p *pseudonyms) tag(ctx context.Context, kind, value string) string {
	l, err := p.label(ctx, kind, value)
	if err != nil {
		return "«" + kind + "»" // sin poder guardarlo, mejor perderlo que enseñarlo
	}
	if kind == "DIR" {
		if h := cityHint(value); h != "" {
			return "«" + l + ":" + h + "»"
		}
	}
	return "«" + l + "»"
}

// cityHint es la localidad de una dirección («Calle Mayor 3, 28013 Madrid» →
// «Madrid»), para que el modelo sepa por dónde cae sin ver la dirección.
func cityHint(addr string) string {
	parts := strings.Split(addr, ",")
	if len(parts) < 2 {
		return ""
	}
	last := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == '»' || r == ':' {
			return -1
		}
		return r
	}, parts[len(parts)-1]))
	if last == "" || len([]rune(last)) > 30 {
		return ""
	}
	return last
}

// hideText cambia por etiquetas lo que se reconoce en un texto libre.
func (p *pseudonyms) hideText(ctx context.Context, s string) string {
	repl := func(re *regexp.Regexp, kind string, ok func(string) bool) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if ok != nil && !ok(m) {
				return m
			}
			return p.tag(ctx, kind, m)
		})
	}
	repl(reMaps, "ENLACE", nil)
	repl(reEmail, "EMAIL", nil)
	repl(reIBAN, "IBAN", validIBAN)
	repl(reCard, "TARJETA", luhn)
	repl(reDNI, "DNI", validDNI)
	repl(rePlate, "MATRICULA", nil)
	repl(rePhone, "TEL", phoneLike)
	return s
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// phoneLike: un móvil o fijo de España (9 cifras que empiezan por 6-9) o un
// número internacional con prefijo; así no se confunde con fechas o importes.
func phoneLike(m string) bool {
	d := digits(m)
	t := strings.TrimSpace(m)
	if strings.HasPrefix(t, "+") || strings.HasPrefix(t, "00") {
		return len(d) >= 9 && len(d) <= 15
	}
	if len(d) == 11 && strings.HasPrefix(d, "34") {
		d = d[2:]
	}
	return len(d) == 9 && strings.ContainsRune("6789", rune(d[0]))
}

func luhn(m string) bool {
	d := digits(m)
	if len(d) < 13 || len(d) > 19 {
		return false
	}
	sum := 0
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if (len(d)-1-i)%2 == 1 {
			if n *= 2; n > 9 {
				n -= 9
			}
		}
		sum += n
	}
	return sum%10 == 0
}

func validIBAN(m string) bool {
	s := strings.ReplaceAll(m, " ", "")
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	var b strings.Builder
	for _, r := range s[4:] + s[:4] {
		if r >= 'A' && r <= 'Z' {
			fmt.Fprintf(&b, "%d", r-'A'+10)
		} else {
			b.WriteRune(r)
		}
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

func validDNI(m string) bool {
	s := strings.ReplaceAll(m, "-", "")
	num := strings.NewReplacer("X", "0", "Y", "1", "Z", "2").Replace(s[:len(s)-1])
	n, ok := new(big.Int).SetString(num, 10)
	return ok && "TRWAGMYFPDXBNJZSQVHLCKE"[new(big.Int).Mod(n, big.NewInt(23)).Int64()] == s[len(s)-1]
}

// hideJSON oculta los datos de lo que devuelve una herramienta: por el
// nombre del campo y, en todos los textos, por lo que se reconoce.
func (p *pseudonyms) hideJSON(ctx context.Context, s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return p.hideText(ctx, s)
	}
	v = p.hideValue(ctx, v, "")
	b, err := json.Marshal(v)
	if err != nil {
		return p.hideText(ctx, s)
	}
	return string(b)
}

func (p *pseudonyms) hideValue(ctx context.Context, v any, kind string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			sub := piiFields[k]
			if k == "valor" {
				sub = kind
			}
			x[k] = p.hideValue(ctx, val, sub)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = p.hideValue(ctx, val, kind)
		}
		return x
	case string:
		// Un lugar sin números («Oficina», «Club») no es una dirección.
		if x != "" && kind != "" && (kind != "DIR" || strings.IndexFunc(x, unicode.IsDigit) >= 0) {
			return p.tag(ctx, kind, x)
		}
		return p.hideText(ctx, x)
	}
	return v
}

// reveal pone los datos reales en lugar de las etiquetas que conozca.
func (p *pseudonyms) reveal(ctx context.Context, s string) string {
	return reLabel.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLabel.FindStringSubmatch(m)
		l := sub[1] + sub[2]
		if v, ok := p.value(ctx, l); ok {
			return v
		}
		return m
	})
}

// revealJSON hace reveal en los textos de unos argumentos JSON.
func (p *pseudonyms) revealJSON(ctx context.Context, s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				x[k] = walk(val)
			}
		case []any:
			for i, val := range x {
				x[i] = walk(val)
			}
		case string:
			return p.reveal(ctx, x)
		}
		return v
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return s
	}
	return string(b)
}

// prunePseudonyms borra las etiquetas que no se usan desde hace un mes (lo
// mismo que dura la conversación guardada).
func (s store) prunePseudonyms(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM pseudonyms WHERE used_at < $1`, time.Now().Add(-keepTurns))
	return err
}
