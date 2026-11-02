package transit

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Las páginas de llegadas son HTML hecho a mano y estable desde hace años;
// se leen con expresiones regulares. Si cambian, los tests con las páginas
// reales guardadas y el test en vivo lo dirán.

var (
	// Cabecera: «2314 - Periodista Azzati - Plaça de l'Ajuntament» (EMT) o
	// «Xàtiva» (metro), en el recuadro gris.
	reHeader = regexp.MustCompile(`padding-top: 10px;">\s*([^<]*?)\s*</div>`)

	// EMT: <img … title="19" …></span><span …>&nbsp;&nbsp;La Malva-rosa - 19 min.</span>
	reBusRow = regexp.MustCompile(`(?s)<img[^>]*\btitle="([^"]*)"[^>]*>\s*</span>\s*<span[^>]*>(.*?)</span>`)
	reTag    = regexp.MustCompile(`<[^>]*>`)
	reMin    = regexp.MustCompile(`^(\d+)\s*min\.?$`)

	// Metro: <img src="…/linea_metro_5.png" …> … <b>Machado</b> - <span>23:12:00</span>
	reMetroRow = regexp.MustCompile(`(?s)linea_metro_(\w+)\.png.*?<b>\s*(.*?)\s*</b>\s*-\s*<span>\s*([^<]*?)\s*</span>`)
)

func text(s string) string {
	s = html.UnescapeString(reTag.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ") // Fields también corta en &nbsp; (U+00A0)
}

// busBoard lee la página de próximas llegadas de la EMT, la que enlaza el
// campo proximas_llegadas de las paradas (por HTTPS).
func (c *Client) busBoard(ctx context.Context, id string) (Board, error) {
	body, err := c.get(ctx, c.EMT+"/QR.php?sec=est&p="+id, 512<<10)
	if err != nil {
		return Board{}, fmt.Errorf("EMT: %w", err)
	}
	return parseBus(string(body), id, c.now())
}

func parseBus(page, id string, now time.Time) (Board, error) {
	h := reHeader.FindStringSubmatch(page)
	if h == nil || text(h[1]) == "" {
		return Board{}, fmt.Errorf("EMT: %w %s", ErrUnknownStop, id)
	}
	b := Board{Name: strings.TrimPrefix(text(h[1]), id+" - "), Arrivals: []Arrival{}}
	seen := map[string]bool{} // la página repite a veces la misma llegada
	for _, m := range reBusRow.FindAllStringSubmatch(page, -1) {
		line, t := strings.ToUpper(text(m[1])), text(m[2])
		if line == "" || t == "" || seen[line+"|"+t] {
			continue
		}
		seen[line+"|"+t] = true
		a := Arrival{Line: line}
		toward, when, ok := cutLast(t, " - ")
		if !ok {
			a.Note = t
			b.Arrivals = append(b.Arrivals, a)
			continue
		}
		a.Toward = toward
		if mm := reMin.FindStringSubmatch(when); mm != nil {
			n, _ := strconv.Atoi(mm[1])
			a.Minutes = intPtr(n)
		} else if hh, mi, ok := clock(when); ok {
			a.Time = fmt.Sprintf("%02d:%02d", hh%24, mi)
			a.Minutes = intPtr(minutesUntil(now, hh, mi))
		} else {
			a.Note = when
			// «Próximo»: está llegando.
			if f := fold(when); strings.HasPrefix(f, "proxim") || strings.HasPrefix(f, "llegando") || strings.HasPrefix(f, "en parada") {
				a.Minutes = intPtr(0)
			}
		}
		b.Arrivals = append(b.Arrivals, a)
	}
	return b, nil
}

// metroBoard lee la página de próximas salidas de Metrovalencia del
// Geoportal, la que enlaza el campo proximas_llegadas de las estaciones.
func (c *Client) metroBoard(ctx context.Context, code string) (Board, error) {
	body, err := c.get(ctx, c.Geoportal+"/geoportal-services/api/v1/salidas-metro.html?estacion="+code+"&lang=es", 512<<10)
	if err != nil {
		return Board{}, fmt.Errorf("Metrovalencia: %w", err)
	}
	return parseMetro(string(body), c.now()), nil
}

func parseMetro(page string, now time.Time) Board {
	b := Board{Arrivals: []Arrival{}}
	if h := reHeader.FindStringSubmatch(page); h != nil {
		b.Name = text(h[1])
	}
	seen := map[string]bool{}
	for _, m := range reMetroRow.FindAllStringSubmatch(page, -1) {
		line, toward, when := strings.ToUpper(text(m[1])), text(m[2]), text(m[3])
		// La página repite trenes: se quitan los iguales.
		if k := line + "|" + toward + "|" + when; seen[k] {
			continue
		} else {
			seen[k] = true
		}
		a := Arrival{Line: line, Toward: toward}
		if hh, mi, ok := clock(when); ok {
			a.Time = fmt.Sprintf("%02d:%02d", hh%24, mi)
			a.Minutes = intPtr(minutesUntil(now, hh, mi))
		} else {
			a.Note = when
		}
		b.Arrivals = append(b.Arrivals, a)
	}
	return b
}

func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):]), true
}
