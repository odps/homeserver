package main

import (
	"context"
	"strings"
	"testing"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

func TestPseudonyms(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.Load("")
	st := store{dbtest.MigratedSteps(t, migrations, steps(box)), box}
	p := newPseudonyms(st, 42, false)

	in := "Llama al 612 345 678 o al +44 20 7946 0958, escribe a Ana.Ruiz@correo.es. IBAN ES91 2100 0418 4502 0005 1332, " +
		"tarjeta 4111 1111 1111 1111, DNI 12345678Z, NIE X1234567L, coche 1234 BCD. " +
		"Reunión el 2026-11-04 a las 17:30, 1.250 € y pedido 12345678; DNI malo 12345678A."
	out := p.hideText(ctx, in)
	for _, leak := range []string{"612 345", "7946", "Ana.Ruiz", "ES91", "4111", "12345678Z", "X1234567L", "1234 BCD"} {
		if strings.Contains(out, leak) {
			t.Errorf("se ve %q: %s", leak, out)
		}
	}
	for _, keep := range []string{"2026-11-04", "17:30", "1.250 €", "pedido 12345678", "12345678A"} {
		if !strings.Contains(out, keep) {
			t.Errorf("se ha tapado %q, que no es un dato personal: %s", keep, out)
		}
	}
	if back := p.reveal(ctx, out); back != in {
		t.Fatalf("al revés:\n%s\n%s", back, in)
	}

	// El mismo dato, la misma etiqueta, también en otra conversación del mismo
	// chat (sale de la base, cifrado).
	again := newPseudonyms(st, 42, false)
	if a, b := p.hideText(ctx, "612345678"), again.hideText(ctx, "612 34 56 78"); a != b || a != "«TEL_1»" {
		t.Fatalf("etiquetas: %q %q", a, b)
	}
	if v := again.reveal(ctx, "llama a TEL_1 o «TEL_1»"); v != "llama a 612 34 56 78 o 612 34 56 78" { // con el formato con que lo vio esta conversación
		t.Fatalf("reveal desde la base: %q", v)
	}
	var plain int
	st.db.QueryRow(ctx, `SELECT count(*) FROM pseudonyms WHERE position('612'::bytea in sealed) > 0`).Scan(&plain)
	if plain != 0 {
		t.Fatal("la tabla guarda el dato en claro")
	}
	if other := newPseudonyms(st, 7, false); other.reveal(ctx, "«TEL_1»") != "«TEL_1»" {
		t.Fatal("otro chat ve las etiquetas de este")
	}

	// Resultados de herramientas: por campo, y los lugares sin número se quedan.
	js := p.hideJSON(ctx, `{"elementos":[{"titulo":"Dentista","lugar":"Calle Pez 3, 28013 Madrid","mapa":"https://www.google.com/maps/search/?api=1&query=Calle+Pez+3"},`+
		`{"titulo":"Trabajo","lugar":"Oficina"}],"cumpleanos":"1990-11-05"}`)
	if strings.Contains(js, "Pez") || strings.Contains(js, "1990") || !strings.Contains(js, "«DIR_1:Madrid»") || !strings.Contains(js, `"Oficina"`) ||
		!strings.Contains(js, "«ENLACE_1»") || !strings.Contains(js, "Dentista") {
		t.Fatalf("JSON: %s", js)
	}
	args := p.revealJSON(ctx, `{"destino":"«DIR_1:Madrid»","texto":"cita en DIR_1"}`)
	if !strings.Contains(args, `"destino":"Calle Pez 3, 28013 Madrid"`) || !strings.Contains(args, "cita en Calle Pez 3") {
		t.Fatalf("argumentos: %s", args)
	}

	// Sin base (resúmenes internos): en memoria.
	m := newPseudonyms(st, 0, true)
	if h := m.hideText(ctx, "De: x@y.es"); h != "De: «EMAIL_1»" || m.reveal(ctx, h) != "De: x@y.es" {
		t.Fatalf("en memoria: %q", h)
	}
}
