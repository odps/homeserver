package transit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Respuestas reales (02-11-2026), recortadas.

// Capa 226 del Geoportal (paradas de la EMT), con los campos que se piden.
const sampleBusStops = `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.41671632169767564,39.507807540394467]},"properties":{"id_parada":1044,"denominacion":"Poliesportiu de Burjassot (1044)","lineas":"63","suprimida":0}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.37762689500910596,39.469443799436689]},"properties":{"id_parada":2314,"denominacion":"Periodista Azzati - Plaça de l'Ajuntament (2314)","lineas":"10,19,31,70,71,72","suprimida":0}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.3758577883648716,39.470103433460743]},"properties":{"id_parada":2219,"denominacion":"Plaça de l'Ajuntament - Barques (2219)","lineas":"11,6,70,8","suprimida":0}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.37626105026101969,39.470535917069618]},"properties":{"id_parada":2403,"denominacion":"Plaça de l'Ajuntament - Ateneu (2403)","lineas":"32,4","suprimida":0}}]}`

// Capa 221 del Geoportal (estaciones de Metrovalencia).
const sampleMetroStations = `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.59027779121037838,39.62284092097164]},"properties":{"codigo":"54","nombre":"Llíria","linea":"2"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.37092778143059241,39.470146218556067]},"properties":{"codigo":"70","nombre":"Colón","linea":"3,5,7,9"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.37737500609108299,39.467186014007119]},"properties":{"codigo":"71","nombre":"Xàtiva","linea":"3,5,9"}}]}`

// https://www.emtvalencia.es/QR.php?sec=est&p=2314 (sin los scripts del final).
const sampleBusPage = `<meta http-equiv="Content-Type" content="text/html; charset=UTF-8"/> <meta name="viewport" content="width=device-width, initial-scale=1"><script type="text/javascript" src="//code.jquery.com/jquery-1.7.1.min.js"></script><script type="text/javascript" src="//code.jquery.com/mobile/1.0/jquery.mobile-1.0.min.js"></script><link href="//code.jquery.com/mobile/1.0/jquery.mobile-1.0.min.css" rel="stylesheet"><style> .ui-body-c, .ui-dialog.ui-overlay-c {background:white;}
                .ui-btn-icon-left .ui-icon, .ui-btn-icon-right .ui-icon{margin-top:-9px}
                .recargar .ui-btn-corner-all{height:8px;}                </style><div data-role="page" id="page"><div data-role="header" data-theme="b"> <h1 style="float:left;margin-left:15px;font-size:18px;">Gestiona tu tiempo</h1> <img src="/ciudadano/images/logo/emt_peq_blanco.png" class="ui-btn-right"></div> <div data-role="content"><div style=" background: grey;
                                    color: white;
                                    padding-bottom: 10px;
                                    padding-left: 10px;
                                    padding-top: 10px;">2314 - Periodista Azzati - Plaça de l'Ajuntament</div><div style="padding-left: 5px;padding-top: 5px; padding-bottom: 5px;border-bottom:1px solid black;" ><span class="imagenParada"><img src="http://www.emtvalencia.es/EmtEsquemas_graphics/line-icons/19bigW.gif" title="19" width="25px" height="25px"></span><span style="position: relative; top: -5px;">&nbsp;&nbsp;La Malva-rosa - 19 min.</span></div><div style="padding-left: 5px;padding-top: 5px; padding-bottom: 5px;border-bottom:1px solid black;" ><span class="imagenParada"><img src="http://www.emtvalencia.es/EmtEsquemas_graphics/line-icons/10bigW.gif" title="10" width="25px" height="25px"></span><span style="position: relative; top: -5px;">&nbsp;&nbsp;Benimaclet - 7 min.</span></div><div style="padding-left: 5px;padding-top: 5px; padding-bottom: 5px;border-bottom:1px solid black;" ><span class="imagenParada"><img src="http://www.emtvalencia.es/EmtEsquemas_graphics/line-icons/72bigW.gif" title="72" width="25px" height="25px"></span><span style="position: relative; top: -5px;">&nbsp;&nbsp;Est.del Nord - 00:12:31</span></div><div style="padding-left: 5px;padding-top: 5px; padding-bottom: 5px;border-bottom:1px solid black;" ><span class="imagenParada"><img src="http://www.emtvalencia.es/EmtEsquemas_graphics/line-icons/7bigW.gif" title="7" width="25px" height="25px"></span><span style="position: relative; top: -5px;">&nbsp;&nbsp;Fta. S.Lluis - Próximo</span></div></div><div data-role='footer' data-theme='c' data-position='fixed' style='padding-left:15px;padding-top:5px;padding-bottom:5px;'><a href='#' title='Descarga APP' onclick="location.href='http://www.emtvalencia.es/app.php'" style='text-align:center'>Descarga APP</a><a href='javascript:location.reload(true)' class='recargar' data-role='button' data-icon='refresh' data-theme='c' style='width:27px;padding:4px;float:right;margin-right:30px;'></a>        </div></div>`

// Parada que no existe: el recuadro de la cabecera sale vacío.
const sampleBusUnknown = `<div data-role="content"><div style=" background: grey;
                                    color: white;
                                    padding-bottom: 10px;
                                    padding-left: 10px;
                                    padding-top: 10px;"></div></div><div data-role='footer' data-theme='c' data-position='fixed' style='padding-left:15px;padding-top:5px;padding-bottom:5px;'><a href='#' title='Descarga APP' onclick="location.href='http://www.emtvalencia.es/app.php'" style='text-align:center'>Descarga APP</a></div>`

// https://geoportal.valencia.es/geoportal-services/api/v1/salidas-metro.html?estacion=71&lang=es
// (recortada; la página repite trenes).
const sampleMetroPage = `<!DOCTYPE html>
<html>
<head>
<meta http-equiv="Content-Type" content="text/html; charset=UTF-8" />
</head>
<body>
	<div data-role="page" id="page">
		<div data-role="header" class="ui-header ui-bar-a" role="banner">
			<h1 class="ui-title" role="heading" aria-level="1" style="margin-left: 15px; font-size: 16px">
				<span style="float: left">Próximas salidas</span>
			</h1>
			<img src="/geoportal-services/images/logo_metrovalencia.png" class="ui-btn-right" style="width: 160px; height: 35px" />
		</div>
		<div data-role="content">
			<div style="background: grey; color: white; padding-bottom: 10px; padding-left: 10px; padding-top: 10px;">Xàtiva</div>

			<div>
				<div
					style="padding-left: 5px; padding-top: 5px; padding-bottom: 5px; border-bottom: 1px solid black;">
					<span class="imagenParada"><img
						src="/geoportal-services/images/linea_metro_5.png"
						title="1" width="25px" height="25px" /></span><span
						style="position: relative; top: -5px; margin-left: 10px"> <span>5 - Aeroport - Machado</span>, <span>Destino</span> <b>Machado</b> - <span>23:12:00</span></span>
				</div>
			</div>
			<div>
				<div
					style="padding-left: 5px; padding-top: 5px; padding-bottom: 5px; border-bottom: 1px solid black;">
					<span class="imagenParada"><img
						src="/geoportal-services/images/linea_metro_3.png"
						title="1" width="25px" height="25px" /></span><span
						style="position: relative; top: -5px; margin-left: 10px"> <span>3 - Rafelbunyol - Aeroport</span>, <span>Destino</span> <b>Aeroport</b> - <span>23:31:00</span></span>
				</div>
			</div>
			<div>
				<div
					style="padding-left: 5px; padding-top: 5px; padding-bottom: 5px; border-bottom: 1px solid black;">
					<span class="imagenParada"><img
						src="/geoportal-services/images/linea_metro_3.png"
						title="1" width="25px" height="25px" /></span><span
						style="position: relative; top: -5px; margin-left: 10px"> <span>3 - Aeroport - Machado</span>, <span>Destino</span> <b>Machado</b> - <span>24:18:00</span></span>
				</div>
			</div>
			<div>
				<div
					style="padding-left: 5px; padding-top: 5px; padding-bottom: 5px; border-bottom: 1px solid black;">
					<span class="imagenParada"><img
						src="/geoportal-services/images/linea_metro_5.png"
						title="1" width="25px" height="25px" /></span><span
						style="position: relative; top: -5px; margin-left: 10px"> <span>5 - Aeroport - Machado</span>, <span>Destino</span> <b>Machado</b> - <span>23:12:00</span></span>
				</div>
			</div>
		</div>
	</div>
</body>
</html>`

// Estación sin salidas (de noche, o código que no existe).
const sampleMetroEmpty = `<div data-role="content">

			<div>No hay paradas</div>

		</div>`

type fake struct {
	srv                       *httptest.Server
	static, emt, metro, pages atomic.Int32
	fail                      atomic.Bool
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("sin User-Agent: %q", r.Header.Get("User-Agent"))
		}
		if f.fail.Load() {
			http.Error(w, "caído", http.StatusBadGateway)
			return
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/server/rest/services/OPENDATA/Trafico/MapServer/226/query":
			f.static.Add(1)
			if q.Get("f") != "geojson" || q.Get("outFields") == "" || q.Get("resultOffset") != "0" {
				t.Errorf("consulta de paradas: %s", r.URL.RawQuery)
			}
			io.WriteString(w, sampleBusStops)
		case "/server/rest/services/OPENDATA/Trafico/MapServer/221/query":
			f.static.Add(1)
			io.WriteString(w, sampleMetroStations)
		case "/QR.php":
			f.emt.Add(1)
			switch q.Get("p") {
			case "2314":
				io.WriteString(w, sampleBusPage)
			default:
				io.WriteString(w, sampleBusUnknown)
			}
		case "/geoportal-services/api/v1/salidas-metro.html":
			f.metro.Add(1)
			if q.Get("estacion") == "71" {
				io.WriteString(w, sampleMetroPage)
			} else {
				io.WriteString(w, sampleMetroEmpty)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	c := New()
	c.Geoportal, c.EMT = f.srv.URL, f.srv.URL
	return f, c
}

// A las 23:05 del 2 de noviembre en València.
var testNow = time.Date(2026, 11, 2, 23, 5, 0, 0, madrid)

func TestFindStops(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()

	s, err := c.Find(ctx, Bus, "2314", "")
	if err != nil || len(s) != 1 || s[0].Name != "Periodista Azzati - Plaça de l'Ajuntament" || strings.Join(s[0].Lines, ",") != "10,19,31,70,71,72" {
		t.Fatalf("por número: %+v %v", s, err)
	}
	if s, _ := c.Find(ctx, Bus, "parada nº 2314", ""); len(s) != 1 || s[0].ID != "2314" {
		t.Fatalf("«parada nº 2314»: %+v", s)
	}
	if s, _ := c.Find(ctx, Bus, "2219", ""); strings.Join(s[0].Lines, ",") != "6,8,11,70" {
		t.Fatalf("líneas ordenadas: %v", s[0].Lines)
	}
	// En castellano y sin acentos encuentra los nombres en valenciano.
	s, _ = c.Find(ctx, Bus, "plaza del ayuntamiento", "")
	if len(s) != 3 {
		t.Fatalf("plaza del ayuntamiento: %+v", s)
	}
	s, _ = c.Find(ctx, Bus, "Plaza Ayuntamiento", "19")
	if len(s) != 1 || s[0].ID != "2314" {
		t.Fatalf("con línea: %+v", s)
	}
	if s, _ := c.Find(ctx, Bus, "ateneu", ""); len(s) != 1 || s[0].ID != "2403" {
		t.Fatalf("ateneu: %+v", s)
	}
	if s, _ := c.Find(ctx, Bus, "Sorolla", ""); len(s) != 0 {
		t.Fatalf("no debería encontrar nada: %+v", s)
	}
	m, err := c.Find(ctx, Metro, "xativa", "")
	if err != nil || len(m) != 1 || m[0].ID != "71" || m[0].Name != "Xàtiva" {
		t.Fatalf("metro: %+v %v", m, err)
	}
	if m, _ := c.Find(ctx, Metro, "estación de Colón", ""); len(m) != 1 || m[0].ID != "70" {
		t.Fatalf("colón: %+v", m)
	}
}

func TestNear(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()
	// Junto a la parada 2219, en la plaza.
	lat, lon := 39.4702, -0.3760
	n, err := c.Near(ctx, Bus, lat, lon, 300, 10, "")
	if err != nil || len(n) != 3 || n[0].ID != "2219" || n[0].Meters > 30 || n[2].ID != "2314" {
		t.Fatalf("cerca: %+v %v", n, err)
	}
	if n, _ := c.Near(ctx, Bus, lat, lon, 300, 10, "4"); len(n) != 1 || n[0].ID != "2403" {
		t.Fatalf("línea 4: %+v", n)
	}
	if n, _ := c.Near(ctx, Bus, lat, lon, 300, 1, ""); len(n) != 1 {
		t.Fatalf("límite: %+v", n)
	}
	m, _ := c.Near(ctx, Metro, lat, lon, 1000, 3, "")
	if len(m) != 2 || m[0].Name != "Xàtiva" || m[1].Name != "Colón" {
		t.Fatalf("metro cerca: %+v", m)
	}
	// Las coordenadas no salen en el JSON.
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), "39.4") || strings.Contains(string(b), "-0.37") {
		t.Fatalf("coordenadas en el JSON: %s", b)
	}
}

func TestBusArrivals(t *testing.T) {
	f, c := newFake(t)
	c.Now = func() time.Time { return testNow }
	ctx := context.Background()
	b, err := c.Arrivals(ctx, Bus, "2314")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Periodista Azzati - Plaça de l'Ajuntament" || len(b.Arrivals) != 4 {
		t.Fatalf("%+v", b)
	}
	a := b.Arrivals
	// «Próximo» es que está llegando: va primero.
	if a[0].Line != "7" || a[0].Toward != "Fta. S.Lluis" || *a[0].Minutes != 0 || a[0].Note != "Próximo" {
		t.Fatalf("próximo: %+v", a[0])
	}
	if a[1].Line != "10" || a[1].Toward != "Benimaclet" || *a[1].Minutes != 7 || a[2].Line != "19" || *a[2].Minutes != 19 {
		t.Fatalf("orden o minutos: %+v", a)
	}
	// Más allá de una hora la EMT da la hora: 00:12 es dentro de 67 min.
	if a[3].Toward != "Est.del Nord" || a[3].Time != "00:12" || *a[3].Minutes != 67 {
		t.Fatalf("con hora: %+v %d", a[3], *a[3].Minutes)
	}
	// La segunda vez, de la caché.
	c.Arrivals(ctx, Bus, "2314")
	if f.emt.Load() != 1 {
		t.Fatalf("peticiones a la EMT: %d", f.emt.Load())
	}
	c.Now = func() time.Time { return testNow.Add(time.Minute) }
	c.Arrivals(ctx, Bus, "02314")
	if f.emt.Load() != 2 {
		t.Fatalf("la caché no caduca: %d", f.emt.Load())
	}

	if _, err := c.Arrivals(ctx, Bus, "99999"); !errors.Is(err, ErrUnknownStop) {
		t.Fatalf("parada desconocida: %v", err)
	}
	if _, err := c.Arrivals(ctx, Bus, "1&x=2"); err == nil || f.emt.Load() != 3 {
		t.Fatalf("número no válido: %v", err)
	}
}

func TestMetroArrivals(t *testing.T) {
	_, c := newFake(t)
	c.Now = func() time.Time { return testNow }
	ctx := context.Background()
	b, err := c.Arrivals(ctx, Metro, "71")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Xàtiva" || len(b.Arrivals) != 3 {
		t.Fatalf("sin repetidos: %+v", b)
	}
	a := b.Arrivals
	if a[0].Line != "5" || a[0].Toward != "Machado" || a[0].Time != "23:12" || *a[0].Minutes != 7 {
		t.Fatalf("primero: %+v", a[0])
	}
	// «24:18» es las 00:18 de mañana.
	if a[2].Line != "3" || a[2].Time != "00:18" || *a[2].Minutes != 73 {
		t.Fatalf("pasada la medianoche: %+v %d", a[2], *a[2].Minutes)
	}
	e, err := c.Arrivals(ctx, Metro, "112")
	if err != nil || len(e.Arrivals) != 0 {
		t.Fatalf("sin salidas: %+v %v", e, err)
	}
}

func TestMinutesUntil(t *testing.T) {
	now := time.Date(2026, 11, 3, 0, 10, 0, 0, madrid)
	for _, c := range []struct{ h, m, want int }{{0, 18, 8}, {24, 18, 8}, {23, 58, 0}, {6, 0, 350}} {
		if got := minutesUntil(now, c.h, c.m); got != c.want {
			t.Errorf("%02d:%02d: %d, quería %d", c.h, c.m, got, c.want)
		}
	}
}

func TestStaticCache(t *testing.T) {
	f, c := newFake(t)
	now := testNow
	c.Now = func() time.Time { return now }
	ctx := context.Background()
	c.Stops(ctx, Bus)
	c.Stops(ctx, Bus)
	if f.static.Load() != 1 {
		t.Fatalf("descargas: %d", f.static.Load())
	}
	// Al día siguiente se vuelve a bajar; si falla, sigue la copia anterior.
	now = now.Add(25 * time.Hour)
	f.fail.Store(true)
	if s, err := c.Stops(ctx, Bus); err != nil || len(s) != 4 || f.static.Load() != 1 {
		t.Fatalf("copia anterior: %d %v", len(s), err)
	}
	f.fail.Store(false)
	c.Stops(ctx, Bus)
	if f.static.Load() != 2 {
		t.Fatalf("no se refresca: %d", f.static.Load())
	}
	// Sin copia y con el servicio caído, error.
	_, c2 := newFake(t)
	c2.Geoportal = f.srv.URL + "/nada"
	if _, err := c2.Stops(ctx, Metro); err == nil {
		t.Fatal("debería fallar")
	}
}

func TestPagination(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("resultOffset") == "0" {
			io.WriteString(w, strings.TrimSuffix(sampleMetroStations, "}")+`,"exceededTransferLimit":true}`)
			return
		}
		io.WriteString(w, `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.3,39.4]},"properties":{"codigo":"9","nombre":"Otra","linea":"1"}}]}`)
	}))
	defer srv.Close()
	c := New()
	c.Geoportal = srv.URL
	s, err := c.Stops(context.Background(), Metro)
	if err != nil || len(s) != 4 || calls != 2 {
		t.Fatalf("%d paradas en %d peticiones: %v", len(s), calls, err)
	}
}
