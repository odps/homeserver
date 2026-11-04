package main

import (
	"strings"

	"homeserver/platform/seal"
)

// Secciones del súper y cómo se decide la de cada cosa: con un diccionario de
// palabras (sin tildes, singular y plural valen igual), sin IA. Lo que la
// persona cambia a mano se recuerda aparte (section_prefs).

type section struct {
	Key   string
	Label string
}

// sections es el orden de siempre; cada persona puede cambiarlo.
var sections = []section{
	{"fruta", "Fruta y verdura"},
	{"carne", "Carne"},
	{"pescado", "Pescado"},
	{"charcuteria", "Charcutería y quesos"},
	{"lacteos", "Lácteos y huevos"},
	{"panaderia", "Panadería"},
	{"despensa", "Despensa"},
	{"conservas", "Conservas"},
	{"congelados", "Congelados"},
	{"bebidas", "Bebidas"},
	{"desayuno", "Desayuno y dulces"},
	{"limpieza", "Limpieza"},
	{"higiene", "Higiene"},
	{"mascotas", "Mascotas"},
	{"otros", "Otros"},
}

const otherSection = "otros"

var sectionLabels = func() map[string]string {
	m := map[string]string{}
	for _, s := range sections {
		m[s.Key] = s.Label
	}
	return m
}()

func validSection(k string) bool { return sectionLabels[k] != "" }

func sectionLabel(k string) string {
	if l := sectionLabels[k]; l != "" {
		return l
	}
	return sectionLabels[otherSection]
}

// sectionByName acepta la clave o el nombre visible («Lácteos y huevos»).
func sectionByName(s string) (string, bool) {
	f := strings.Join(seal.Words(s), " ")
	for _, x := range sections {
		if f == x.Key || f == strings.Join(seal.Words(x.Label), " ") {
			return x.Key, true
		}
	}
	return "", false
}

// normalizeOrder deja un orden válido: las claves conocidas en el orden
// guardado, sin repetir, y al final las que falten en el orden de siempre.
func normalizeOrder(saved []string) []string {
	out := make([]string, 0, len(sections))
	seen := map[string]bool{}
	for _, k := range saved {
		if validSection(k) && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, s := range sections {
		if !seen[s.Key] {
			out = append(out, s.Key)
		}
	}
	return out
}

func defaultOrder() []string { return normalizeOrder(nil) }

// ---- Nombres normalizados ----

// stopwords no cuentan para reconocer ni para comparar nombres: «pan de
// molde» y «pan molde» son lo mismo. «sin» sí cuenta (cerveza sin).
var stopwords = map[string]bool{"de": true, "del": true, "para": true, "con": true, "en": true, "el": true, "la": true,
	"los": true, "las": true, "al": true, "a": true, "y": true, "e": true, "un": true, "una": true, "o": true}

// stem lleva una palabra (ya sin tildes ni mayúsculas) a una forma común al
// singular y al plural. No tiene por qué ser una palabra de verdad, solo
// coincidir: «tomates» y «tomate» → «tomate»; «limones» y «limón» → «limon»;
// «nueces» y «nuez» → «nuez»; «carnes» y «carne» → «carn».
func stem(w string) string {
	r := []rune(w)
	if len(r) <= 3 {
		return w
	}
	if r[len(r)-1] == 's' {
		r = r[:len(r)-1]
	}
	n := len(r)
	switch {
	case n >= 3 && r[n-2] == 'c' && r[n-1] == 'e':
		r = append(r[:n-2], 'z')
	case n >= 4 && r[n-1] == 'e' && strings.ContainsRune("lnrdj", r[n-2]):
		r = r[:n-1]
	}
	return string(r)
}

// keyWords son las palabras de un nombre que cuentan, normalizadas.
func keyWords(name string) []string {
	var out []string
	for _, w := range seal.Words(name) {
		if stopwords[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

// nameKey es el nombre normalizado: lo que decide si dos cosas son la misma
// (y su huella, la fila del historial).
func nameKey(name string) string { return strings.Join(keyWords(name), " ") }

// ---- Diccionario ----

// dictionary: por sección, palabras y expresiones en singular (las tildes y
// los plurales dan igual). Gana la expresión más larga que aparezca en el
// nombre; a igual longitud, la que va antes («zumo de naranja» → bebidas).
var dictionary = map[string][]string{
	"fruta": {"fruta", "verdura", "hortaliza", "manzana", "pera", "plátano", "banana", "naranja", "mandarina", "clementina", "limón", "lima",
		"pomelo", "uva", "fresa", "fresón", "frambuesa", "arándano", "mora", "cereza", "melocotón", "nectarina", "paraguayo", "albaricoque",
		"ciruela", "kiwi", "piña", "mango", "papaya", "melón", "sandía", "higo", "granada", "caqui", "chirimoya", "aguacate", "coco",
		"tomate", "tomate cherry", "lechuga", "escarola", "canónigo", "rúcula", "espinaca", "acelga", "col", "repollo", "lombarda",
		"coliflor", "brócoli", "romanesco", "alcachofa", "espárrago", "espárrago verde", "judía verde", "guisante", "haba", "calabacín",
		"berenjena", "pimiento", "pimiento rojo", "pimiento verde", "pepino", "zanahoria", "cebolla", "cebolleta", "chalota", "puerro", "ajo",
		"ajo tierno", "patata", "boniato", "batata", "calabaza", "nabo", "rábano", "remolacha", "apio", "champiñón", "seta", "perejil",
		"cilantro", "albahaca", "hierbabuena", "menta", "jengibre", "ensalada", "brote", "endibia", "maíz en mazorca"},
	"carne": {"carne", "pollo", "pechuga", "pechuga de pollo", "muslo", "contramuslo", "alita", "ternera", "filete", "solomillo",
		"entrecot", "chuleta", "chuletón", "cerdo", "lomo", "costilla", "secreto", "presa", "pluma", "cordero", "conejo", "pavo",
		"carne picada", "picada", "hamburguesa", "salchicha", "albóndiga", "carrillera", "morcillo", "aguja", "panceta", "codorniz",
		"pato", "hígado", "osobuco", "redondo", "escalope", "churrasco", "pinchito", "brocheta"},
	"pescado": {"pescado", "marisco", "merluza", "bacalao", "salmón", "atún fresco", "bonito", "emperador", "pez espada", "dorada",
		"lubina", "sardina", "boquerón", "gamba", "langostino", "mejillón", "almeja", "calamar", "sepia", "pulpo", "rape", "rodaballo",
		"lenguado", "trucha", "caballa", "jurel", "pescadilla", "palometa", "gallo", "surimi", "chipirón", "navaja", "cigala",
		"salmón ahumado"},
	"charcuteria": {"embutido", "fiambre", "jamón", "jamón serrano", "jamón ibérico", "jamón york", "jamón cocido", "chorizo",
		"salchichón", "fuet", "lomo embuchado", "mortadela", "salami", "pavo en lonchas", "pechuga de pavo", "sobrasada", "morcilla",
		"butifarra", "chistorra", "lacón", "cecina", "bacon", "beicon", "queso", "queso rallado", "queso curado", "queso semicurado",
		"queso tierno", "queso de cabra", "queso en lonchas", "mozzarella", "parmesano", "manchego", "brie", "camembert", "emmental",
		"gouda", "cheddar", "paté", "foie", "loncha"},
	"lacteos": {"leche", "leche entera", "leche desnatada", "leche semidesnatada", "leche sin lactosa", "yogur", "yogurt",
		"yogur griego", "bífidus", "kéfir", "skyr", "nata", "nata para cocinar", "mantequilla", "margarina", "cuajada", "natillas",
		"flan", "arroz con leche", "huevo", "huevo campero", "batido", "requesón", "queso fresco", "queso de burgos", "queso batido",
		"petit suisse", "bebida de avena", "bebida de soja", "bebida de almendra", "bebida vegetal", "postre", "gelatina"},
	"panaderia": {"pan", "barra", "barra de pan", "baguette", "chapata", "pan de molde", "pan integral", "pan sin gluten", "panecillo",
		"pan de hamburguesa", "pan de perrito", "pan tostado", "pan de pita", "bollo", "croissant", "cruasán", "napolitana",
		"ensaimada", "picos", "regaña", "colines", "tortilla de trigo", "wrap", "empanada", "hogaza", "mollete"},
	"despensa": {"arroz", "pasta", "macarrón", "espagueti", "tallarín", "fideo", "lasaña", "canelón", "tortellini", "harina", "azúcar",
		"sal", "pimienta", "aceite", "aceite de oliva", "aceite de girasol", "vinagre", "legumbre", "lenteja", "garbanzo", "alubia",
		"judía", "quinoa", "cuscús", "pan rallado", "levadura", "especia", "orégano", "comino", "pimentón", "canela", "curry", "caldo",
		"pastilla de caldo", "tomate frito", "salsa", "salsa de tomate", "mayonesa", "ketchup", "mostaza", "salsa de soja",
		"frutos secos", "almendra", "nuez", "cacahuete", "pistacho", "avellana", "pipa", "patatas fritas", "snack", "palomitas",
		"maicena", "sopa", "puré", "puré de patata", "crema de verduras", "nachos", "edulcorante", "leche de coco", "tahini", "hummus",
		"bicarbonato", "picatostes", "fideos chinos", "noodles"},
	"conservas": {"conserva", "lata", "atún", "atún en aceite", "atún al natural", "sardinas en lata", "berberecho", "anchoa", "melva",
		"bonito en aceite", "caballa en aceite", "mejillones en escabeche", "pimiento del piquillo", "espárrago blanco", "maíz",
		"maíz dulce", "garbanzos cocidos", "lentejas cocidas", "tomate triturado", "tomate natural", "tomate en lata", "aceituna",
		"pepinillo", "alcaparra", "pisto", "fabada", "melocotón en almíbar", "piña en almíbar", "paté de atún", "cebolla frita"},
	"congelados": {"congelado", "congelada", "helado", "pizza", "hielo", "croqueta", "empanadilla", "nugget", "varitas de merluza",
		"palitos de pescado", "verdura congelada", "patatas congeladas", "san jacobo", "lasaña congelada", "gyozas", "polo"},
	"bebidas": {"bebida", "agua", "agua con gas", "agua mineral", "refresco", "coca cola", "cocacola", "fanta", "sprite", "tónica",
		"gaseosa", "zumo", "néctar", "cerveza", "cerveza sin", "cerveza sin alcohol", "vino", "vino tinto", "vino blanco", "cava",
		"sidra", "vermut", "whisky", "ginebra", "ron", "vodka", "licor", "isotónica", "aquarius", "nestea", "té frío", "kombucha",
		"bebida energética", "red bull", "horchata", "mosto", "tinto de verano"},
	"desayuno": {"café", "café molido", "café en grano", "café soluble", "cápsula", "cápsulas de café", "descafeinado", "té", "té verde",
		"infusión", "manzanilla", "poleo", "tila", "cacao", "colacao", "cola cao", "nesquik", "cereales", "muesli", "granola", "avena",
		"copos de avena", "galleta", "magdalena", "bizcocho", "mermelada", "miel", "crema de cacahuete", "nocilla", "nutella",
		"chocolate", "bombón", "caramelo", "chuche", "chicle", "turrón", "polvorón", "donut", "barrita", "biscote", "tortita",
		"dulce", "golosina", "cruasán de chocolate", "palmera", "sobao"},
	"limpieza": {"lejía", "detergente", "suavizante", "lavavajillas", "gel lavavajillas", "pastillas lavavajillas", "sal lavavajillas",
		"friegasuelos", "limpiacristales", "limpiahogar", "amoniaco", "quitagrasas", "desengrasante", "estropajo", "bayeta", "fregona",
		"escoba", "recogedor", "bolsa de basura", "bolsas basura", "papel higiénico", "papel de cocina", "servilleta",
		"papel de aluminio", "papel albal", "papel film", "film transparente", "papel de horno", "ambientador", "insecticida",
		"guantes de fregar", "quitamanchas", "limpiador", "pila", "bombilla", "vela", "cerilla", "mechero"},
	"higiene": {"champú", "gel", "gel de ducha", "jabón", "jabón de manos", "desodorante", "pasta de dientes", "dentífrico",
		"cepillo de dientes", "enjuague bucal", "colutorio", "hilo dental", "seda dental", "compresa", "tampón", "salvaslip", "pañal",
		"toallita", "toallitas húmedas", "maquinilla", "cuchilla de afeitar", "espuma de afeitar", "crema hidratante", "crema de manos",
		"protector solar", "crema solar", "bastoncillo", "algodón", "discos desmaquillantes", "colonia", "perfume", "laca", "gomina",
		"acondicionador", "mascarilla", "pañuelos", "pañuelos de papel", "preservativo", "tirita", "cuchilla", "lima de uñas"},
	"mascotas": {"mascota", "pienso", "comida para perro", "comida para gato", "arena para gato", "arena", "perro", "gato",
		"premio para perro", "snack para perro", "alpiste", "lata para gato", "comida de gato", "comida de perro"},
}

// dict es el diccionario ya normalizado: expresión → sección. maxPhrase es
// la expresión más larga (en palabras).
var dict, maxPhrase = func() (map[string]string, int) {
	m := map[string]string{}
	longest := 1
	for sec, words := range dictionary {
		for _, w := range words {
			k := keyWords(w)
			m[strings.Join(k, " ")] = sec
			longest = max(longest, len(k))
		}
	}
	return m, longest
}()

// guessSection decide la sección por el diccionario («otros» si no sabe).
// «congelado» en el nombre manda siempre: «guisantes congelados».
func guessSection(name string) string {
	words := keyWords(name)
	for _, w := range words {
		if w == "congelado" || w == "congelada" {
			return "congelados"
		}
	}
	for n := min(maxPhrase, len(words)); n >= 1; n-- {
		for i := 0; i+n <= len(words); i++ {
			if sec, ok := dict[strings.Join(words[i:i+n], " ")]; ok {
				return sec
			}
		}
	}
	return otherSection
}
