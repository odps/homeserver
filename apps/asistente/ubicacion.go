package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/geo"
)

// Ubicación: la persona la comparte en Telegram (clip → Ubicación, o en
// tiempo real). Se guarda cifrada y caduca: 3 horas si es una ubicación suelta;
// si es en tiempo real, lo que dure. El modelo nunca ve las coordenadas: las
// herramientas buscan con ellas aquí y le dan solo la zona («Chamberí,
// Madrid»), los lugares y las distancias.

const (
	sealLocation = "asistente/locations"
	locationTTL  = 3 * time.Hour
)

type location struct {
	Lat   float64   `json:"lat"`
	Lon   float64   `json:"lon"`
	Zone  string    `json:"zone"`
	At    time.Time `json:"at"`
	Until time.Time `json:"until"`
	Live  bool      `json:"live"`
}

func (s store) saveLocation(ctx context.Context, chat int64, l location) error {
	sealed, err := s.box.SealJSON(sealLocation, l)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO locations (chat_id, sealed, until) VALUES ($1, $2, $3)
		ON CONFLICT (chat_id) DO UPDATE SET sealed = excluded.sealed, until = excluded.until`, chat, sealed, l.Until)
	return err
}

// location devuelve la ubicación si sigue valiendo; si caducó, la borra.
func (s store) location(ctx context.Context, chat int64) (location, bool) {
	var sealed []byte
	var until time.Time
	err := s.db.QueryRow(ctx, `SELECT sealed, until FROM locations WHERE chat_id = $1`, chat).Scan(&sealed, &until)
	if err != nil {
		return location{}, false
	}
	if time.Now().After(until) {
		s.forgetLocation(ctx, chat)
		return location{}, false
	}
	var l location
	if s.box.OpenJSON(sealLocation, sealed, &l) != nil {
		return location{}, false
	}
	return l, true
}

func (s store) forgetLocation(ctx context.Context, chat int64) {
	s.db.Exec(ctx, `DELETE FROM locations WHERE chat_id = $1`, chat)
}

func pruneLocations(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM locations WHERE until < now()`)
	return err
}

// gotLocation guarda la ubicación que llega por Telegram. quiet: las
// actualizaciones de la ubicación en tiempo real no se contestan.
func (a *agent) gotLocation(ctx context.Context, tg telegram, chat int64, m *tgMessage, quiet bool) {
	now := time.Now()
	l := location{Lat: m.Location.Latitude, Lon: m.Location.Longitude, At: now, Until: now.Add(locationTTL)}
	if old, ok := a.store.location(ctx, chat); ok && old.Live && quiet {
		l.Until, l.Live, l.Zone = old.Until, true, old.Zone
	}
	if m.Location.LivePeriod > 0 && !quiet {
		l.Live, l.Until = true, now.Add(time.Duration(m.Location.LivePeriod)*time.Second)
	}
	if !quiet || l.Zone == "" {
		zctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if z, err := a.geo.Zone(zctx, l.Lat, l.Lon); err == nil {
			l.Zone = z
		}
		cancel()
	}
	if err := a.store.saveLocation(ctx, chat, l); err != nil {
		a.log.Warn("guardar ubicación", "err", err)
		if !quiet {
			tg.send(ctx, chat, "No he podido guardar tu ubicación.")
		}
		return
	}
	if quiet {
		return
	}
	zone := ""
	if l.Zone != "" {
		zone = " (" + l.Zone + ")"
	}
	until := "durante 3 horas"
	if l.Live {
		until = "mientras la compartas"
	}
	tg.send(ctx, chat, "📍 Tengo tu ubicación"+zone+". La usaré "+until+" para lo que me pidas «cerca de mí», cómo llegar o el tiempo. "+
		"La IA no ve las coordenadas, solo la zona. /sinubicacion para borrarla.")
}

func (a *agent) locationStatus(ctx context.Context, chat int64) string {
	l, ok := a.store.location(ctx, chat)
	if !ok {
		return "No tengo tu ubicación. Compártela con el clip → Ubicación (o en tiempo real) y la usaré unas horas."
	}
	return fmt.Sprintf("📍 %s, de hace %d min; la uso hasta las %s. /sinubicacion para borrarla.",
		orText(l.Zone, "ubicación guardada"), int(time.Since(l.At).Minutes()), l.Until.In(madrid).Format("15:04"))
}

func orText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var errNoLocation = errors.New("no sé dónde está la persona: pídele que comparta su ubicación en Telegram (clip → Ubicación) o que diga el sitio")

// geoTools son las herramientas de lugares, ubicación y tiempo.
func (a *agent) geoTools() []tool {
	here := func(ctx context.Context) (location, error) {
		l, ok := a.store.location(ctx, a.cfg().TelegramUser)
		if !ok {
			return l, errNoLocation
		}
		return l, nil
	}
	return []tool{
		newTool("lugares_cerca", "Busca negocios y sitios cerca de donde está ahora la persona (farmacia, restaurante, cajero, gasolinera, supermercado, una cadena por su nombre…). Datos de OpenStreetMap: nombre, calle, horario, teléfono, distancia en metros y enlace al mapa. Usa la ubicación que compartió en Telegram.",
			object([]string{"que"}, map[string]map[string]any{
				"que":     str("Qué buscar, en pocas palabras: «farmacia», «restaurante italiano», «Mercadona»."),
				"radio_m": integer("Radio de búsqueda en metros (100 a 10000; por defecto 1000)."),
			}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Que    string
					RadioM int `json:"radio_m"`
				}
				if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Que) == "" {
					return "", errors.New("falta qué buscar")
				}
				l, err := here(ctx)
				if err != nil {
					return "", err
				}
				if in.RadioM == 0 {
					in.RadioM = 1000
				}
				ps, err := a.geo.Places(ctx, in.Que, l.Lat, l.Lon, in.RadioM)
				if err != nil {
					return "", err
				}
				return toJSON(map[string]any{"zona": l.Zone, "lugares": ps, "nota": "Horario en formato de OpenStreetMap; si falta, no se sabe."}), nil
			}),
		newTool("mi_ubicacion", "Dónde está la persona ahora (solo la zona: barrio y ciudad) y desde cuándo, según la ubicación que compartió en Telegram.",
			object(nil, map[string]map[string]any{}),
			func(ctx context.Context, _ json.RawMessage) (string, error) {
				l, err := here(ctx)
				if err != nil {
					return "", err
				}
				return toJSON(map[string]any{"zona": l.Zone, "hace_min": int(time.Since(l.At).Minutes()), "en_tiempo_real": l.Live}), nil
			}),
		newTool("como_llegar", "Enlace de Google Maps con la ruta desde donde está ahora la persona hasta un destino (dirección, sitio o un lugar de lugares_cerca); Maps calcula el tiempo en su móvil.",
			object([]string{"destino"}, map[string]map[string]any{"destino": str("Adónde ir: dirección, nombre del sitio o ciudad.")}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ Destino string }
				if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Destino) == "" {
					return "", errors.New("falta el destino")
				}
				l, err := here(ctx)
				if err != nil {
					return "", err
				}
				return toJSON(map[string]any{"ruta": geo.RouteLink(l.Lat, l.Lon, strings.TrimSpace(in.Destino))}), nil
			}),
		newTool("tiempo", "Previsión del tiempo (Open-Meteo) de hoy y los próximos días: cielo, mínima y máxima, probabilidad de lluvia y a qué horas, viento. Sin lugar, donde está la persona.",
			object(nil, map[string]map[string]any{
				"lugar": str("Ciudad o pueblo (opcional; sin él, la ubicación compartida)."),
				"dias":  integer("Días de previsión, de 1 a 7 (por defecto 2: hoy y mañana)."),
			}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct {
					Lugar string
					Dias  int
				}
				json.Unmarshal(raw, &in)
				if in.Dias == 0 {
					in.Dias = 2
				}
				var lat, lon float64
				place := ""
				if strings.TrimSpace(in.Lugar) != "" {
					var err error
					if lat, lon, place, err = a.geo.City(ctx, in.Lugar); err != nil {
						return "", err
					}
				} else {
					l, err := here(ctx)
					if err != nil {
						return "", errors.New("no sé de dónde: pregunta la ciudad o pide que comparta su ubicación")
					}
					lat, lon, place = l.Lat, l.Lon, l.Zone
				}
				f, err := a.geo.Weather(ctx, lat, lon, in.Dias)
				f.Place = place
				return toJSON(f), err
			}),
	}
}
