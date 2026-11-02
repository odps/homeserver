package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"homeserver/platform/transit"
)

// Transporte público de València: autobuses de la EMT y Metrovalencia, con
// los datos abiertos del Ajuntament (paradas) y los enlaces de llegadas que
// publican. Las paradas cercanas se calculan aquí con la lista de paradas: la
// ubicación no sale del servidor y el modelo solo ve nombres, líneas y metros.

// transitClient es compartido: guarda la lista de paradas (un día) y las
// llegadas (30 s). Los tests cambian sus URL.
var transitClient = transit.New()

const (
	maxBoards   = 4  // paradas que se consultan a la vez si el nombre encaja con varias
	maxArrivals = 8  // llegadas por parada
	maxNearBus  = 8  // paradas de bus cercanas
	maxNearRail = 3  // estaciones de metro cercanas
	maxChoices  = 10 // paradas que se ofrecen para elegir
)

// transitTools son las herramientas de autobús y metro de València.
func (a *agent) transitTools() []tool {
	return []tool{
		newTool("bus_llegadas", "Cuándo llegan los próximos autobuses de la EMT de València a una parada, en tiempo real (minutos que faltan; «hora» si falta más de una hora). La parada, por su número (el del poste) o por su nombre o calles («Plaza del Ayuntamiento», «Xàtiva - Colón»). Si el nombre encaja con varias paradas, devuelve sus llegadas o la lista para elegir.",
			object([]string{"parada"}, map[string]map[string]any{
				"parada": str("Número de la parada o su nombre."),
				"linea":  str("Solo esta línea (opcional), p. ej. «19»."),
			}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ Parada, Linea string }
				if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Parada) == "" {
					return "", errors.New("falta la parada (número o nombre)")
				}
				return transitArrivals(ctx, transit.Bus, in.Parada, in.Linea)
			}),
		newTool("metro_llegadas", "Próximos trenes y tranvías de Metrovalencia (FGV) en una estación de València: línea, hacia dónde y a qué hora sale (y los minutos que faltan). La estación, por su nombre («Xàtiva», «Colón», «Benimaclet»).",
			object([]string{"estacion"}, map[string]map[string]any{
				"estacion": str("Nombre de la estación."),
				"linea":    str("Solo esta línea (opcional), p. ej. «3»."),
			}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ Estacion, Linea string }
				if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Estacion) == "" {
					return "", errors.New("falta la estación")
				}
				return transitArrivals(ctx, transit.Metro, in.Estacion, in.Linea)
			}),
		newTool("paradas_cerca", "Paradas de autobús de la EMT y estaciones de Metrovalencia (metro y tranvía) cerca de donde está ahora la persona en València: nombre, número, líneas y distancia en metros. Usa la ubicación que compartió en Telegram. Para ver cuándo pasan, después bus_llegadas (con el número) o metro_llegadas.",
			object(nil, map[string]map[string]any{
				"radio_m": integer("Radio en metros (100 a 2000; por defecto 500)."),
				"linea":   str("Solo paradas de esta línea (opcional), p. ej. «19»."),
			}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					RadioM int `json:"radio_m"`
					Linea  string
				}
				json.Unmarshal(raw, &in)
				l, ok := a.store.location(ctx, a.cfg().TelegramUser)
				if !ok {
					return "", errNoLocation
				}
				return nearStops(ctx, l, in.RadioM, in.Linea)
			}),
	}
}

// stopOut es una parada con sus llegadas, tal como la ve el modelo.
type stopOut struct {
	transit.Stop
	Arrivals []transit.Arrival `json:"llegadas"`
	Note     string            `json:"nota,omitempty"`
}

func transitArrivals(ctx context.Context, n transit.Network, query, line string) (string, error) {
	query, line = strings.TrimSpace(query), strings.ToUpper(strings.TrimSpace(line))
	what := "la parada"
	if n == transit.Metro {
		what = "la estación"
	}
	stops, err := transitClient.Find(ctx, n, query, line)
	if err != nil {
		return "", err
	}
	if len(stops) == 0 {
		id, isNumber := transit.Number(query)
		switch {
		case isNumber && n == transit.Bus:
			// La lista del Geoportal puede ir con retraso: se pregunta a la EMT.
			stops = []transit.Stop{{ID: id}}
		case line != "":
			if all, _ := transitClient.Find(ctx, n, query, ""); len(all) > 0 {
				return "", fmt.Errorf("por %s «%s» no pasa la línea %s", what, query, line)
			}
			fallthrough
		default:
			return "", fmt.Errorf("no encuentro %s «%s» en %s; prueba con otro nombre o calle", what, query, n)
		}
	}
	if len(stops) > maxBoards {
		stops = stops[:min(len(stops), maxChoices)]
		return toJSON(map[string]any{"red": n.String(), "paradas": stops,
			"nota": fmt.Sprintf("Varias paradas encajan con «%s»: pregunta cuál (o el número del poste) y vuelve a llamar con el número o el nombre completo.", query)}), nil
	}

	out := make([]stopOut, len(stops))
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var lastErr error
	for i, s := range stops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := stopOut{Stop: s, Arrivals: []transit.Arrival{}}
			b, err := transitClient.Arrivals(ctx, n, s.ID)
			switch {
			case errors.Is(err, transit.ErrUnknownStop):
				o.Note = "La EMT no conoce esta parada."
			case err != nil:
				errMu.Lock()
				lastErr = err
				errMu.Unlock()
				o.Note = "No se han podido consultar las llegadas."
			default:
				if o.Name == "" {
					o.Name = b.Name
				}
				for _, ar := range b.Arrivals {
					if line == "" || ar.Line == line {
						o.Arrivals = append(o.Arrivals, ar)
					}
				}
				if len(o.Arrivals) > maxArrivals {
					o.Arrivals = o.Arrivals[:maxArrivals]
				}
				if len(o.Arrivals) == 0 {
					o.Note = "Ahora no hay llegadas previstas (puede que no haya servicio a esta hora)."
				}
			}
			out[i] = o
		}()
	}
	wg.Wait()
	if lastErr != nil && len(out) == 1 {
		return "", lastErr
	}
	note := "minutos: lo que falta según la EMT, en tiempo real; con «hora», falta más de una hora."
	if n == transit.Metro {
		note = "Horas de salida que publica Metrovalencia; minutos: lo que falta."
	}
	return toJSON(map[string]any{"red": n.String(), "paradas": out, "nota": note}), nil
}

func nearStops(ctx context.Context, l location, radius int, line string) (string, error) {
	if radius == 0 {
		radius = 500
	}
	radius = min(max(radius, 100), 2000)
	bus, errBus := transitClient.Near(ctx, transit.Bus, l.Lat, l.Lon, radius, maxNearBus, line)
	// Las estaciones están más separadas: al menos 1 km.
	rail, errRail := transitClient.Near(ctx, transit.Metro, l.Lat, l.Lon, max(radius, 1000), maxNearRail, line)
	if errBus != nil && errRail != nil {
		return "", errBus
	}
	res := map[string]any{"zona": l.Zone, "radio_m": radius,
		"bus": orEmpty(bus), "metro": orEmpty(rail),
		"nota": "Distancias en línea recta. Para ver cuándo pasan: bus_llegadas con el número de la parada, metro_llegadas con el nombre de la estación."}
	switch {
	case errBus != nil:
		res["aviso"] = "No se han podido cargar las paradas de autobús."
	case errRail != nil:
		res["aviso"] = "No se han podido cargar las estaciones de metro."
	case len(bus) == 0 && len(rail) == 0:
		res["aviso"] = fmt.Sprintf("No hay paradas a menos de %d m (o la persona no está en València).", radius)
	}
	return toJSON(res), nil
}

func orEmpty(s []transit.Nearby) []transit.Nearby {
	if s == nil {
		return []transit.Nearby{}
	}
	return s
}
