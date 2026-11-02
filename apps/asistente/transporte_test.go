package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"homeserver/platform/transit"
)

// Respuestas reales del Geoportal y de la EMT, recortadas (las completas están
// en platform/transit).
const (
	fakeBusStops      = `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.37762689500910596,39.469443799436689]},"properties":{"id_parada":2314,"denominacion":"Periodista Azzati - Plaça de l'Ajuntament (2314)","lineas":"10,19,31,70,71,72","suprimida":0}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.3758577883648716,39.470103433460743]},"properties":{"id_parada":2219,"denominacion":"Plaça de l'Ajuntament - Barques (2219)","lineas":"11,6,70,8","suprimida":0}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.41671632169767564,39.507807540394467]},"properties":{"id_parada":1044,"denominacion":"Poliesportiu de Burjassot (1044)","lineas":"63","suprimida":0}}]}`
	fakeMetroStations = `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.37737500609108299,39.467186014007119]},"properties":{"codigo":"71","nombre":"Xàtiva","linea":"3,5,9"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.59027779121037838,39.62284092097164]},"properties":{"codigo":"54","nombre":"Llíria","linea":"2"}}]}`
	fakeBusPage       = `<div data-role="content"><div style=" background: grey;
                                    color: white;
                                    padding-bottom: 10px;
                                    padding-left: 10px;
                                    padding-top: 10px;">2314 - Periodista Azzati - Plaça de l'Ajuntament</div><div style="padding-left: 5px;padding-top: 5px; padding-bottom: 5px;border-bottom:1px solid black;" ><span class="imagenParada"><img src="http://www.emtvalencia.es/EmtEsquemas_graphics/line-icons/10bigW.gif" title="10" width="25px" height="25px"></span><span style="position: relative; top: -5px;">&nbsp;&nbsp;Benimaclet - 7 min.</span></div><div style="padding-left: 5px;padding-top: 5px; padding-bottom: 5px;border-bottom:1px solid black;" ><span class="imagenParada"><img src="http://www.emtvalencia.es/EmtEsquemas_graphics/line-icons/19bigW.gif" title="19" width="25px" height="25px"></span><span style="position: relative; top: -5px;">&nbsp;&nbsp;La Malva-rosa - 19 min.</span></div></div>`
	fakeBusEmpty = `<div data-role="content"><div style=" background: grey;
                                    color: white;
                                    padding-bottom: 10px;
                                    padding-left: 10px;
                                    padding-top: 10px;">2219 - Plaça de l'Ajuntament - Barques</div></div>`
	fakeMetroPage = `<div data-role="content">
			<div style="background: grey; color: white; padding-bottom: 10px; padding-left: 10px; padding-top: 10px;">Xàtiva</div>
			<div>
				<div
					style="padding-left: 5px; padding-top: 5px; padding-bottom: 5px; border-bottom: 1px solid black;">
					<span class="imagenParada"><img
						src="/geoportal-services/images/linea_metro_3.png"
						title="1" width="25px" height="25px" /></span><span
						style="position: relative; top: -5px; margin-left: 10px"> <span>3 - Rafelbunyol - Aeroport</span>, <span>Destino</span> <b>Aeroport</b> - <span>23:31:00</span></span>
				</div>
			</div>
		</div>`
)

func fakeTransit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/server/rest/services/OPENDATA/Trafico/MapServer/226/query":
			io.WriteString(w, fakeBusStops)
		case "/server/rest/services/OPENDATA/Trafico/MapServer/221/query":
			io.WriteString(w, fakeMetroStations)
		case "/QR.php":
			if r.URL.Query().Get("p") == "2314" {
				io.WriteString(w, fakeBusPage)
			} else {
				io.WriteString(w, fakeBusEmpty)
			}
		case "/geoportal-services/api/v1/salidas-metro.html":
			io.WriteString(w, fakeMetroPage)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := transitClient
	transitClient = transit.New()
	transitClient.Geoportal, transitClient.EMT = srv.URL, srv.URL
	t.Cleanup(func() { transitClient = old })
}

func TestTransitTools(t *testing.T) {
	a, _, _ := newTestAgent(t)
	fakeTransit(t)
	ctx := context.Background()
	tools := map[string]tool{}
	for _, x := range a.transitTools() {
		tools[x.def.Function.Name] = x
	}
	run := func(name, args string) string {
		return a.runTool(ctx, tools, toolCall{Function: toolFunc(name, args)})
	}

	// Por número.
	out := run("bus_llegadas", `{"parada":"2314"}`)
	if !strings.Contains(out, `"nombre":"Periodista Azzati - Plaça de l'Ajuntament"`) || !strings.Contains(out, `"hacia":"Benimaclet","minutos":7`) {
		t.Fatalf("por número: %s", out)
	}
	// Por nombre, en castellano, con línea: solo la parada del 19 y solo el 19.
	out = run("bus_llegadas", `{"parada":"plaza del ayuntamiento","linea":"19"}`)
	if !strings.Contains(out, `"numero":"2314"`) || strings.Contains(out, "2219") || strings.Contains(out, "Benimaclet") || !strings.Contains(out, "La Malva-rosa") {
		t.Fatalf("por nombre y línea: %s", out)
	}
	// Varias paradas: las dos, cada una con lo suyo.
	out = run("bus_llegadas", `{"parada":"Plaça de l'Ajuntament"}`)
	if !strings.Contains(out, "2314") || !strings.Contains(out, "2219") || !strings.Contains(out, "no hay llegadas previstas") {
		t.Fatalf("varias: %s", out)
	}
	if out := run("bus_llegadas", `{"parada":"plaza del ayuntamiento","linea":"63"}`); !strings.Contains(out, "no pasa la línea 63") {
		t.Fatalf("línea que no pasa: %s", out)
	}
	if out := run("bus_llegadas", `{"parada":"Sorolla"}`); !strings.Contains(out, "no encuentro la parada") {
		t.Fatalf("no existe: %s", out)
	}
	if out := run("metro_llegadas", `{"estacion":"xativa"}`); !strings.Contains(out, `"linea":"3","hacia":"Aeroport"`) || !strings.Contains(out, `"hora":"23:31"`) {
		t.Fatalf("metro: %s", out)
	}

	// Paradas cerca: sin ubicación, la pide.
	if out := run("paradas_cerca", `{}`); !strings.Contains(out, "comparta su ubicación") {
		t.Fatalf("sin ubicación: %s", out)
	}
	now := time.Now()
	if err := a.store.saveLocation(ctx, 42, location{Lat: 39.4702123, Lon: -0.3760456, Zone: "Ciutat Vella, València", At: now, Until: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	out = run("paradas_cerca", `{"radio_m":300}`)
	if !strings.Contains(out, `"numero":"2219","nombre":"Plaça de l'Ajuntament - Barques"`) || !strings.Contains(out, `"metros":`) ||
		!strings.Contains(out, `"Xàtiva"`) || strings.Contains(out, "Burjassot") || strings.Contains(out, "Llíria") {
		t.Fatalf("cerca: %s", out)
	}
	// El modelo no ve las coordenadas, y los nombres de parada no se toman por
	// direcciones al pasar por los seudónimos.
	hidden := newPseudonyms(a.store, 42, true).hideJSON(ctx, out)
	if strings.Contains(hidden, "39.47") || strings.Contains(hidden, "-0.37") || strings.Contains(hidden, "DIR_") || strings.Contains(hidden, "TEL_") || !strings.Contains(hidden, "Barques") {
		t.Fatalf("al modelo: %s", hidden)
	}
	out = run("paradas_cerca", `{"radio_m":300,"linea":"19"}`)
	if !strings.Contains(out, "2314") || strings.Contains(out, "2219") {
		t.Fatalf("cerca con línea: %s", out)
	}
}
