//go:build ignore

// Genera datos/alimentos.json: una tabla pequeña de alimentos genéricos con
// nombre en español y sus valores por 100 g (o 100 ml si es líquido).
//
// Los valores salen de USDA FoodData Central, SR Legacy (dominio público).
// El mapeo nombre → fdc_id está hecho a mano aquí abajo; cuando USDA no
// tiene el alimento español se usa el más parecido y se dice al lado.
//
// Uso (desde apps/entreno): go run generar_alimentos.go [carpeta-csv]
//
// Sin carpeta, descarga el zip de CSV de USDA; con ella, lee food.csv y
// food_nutrient.csv de esa carpeta (el zip ya descomprimido).
package main

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const zipURL = "https://fdc.nal.usda.gov/fdc-datasets/FoodData_Central_sr_legacy_food_csv_2018-04.zip"

// Nutrientes de USDA (nutrient.csv).
const (
	nKcal     = "1008"
	nKcalAtw  = "2047" // Atwater general, si falta 1008
	nKcalAtw2 = "2048" // Atwater específico
	nProt     = "1003"
	nCarb     = "1005" // por diferencia
	nFat      = "1004"
	nFiber    = "1079"
)

type entrada struct {
	key, name string
	fdc       int
	aliases   []string
	unit      string
	unitG     float64
	liquid    bool
	def       bool
}

func a(key, name string, fdc int, aliases ...string) entrada {
	return entrada{key: key, name: name, fdc: fdc, aliases: aliases}
}

// u pone la unidad natural y sus gramos (ml si es líquido).
func (e entrada) u(unit string, g float64) entrada { e.unit, e.unitG = unit, g; return e }

// liq: se mide en ml (100 ml ≈ 100 g).
func (e entrada) liq() entrada { e.liquid = true; return e }

// d: la variante por defecto cuando comparte alias con otras.
func (e entrada) d() entrada { e.def = true; return e }

var tabla = []entrada{
	// ---- Carne, huevos y pescado ----
	a("pollo-pechuga-cruda", "Pechuga de pollo, cruda", 171077, "pollo", "pechuga de pollo", "pechuga", "pechuga de pollo cruda").u("filete", 150),
	a("pollo-pechuga-cocinada", "Pechuga de pollo, cocinada", 171477, "pollo", "pechuga de pollo", "pechuga", "pollo a la plancha", "pechuga a la plancha").u("filete", 120).d(),
	a("pollo-muslo-crudo", "Muslo de pollo sin piel, crudo", 173627, "muslo de pollo", "muslo", "contramuslo"),
	a("pollo-muslo-cocinado", "Muslo de pollo sin piel, cocinado", 172388, "muslo de pollo", "muslo", "contramuslo").d(),
	a("pavo-pechuga-cruda", "Pechuga de pavo, cruda", 171098, "pavo", "pechuga de pavo").u("filete", 150),
	a("pavo-pechuga-cocinada", "Pechuga de pavo, cocinada", 171496, "pavo", "pechuga de pavo", "pavo a la plancha").u("filete", 120).d(),
	a("pavo-fiambre", "Pechuga de pavo en lonchas (fiambre)", 172941, "fiambre de pavo", "pavo en lonchas", "embutido de pavo").u("loncha", 16),
	a("ternera-magra-cruda", "Ternera magra (filete), cruda", 174055, "ternera", "filete de ternera", "carne de ternera", "ternera magra").u("filete", 150),
	a("ternera-magra-cocinada", "Ternera magra (filete), cocinada", 174054, "ternera", "filete de ternera", "carne de ternera", "ternera magra", "ternera a la plancha").u("filete", 110).d(),
	a("carne-picada-5", "Carne picada de ternera 5 % grasa, cruda", 171790, "carne picada magra", "carne picada 5%", "picada 5%"),
	a("carne-picada-15", "Carne picada de ternera 15 % grasa, cruda", 171796, "carne picada", "picada", "carne picada de ternera", "carne picada 10%", "carne picada 15%", "carne picada 20%"),
	a("cerdo-lomo-crudo", "Lomo de cerdo, crudo", 168251, "lomo", "lomo de cerdo", "cinta de lomo", "cerdo", "chuleta de lomo").u("filete", 100),
	a("cerdo-lomo-cocinado", "Lomo de cerdo, cocinado", 168294, "lomo", "lomo de cerdo", "cinta de lomo", "cerdo", "chuleta de lomo", "lomo a la plancha").u("filete", 75).d(),
	a("cerdo-solomillo-crudo", "Solomillo de cerdo, crudo", 168249, "solomillo", "solomillo de cerdo"),
	a("cerdo-solomillo-cocinado", "Solomillo de cerdo, cocinado", 168250, "solomillo", "solomillo de cerdo").d(),
	a("conejo-crudo", "Conejo, crudo", 172521, "conejo"),
	a("conejo-cocinado", "Conejo, cocinado", 172522, "conejo", "conejo asado").d(),
	a("bacon", "Bacon (panceta curada), crudo", 168277, "bacon", "beicon", "panceta"),
	// USDA no tiene serrano: jamón curado en seco «country-style», solo magro.
	a("jamon-serrano", "Jamón serrano (magro)", 168282, "jamón serrano", "jamón", "jamón curado").u("loncha", 15),
	a("jamon-cocido", "Jamón cocido (york)", 173863, "jamón cocido", "jamón york", "york", "fiambre de jamón").u("loncha", 13),
	// El «chorizo» de USDA es el mexicano, fresco; para el curado, salami seco.
	a("chorizo", "Chorizo o salchichón curado", 172938, "chorizo", "chorizo curado", "salchichón", "fuet", "salami", "embutido"),
	a("salchichas", "Salchichas tipo Frankfurt", 173875, "salchicha", "salchichas", "frankfurt", "salchichas frankfurt").u("salchicha", 40),
	a("huevo", "Huevo, crudo", 171287, "huevo", "huevos", "huevo crudo", "huevo entero").u("huevo", 50),
	a("huevo-cocido", "Huevo cocido", 173424, "huevo cocido", "huevo duro", "huevos cocidos").u("huevo", 50),
	a("huevo-frito", "Huevo frito", 173423, "huevo frito", "huevos fritos").u("huevo", 46),
	a("clara", "Clara de huevo, cruda", 172183, "clara", "claras", "clara de huevo", "claras de huevo").u("clara", 33),
	a("atun-natural", "Atún al natural, escurrido", 171986, "atún", "atún al natural", "lata de atún", "atún en lata").u("lata", 56),
	a("atun-aceite", "Atún en aceite, escurrido", 173708, "atún en aceite", "atún en aceite de oliva").u("lata", 56),
	a("atun-fresco", "Atún fresco, crudo", 175159, "atún fresco", "bonito"),
	a("salmon-crudo", "Salmón, crudo", 175167, "salmón", "salmón crudo"),
	a("salmon-cocinado", "Salmón, cocinado", 175168, "salmón", "salmón a la plancha", "salmón al horno").d(),
	a("salmon-ahumado", "Salmón ahumado", 173687, "salmón ahumado"),
	// No hay merluza: bacalao del Atlántico, pescado blanco muy parecido.
	a("bacalao-crudo", "Merluza o bacalao fresco, crudo", 171955, "merluza", "bacalao", "bacalao fresco", "pescado blanco"),
	a("bacalao-cocinado", "Merluza o bacalao fresco, cocinado", 171956, "merluza", "bacalao", "bacalao fresco", "pescado blanco", "merluza a la plancha").d(),
	a("lubina", "Lubina o dorada, cruda", 175142, "lubina", "dorada"),
	a("sardinas-lata", "Sardinas en aceite (lata), escurridas", 175139, "sardinas", "sardinas en lata", "sardinillas", "sardina"),
	a("caballa-lata", "Caballa en lata, escurrida", 175121, "caballa", "caballa en lata"),
	a("gambas-crudas", "Gambas o langostinos, crudos", 175179, "gambas", "gamba", "langostinos", "langostino"),
	a("gambas-cocidas", "Gambas o langostinos, cocidos", 175180, "gambas", "gamba", "langostinos", "langostino", "gambas cocidas").d(),
	a("calamar", "Calamar, crudo", 174223, "calamar", "calamares", "chipirones"),
	a("mejillones", "Mejillones, cocidos", 174217, "mejillones", "mejillón"),
	a("palitos-surimi", "Palitos de surimi", 174203, "surimi", "palitos de surimi", "palitos de cangrejo"),
	a("proteina-whey", "Proteína de suero (whey) en polvo", 173180, "whey", "proteína", "proteína en polvo", "proteína whey", "batido de proteína", "batido de proteínas").u("cacito", 30),

	// ---- Lácteos ----
	a("leche-entera", "Leche entera", 171265, "leche entera").u("vaso", 250).liq(),
	// Semidesnatada española (~1,6 % grasa): la de 2 % de USDA.
	a("leche-semidesnatada", "Leche semidesnatada", 171267, "leche", "leche semidesnatada", "leche semi").u("vaso", 250).liq(),
	a("leche-desnatada", "Leche desnatada", 171269, "leche desnatada", "leche sin grasa").u("vaso", 250).liq(),
	a("batido-cacao", "Batido de cacao", 170880, "batido de cacao", "batido de chocolate", "leche con cacao").u("brik", 200).liq(),
	a("bebida-soja", "Bebida de soja", 172446, "bebida de soja", "leche de soja").u("vaso", 250).liq(),
	a("bebida-almendras", "Bebida de almendras sin azúcar", 174832, "bebida de almendras", "leche de almendras").u("vaso", 250).liq(),
	a("yogur-natural", "Yogur natural", 171284, "yogur", "yogur natural", "yogurt").u("yogur", 125),
	a("yogur-desnatado", "Yogur natural desnatado", 170887, "yogur desnatado", "yogur 0%", "yogur light").u("yogur", 125),
	a("yogur-griego", "Yogur griego natural", 171304, "yogur griego", "griego").u("yogur", 125),
	a("yogur-griego-0", "Yogur griego 0 % o skyr", 170894, "yogur griego 0%", "griego 0%", "yogur griego desnatado", "skyr", "yogur proteico").u("yogur", 125),
	// Sin «queso fresco batido»: requesón seco desnatado de USDA.
	a("queso-batido", "Queso fresco batido 0 %", 172181, "queso batido", "queso fresco batido", "queso batido 0%"),
	a("queso-cottage", "Queso cottage", 172179, "cottage", "queso cottage"),
	a("requeson", "Requesón o ricotta", 170851, "requesón", "ricotta"),
	// No hay manchego: cheddar, el curado de USDA que más se le parece.
	a("queso-curado", "Queso curado (tipo manchego)", 173414, "queso", "queso curado", "queso semicurado", "manchego", "queso manchego").u("loncha", 20),
	a("queso-parmesano", "Queso parmesano", 170848, "parmesano", "queso parmesano", "queso rallado"),
	a("mozzarella", "Mozzarella", 170845, "mozzarella", "queso mozzarella").u("bola", 125),
	a("queso-fresco", "Queso fresco (tipo Burgos)", 172223, "queso fresco", "queso de burgos", "burgos"),
	a("queso-crema", "Queso crema para untar", 173418, "queso crema", "queso de untar", "queso para untar"),
	a("nata", "Nata para montar", 170859, "nata", "nata para montar", "nata para cocinar").u("cucharada", 15).liq(),

	// ---- Cereales, pan y tubérculos ----
	a("arroz-blanco-crudo", "Arroz blanco, crudo", 168877, "arroz", "arroz blanco", "arroz crudo"),
	a("arroz-blanco-cocido", "Arroz blanco, cocido", 168878, "arroz", "arroz blanco", "arroz cocido", "arroz hervido").d(),
	a("arroz-integral-crudo", "Arroz integral, crudo", 169703, "arroz integral"),
	a("arroz-integral-cocido", "Arroz integral, cocido", 169704, "arroz integral", "arroz integral cocido").d(),
	a("pasta-cruda", "Pasta, cruda", 169736, "pasta", "macarrones", "espaguetis", "fideos", "tallarines"),
	a("pasta-cocida", "Pasta, cocida", 169737, "pasta", "macarrones", "espaguetis", "fideos", "tallarines", "pasta cocida").d(),
	a("pasta-integral-cruda", "Pasta integral, cruda", 169738, "pasta integral", "macarrones integrales", "espaguetis integrales"),
	a("pasta-integral-cocida", "Pasta integral, cocida", 168910, "pasta integral", "macarrones integrales", "espaguetis integrales").d(),
	a("pan-blanco", "Pan blanco (barra)", 172675, "pan", "pan blanco", "barra de pan", "pan de barra", "baguette").u("rebanada", 30),
	a("pan-integral", "Pan integral", 172688, "pan integral", "pan de molde integral").u("rebanada", 32),
	a("pan-molde", "Pan de molde blanco", 174924, "pan de molde", "pan de sándwich").u("rebanada", 25),
	// Biscote: «zwieback», el pan tostado seco de USDA.
	a("biscotes", "Biscotes", 171372, "biscote", "biscotes", "pan tostado", "tostadas").u("biscote", 10),
	a("tortitas-arroz", "Tortitas de arroz inflado", 170250, "tortitas de arroz", "tortita de arroz", "tortitas de arroz inflado").u("tortita", 9),
	a("tortilla-trigo", "Tortilla de trigo (wrap)", 167535, "tortilla de trigo", "wrap", "fajita", "tortilla mexicana").u("tortilla", 49),
	a("avena", "Copos de avena, crudos", 173904, "avena", "copos de avena", "harina de avena"),
	a("avena-cocida", "Gachas de avena (cocidas en agua)", 173905, "gachas", "gachas de avena", "porridge", "avena cocida"),
	a("patata-cruda", "Patata, cruda", 170026, "patata", "patatas", "patata cruda").u("patata", 213),
	a("patata-cocida", "Patata cocida", 170440, "patata", "patatas", "patata cocida", "patata hervida").u("patata", 167).d(),
	a("patata-asada", "Patata asada", 170093, "patata asada", "patata al horno").u("patata", 173),
	a("patatas-fritas", "Patatas fritas (de freidora)", 170698, "patatas fritas", "patatas fritas caseras", "patatas de freidora"),
	a("boniato-crudo", "Boniato, crudo", 168482, "boniato", "batata", "camote"),
	a("boniato-asado", "Boniato asado", 168483, "boniato", "batata", "camote", "boniato asado").d(),
	a("quinoa-cruda", "Quinoa, cruda", 168874, "quinoa", "quinua"),
	a("quinoa-cocida", "Quinoa, cocida", 168917, "quinoa", "quinua", "quinoa cocida").d(),
	a("cuscus-crudo", "Cuscús, crudo", 169699, "cuscús", "cous cous"),
	a("cuscus-cocido", "Cuscús, cocido", 169700, "cuscús", "cous cous", "cuscús cocido").d(),
	// Copos de maíz: el genérico que hay en USDA es de marca blanca (Ralston).
	a("cereales", "Copos de maíz (corn flakes)", 174648, "cereales", "corn flakes", "copos de maíz", "cereales de desayuno"),
	a("muesli", "Muesli con frutos secos y fruta", 169077, "muesli"),
	a("galletas", "Galletas tipo María", 171867, "galletas", "galleta", "galletas maría").u("galleta", 5.6),

	// ---- Legumbres y soja ----
	a("lentejas-crudas", "Lentejas, crudas", 172420, "lentejas", "lentejas secas", "lentejas crudas"),
	a("lentejas-cocidas", "Lentejas, cocidas", 172421, "lentejas", "lentejas cocidas", "lentejas de bote").d(),
	a("garbanzos-crudos", "Garbanzos, crudos", 173756, "garbanzos", "garbanzos secos", "garbanzos crudos"),
	a("garbanzos-cocidos", "Garbanzos, cocidos", 173757, "garbanzos", "garbanzos cocidos", "garbanzos de bote").d(),
	a("alubias-crudas", "Alubias blancas, crudas", 175202, "alubias", "alubias blancas", "judías blancas", "fabes"),
	a("alubias-cocidas", "Alubias blancas, cocidas", 175203, "alubias", "alubias blancas", "judías blancas", "fabes", "alubias de bote").d(),
	a("guisantes", "Guisantes, cocidos", 170017, "guisantes"),
	a("edamame", "Edamame, cocido", 168411, "edamame", "habas de soja"),
	a("tofu", "Tofu firme", 172475, "tofu"),
	a("hummus", "Hummus", 174289, "hummus", "humus"),

	// ---- Fruta ----
	a("platano", "Plátano", 173944, "plátano", "plátanos", "banana").u("plátano", 118),
	a("manzana", "Manzana", 171688, "manzana", "manzanas").u("manzana", 182),
	a("naranja", "Naranja", 169097, "naranja", "naranjas").u("naranja", 131),
	a("mandarina", "Mandarina", 169105, "mandarina", "mandarinas").u("mandarina", 88),
	a("pera", "Pera", 169118, "pera", "peras").u("pera", 178),
	a("fresas", "Fresas", 167762, "fresas", "fresa", "fresones").u("fresa", 12),
	a("uvas", "Uvas", 174683, "uvas", "uva").u("uva", 4.9),
	a("sandia", "Sandía", 167765, "sandía").u("rodaja", 286),
	a("melon", "Melón", 169911, "melón").u("rodaja", 160),
	a("pina", "Piña", 169124, "piña").u("rodaja", 84),
	a("kiwi", "Kiwi", 168153, "kiwi", "kiwis").u("kiwi", 69),
	a("melocoton", "Melocotón", 169928, "melocotón", "melocotones").u("melocotón", 150),
	a("arandanos", "Arándanos", 171711, "arándanos", "arándano"),
	a("frambuesas", "Frambuesas", 167755, "frambuesas", "frambuesa"),
	a("cerezas", "Cerezas", 171719, "cerezas", "cereza").u("cereza", 8.2),
	a("ciruela", "Ciruela", 169949, "ciruela", "ciruelas").u("ciruela", 66),
	a("albaricoque", "Albaricoque", 171697, "albaricoque", "albaricoques").u("albaricoque", 35),
	a("higos", "Higos", 173021, "higo", "higos").u("higo", 50),
	a("mango", "Mango", 169910, "mango").u("mango", 336),
	a("aguacate", "Aguacate", 171705, "aguacate", "aguacates").u("aguacate", 136),
	a("datiles", "Dátiles", 168191, "dátiles", "dátil").u("dátil", 24),
	a("pasas", "Pasas", 168165, "pasas", "uvas pasas"),

	// ---- Verdura ----
	a("tomate", "Tomate", 170457, "tomate", "tomates").u("tomate", 123),
	a("tomate-triturado", "Tomate triturado (lata)", 170054, "tomate triturado", "tomate natural triturado"),
	a("lechuga", "Lechuga", 169247, "lechuga", "lechuga romana"),
	a("cebolla", "Cebolla", 170000, "cebolla", "cebollas").u("cebolla", 110),
	a("zanahoria", "Zanahoria", 170393, "zanahoria", "zanahorias").u("zanahoria", 61),
	a("pimiento-rojo", "Pimiento rojo", 170108, "pimiento rojo", "pimiento").u("pimiento", 119),
	a("pimiento-verde", "Pimiento verde", 170427, "pimiento verde").u("pimiento", 119),
	a("pepino", "Pepino", 168409, "pepino").u("pepino", 301),
	a("calabacin", "Calabacín", 169291, "calabacín").u("calabacín", 196),
	a("berenjena", "Berenjena", 169228, "berenjena"),
	a("brocoli-crudo", "Brócoli, crudo", 170379, "brócoli", "brócoli crudo"),
	a("brocoli-cocido", "Brócoli, cocido", 169967, "brócoli", "brócoli cocido").d(),
	a("espinacas", "Espinacas", 168462, "espinacas"),
	a("champinones", "Champiñones", 169251, "champiñones", "champiñón", "setas").u("champiñón", 18),
	a("judias-verdes", "Judías verdes, cocidas", 169141, "judías verdes", "judías", "vainas"),
	a("coliflor", "Coliflor", 169986, "coliflor"),
	a("ajo", "Ajo", 169230, "ajo", "ajos").u("diente", 3),
	a("esparragos", "Espárragos verdes", 168389, "espárragos", "espárrago", "espárragos verdes", "trigueros").u("espárrago", 16),
	a("alcachofa", "Alcachofa", 169205, "alcachofa", "alcachofas").u("alcachofa", 128),
	a("maiz", "Maíz dulce (lata), escurrido", 169214, "maíz", "maíz dulce"),

	// ---- Grasas, frutos secos y semillas ----
	// Aceite: 1 cucharada (15 ml) pesa 13,5 g.
	a("aceite-oliva", "Aceite de oliva", 171413, "aceite", "aceite de oliva", "aove", "aceite de oliva virgen extra").u("cucharada", 13.5).liq(),
	a("aceite-girasol", "Aceite de girasol", 172338, "aceite de girasol").u("cucharada", 13.6).liq(),
	a("mantequilla", "Mantequilla", 173410, "mantequilla").u("cucharada", 14.2),
	a("almendras", "Almendras", 170567, "almendras", "almendra").u("almendra", 1.2),
	a("nueces", "Nueces", 170187, "nueces", "nuez").u("nuez", 4),
	a("cacahuetes", "Cacahuetes tostados", 173806, "cacahuetes", "cacahuete", "maní").u("cacahuete", 1),
	a("crema-cacahuete", "Crema de cacahuete", 172470, "crema de cacahuete", "mantequilla de cacahuete").u("cucharada", 16),
	a("anacardos", "Anacardos tostados", 170571, "anacardos", "anacardo"),
	a("pistachos", "Pistachos tostados", 170185, "pistachos", "pistacho").u("pistacho", 0.7),
	a("avellanas", "Avellanas", 170581, "avellanas", "avellana").u("avellana", 1.4),
	a("pipas-girasol", "Pipas de girasol peladas", 170562, "pipas", "pipas de girasol"),
	a("chia", "Semillas de chía", 170554, "chía", "semillas de chía"),
	a("lino", "Semillas de lino", 169414, "lino", "semillas de lino", "linaza").u("cucharada", 10.3),
	a("chocolate-negro", "Chocolate negro 70–85 %", 170273, "chocolate negro", "chocolate amargo"),
	a("chocolate-leche", "Chocolate con leche", 167587, "chocolate con leche", "chocolate"),
	a("crema-cacao", "Crema de cacao y avellanas", 168000, "crema de cacao", "nocilla", "nutella").u("cucharada", 18.5),
	a("aceitunas-verdes", "Aceitunas verdes", 169096, "aceitunas", "aceituna", "aceitunas verdes").u("aceituna", 2.7),
	a("aceitunas-negras", "Aceitunas negras", 169094, "aceitunas negras").u("aceituna", 4.4),

	// ---- Otros: dulces, salsas, bebidas, caprichos ----
	a("azucar", "Azúcar", 169655, "azúcar", "azúcar blanco").u("cucharadita", 4.2),
	a("miel", "Miel", 169640, "miel").u("cucharada", 21),
	a("mermelada", "Mermelada", 169641, "mermelada", "confitura").u("cucharada", 20),
	a("cacao-puro", "Cacao en polvo puro (sin azúcar)", 169593, "cacao puro", "cacao en polvo", "cacao sin azúcar").u("cucharada", 5.4),
	a("cacao-soluble", "Cacao soluble azucarado", 174122, "cacao soluble", "colacao", "cola cao", "nesquik"),
	a("ketchup", "Ketchup", 168556, "ketchup", "kétchup").u("cucharada", 17),
	a("mayonesa", "Mayonesa", 171009, "mayonesa").u("cucharada", 13.8),
	// No hay tomate frito: salsa de tomate para pasta (marinara).
	a("tomate-frito", "Tomate frito (salsa de tomate)", 171192, "tomate frito", "salsa de tomate"),
	a("zumo-naranja", "Zumo de naranja natural", 169098, "zumo de naranja", "zumo", "zumo natural").u("vaso", 200).liq(),
	a("cerveza", "Cerveza", 168746, "cerveza", "caña", "birra").u("caña", 200).liq(),
	a("vino-tinto", "Vino tinto", 173190, "vino tinto", "vino").u("copa", 150).liq(),
	a("refresco-cola", "Refresco de cola", 174852, "refresco de cola", "cola", "coca cola", "refresco").u("lata", 330).liq(),
	a("refresco-cola-zero", "Refresco de cola zero", 175099, "cola zero", "coca cola zero", "refresco zero", "cola light", "coca cola light").u("lata", 330).liq(),
	a("cafe", "Café solo", 171890, "café", "café solo", "expreso").liq(),
	a("te", "Té o infusión", 173227, "té", "infusión", "té verde", "té negro").liq(),
	a("patatas-bolsa", "Patatas fritas de bolsa", 169677, "patatas de bolsa", "patatas chips", "chips"),
	a("palomitas", "Palomitas", 170247, "palomitas", "palomitas de maíz"),
	a("pizza", "Pizza de queso", 170317, "pizza"),
	a("croissant", "Cruasán", 174987, "croissant", "cruasán").u("cruasán", 57),
	// Magdalena: bizcocho tipo «pound cake» de USDA.
	a("magdalena", "Magdalena", 174940, "magdalena", "magdalenas", "bizcocho").u("magdalena", 30),
	a("helado", "Helado de vainilla", 167575, "helado", "helado de vainilla").u("bola", 66),
	a("barrita-cereales", "Barrita de cereales", 167954, "barrita", "barrita de cereales", "barrita de avena").u("barrita", 28),
}

var reKey = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// valida claves, alias en minúscula y que un alias repetido tenga una sola
// variante por defecto.
func valida() error {
	keys := map[string]bool{}
	byAlias := map[string][]int{}
	for i, e := range tabla {
		if !reKey.MatchString(e.key) {
			return fmt.Errorf("clave no válida: %q", e.key)
		}
		if keys[e.key] {
			return fmt.Errorf("clave repetida: %q", e.key)
		}
		keys[e.key] = true
		seen := map[string]bool{}
		for _, al := range e.aliases {
			if al != strings.ToLower(al) || strings.TrimSpace(al) != al || al == "" {
				return fmt.Errorf("%s: alias no válido %q", e.key, al)
			}
			if seen[al] {
				return fmt.Errorf("%s: alias repetido %q", e.key, al)
			}
			seen[al] = true
			byAlias[al] = append(byAlias[al], i)
		}
	}
	for al, idx := range byAlias {
		if len(idx) < 2 {
			continue
		}
		defs := 0
		for _, i := range idx {
			if tabla[i].def {
				defs++
			}
		}
		if defs != 1 {
			return fmt.Errorf("alias %q en %d alimentos con %d por defecto", al, len(idx), defs)
		}
	}
	return nil
}

// abrir da un CSV de USDA por su nombre (food.csv…).
type abrir func(nombre string) (io.ReadCloser, error)

func desdeCarpeta(dir string) abrir {
	return func(nombre string) (io.ReadCloser, error) { return os.Open(filepath.Join(dir, nombre)) }
}

func desdeZip() (abrir, error) {
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(zipURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", zipURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return func(nombre string) (io.ReadCloser, error) {
		for _, f := range zr.File {
			if path.Base(f.Name) == nombre {
				return f.Open()
			}
		}
		return nil, fmt.Errorf("%s no está en el zip", nombre)
	}, nil
}

// filas recorre un CSV con cabecera y llama a fn con cada fila por columna.
func filas(open abrir, nombre string, fn func(col func(string) string)) error {
	f, err := open(nombre)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true
	head, err := r.Read()
	if err != nil {
		return fmt.Errorf("%s: %w", nombre, err)
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.TrimPrefix(h, "\xef\xbb\xbf")] = i
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", nombre, err)
		}
		fn(func(c string) string {
			if i, ok := idx[c]; ok && i < len(rec) {
				return rec[i]
			}
			return ""
		})
	}
}

type alimento struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	FDC     int      `json:"fdc"`
	Kcal    float64  `json:"kcal"`
	Prot    float64  `json:"prot"`
	Carb    float64  `json:"carb"`
	Fat     float64  `json:"fat"`
	Fiber   float64  `json:"fiber"`
	Unit    string   `json:"unit,omitempty"`
	UnitG   float64  `json:"unit_g,omitempty"`
	Liquid  bool     `json:"liquid"`
	Default bool     `json:"default,omitempty"`
}

func r1(x float64) float64 { return math.Round(x*10) / 10 }

func fallo(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	if err := valida(); err != nil {
		fallo(err)
	}
	var open abrir
	if len(os.Args) > 1 {
		open = desdeCarpeta(os.Args[1])
	} else {
		var err error
		if open, err = desdeZip(); err != nil {
			fallo(err)
		}
	}

	want := map[string]bool{}
	for _, e := range tabla {
		want[strconv.Itoa(e.fdc)] = true
	}
	desc := map[string]string{}
	err := filas(open, "food.csv", func(col func(string) string) {
		if id := col("fdc_id"); want[id] {
			desc[id] = col("description")
		}
	})
	if err != nil {
		fallo(err)
	}
	nut := map[string]map[string]float64{}
	err = filas(open, "food_nutrient.csv", func(col func(string) string) {
		id := col("fdc_id")
		if !want[id] {
			return
		}
		switch n := col("nutrient_id"); n {
		case nKcal, nKcalAtw, nKcalAtw2, nProt, nCarb, nFat, nFiber:
			v, err := strconv.ParseFloat(col("amount"), 64)
			if err != nil {
				return
			}
			if nut[id] == nil {
				nut[id] = map[string]float64{}
			}
			nut[id][n] = v
		}
	})
	if err != nil {
		fallo(err)
	}

	var out []alimento
	for _, e := range tabla {
		id := strconv.Itoa(e.fdc)
		if desc[id] == "" {
			fallo(fmt.Errorf("%s: fdc_id %d no está en SR Legacy", e.key, e.fdc))
		}
		n := nut[id]
		kcal, ok := n[nKcal]
		for _, alt := range []string{nKcalAtw, nKcalAtw2} {
			if !ok {
				kcal, ok = n[alt]
			}
		}
		if !ok {
			fallo(fmt.Errorf("%s: sin energía", e.key))
		}
		for _, c := range []string{nProt, nCarb, nFat, nFiber} {
			if _, ok := n[c]; !ok {
				fmt.Fprintf(os.Stderr, "aviso: %s sin nutriente %s, se pone 0\n", e.key, c)
			}
		}
		out = append(out, alimento{
			Key: e.key, Name: e.name, Aliases: e.aliases, FDC: e.fdc,
			Kcal: r1(kcal), Prot: r1(n[nProt]), Carb: r1(n[nCarb]), Fat: r1(n[nFat]), Fiber: r1(n[nFiber]),
			Unit: e.unit, UnitG: e.unitG, Liquid: e.liquid, Default: e.def,
		})
		fmt.Printf("%-26s %6.1f kcal %5.1f P %5.1f C %5.1f G  ← %s\n", e.key, kcal, n[nProt], n[nCarb], n[nFat], desc[id])
	}

	// Un alimento por línea: compacto y fácil de comparar en git.
	var buf bytes.Buffer
	buf.WriteString("[\n")
	for i, x := range out {
		var line bytes.Buffer
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(x); err != nil {
			fallo(err)
		}
		buf.Write(bytes.TrimRight(line.Bytes(), "\n"))
		if i < len(out)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]\n")
	if err := os.WriteFile("datos/alimentos.json", buf.Bytes(), 0o644); err != nil {
		fallo(err)
	}
	fmt.Printf("%d alimentos en datos/alimentos.json\n", len(out))
}
