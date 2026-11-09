package main

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"homeserver/platform/seal"
)

// maxTools es el máximo de herramientas por petición: Azure OpenAI (y la API
// de OpenAI) rechazan más de 128 con un 400.
const maxTools = 128

// appWords son palabras (sin tildes, en minúsculas; valen como comienzo de
// palabra) que hacen pensar en una app. El nombre de la app y el comienzo del
// nombre de sus herramientas («pelis_lista» → «pelis») cuentan también.
var appWords = map[string][]string{
	"agenda":   {"agenda", "evento", "cita", "reunion", "calendario", "tarea", "recuerda", "recordatorio", "avisa", "correo", "email", "mail", "contacto", "telefono", "cumple", "rutina", "hoy", "manana", "semana", "lunes", "martes", "miercoles", "jueves", "viernes", "sabado", "domingo", "resumen", "hueco", "envio", "paquete", "pedido"},
	"entreno":  {"entreno", "entrena", "gimnasio", "gym", "ejercicio", "press", "sentadilla", "peso", "kg", "rir", "cardio", "correr", "comida", "comido", "cena", "desayuno", "almuerzo", "kcal", "caloria", "proteina", "macros", "medida", "nutricion"},
	"compra":   {"compra", "lista", "super", "mercadona", "leche", "huevos", "pan", "comprar"},
	"papeles":  {"papeles", "documento", "dni", "pasaporte", "carne", "nie", "tie", "itv", "seguro", "garantia", "contrato", "caduca", "vence", "renovar"},
	"libros":   {"libro", "leyendo", "novela", "autor", "capitulo", "comic", "manga", "manhwa", "biblioteca", "isbn"},
	"noticias": {"noticia", "titular", "prensa", "periodico", "actualidad"},
	"cartera":  {"cartera", "accion", "acciones", "etf", "fondo", "cripto", "bitcoin", "btc", "eth", "kraken", "binance", "degiro", "inversion", "cotiza", "bolsa"},
	"gastos":   {"gasto", "gastado", "gaste", "pague", "pagado", "ingreso", "cobro", "suscripcion", "presupuesto", "dinero", "euros", "nomina", "factura"},
	"casa":     {"casa", "luz", "electricidad", "kwh", "pvpc", "lavadora", "mantenimiento", "caldera", "filtro", "contador", "lectura", "agua", "gas", "consumo"},
	"coche":    {"coche", "vehiculo", "gasolina", "gasoil", "diesel", "repost", "gasolinera", "litros", "km", "kilometros", "taller", "aceite", "neumatico", "itv"},
	"habitos":  {"habito", "racha", "diario", "animo", "energia", "dormido", "sueno", "medita", "vasos", "hecho"},
	"leer":     {"leer", "articulo", "enlace", "link", "http", "https", "www", "guarda"},
	"series":   {"serie", "episodio", "temporada", "capitulo", "peli", "pelicula", "juego", "juegos", "estreno", "netflix", "hbo", "visto", "ver"},
}

// appOrder es el orden de reserva cuando no hay pistas: de más a menos usada.
var appOrder = []string{"agenda", "entreno", "compra", "papeles", "gastos", "habitos", "cartera", "casa", "coche", "series", "leer", "libros", "noticias"}

var reUsedTools = regexp.MustCompile(`\[Herramientas:([^\]\n]*)\]`)

// pickTools deja como mucho maxTools herramientas: todas las propias del
// asistente y, de las apps, primero las que pegan con lo que se habla (el
// mensaje, los últimos de la persona y las herramientas usadas hace poco) y
// luego las demás por orden, siempre la app entera o nada. Con pocas
// herramientas, todas.
func pickTools(tools []tool, text string, recent []string) []tool {
	if len(tools) <= maxTools {
		return tools
	}
	byApp := map[string][]tool{}
	var out []tool
	var apps []string
	for _, t := range tools {
		if t.app == "" {
			out = append(out, t)
			continue
		}
		if _, ok := byApp[t.app]; !ok {
			apps = append(apps, t.app)
		}
		byApp[t.app] = append(byApp[t.app], t)
	}
	score := map[string]int{}
	words := seal.Words(text)
	var older []string
	for _, r := range recent {
		older = append(older, seal.Words(reUsedTools.ReplaceAllString(r, ""))...)
	}
	for _, app := range apps {
		keys := append([]string{app}, appWords[app]...)
		for _, t := range byApp[app] {
			if p, _, ok := strings.Cut(t.def.Function.Name, "_"); ok && len(p) >= 3 {
				keys = append(keys, p)
			}
		}
		score[app] = 3*matches(words, keys) + matches(older, keys)
	}
	// Lo usado en los últimos turnos sigue a mano para «bórralo», «cámbialo»…
	for _, r := range recent {
		for _, m := range reUsedTools.FindAllStringSubmatch(r, -1) {
			for _, name := range strings.Split(m[1], ",") {
				name = strings.TrimSpace(name)
				for _, app := range apps {
					if slices.ContainsFunc(byApp[app], func(t tool) bool { return t.def.Function.Name == name }) {
						score[app] += 2
					}
				}
			}
		}
	}
	rank := func(app string) int {
		if i := slices.Index(appOrder, app); i >= 0 {
			return i
		}
		return len(appOrder)
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if score[apps[i]] != score[apps[j]] {
			return score[apps[i]] > score[apps[j]]
		}
		return rank(apps[i]) < rank(apps[j])
	})
	for _, app := range apps {
		if len(out)+len(byApp[app]) <= maxTools {
			out = append(out, byApp[app]...)
		}
	}
	return out
}

// matches cuenta las palabras que empiezan por alguna clave.
func matches(words, keys []string) int {
	n := 0
	for _, w := range words {
		for _, k := range keys {
			if strings.HasPrefix(w, k) {
				n++
				break
			}
		}
	}
	return n
}
