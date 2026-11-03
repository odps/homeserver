package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

type recordIn struct {
	Texto string `json:"texto" jsonschema:"lo hecho, una línea por ejercicio y en el formato de siempre: «Lat machine 3x10 50kg rir 2», «Farmer carry 3x20m 22kg», «Hollow hold 3x45s», «Rir 2» suelto para el anterior"`
	Fecha string `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD si no es hoy"`
}

// exerciseView es un ejercicio de una sesión, legible.
type exerciseView struct {
	Ejercicio string `json:"ejercicio"`
	Series    string `json:"series"`
	Mejor1RM  string `json:"mejor_1rm_estimado,omitempty"`
	Nota      string `json:"nota,omitempty"`
}

type sessionView struct {
	ID         int64          `json:"id"`
	Dia        string         `json:"dia"`
	Ejercicios []exerciseView `json:"ejercicios"`
	Notas      []string       `json:"notas,omitempty"`
}

type recordOut struct {
	Sesion       int64          `json:"sesion"`
	Apuntados    []exerciseView `json:"apuntados"`
	SinReconocer []string       `json:"sin_reconocer,omitempty"`
}

type lastIn struct {
	Sesiones int `json:"sesiones,omitempty" jsonschema:"cuántas (por defecto 5, como mucho 30)"`
}

type sessionsOut struct {
	Sesiones []sessionView `json:"sesiones"`
}

type historyIn struct {
	Ejercicio string `json:"ejercicio" jsonschema:"nombre del ejercicio, como lo diga la persona"`
	Veces     int    `json:"veces,omitempty" jsonschema:"cuántas veces (por defecto 10)"`
}

type historyOut struct {
	Ejercicio string       `json:"ejercicio"`
	Veces     []historyRow `json:"veces"`
	Record    string       `json:"record_1rm_estimado,omitempty"`
}

type historyRow struct {
	Dia    string `json:"dia"`
	Series string `json:"series"`
	E1RM   string `json:"1rm_estimado,omitempty"`
	Nota   string `json:"nota,omitempty"`
}

type aliasIn struct {
	Nombre    string `json:"nombre" jsonschema:"como lo escribe la persona («Pressa»)"`
	Ejercicio string `json:"ejercicio" jsonschema:"clave del catálogo (de entreno_catalogo) o «nuevo» para crear un ejercicio propio con ese nombre"`
}

type catalogIn struct {
	Texto string `json:"texto" jsonschema:"palabras del nombre"`
}

type catalogRow struct {
	Clave   string   `json:"clave"`
	Nombre  string   `json:"nombre"`
	Musculo []string `json:"musculos"`
}

type idIn struct {
	ID int64 `json:"id"`
}

func (s *service) views(ctx context.Context, owner string, x session) sessionView {
	custom, _ := s.store.customNames(ctx, owner)
	v := sessionView{ID: x.ID, Dia: x.Day.Format("2006-01-02"), Notas: x.Notes, Ejercicios: []exerciseView{}}
	for _, e := range x.Exercises {
		v.Ejercicios = append(v.Ejercicios, exView(e, custom))
	}
	return v
}

func exView(e exerciseLog, custom map[string]string) exerciseView {
	ev := exerciseView{Ejercicio: exerciseName(e.Key, e.Name, custom), Series: summarize(e), Nota: e.Note}
	if _, v := best(e); v > 0 {
		ev.Mejor1RM = trimNum(v) + " kg"
	}
	return ev
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_registrar", Description: "Apunta ejercicios del gimnasio en la sesión de hoy (o de la fecha), en el formato de las notas de la persona. Lo interpreta el programa, no tú: pasa el texto tal cual lo dice o lo dicta. Devuelve lo apuntado y los nombres que no reconoce (pregúntale a cuál corresponden y usa entreno_alias)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in recordIn) (*mcp.CallToolResult, recordOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, recordOut{}, err
			}
			at := svc.clock()
			if in.Fecha != "" {
				d, err := time.ParseInLocation("2006-01-02", in.Fecha, madrid)
				if err != nil {
					return nil, recordOut{}, errors.New("fecha no válida: AAAA-MM-DD")
				}
				at = d.Add(12 * time.Hour)
			}
			res, err := svc.record(ctx, who.User, in.Texto, at, "asistente")
			out := recordOut{Sesion: res.SessionID, SinReconocer: res.Unknown, Apuntados: []exerciseView{}}
			custom, _ := svc.store.customNames(ctx, who.User)
			for _, e := range res.Saved {
				out.Apuntados = append(out.Apuntados, exView(e, custom))
			}
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_ultimas", Description: "Las últimas sesiones de gimnasio, con cada ejercicio, sus series y el 1RM estimado"},
		func(ctx context.Context, req *mcp.CallToolRequest, in lastIn) (*mcp.CallToolResult, sessionsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, sessionsOut{}, err
			}
			n := in.Sesiones
			if n <= 0 || n > 30 {
				n = 5
			}
			list, err := svc.store.sessions(ctx, who.User, time.Time{}, svc.clock().AddDate(0, 0, 1), n)
			out := sessionsOut{Sesiones: []sessionView{}}
			for _, x := range list {
				out.Sesiones = append(out.Sesiones, svc.views(ctx, who.User, x))
			}
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_historial", Description: "Cómo ha ido un ejercicio en el tiempo: cada vez que se hizo, con series, carga, RIR y 1RM estimado, y el récord"},
		func(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, historyOut{}, err
			}
			nm, err := svc.store.matcherFor(ctx, who.User)
			if err != nil {
				return nil, historyOut{}, err
			}
			key, ok := nm.resolve(in.Ejercicio)
			if !ok {
				return nil, historyOut{}, fmt.Errorf("no reconozco «%s»: búscalo con entreno_catalogo", in.Ejercicio)
			}
			n := in.Veces
			if n <= 0 || n > 60 {
				n = 10
			}
			list, err := svc.store.history(ctx, who.User, key, 0)
			if err != nil {
				return nil, historyOut{}, err
			}
			custom, _ := svc.store.customNames(ctx, who.User)
			out := historyOut{Ejercicio: exerciseName(key, in.Ejercicio, custom), Veces: []historyRow{}}
			record := 0.0
			for i, d := range list {
				_, v := best(d.Entry)
				record = max(record, v)
				if i < n {
					row := historyRow{Dia: d.Day.Format("2006-01-02"), Series: summarize(d.Entry), Nota: d.Entry.Note}
					if v > 0 {
						row.E1RM = trimNum(v) + " kg"
					}
					out.Veces = append(out.Veces, row)
				}
			}
			if record > 0 {
				out.Record = trimNum(record) + " kg"
			}
			return nil, out, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_ficha", Description: "Cómo se hace un ejercicio: explicación (de wger o free-exercise-db), músculos y material. Cita la fuente si la usas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, map[string]any, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nil, err
			}
			nm, err := svc.store.matcherFor(ctx, who.User)
			if err != nil {
				return nil, nil, err
			}
			key, ok := nm.resolve(in.Ejercicio)
			e, found := lookup(key)
			if !ok || !found {
				return nil, nil, fmt.Errorf("no tengo ficha de «%s»", in.Ejercicio)
			}
			d := details(e)
			desc := d.Desc
			if r := []rune(desc); len(r) > 1500 {
				desc = string(r[:1500]) + "…"
			}
			return nil, map[string]any{"ejercicio": d.Name, "en_ingles": d.English, "como_se_hace": desc, "idioma": d.DescLang,
				"musculos": d.Muscles, "material": d.Equipment, "fuente": d.Sources}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_catalogo", Description: "Busca ejercicios en el catálogo (para saber la clave al asignar un nombre que no se reconoce)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in catalogIn) (*mcp.CallToolResult, map[string][]catalogRow, error) {
			if _, err := identity(req); err != nil {
				return nil, nil, err
			}
			return nil, map[string][]catalogRow{"ejercicios": searchCatalog(in.Texto, 15)}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_alias", Description: "Recuerda que la persona llama así a un ejercicio («Pressa» = prensa de piernas), o crea un ejercicio propio con ese nombre («nuevo»). Después, entreno_registrar ya lo reconoce"},
		func(ctx context.Context, req *mcp.CallToolRequest, in aliasIn) (*mcp.CallToolResult, map[string]string, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nil, err
			}
			key := in.Ejercicio
			if key == "nuevo" {
				if key, err = svc.store.addCustom(ctx, who.User, in.Nombre, "reps"); err != nil {
					return nil, nil, err
				}
			} else if _, ok := lookup(key); !ok {
				return nil, nil, fmt.Errorf("«%s» no es una clave del catálogo: búscala con entreno_catalogo", key)
			}
			if err := svc.store.setAlias(ctx, who.User, in.Nombre, key); err != nil {
				return nil, nil, err
			}
			custom, _ := svc.store.customNames(ctx, who.User)
			return nil, map[string]string{"nombre": in.Nombre, "ejercicio": exerciseName(key, in.Nombre, custom)}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_borrar_sesion", Description: "Borra una sesión de gimnasio entera por su id. Confirma antes con la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, map[string]bool, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]bool{"borrada": true}, svc.store.deleteSession(ctx, who.User, in.ID)
		})
}

// searchCatalog busca en el catálogo por palabras del nombre o los alias.
func searchCatalog(q string, limit int) []catalogRow {
	words := strings.Fields(fold(q))
	type scored struct {
		e     exercise
		score int
	}
	var hits []scored
	for _, e := range allExercises() {
		hay := " " + fold(e.Name+" "+strings.Join(e.Aliases, " ")+" "+strings.Join(e.Muscles, " ")) + " "
		score := 0
		for _, w := range words {
			if strings.Contains(hay, " "+w) {
				score++
			}
		}
		if score == len(words) && score > 0 {
			hits = append(hits, scored{e, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].e.Name < hits[j].e.Name })
	out := []catalogRow{}
	for i, h := range hits {
		if i == limit {
			break
		}
		out = append(out, catalogRow{Clave: h.e.Key, Nombre: h.e.Name, Musculo: h.e.Muscles})
	}
	return out
}
