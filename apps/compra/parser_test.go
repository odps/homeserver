package main

import (
	"strings"
	"testing"
)

func show(list []parsed) string {
	var out []string
	for _, p := range list {
		s := p.Name
		if p.Qty != "" {
			s += " [" + p.Qty + "]"
		}
		if p.Note != "" {
			s += " (" + p.Note + ")"
		}
		out = append(out, s)
	}
	return strings.Join(out, " | ")
}

func TestParseAdd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"leche, 2 kg de patatas, huevos y pan", "leche | patatas [2 kg] | huevos | pan"},
		{"leche", "leche"},
		{"Leche\nhuevos\n\npan", "Leche | huevos | pan"},
		{"leche; pan", "leche | pan"},
		{"leche, huevos, y pan", "leche | huevos | pan"},
		{"sal y pimienta", "sal | pimienta"},
		{"uvas e higos", "uvas | higos"},
		{"agua e hielo", "agua | hielo"},
		{"vitamina e", "vitamina e"},
		// Cantidad delante.
		{"2kg patatas", "patatas [2 kg]"},
		{"2 kg patatas", "patatas [2 kg]"},
		{"1,5 l de leche", "leche [1,5 l]"},
		{"1.5 litros de leche", "leche [1,5 l]"},
		{"500 gr de harina", "harina [500 g]"},
		{"500g harina", "harina [500 g]"},
		{"250 gramos de queso rallado", "queso rallado [250 g]"},
		{"6 huevos", "huevos [6]"},
		{"x6 cervezas", "cervezas [x6]"},
		{"6x cervezas", "cervezas [x6]"},
		{"x 6 cervezas", "cervezas [x6]"},
		{"dos litros de leche", "leche [2 l]"},
		{"un kilo de naranjas", "naranjas [1 kg]"},
		{"medio kilo de carne picada", "carne picada [500 g]"},
		{"un kilo y medio de patatas", "patatas [1,5 kg]"},
		{"2 kilos y medio de patatas", "patatas [2,5 kg]"},
		{"una docena de huevos", "huevos [12]"},
		{"media docena de huevos", "huevos [6]"},
		{"2 docenas de huevos", "huevos [24]"},
		{"una lata de atún", "atún [1 lata]"},
		{"3 latas de atún", "atún [3 latas]"},
		{"2 paquetes de pasta", "pasta [2 paquetes]"},
		{"un cartón de leche", "leche [1 carton]"},
		{"2 bricks de leche", "leche [2 briks]"},
		{"una lechuga", "lechuga"},
		{"un melón", "melón"},
		{"dos aguacates", "aguacates [2]"},
		{"medio melón", "melón [½]"},
		{"33 cl de cerveza", "cerveza [330 ml]"},
		// Cantidad detrás.
		{"patatas 2 kg", "patatas [2 kg]"},
		{"patatas 2kg", "patatas [2 kg]"},
		{"leche x6", "leche [x6]"},
		{"huevos x 12", "huevos [x12]"},
		{"cerveza 12x", "cerveza [x12]"},
		{"pan 2", "pan [2]"},
		{"agua 6 l", "agua [6 l]"},
		{"patatas de 2 kg", "patatas [2 kg]"},
		{"huevos: 12", "huevos [12]"},
		{"patatas: un saco", "patatas [un saco]"},
		{"tomates: 1 kg", "tomates [1 kg]"},
		// Notas y nombres que no son cantidades.
		{"leche (sin lactosa)", "leche (sin lactosa)"},
		{"2 l de leche (sin lactosa), pan", "leche [2 l] (sin lactosa) | pan"},
		{"7up", "7up"},
		{"atún 3x80", "atún 3x80"},
		{"huevos L", "huevos L"},
		{"cerveza sin", "cerveza sin"},
		{"Coca cola zero 2 l", "Coca cola zero [2 l]"},
		// Verbos y viñetas.
		{"añade leche y huevos a la lista", "leche | huevos"},
		{"Apunta: pan, leche", "pan | leche"},
		{"- leche\n- pan\n* huevos\n1. yogures\n2) lejía", "leche | pan | huevos | yogures | lejía"},
		{"leche.", "leche"},
		// Nada que añadir.
		{"", ""},
		{" , y ,, ", ""},
		{"3", ""},
		{"2 kg", ""},
	}
	for _, c := range cases {
		if got := show(parseAdd(c.in)); got != c.want {
			t.Errorf("%q:\n  da   %q\n  quiero %q", c.in, got, c.want)
		}
	}
}

func TestParseLimits(t *testing.T) {
	if n := len(parseAdd(strings.Repeat("leche, ", 200))); n != maxParts {
		t.Errorf("como mucho %d de una vez: %d", maxParts, n)
	}
	long := strings.Repeat("a", 300)
	if p := parseAdd(long); len(p) != 1 || len([]rune(p[0].Name)) != maxName {
		t.Errorf("nombre largo: %+v", p)
	}
	if p := parseAdd("pan: " + strings.Repeat("muchísimo ", 10)); len(p) != 1 || len([]rune(p[0].Qty)) > maxQty {
		t.Errorf("cantidad larga: %+v", p)
	}
}

func TestMergeQty(t *testing.T) {
	cases := []struct {
		had, add, want string
		ok             bool
	}{
		{"", "", "", false},
		{"2 kg", "", "2 kg", false},
		{"", "2 kg", "2 kg", true},
		{"1 kg", "500 g", "1,5 kg", true},
		{"500 g", "250 g", "750 g", true},
		{"1 l", "500 ml", "1,5 l", true},
		{"x6", "x6", "x12", true},
		{"6", "x6", "12", true},
		{"6", "6", "12", true},
		{"12", "media docena", "18", true},
		{"1 lata", "2 latas", "3 latas", true},
		{"2 kg", "x6", "2 kg", false},
		{"un saco", "2 kg", "un saco", false},
		{"1 lata", "1 paquete", "1 lata", false},
	}
	for _, c := range cases {
		got, ok := mergeQty(c.had, c.add)
		if got != c.want || ok != c.ok {
			t.Errorf("juntar %q + %q = %q %v; quiero %q %v", c.had, c.add, got, ok, c.want, c.ok)
		}
	}
}

func TestNameKey(t *testing.T) {
	same := [][2]string{
		{"tomates", "Tomate"}, {"limones", "limón"}, {"nueces", "nuez"}, {"carnes", "carne"}, {"Patatas", "patata"},
		{"huevos", "huevo"}, {"yogures", "yogur"}, {"panes", "pan"}, {"pan de molde", "Pan molde"}, {"lejías", "Lejía"},
		{"calabacines", "calabacín"}, {"champiñones", "champiñón"}, {"dulces", "dulce"}, {"arroces", "arroz"},
		{"papeles higiénicos", "papel higiénico"}, {"sales", "sal"}, {"ajos", "ajo"}, {"cereales", "cereal"},
	}
	for _, p := range same {
		if a, b := nameKey(p[0]), nameKey(p[1]); a != b {
			t.Errorf("%q → %q y %q → %q deberían ser iguales", p[0], a, p[1], b)
		}
	}
	differ := [][2]string{{"cerveza", "cerveza sin"}, {"leche", "leche de coco"}, {"pan", "pan rallado"}, {"tomate", "tomate frito"}}
	for _, p := range differ {
		if nameKey(p[0]) == nameKey(p[1]) {
			t.Errorf("%q y %q no deberían ser lo mismo", p[0], p[1])
		}
	}
}

func TestGuessSection(t *testing.T) {
	cases := map[string]string{
		"tomates": "fruta", "Tomate cherry": "fruta", "plátanos de Canarias": "fruta", "patatas": "fruta", "judías verdes": "fruta",
		"pechugas de pollo": "carne", "carne picada": "carne", "lomo": "carne", "lomo embuchado": "charcuteria",
		"merluza": "pescado", "salmón": "pescado", "atún fresco": "pescado", "atún": "conservas", "latas de atún": "conservas",
		"jamón serrano": "charcuteria", "queso": "charcuteria", "Queso rallado": "charcuteria", "queso fresco": "lacteos",
		"leche": "lacteos", "leche sin lactosa": "lacteos", "yogures griegos": "lacteos", "huevos": "lacteos", "Yogur de fresa": "lacteos",
		"pan": "panaderia", "pan de molde": "panaderia", "barra de pan": "panaderia", "pan rallado": "despensa",
		"arroz": "despensa", "macarrones": "despensa", "aceite de oliva": "despensa", "patatas fritas": "despensa", "tomate frito": "despensa",
		"leche de coco": "despensa", "tomate triturado": "conservas", "aceitunas": "conservas", "garbanzos cocidos": "conservas",
		"helado de chocolate": "congelados", "guisantes congelados": "congelados", "pizza": "congelados", "varitas de merluza": "congelados",
		"agua con gas": "bebidas", "zumo de naranja": "bebidas", "cervezas": "bebidas", "cerveza sin": "bebidas", "vino tinto": "bebidas",
		"café": "desayuno", "galletas": "desayuno", "cereales": "desayuno", "mermelada de fresa": "desayuno", "crema de cacahuete": "desayuno",
		"lejía": "limpieza", "detergente": "limpieza", "lavavajillas": "limpieza", "papel higiénico": "limpieza", "bolsas de basura": "limpieza",
		"sal para lavavajillas": "limpieza", "gel de ducha": "higiene", "pasta de dientes": "higiene", "champú": "higiene", "pañales": "higiene",
		"pienso": "mascotas", "comida para gato": "mascotas", "arena para gato": "mascotas", "sal": "despensa",
		"destornillador": "otros", "regalo cumpleaños": "otros",
	}
	for name, want := range cases {
		if got := guessSection(name); got != want {
			t.Errorf("%q → %s; quiero %s", name, got, want)
		}
	}
}

func TestDictionary(t *testing.T) {
	seen := map[string]string{}
	for sec, words := range dictionary {
		if !validSection(sec) {
			t.Errorf("sección desconocida %q", sec)
		}
		for _, w := range words {
			k := nameKey(w)
			if k == "" {
				t.Errorf("%q se queda sin palabras", w)
			}
			if other, ok := seen[k]; ok && other != sec {
				t.Errorf("%q está en %s y en %s", w, other, sec)
			}
			seen[k] = sec
		}
	}
	if o := normalizeOrder([]string{"limpieza", "nada", "limpieza", "fruta"}); len(o) != len(sections) || o[0] != "limpieza" || o[1] != "fruta" || o[2] != "carne" {
		t.Errorf("orden: %v", o)
	}
	if k, ok := sectionByName("Lácteos y huevos"); !ok || k != "lacteos" {
		t.Errorf("sección por nombre: %q %v", k, ok)
	}
}
