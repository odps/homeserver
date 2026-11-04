package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools del asistente para el peso, las medidas y el cardio. Las fotos de
// progreso no pasan nunca por aquí (no van a la IA): se suben en la web.

type bodyIn struct {
	Fecha   string             `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD si no es hoy"`
	PesoKg  float64            `json:"peso_kg,omitempty" jsonschema:"peso en kg (78.4)"`
	Grasa   float64            `json:"grasa_pct,omitempty" jsonschema:"porcentaje de grasa corporal, si lo da la báscula"`
	Medidas map[string]float64 `json:"medidas,omitempty" jsonschema:"perímetros en cm; claves: cuello, hombros, pecho, cintura, cadera, brazo, muslo, gemelo"`
	Nota    string             `json:"nota,omitempty"`
}

type bodyDayView struct {
	Dia     string             `json:"dia"`
	PesoKg  float64            `json:"peso_kg,omitempty"`
	Grasa   float64            `json:"grasa_pct,omitempty"`
	Medidas map[string]float64 `json:"medidas_cm,omitempty"`
	Nota    string             `json:"nota,omitempty"`
}

func bodyDay(b bodyLog) bodyDayView {
	v := bodyDayView{Dia: b.Day.Format("2006-01-02"), Medidas: b.Measures, Nota: b.Note}
	if b.Kg != nil {
		v.PesoKg = *b.Kg
	}
	if b.Fat != nil {
		v.Grasa = *b.Fat
	}
	return v
}

type bodyQueryIn struct {
	Dias int `json:"dias,omitempty" jsonschema:"cuántos días hacia atrás (por defecto 90)"`
}

type trendView struct {
	Ultimo       float64 `json:"ultimo_kg"`
	Dia          string  `json:"dia"`
	Media7       float64 `json:"media_7_dias_kg,omitempty"`
	CambioSemana string  `json:"cambio_media_semanal_kg,omitempty"`
	Cambio4Sem   string  `json:"cambio_media_4_semanas_kg,omitempty"`
	Pesajes      int     `json:"pesajes"`
}

type measureView struct {
	Medida      string  `json:"medida"`
	Ultima      float64 `json:"ultima_cm"`
	Dia         string  `json:"dia"`
	DesdeInicio string  `json:"cambio_desde_la_primera_cm,omitempty"`
	Primera     string  `json:"primera,omitempty"`
}

type bodyOut struct {
	Peso      *trendView    `json:"peso,omitempty"`
	Medidas   []measureView `json:"medidas,omitempty"`
	Registros []bodyDayView `json:"registros"`
	Fotos     int           `json:"fotos_de_progreso"`
}

type cardioIn struct {
	Tipo     string  `json:"tipo" jsonschema:"correr, cinta, caminar, bici, bici_estatica, eliptica, remo, natacion, comba, escaleras u otro (o como lo diga la persona)"`
	Minutos  float64 `json:"minutos" jsonschema:"duración en minutos (28.5 = 28:30)"`
	Km       float64 `json:"km,omitempty" jsonschema:"distancia en km"`
	Pulso    int     `json:"pulso_medio,omitempty" jsonschema:"pulsaciones medias"`
	Kcal     int     `json:"kcal,omitempty"`
	Esfuerzo int     `json:"esfuerzo,omitempty" jsonschema:"de 1 a 10, si lo dice"`
	Fecha    string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD si no es hoy"`
	Nota     string  `json:"nota,omitempty"`
}

type cardioView struct {
	ID       int64   `json:"id"`
	Dia      string  `json:"dia"`
	Tipo     string  `json:"tipo"`
	Duracion string  `json:"duracion"`
	Km       float64 `json:"km,omitempty"`
	Ritmo    string  `json:"ritmo,omitempty"`
	Pulso    int     `json:"pulso_medio,omitempty"`
	Kcal     int     `json:"kcal,omitempty"`
	Esfuerzo int     `json:"esfuerzo,omitempty"`
	Nota     string  `json:"nota,omitempty"`
}

func cardioViewOf(c cardioLog) cardioView {
	t, _ := cardioTypeOf(c.Type)
	return cardioView{ID: c.ID, Dia: c.Day.Format("2006-01-02"), Tipo: t.Name, Duracion: durText(c.Seconds),
		Km: math.Round(c.Meters/10) / 100, Ritmo: paceText(c), Pulso: c.HR, Kcal: c.Kcal, Esfuerzo: c.RPE, Nota: c.Note}
}

type cardioQueryIn struct {
	Dias int `json:"dias,omitempty" jsonschema:"cuántos días hacia atrás (por defecto 30)"`
}

type weekView struct {
	Semana   string  `json:"semana_del"`
	Sesiones int     `json:"sesiones"`
	Minutos  int     `json:"minutos"`
	Km       float64 `json:"km,omitempty"`
}

type cardioOut struct {
	Sesiones []cardioView `json:"sesiones"`
	Semanas  []weekView   `json:"ultimas_semanas"`
}

// dayArg lee la fecha de una tool (vacía: hoy); nada de más de un día en el
// futuro.
func (s *service) dayArg(f string) (time.Time, error) {
	now := s.clock().In(madrid)
	if f == "" {
		return now, nil
	}
	d, err := time.ParseInLocation("2006-01-02", f, madrid)
	if err != nil {
		return time.Time{}, errors.New("la fecha va como AAAA-MM-DD")
	}
	if d.After(now.AddDate(0, 0, 1)) || d.Year() < 2000 {
		return time.Time{}, errors.New("esa fecha no vale")
	}
	return d.Add(12 * time.Hour), nil
}

func optional(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

// bodySummary es lo que ve el asistente de su cuerpo.
func (s *service) bodySummary(ctx context.Context, owner string, days int) (bodyOut, error) {
	if days <= 0 {
		days = 90
	}
	days = min(days, 3660)
	today := s.clock().In(madrid)
	all, err := s.store.bodyLogs(ctx, owner, time.Time{}, today)
	if err != nil {
		return bodyOut{}, err
	}
	out := bodyOut{Registros: []bodyDayView{}}
	if t := trend(all, today); t != nil {
		tv := &trendView{Ultimo: t.Last, Dia: t.LastDay.Format("2006-01-02"), Media7: t.Avg7, Pesajes: t.Count}
		if t.Week != nil {
			tv.CambioSemana = signed(*t.Week)
		}
		if t.Month != nil {
			tv.Cambio4Sem = signed(*t.Month)
		}
		out.Peso = tv
	}
	for _, r := range measureRows(all) {
		mv := measureView{Medida: r.Key, Ultima: r.Last, Dia: r.LastDay.Format("2006-01-02")}
		if r.Times > 1 {
			mv.DesdeInicio = signed(round1(r.Last - r.First))
			mv.Primera = r.FirstDay.Format("2006-01-02")
		}
		out.Medidas = append(out.Medidas, mv)
	}
	from := dayOf(today).AddDate(0, 0, -days)
	for _, b := range all {
		if b.Day.Before(time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)) {
			break
		}
		out.Registros = append(out.Registros, bodyDay(b))
	}
	if err := s.store.db.QueryRow(ctx, `SELECT count(*) FROM photos WHERE owner = $1`, owner).Scan(&out.Fotos); err != nil {
		return bodyOut{}, err
	}
	return out, nil
}

func (s *service) cardioSummary(ctx context.Context, owner string, days int) (cardioOut, error) {
	if days <= 0 {
		days = 30
	}
	days = min(days, 3660)
	today := s.clock().In(madrid)
	list, err := s.store.cardioList(ctx, owner, dayOf(today).AddDate(0, 0, -max(days, 56)), today, 0)
	if err != nil {
		return cardioOut{}, err
	}
	out := cardioOut{Sesiones: []cardioView{}}
	from := dayOf(today).AddDate(0, 0, -days)
	for _, c := range list {
		if c.Day.Before(time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)) {
			break
		}
		out.Sesiones = append(out.Sesiones, cardioViewOf(c))
	}
	for _, w := range cardioWeeks(list, today, 8) {
		out.Semanas = append(out.Semanas, weekView{Semana: w.Monday.Format("2006-01-02"), Sesiones: w.Count,
			Minutos: w.Seconds / 60, Km: math.Round(w.Meters/10) / 100})
	}
	return out, nil
}

func addBodyTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_cuerpo_registrar", Description: "Apunta el peso corporal, el % de grasa o medidas (perímetros en cm) de hoy o de una fecha. Lo que no se pase se queda como estaba ese día"},
		func(ctx context.Context, req *mcp.CallToolRequest, in bodyIn) (*mcp.CallToolResult, bodyDayView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, bodyDayView{}, err
			}
			day, err := svc.dayArg(in.Fecha)
			if err != nil {
				return nil, bodyDayView{}, err
			}
			upd := bodyLog{Kg: optional(in.PesoKg), Fat: optional(in.Grasa), Measures: in.Medidas, Note: in.Nota}
			if upd.empty() {
				return nil, bodyDayView{}, errors.New("no hay nada que apuntar: pasa peso_kg, grasa_pct o medidas")
			}
			b, err := svc.store.saveBody(ctx, who.User, day, upd, true)
			if err != nil {
				return nil, bodyDayView{}, err
			}
			return nil, bodyDay(b), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_cuerpo", Description: "Evolución del peso corporal (último, media de 7 días y cambio semanal y en 4 semanas, calculados por el programa), de las medidas y lo apuntado en los últimos días. Las fotos de progreso solo se ven en la web"},
		func(ctx context.Context, req *mcp.CallToolRequest, in bodyQueryIn) (*mcp.CallToolResult, bodyOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, bodyOut{}, err
			}
			out, err := svc.bodySummary(ctx, who.User, in.Dias)
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_cardio_registrar", Description: "Apunta una sesión de cardio (correr, bici, elíptica, remo, nadar…) con su duración y, si se sabe, distancia, pulso medio, kcal y esfuerzo. El ritmo lo calcula el programa"},
		func(ctx context.Context, req *mcp.CallToolRequest, in cardioIn) (*mcp.CallToolResult, cardioView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, cardioView{}, err
			}
			t, ok := cardioTypeFor(in.Tipo)
			if !ok {
				t, _ = cardioTypeOf("otro")
				if in.Nota == "" {
					in.Nota = in.Tipo
				}
			}
			at, err := svc.dayArg(in.Fecha)
			if err != nil {
				return nil, cardioView{}, err
			}
			if in.Fecha == "" {
				at = svc.clock()
			}
			c := cardioLog{Start: at, Source: "asistente", Type: t.Key, Seconds: int(math.Round(in.Minutos * 60)),
				Meters: math.Round(in.Km * 1000), HR: in.Pulso, Kcal: in.Kcal, RPE: in.Esfuerzo, Note: in.Nota}
			id, err := svc.store.addCardio(ctx, who.User, c)
			if err != nil {
				return nil, cardioView{}, err
			}
			c.ID, c.Day = id, dayOf(at)
			return nil, cardioViewOf(c), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_cardio", Description: "El cardio de los últimos días (con id, duración, distancia y ritmo) y los totales de las últimas 8 semanas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in cardioQueryIn) (*mcp.CallToolResult, cardioOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, cardioOut{}, err
			}
			out, err := svc.cardioSummary(ctx, who.User, in.Dias)
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_cardio_borrar", Description: "Borra una sesión de cardio por su id (de entreno_cardio). Confirma antes con la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, map[string]bool, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nil, err
			}
			if err := svc.store.deleteCardio(ctx, who.User, in.ID); err != nil {
				if errors.Is(err, errNotFound) {
					return nil, nil, fmt.Errorf("no hay ninguna sesión de cardio con id %d", in.ID)
				}
				return nil, nil, err
			}
			return nil, map[string]bool{"borrada": true}, nil
		})
}
