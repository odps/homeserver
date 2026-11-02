package main

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
)

// Contactos en vCard, con el mismo criterio que los eventos: se guarda la
// tarjeta tal cual (con su foto y lo que traiga del móvil) y se derivan los
// campos para buscar y mostrar.

type labeled struct {
	Label string `json:"etiqueta,omitempty"`
	Value string `json:"valor"`
	Maps  string `json:"mapa,omitempty"` // direcciones: enlace a Google Maps
}

type contact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"nombre"`
	Org       string    `json:"empresa,omitempty"`
	Role      string    `json:"cargo,omitempty"`
	Emails    []labeled `json:"correos,omitempty"`
	Phones    []labeled `json:"telefonos,omitempty"`
	Addresses []labeled `json:"direcciones,omitempty"`
	Birthday  string    `json:"cumpleanos,omitempty"` // 1990-05-17 o --05-17
	Notes     string    `json:"notas,omitempty"`
	Updated   time.Time `json:"-"`
}

func encodeCard(c vcard.Card) (string, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(c); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func decodeCard(data string) (vcard.Card, error) {
	return vcard.NewDecoder(strings.NewReader(data)).Decode()
}

var typeNames = map[string]string{
	"home": "casa", "work": "trabajo", "cell": "móvil", "mobile": "móvil", "voice": "", "pref": "",
	"internet": "", "fax": "fax", "other": "otro", "main": "principal",
}

func label(f *vcard.Field) string {
	var out []string
	for _, t := range f.Params.Types() {
		if n, ok := typeNames[strings.ToLower(t)]; ok {
			if n != "" {
				out = append(out, n)
			}
		} else if !strings.HasPrefix(strings.ToLower(t), "x-") {
			out = append(out, strings.ToLower(t))
		}
	}
	return strings.Join(out, ", ")
}

func addressText(a *vcard.Address) string {
	if l := a.Params.Get("LABEL"); l != "" {
		return strings.Join(strings.Fields(strings.ReplaceAll(l, "\n", ", ")), " ")
	}
	var parts []string
	for _, p := range []string{a.ExtendedAddress, a.StreetAddress, strings.TrimSpace(a.PostalCode + " " + a.Locality), a.Region, a.Country} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// mapsLink abre la dirección en Google Maps (sin clave: es un enlace normal).
func mapsLink(place string) string {
	if strings.TrimSpace(place) == "" {
		return ""
	}
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(place)
}

// routeLink abre en Google Maps la ruta hasta el destino (desde donde estés,
// si no hay origen).
func routeLink(from, to string) string {
	if strings.TrimSpace(to) == "" {
		return ""
	}
	v := url.Values{"api": {"1"}, "destination": {to}}
	if strings.TrimSpace(from) != "" {
		v.Set("origin", from)
	}
	return "https://www.google.com/maps/dir/?" + v.Encode()
}

func cardName(c vcard.Card) string {
	if fn := strings.TrimSpace(c.PreferredValue(vcard.FieldFormattedName)); fn != "" {
		return fn
	}
	if n := c.Name(); n != nil {
		return strings.TrimSpace(strings.Join(strings.Fields(n.GivenName+" "+n.AdditionalName+" "+n.FamilyName), " "))
	}
	if org := c.Value(vcard.FieldOrganization); org != "" {
		return strings.ReplaceAll(org, ";", " ")
	}
	return c.Value(vcard.FieldEmail)
}

func viewContact(id int64, c vcard.Card) contact {
	out := contact{ID: id, Name: cardName(c), Role: c.Value(vcard.FieldTitle), Notes: c.Value(vcard.FieldNote)}
	out.Org = strings.Trim(strings.ReplaceAll(c.Value(vcard.FieldOrganization), ";", ", "), ", ")
	for _, f := range c[vcard.FieldEmail] {
		out.Emails = append(out.Emails, labeled{Label: label(f), Value: f.Value})
	}
	for _, f := range c[vcard.FieldTelephone] {
		out.Phones = append(out.Phones, labeled{Label: label(f), Value: strings.TrimPrefix(f.Value, "tel:")})
	}
	for _, a := range c.Addresses() {
		if t := addressText(a); t != "" {
			out.Addresses = append(out.Addresses, labeled{Label: label(a.Field), Value: t, Maps: mapsLink(t)})
		}
	}
	out.Birthday = normalBirthday(c.Value(vcard.FieldBirthday))
	return out
}

// normalBirthday deja el cumpleaños como 1990-05-17 o --05-17 (sin año).
func normalBirthday(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if i := strings.IndexAny(v, "T"); i > 0 {
		v = v[:i]
	}
	if strings.HasPrefix(v, "--") {
		v = strings.TrimPrefix(v, "--")
		v = strings.ReplaceAll(v, "-", "")
		if len(v) == 4 {
			return "--" + v[:2] + "-" + v[2:]
		}
		return ""
	}
	v = strings.ReplaceAll(v, "-", "")
	if len(v) == 8 {
		return v[:4] + "-" + v[4:6] + "-" + v[6:]
	}
	return ""
}

// birthdayKey es MM-DD, para buscar los cumpleaños de un día.
func birthdayKey(b string) string {
	if len(b) >= 5 {
		return b[len(b)-5:]
	}
	return ""
}

func contactSearch(c contact) string {
	parts := []string{c.Name, c.Org, c.Role, c.Notes}
	for _, l := range [][]labeled{c.Emails, c.Phones, c.Addresses} {
		for _, v := range l {
			parts = append(parts, v.Value, strings.ReplaceAll(v.Value, " ", ""))
		}
	}
	// Se busca por huellas de palabras (enteras o por su comienzo): el
	// teléfono va también en cifras seguidas y sin el prefijo del país.
	for _, v := range c.Phones {
		d := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, v.Value)
		parts = append(parts, d)
		if len(d) > 9 {
			parts = append(parts, d[len(d)-9:])
		}
	}
	return fold(strings.Join(parts, " "))
}

// contactInput son los datos que se cambian desde la app o el asistente; lo
// demás de la tarjeta (foto, campos del móvil) se conserva. Las listas nil
// no se tocan; vacías, se borran.
type contactInput struct {
	Name      *string
	Org       *string
	Role      *string
	Emails    []string
	Phones    []string
	Addresses []string
	Birthday  *string
	Notes     *string
}

func newCard() vcard.Card {
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "3.0")
	c.SetValue(vcard.FieldUID, newUID())
	c.SetValue(vcard.FieldProductID, prodID)
	return c
}

func applyContact(c vcard.Card, in contactInput, now time.Time) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return errors.New("falta el nombre")
		}
		c.SetValue(vcard.FieldFormattedName, name)
		given, family, _ := strings.Cut(name, " ")
		c.SetName(&vcard.Name{GivenName: given, FamilyName: family})
	}
	if strings.TrimSpace(c.Value(vcard.FieldFormattedName)) == "" {
		return errors.New("falta el nombre")
	}
	set := func(field string, v *string) {
		if v == nil {
			return
		}
		if s := strings.TrimSpace(*v); s != "" {
			c.SetValue(field, s)
		} else {
			delete(c, field)
		}
	}
	set(vcard.FieldOrganization, in.Org)
	set(vcard.FieldTitle, in.Role)
	set(vcard.FieldNote, in.Notes)
	if in.Birthday != nil {
		b := normalBirthday(*in.Birthday)
		if strings.TrimSpace(*in.Birthday) != "" && b == "" {
			return errors.New("cumpleaños no válido: usa 1990-05-17 o --05-17")
		}
		set(vcard.FieldBirthday, &b)
	}
	list := func(field string, values []string, typ string) {
		if values == nil {
			return
		}
		delete(c, field)
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" {
				f := &vcard.Field{Value: v, Params: vcard.Params{}}
				if typ != "" {
					f.Params.Add(vcard.ParamType, typ)
				}
				c.Add(field, f)
			}
		}
	}
	list(vcard.FieldEmail, in.Emails, "")
	list(vcard.FieldTelephone, in.Phones, "")
	if in.Addresses != nil {
		delete(c, vcard.FieldAddress)
		for _, a := range in.Addresses {
			if a = strings.TrimSpace(a); a != "" {
				c.AddAddress(&vcard.Address{Field: &vcard.Field{Params: vcard.Params{}}, StreetAddress: strings.ReplaceAll(a, ";", ",")})
			}
		}
	}
	c.SetRevision(now.UTC())
	return nil
}
