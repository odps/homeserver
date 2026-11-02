// Package geo busca lugares cerca de un punto (OpenStreetMap: Overpass y
// Nominatim) y da el tiempo (Open-Meteo). Son servicios gratuitos, sin clave
// y por HTTPS. Para no mandar más de lo necesario, las coordenadas salen
// redondeadas: unos 100 m para buscar lugares y la zona, 1 km para el tiempo.
package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Client habla con los servicios. Los campos de URL se cambian en los tests.
type Client struct {
	HTTP      *http.Client
	Overpass  string // https://overpass-api.de/api/interpreter
	Nominatim string // https://nominatim.openstreetmap.org
	Meteo     string // https://api.open-meteo.com
	Geocoding string // https://geocoding-api.open-meteo.com
}

// userAgent identifica la app, como pide la política de uso de Nominatim.
const userAgent = "homeserver-personal/1.0 (asistente de una persona; uso ocasional)"

func New() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 25 * time.Second},
		Overpass:  "https://overpass-api.de/api/interpreter",
		Nominatim: "https://nominatim.openstreetmap.org",
		Meteo:     "https://api.open-meteo.com",
		Geocoding: "https://geocoding-api.open-meteo.com",
	}
}

func round(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

func (c *Client) getJSON(ctx context.Context, method, u string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "es")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s respondió %s", req.URL.Host, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// Distance es la distancia en metros entre dos puntos (en línea recta).
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// ---- Lugares ----

// Place es un negocio o sitio de OpenStreetMap.
type Place struct {
	Name     string `json:"nombre"`
	Kind     string `json:"tipo,omitempty"`
	Street   string `json:"calle,omitempty"`
	Hours    string `json:"horario,omitempty"` // formato de OSM, p. ej. «Mo-Fr 09:00-21:00»
	Phone    string `json:"telefono,omitempty"`
	Web      string `json:"web,omitempty"`
	Meters   int    `json:"metros"`
	Maps     string `json:"mapa"`
	lat, lon float64
}

// categories pasa lo que se pide en español a etiquetas de OpenStreetMap.
var categories = []struct {
	words []string
	tags  []string // clave=valor (valor con | para varios)
	kind  string
}{
	{[]string{"farmacia"}, []string{"amenity=pharmacy"}, "farmacia"},
	{[]string{"restaurante", "comer", "cenar"}, []string{"amenity=restaurant"}, "restaurante"},
	{[]string{"comida rapida", "hamburgues", "pizza"}, []string{"amenity=fast_food", "amenity=restaurant"}, "comida"},
	{[]string{"cafeteria", "cafe", "desayun"}, []string{"amenity=cafe"}, "cafetería"},
	{[]string{"bar", "cerveza", "copa"}, []string{"amenity=bar|pub"}, "bar"},
	{[]string{"cajero"}, []string{"amenity=atm", "atm=yes"}, "cajero"},
	{[]string{"banco"}, []string{"amenity=bank"}, "banco"},
	{[]string{"gasolinera", "gasolina", "repostar"}, []string{"amenity=fuel"}, "gasolinera"},
	{[]string{"cargador", "recarga", "electrolinera"}, []string{"amenity=charging_station"}, "recarga"},
	{[]string{"supermercado", "super", "compra"}, []string{"shop=supermarket|convenience"}, "supermercado"},
	{[]string{"panaderia", "pan"}, []string{"shop=bakery"}, "panadería"},
	{[]string{"fruteria", "fruta"}, []string{"shop=greengrocer"}, "frutería"},
	{[]string{"carniceria"}, []string{"shop=butcher"}, "carnicería"},
	{[]string{"hospital", "urgencias"}, []string{"amenity=hospital"}, "hospital"},
	{[]string{"centro de salud", "medico", "clinica", "ambulatorio"}, []string{"amenity=clinic|doctors"}, "médico"},
	{[]string{"dentista"}, []string{"amenity=dentist"}, "dentista"},
	{[]string{"veterinari"}, []string{"amenity=veterinary"}, "veterinario"},
	{[]string{"parking", "aparcamiento", "aparcar"}, []string{"amenity=parking"}, "parking"},
	{[]string{"gimnasio"}, []string{"leisure=fitness_centre"}, "gimnasio"},
	{[]string{"peluqueria", "barberia"}, []string{"shop=hairdresser"}, "peluquería"},
	{[]string{"ferreteria"}, []string{"shop=hardware|doityourself"}, "ferretería"},
	{[]string{"estanco", "tabaco"}, []string{"shop=tobacco"}, "estanco"},
	{[]string{"correos", "oficina de correos"}, []string{"amenity=post_office"}, "correos"},
	{[]string{"heladeria", "helado"}, []string{"amenity=ice_cream"}, "heladería"},
	{[]string{"libreria"}, []string{"shop=books"}, "librería"},
	{[]string{"lavanderia"}, []string{"shop=laundry"}, "lavandería"},
	{[]string{"taller"}, []string{"shop=car_repair"}, "taller"},
	{[]string{"hotel"}, []string{"tourism=hotel"}, "hotel"},
	{[]string{"museo"}, []string{"tourism=museum"}, "museo"},
	{[]string{"parque"}, []string{"leisure=park"}, "parque"},
	{[]string{"cine"}, []string{"amenity=cinema"}, "cine"},
	{[]string{"biblioteca"}, []string{"amenity=library"}, "biblioteca"},
	{[]string{"policia", "comisaria"}, []string{"amenity=police"}, "policía"},
	{[]string{"metro"}, []string{"station=subway", "railway=subway_entrance"}, "metro"},
	{[]string{"estacion de tren", "tren", "cercanias"}, []string{"railway=station"}, "estación"},
	{[]string{"parada de autobus", "autobus", "bus"}, []string{"highway=bus_stop"}, "parada de bus"},
	{[]string{"aseo", "bano", "servicio publico"}, []string{"amenity=toilets"}, "aseo"},
}

// matchWord: la palabra entera, en plural, o su comienzo si es larga
// («farmacias», «hamburguesería»), sin que «bar» encaje en «barbería».
func matchWord(q, word string) bool {
	return q == word || strings.TrimSuffix(q, "s") == word || strings.TrimSuffix(q, "es") == word ||
		(len(word) >= 6 && strings.HasPrefix(q, word))
}

func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var overpassSafe = regexp.MustCompile(`[^\p{L}\p{N} '&.-]`)

// placesQuery es la consulta de Overpass para lo que se pide: por categoría
// si se reconoce y, si no, por nombre.
// También devuelve qué pedir a Nominatim (la etiqueta en inglés, «pharmacy»,
// que entiende mejor) y los tipos válidos de la categoría.
func placesQuery(what string, lat, lon float64, radius int) (query, kind, phrase string, types []string) {
	qwords := strings.Fields(fold(what))
	w := " " + strings.Join(qwords, " ") + " "
	around := fmt.Sprintf("(around:%d,%.3f,%.3f)", radius, round(lat, 3), round(lon, 3))
	var parts []string
	for _, c := range categories {
		hit := false
		for _, word := range c.words {
			if strings.Contains(word, " ") {
				hit = hit || strings.Contains(w, " "+word+" ")
				continue
			}
			for _, q := range qwords {
				hit = hit || matchWord(q, word)
			}
		}
		if !hit {
			continue
		}
		kind = c.kind
		for _, t := range c.tags {
			k, v, _ := strings.Cut(t, "=")
			if phrase == "" && v != "yes" {
				phrase = strings.ReplaceAll(strings.Split(v, "|")[0], "_", " ")
			}
			types = append(types, strings.Split(v, "|")...)
			if strings.Contains(v, "|") {
				parts = append(parts, fmt.Sprintf(`nwr%s["%s"~"^(%s)$"];`, around, k, v))
			} else {
				parts = append(parts, fmt.Sprintf(`nwr%s["%s"="%s"];`, around, k, v))
			}
		}
		break
	}
	// Sin categoría conocida, por nombre (cadenas, marcas: «Mercadona»).
	if name := strings.Join(strings.Fields(overpassSafe.ReplaceAllString(what, " ")), " "); kind == "" && name != "" {
		parts = append(parts, fmt.Sprintf(`nwr%s["name"~"%s",i];`, around, regexp.QuoteMeta(name)))
	}
	if phrase == "" {
		phrase = what
	}
	return "[out:json][timeout:20];(" + strings.Join(parts, "") + ");out center tags 60;", kind, phrase, types
}

// Places busca lo que se pide a menos de radius metros, por cercanía.
func (c *Client) Places(ctx context.Context, what string, lat, lon float64, radius int) ([]Place, error) {
	if strings.TrimSpace(what) == "" {
		return nil, errors.New("falta qué buscar")
	}
	radius = min(max(radius, 100), 10000)
	q, kind, phrase, types := placesQuery(what, lat, lon, radius)
	var res struct{ Elements []osmElement }
	octx, cancel := context.WithTimeout(ctx, 12*time.Second)
	err := c.getJSON(octx, "POST", c.Overpass, strings.NewReader(url.Values{"data": {q}}.Encode()), &res)
	cancel()
	if err != nil {
		// El Overpass público se satura a menudo: entonces, Nominatim.
		if res.Elements, err = c.nominatimPlaces(ctx, phrase, types, lat, lon, radius); err != nil {
			return nil, fmt.Errorf("OpenStreetMap: %w", err)
		}
	}
	seen := map[string]bool{}
	var out []Place
	for _, e := range res.Elements {
		plat, plon := e.Lat, e.Lon
		if e.Center != nil {
			plat, plon = e.Center.Lat, e.Center.Lon
		}
		t := e.Tags
		name := t["name"]
		if name == "" {
			name = t["brand"]
		}
		if name == "" {
			name = kind
		}
		if name == "" {
			continue
		}
		p := Place{Name: name, Kind: kind, Hours: t["opening_hours"], Phone: firstOf(t["phone"], t["contact:phone"]),
			Web: firstOf(t["website"], t["contact:website"]), lat: plat, lon: plon}
		if s := t["addr:street"]; s != "" {
			p.Street = strings.TrimSpace(s + " " + t["addr:housenumber"])
		}
		p.Meters = int(math.Round(Distance(lat, lon, plat, plon)))
		if p.Meters > radius+radius/5 {
			continue
		}
		key := fold(name) + "|" + fmt.Sprint(p.Meters/30)
		if seen[key] {
			continue
		}
		seen[key] = true
		p.Maps = "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(fmt.Sprintf("%.6f,%.6f", plat, plon))
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Meters < out[j].Meters })
	if len(out) > 10 {
		out = out[:10]
	}
	return out, nil
}

type osmElement struct {
	Lat, Lon float64
	Center   *struct{ Lat, Lon float64 }
	Tags     map[string]string
}

// nominatimPlaces busca con Nominatim dentro del cuadrado del radio (entiende
// categorías en español, «farmacia», y nombres). Da menos que Overpass, pero
// responde cuando este está saturado.
func (c *Client) nominatimPlaces(ctx context.Context, what string, types []string, lat, lon float64, radius int) ([]osmElement, error) {
	dLat := float64(radius) / 111320
	dLon := dLat / math.Cos(lat*math.Pi/180)
	lat, lon = round(lat, 3), round(lon, 3)
	u := fmt.Sprintf("%s/search?format=jsonv2&bounded=1&limit=20&extratags=1&addressdetails=1&accept-language=es&viewbox=%.4f,%.4f,%.4f,%.4f&q=%s",
		c.Nominatim, lon-dLon, lat+dLat, lon+dLon, lat-dLat, url.QueryEscape(what))
	var res []struct {
		Lat, Lon  string
		Name      string
		Type      string
		Address   map[string]string
		Extratags map[string]string
	}
	if err := c.getJSON(ctx, "GET", u, nil, &res); err != nil {
		return nil, err
	}
	var out []osmElement
	for _, r := range res {
		if len(types) > 0 && !slices.Contains(types, r.Type) {
			continue // «atm» trae también centros de arte
		}
		var e osmElement
		fmt.Sscan(r.Lat, &e.Lat)
		fmt.Sscan(r.Lon, &e.Lon)
		e.Tags = map[string]string{"name": r.Name, "addr:street": r.Address["road"], "addr:housenumber": r.Address["house_number"]}
		for _, k := range []string{"opening_hours", "phone", "contact:phone", "website", "contact:website", "brand"} {
			if v := r.Extratags[k]; v != "" {
				e.Tags[k] = v
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// Zone es el barrio y la ciudad de un punto («Chamberí, Madrid»).
func (c *Client) Zone(ctx context.Context, lat, lon float64) (string, error) {
	u := fmt.Sprintf("%s/reverse?format=jsonv2&zoom=14&lat=%.3f&lon=%.3f&accept-language=es", c.Nominatim, round(lat, 3), round(lon, 3))
	var res struct {
		Address map[string]string `json:"address"`
	}
	if err := c.getJSON(ctx, "GET", u, nil, &res); err != nil {
		return "", fmt.Errorf("OpenStreetMap: %w", err)
	}
	a := res.Address
	area := firstOf(a["suburb"], a["neighbourhood"], a["quarter"], a["city_district"])
	city := firstOf(a["city"], a["town"], a["village"], a["municipality"])
	switch {
	case area != "" && city != "" && area != city:
		return area + ", " + city, nil
	case city != "":
		return city, nil
	}
	return area, nil
}

// RouteLink es el enlace de Google Maps para ir de un punto a un destino. Se
// abre en el móvil de la persona: el servidor no consulta a Google.
func RouteLink(lat, lon float64, destination string) string {
	return "https://www.google.com/maps/dir/?api=1&origin=" + url.QueryEscape(fmt.Sprintf("%.5f,%.5f", lat, lon)) +
		"&destination=" + url.QueryEscape(destination)
}

// ---- Tiempo ----

// Day es la previsión de un día.
type Day struct {
	Date      string  `json:"dia"`
	Sky       string  `json:"cielo"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	RainProb  int     `json:"lluvia_prob"`
	RainMM    float64 `json:"lluvia_mm"`
	RainHours string  `json:"lluvia_horas,omitempty"` // «17:00–20:00»: horas con probabilidad de 50 % o más
	WindMax   float64 `json:"viento_max_kmh"`
}

// Forecast es la previsión de un sitio.
type Forecast struct {
	Place string  `json:"lugar,omitempty"`
	Now   float64 `json:"temperatura_ahora"`
	Sky   string  `json:"cielo_ahora"`
	Days  []Day   `json:"dias"`
}

// City busca una localidad por su nombre (para el tiempo): solo sale el
// nombre de la ciudad, nunca una dirección.
func (c *Client) City(ctx context.Context, name string) (lat, lon float64, label string, err error) {
	u := fmt.Sprintf("%s/v1/search?count=1&language=es&format=json&name=%s", c.Geocoding, url.QueryEscape(strings.TrimSpace(name)))
	var res struct {
		Results []struct {
			Name, Admin1, Country string
			Latitude, Longitude   float64
		}
	}
	if err = c.getJSON(ctx, "GET", u, nil, &res); err != nil {
		return 0, 0, "", fmt.Errorf("Open-Meteo: %w", err)
	}
	if len(res.Results) == 0 {
		return 0, 0, "", fmt.Errorf("no encuentro la localidad «%s»", name)
	}
	r := res.Results[0]
	label = r.Name
	if r.Admin1 != "" && r.Admin1 != r.Name {
		label += " (" + r.Admin1 + ")"
	}
	return r.Latitude, r.Longitude, label, nil
}

// Weather es la previsión de los próximos days días (1 a 7).
func (c *Client) Weather(ctx context.Context, lat, lon float64, days int) (Forecast, error) {
	days = min(max(days, 1), 7)
	u := fmt.Sprintf("%s/v1/forecast?latitude=%.2f&longitude=%.2f&timezone=Europe%%2FMadrid&forecast_days=%d"+
		"&current=temperature_2m,weather_code&hourly=precipitation_probability"+
		"&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,precipitation_sum,wind_speed_10m_max",
		c.Meteo, round(lat, 2), round(lon, 2), days)
	var res struct {
		Current struct {
			Temp float64 `json:"temperature_2m"`
			Code int     `json:"weather_code"`
		} `json:"current"`
		Hourly struct {
			Time []string `json:"time"`
			Prob []*int   `json:"precipitation_probability"`
		} `json:"hourly"`
		Daily struct {
			Time []string  `json:"time"`
			Code []int     `json:"weather_code"`
			Max  []float64 `json:"temperature_2m_max"`
			Min  []float64 `json:"temperature_2m_min"`
			Prob []*int    `json:"precipitation_probability_max"`
			Sum  []float64 `json:"precipitation_sum"`
			Wind []float64 `json:"wind_speed_10m_max"`
		} `json:"daily"`
	}
	if err := c.getJSON(ctx, "GET", u, nil, &res); err != nil {
		return Forecast{}, fmt.Errorf("Open-Meteo: %w", err)
	}
	f := Forecast{Now: res.Current.Temp, Sky: Sky(res.Current.Code)}
	d := res.Daily
	for i, day := range d.Time {
		if i >= len(d.Code) || i >= len(d.Max) || i >= len(d.Min) {
			break
		}
		x := Day{Date: day, Sky: Sky(d.Code[i]), Min: d.Min[i], Max: d.Max[i]}
		if i < len(d.Prob) && d.Prob[i] != nil {
			x.RainProb = *d.Prob[i]
		}
		if i < len(d.Sum) {
			x.RainMM = d.Sum[i]
		}
		if i < len(d.Wind) {
			x.WindMax = d.Wind[i]
		}
		x.RainHours = rainHours(day, res.Hourly.Time, res.Hourly.Prob)
		f.Days = append(f.Days, x)
	}
	return f, nil
}

// rainHours junta las horas de un día con lluvia probable (50 % o más):
// «07:00–09:00, 17:00–21:00».
func rainHours(day string, times []string, probs []*int) string {
	var spans []string
	start, last := "", ""
	flush := func() {
		if start != "" {
			end := last
			if h, err := time.Parse("15:04", last); err == nil {
				end = h.Add(time.Hour).Format("15:04")
			}
			spans = append(spans, start+"–"+end)
			start = ""
		}
	}
	for i, t := range times {
		if !strings.HasPrefix(t, day) || i >= len(probs) {
			continue
		}
		hour := t[len(t)-5:]
		if probs[i] != nil && *probs[i] >= 50 {
			if start == "" {
				start = hour
			}
			last = hour
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(spans, ", ")
}

// Sky pasa el código WMO a palabras.
func Sky(code int) string {
	switch {
	case code == 0:
		return "despejado"
	case code <= 2:
		return "poco nuboso"
	case code == 3:
		return "nublado"
	case code == 45 || code == 48:
		return "niebla"
	case code >= 51 && code <= 57:
		return "llovizna"
	case code >= 61 && code <= 67, code >= 80 && code <= 82:
		return "lluvia"
	case code >= 71 && code <= 77, code == 85 || code == 86:
		return "nieve"
	case code >= 95:
		return "tormenta"
	}
	return "variable"
}

// Line resume un día en una línea: «nublado, 9–17 °C; lluvia probable
// 17:00–20:00 (70 %)».
func (d Day) Line() string {
	s := fmt.Sprintf("%s, %.0f–%.0f °C", d.Sky, d.Min, d.Max)
	switch {
	case d.RainHours != "":
		s += fmt.Sprintf("; lluvia probable %s (%d %%)", d.RainHours, d.RainProb)
	case d.RainProb >= 30:
		s += fmt.Sprintf("; %d %% de probabilidad de lluvia", d.RainProb)
	default:
		s += "; sin lluvia"
	}
	if d.WindMax >= 40 {
		s += fmt.Sprintf("; viento fuerte (hasta %.0f km/h)", d.WindMax)
	}
	return s
}
