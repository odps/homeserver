// Package transit da el transporte público de València con datos abiertos
// oficiales, gratuitos y sin clave:
//
//   - Paradas de la EMT y estaciones de Metrovalencia (metro y tranvía): capas
//     226 y 221 del Geoportal del Ajuntament (conjuntos «EMT» y «FGV
//     Estacions» de opendata.vlci.valencia.es, licencia CC BY 4.0). Se bajan
//     enteras una vez al día y se buscan aquí: las coordenadas de la persona
//     no salen del servidor.
//   - Próximas llegadas: el enlace que publica ese mismo conjunto de datos
//     para cada parada (campo proximas_llegadas): la página de la EMT
//     (QR.php) y la del Geoportal para el metro (salidas-metro.html). Son HTML
//     sencillo, sin protección contra bots; se pide una página por consulta y
//     se guarda 30 s para no repetir.
//
// Fuente de los datos: Ajuntament de València, EMT València y FGV.
package transit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // la hora de Madrid, aunque el sistema no tenga zonas
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// userAgent identifica la app ante los servicios.
const userAgent = "homeserver-personal/1.0 (asistente de una persona; uso ocasional)"

const (
	staticTTL   = 24 * time.Hour
	arrivalsTTL = 30 * time.Second
	pageSize    = 1000
)

// Network es la red: autobuses de la EMT o Metrovalencia.
type Network int

const (
	Bus Network = iota
	Metro
)

func (n Network) String() string {
	if n == Metro {
		return "Metrovalencia"
	}
	return "EMT"
}

// Client habla con los servicios. Los campos de URL se cambian en los tests.
type Client struct {
	HTTP      *http.Client
	Geoportal string           // https://geoportal.valencia.es
	EMT       string           // https://www.emtvalencia.es
	Now       func() time.Time // hora actual (sustituible en los tests)

	staticMu sync.Mutex
	static   [2]staticData

	mu       sync.Mutex
	arrivals map[string]cachedBoard
}

type staticData struct {
	stops []Stop
	at    time.Time
}

type cachedBoard struct {
	b  Board
	at time.Time
}

func New() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		Geoportal: "https://geoportal.valencia.es",
		EMT:       "https://www.emtvalencia.es",
		Now:       time.Now,
	}
}

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "es")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s respondió %s", req.URL.Host, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// ---- Paradas y estaciones ----

// Stop es una parada de la EMT o una estación de Metrovalencia.
type Stop struct {
	ID    string   `json:"numero"` // número de la parada (EMT) o código de la estación (FGV)
	Name  string   `json:"nombre"`
	Lines []string `json:"lineas"`
	Lat   float64  `json:"-"`
	Lon   float64  `json:"-"`
	words []string // el nombre normalizado, para buscar
}

// HasLine dice si por la parada pasa la línea (sin distinguir mayúsculas).
func (s Stop) HasLine(line string) bool {
	line = strings.ToUpper(strings.TrimSpace(line))
	return line == "" || slices.Contains(s.Lines, line)
}

// layer es la capa del Geoportal de cada red y sus campos.
var layers = [2]struct {
	id     int
	fields string
}{
	Bus:   {226, "id_parada,denominacion,lineas,suprimida"},
	Metro: {221, "codigo,nombre,linea"},
}

// Stops devuelve todas las paradas de la red. Se bajan una vez al día; si
// falla la descarga y hay una copia anterior, se sigue usando esa.
func (c *Client) Stops(ctx context.Context, n Network) ([]Stop, error) {
	c.staticMu.Lock()
	defer c.staticMu.Unlock()
	d := c.static[n]
	if d.stops != nil && c.now().Sub(d.at) < staticTTL {
		return d.stops, nil
	}
	stops, err := c.fetchStops(ctx, n)
	if err != nil {
		if d.stops != nil {
			return d.stops, nil
		}
		return nil, fmt.Errorf("paradas de %s: %w", n, err)
	}
	c.static[n] = staticData{stops: stops, at: c.now()}
	return stops, nil
}

type geoFeature struct {
	Geometry struct {
		Coordinates []float64 `json:"coordinates"`
	} `json:"geometry"`
	Properties struct {
		IDParada     json.Number `json:"id_parada"`
		Denominacion string      `json:"denominacion"`
		Lineas       string      `json:"lineas"`
		Suprimida    int         `json:"suprimida"`
		Codigo       json.Number `json:"codigo"`
		Nombre       string      `json:"nombre"`
		Linea        string      `json:"linea"`
	} `json:"properties"`
}

func (c *Client) fetchStops(ctx context.Context, n Network) ([]Stop, error) {
	l := layers[n]
	var out []Stop
	for offset := 0; offset < 20*pageSize; offset += pageSize {
		q := url.Values{
			"where":             {"1=1"},
			"outFields":         {l.fields},
			"orderByFields":     {"gid"},
			"resultOffset":      {strconv.Itoa(offset)},
			"resultRecordCount": {strconv.Itoa(pageSize)},
			"f":                 {"geojson"},
		}
		u := fmt.Sprintf("%s/server/rest/services/OPENDATA/Trafico/MapServer/%d/query?%s", c.Geoportal, l.id, q.Encode())
		body, err := c.get(ctx, u, 16<<20)
		if err != nil {
			return nil, err
		}
		var res struct {
			Features []geoFeature `json:"features"`
			More     bool         `json:"exceededTransferLimit"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			return nil, fmt.Errorf("respuesta del Geoportal no válida: %w", err)
		}
		if res.Error != nil {
			return nil, fmt.Errorf("Geoportal: %s", res.Error.Message)
		}
		for _, f := range res.Features {
			if s, ok := toStop(n, f); ok {
				out = append(out, s)
			}
		}
		if !res.More || len(res.Features) == 0 {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("el Geoportal no ha devuelto paradas")
	}
	return out, nil
}

func toStop(n Network, f geoFeature) (Stop, bool) {
	p := f.Properties
	if len(f.Geometry.Coordinates) < 2 {
		return Stop{}, false
	}
	s := Stop{Lon: f.Geometry.Coordinates[0], Lat: f.Geometry.Coordinates[1]}
	var lines string
	if n == Bus {
		if p.Suprimida != 0 {
			return Stop{}, false
		}
		s.ID, lines = p.IDParada.String(), p.Lineas
		// «Plaça de l'Ajuntament - Barques (2219)»: el número va aparte.
		s.Name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p.Denominacion), "("+s.ID+")"))
	} else {
		s.ID, s.Name, lines = p.Codigo.String(), strings.TrimSpace(p.Nombre), p.Linea
	}
	if s.ID == "" || s.Name == "" {
		return Stop{}, false
	}
	s.Name = strings.Join(strings.Fields(s.Name), " ")
	for l := range strings.SplitSeq(lines, ",") {
		if l = strings.ToUpper(strings.TrimSpace(l)); l != "" && !slices.Contains(s.Lines, l) {
			s.Lines = append(s.Lines, l)
		}
	}
	sortLines(s.Lines)
	s.words = words(s.Name)
	return s, true
}

// sortLines ordena las líneas por número («4, 32, C1, N1»).
func sortLines(ls []string) {
	sort.SliceStable(ls, func(i, j int) bool {
		a, errA := strconv.Atoi(ls[i])
		b, errB := strconv.Atoi(ls[j])
		switch {
		case errA == nil && errB == nil:
			return a < b
		case errA == nil:
			return true
		case errB == nil:
			return false
		}
		return ls[i] < ls[j]
	})
}

// ---- Búsqueda por nombre ----

func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Palabras que no ayudan a distinguir paradas.
var stopWords = map[string]bool{
	"de": true, "del": true, "la": true, "el": true, "les": true, "los": true, "las": true, "l": true, "d": true,
	"y": true, "i": true, "a": true, "en": true, "calle": true, "carrer": true, "c": true, "parada": true,
	"estacion": true, "estacio": true, "metro": true, "bus": true, "autobus": true, "tranvia": true,
	"n": true, "no": true, "nº": true, "num": true, "numero": true,
}

// Los nombres están en valenciano; se buscan también en castellano.
var synonyms = map[string][]string{
	"plaza": {"placa", "pl"}, "ayuntamiento": {"ajuntament"}, "avenida": {"avinguda", "av", "avda"},
	"puerto": {"port"}, "norte": {"nord"}, "universidad": {"universitat"}, "facultades": {"facultats"},
	"mercado": {"mercat"}, "playa": {"platja"}, "puente": {"pont"}, "iglesia": {"esglesia"},
	"camino": {"cami"}, "puerta": {"porta", "pta"}, "ciudad": {"ciutat"},
	"jardin": {"jardi"}, "colegio": {"col"}, "san": {"sant"}, "aeropuerto": {"aeroport"},
	"paseo": {"passeig"}, "estaciones": {"estacions"}, "viejo": {"vell"}, "nuevo": {"nou"},
}

func words(s string) []string {
	var out []string
	for w := range strings.FieldsFuncSeq(fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// matches: cada palabra de la búsqueda está en el nombre, entera o como
// comienzo de una palabra (desde 3 letras), en valenciano o en castellano.
func matches(query, name []string) bool {
	for _, q := range query {
		alts := append([]string{q}, synonyms[q]...)
		hit := false
		for _, w := range name {
			for _, a := range alts {
				if w == a || (len(a) >= 3 && strings.HasPrefix(w, a)) {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// Number da el número de parada si la búsqueda es solo eso («2314», «parada
// nº 2314»).
func Number(query string) (string, bool) {
	q := words(query)
	if len(q) != 1 {
		return "", false
	}
	n, err := strconv.Atoi(q[0])
	if err != nil || n <= 0 || n > 999999 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// Find busca paradas por número o por nombre; line, si no está vacía, deja
// solo las paradas de esa línea. Las más parecidas, primero.
func (c *Client) Find(ctx context.Context, n Network, query, line string) ([]Stop, error) {
	stops, err := c.Stops(ctx, n)
	if err != nil {
		return nil, err
	}
	if id, ok := Number(query); ok {
		for _, s := range stops {
			if s.ID == id {
				return []Stop{s}, nil
			}
		}
	}
	q := words(query)
	if len(q) == 0 {
		return nil, nil
	}
	exact := strings.Join(q, " ")
	var out []Stop
	for _, s := range stops {
		if s.HasLine(line) && matches(q, s.words) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := strings.Join(out[i].words, " ") == exact, strings.Join(out[j].words, " ") == exact
		if ei != ej {
			return ei
		}
		if len(out[i].words) != len(out[j].words) {
			return len(out[i].words) < len(out[j].words)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ---- Cercanía ----

// Nearby es una parada y su distancia en metros, en línea recta.
type Nearby struct {
	Stop
	Meters int `json:"metros"`
}

// Distance es la distancia en metros entre dos puntos (en línea recta).
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// Near devuelve las paradas a menos de radius metros del punto, la más
// cercana primero (como mucho limit). Se calcula aquí, con la lista de
// paradas: el punto no se manda a ningún sitio.
func (c *Client) Near(ctx context.Context, n Network, lat, lon float64, radius, limit int, line string) ([]Nearby, error) {
	stops, err := c.Stops(ctx, n)
	if err != nil {
		return nil, err
	}
	var out []Nearby
	for _, s := range stops {
		if !s.HasLine(line) {
			continue
		}
		if d := Distance(lat, lon, s.Lat, s.Lon); d <= float64(radius) {
			out = append(out, Nearby{Stop: s, Meters: int(math.Round(d))})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Meters < out[j].Meters })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---- Llegadas ----

// Arrival es un autobús o un tren que va a llegar.
type Arrival struct {
	Line    string `json:"linea"`
	Toward  string `json:"hacia"`             // destino del vehículo
	Minutes *int   `json:"minutos,omitempty"` // minutos que faltan
	Time    string `json:"hora,omitempty"`    // hora («23:12») si la fuente da la hora
	Note    string `json:"aviso,omitempty"`   // lo que dice la fuente si no son minutos ni hora
}

// Board son las próximas llegadas a una parada.
type Board struct {
	Name     string    `json:"nombre,omitempty"` // el nombre que da la fuente
	Arrivals []Arrival `json:"llegadas"`
}

// ErrUnknownStop: la fuente no conoce la parada.
var ErrUnknownStop = errors.New("parada desconocida")

// Arrivals da las próximas llegadas a una parada (número de la EMT o código
// de estación de FGV), ordenadas por tiempo.
func (c *Client) Arrivals(ctx context.Context, n Network, id string) (Board, error) {
	num, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || num <= 0 || num > 999999 {
		return Board{}, fmt.Errorf("«%s» no es un número de parada", strings.TrimSpace(id))
	}
	id = strconv.Itoa(num) // solo cifras en la URL
	key := fmt.Sprintf("%d/%s", n, id)
	c.mu.Lock()
	if e, ok := c.arrivals[key]; ok && c.now().Sub(e.at) < arrivalsTTL {
		c.mu.Unlock()
		return e.b, nil
	}
	c.mu.Unlock()

	var b Board
	if n == Metro {
		b, err = c.metroBoard(ctx, id)
	} else {
		b, err = c.busBoard(ctx, id)
	}
	if err != nil {
		return Board{}, err
	}
	sort.SliceStable(b.Arrivals, func(i, j int) bool { return order(b.Arrivals[i]) < order(b.Arrivals[j]) })

	c.mu.Lock()
	if c.arrivals == nil || len(c.arrivals) > 500 {
		c.arrivals = map[string]cachedBoard{}
	}
	c.arrivals[key] = cachedBoard{b, c.now()}
	c.mu.Unlock()
	return b, nil
}

func order(a Arrival) int {
	if a.Minutes != nil {
		return *a.Minutes
	}
	return 1 << 20
}

// minutesUntil son los minutos desde now hasta la hora h:m de Madrid (de hoy,
// o de mañana si ya pasó hace mucho; «24:18» es las 00:18 de mañana).
func minutesUntil(now time.Time, h, m int) int {
	now = now.In(madrid)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, madrid)
	d := int(math.Ceil(midnight.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Sub(now).Minutes()))
	for d < -720 {
		d += 1440
	}
	for d > 720 {
		d -= 1440
	}
	return max(d, 0)
}

// clock lee «23:12», «23:12:00» o «24:18:00» y devuelve hora y minuto.
func clock(s string) (h, m int, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 30 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func intPtr(n int) *int { return &n }
