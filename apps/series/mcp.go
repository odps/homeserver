package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
	"homeserver/platform/seal"
)

// Tools de Talia. Lo que toca ver, el progreso y las fechas los calcula el
// programa (como en la web): la IA solo los cuenta. Respuestas en JSON con
// claves en español y las fechas y horas ya en Madrid.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

// findShow busca una serie seguida por id, por nombre exacto o por parte del
// nombre.
func findShow(v *view, s string) (showView, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return showView{}, errors.New("di qué serie (su nombre o su id)")
	}
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		if sv, ok := v.show(id); ok {
			return sv, nil
		}
	}
	q := strings.Join(seal.Words(s), " ")
	var exact, partial []showView
	for _, sv := range v.shows {
		name := strings.Join(seal.Words(sv.Show.Name), " ")
		switch {
		case name == q:
			exact = append(exact, sv)
		case q != "" && strings.Contains(name, q):
			partial = append(partial, sv)
		}
	}
	list := exact
	if len(list) == 0 {
		list = partial
	}
	switch len(list) {
	case 0:
		return showView{}, errors.New("no sigues ninguna serie que se llame así: búscala con series_buscar y síguela con series_seguir")
	case 1:
		return list[0], nil
	}
	var names []string
	for _, sv := range list {
		names = append(names, sv.Show.Name+" ("+idStr(sv.ID)+")")
	}
	return showView{}, errors.New("hay varias que encajan: " + strings.Join(names, ", ") + ". Usa el id")
}

// ---- Salidas ----

type epOut struct {
	Codigo    string `json:"codigo"` // 3x04
	Temporada int    `json:"temporada"`
	Numero    int    `json:"numero"`
	Titulo    string `json:"titulo,omitempty"`
	Fecha     string `json:"fecha,omitempty"` // AAAA-MM-DD en Madrid
	Hora      string `json:"hora,omitempty"`  // HH:MM en Madrid, si se sabe
	Cuando    string `json:"cuando,omitempty"`
	Estreno   string `json:"estreno,omitempty"`
}

func epOutOf(e episode, today time.Time) *epOut {
	o := &epOut{Codigo: epCode(e), Temporada: e.Season, Numero: e.Number, Titulo: e.Name, Cuando: whenText(e, today), Estreno: premiereNote(e)}
	if d := e.day(); !d.IsZero() {
		o.Fecha = isoDay(d)
	}
	if e.hasTime() {
		o.Hora = e.At.In(madrid).Format("15:04")
	}
	return o
}

type showOut struct {
	ID         int64  `json:"id"`
	Serie      string `json:"serie"`
	Estado     string `json:"estado"`
	EnEmision  string `json:"situacion,omitempty"` // en emisión, terminada…
	Vistos     int    `json:"vistos"`
	Emitidos   int    `json:"emitidos"`
	Quedan     int    `json:"quedan_por_ver"`
	Siguiente  *epOut `json:"siguiente,omitempty"`
	Proximo    *epOut `json:"proximo_en_salir,omitempty"`
	Valoracion int    `json:"valoracion,omitempty"`
	Nota       string `json:"nota,omitempty"`
}

func showOutOf(sv showView, today time.Time) showOut {
	p := sv.Prog
	o := showOut{ID: sv.ID, Serie: sv.Show.Name, Estado: stateLabel(kindShow, sv.State), EnEmision: showStatus(sv.Show.Status),
		Vistos: p.Watched, Emitidos: p.Aired, Quedan: p.Left, Valoracion: sv.Rating, Nota: sv.Note}
	if p.Next != nil {
		o.Siguiente = epOutOf(*p.Next, today)
	}
	if p.Upcoming != nil {
		o.Proximo = epOutOf(*p.Upcoming, today)
	}
	return o
}

type itemOut struct {
	ID         int64  `json:"id"`
	Titulo     string `json:"titulo"`
	Anio       int    `json:"anio,omitempty"`
	Plataforma string `json:"plataforma,omitempty"`
	Estado     string `json:"estado"`
	Valoracion int    `json:"valoracion,omitempty"`
	Fecha      string `json:"fecha,omitempty"`
	Nota       string `json:"nota,omitempty"`
}

func itemOutOf(it item) itemOut {
	return itemOut{ID: it.ID, Titulo: it.Title, Anio: it.Year, Plataforma: it.Platform, Estado: stateLabel(it.Kind, it.State),
		Valoracion: it.Rating, Fecha: it.Day, Nota: it.Note}
}

// ---- Entradas ----

type searchIn struct {
	Consulta string `json:"consulta" jsonschema:"nombre de la serie"`
}

type foundOut struct {
	TVmazeID int64  `json:"tvmaze_id"`
	Nombre   string `json:"nombre"`
	Anio     string `json:"anio,omitempty"`
	Estado   string `json:"situacion,omitempty"`
	Cadena   string `json:"cadena,omitempty"`
	Generos  string `json:"generos,omitempty"`
	Idioma   string `json:"idioma,omitempty"`
	LaSigues bool   `json:"la_sigues"`
	ID       int64  `json:"id,omitempty"`
}

type searchOut struct {
	Resultados []foundOut `json:"resultados"`
	Fuente     string     `json:"fuente"`
}

type followIn struct {
	TVmazeID   int64  `json:"tvmaze_id,omitempty" jsonschema:"id de TVmaze (de series_buscar) para seguir una serie nueva"`
	Serie      string `json:"serie,omitempty" jsonschema:"nombre o id de una serie que ya sigue, para cambiarle el estado, la valoración o la nota, o dejar de seguirla"`
	Estado     string `json:"estado,omitempty" jsonschema:"quiero ver, viendo, en pausa, terminada o abandonada"`
	Valoracion int    `json:"valoracion,omitempty" jsonschema:"de 1 a 5"`
	Nota       string `json:"nota,omitempty" jsonschema:"nota corta de la persona"`
	Dejar      bool   `json:"dejar_de_seguir,omitempty" jsonschema:"true para dejar de seguirla (se borra lo visto). Antes, confirma con la persona"`
}

type followOut struct {
	showOut
	YaLaSeguia bool `json:"ya_la_seguia,omitempty"`
	Dejada     bool `json:"dejada_de_seguir,omitempty"`
}

type watchedIn struct {
	Serie     string `json:"serie" jsonschema:"nombre o id de la serie (de las que sigue)"`
	Temporada int    `json:"temporada,omitempty" jsonschema:"número de temporada; sin episodio, la temporada entera (lo ya emitido)"`
	Episodio  int    `json:"episodio,omitempty" jsonschema:"número del capítulo dentro de la temporada"`
	Hasta     bool   `json:"hasta,omitempty" jsonschema:"true: también todos los anteriores ya emitidos («voy por el 3x04»)"`
	Desmarcar bool   `json:"desmarcar,omitempty" jsonschema:"true para quitar la marca de visto"`
}

type watchedOut struct {
	showOut
	Cambiados int `json:"cambiados"`
}

type pendingOut struct {
	TeToca    []showOut `json:"te_toca"`
	AlDia     []showOut `json:"al_dia"`
	QuieroVer []showOut `json:"quiero_ver"`
	EnPausa   []showOut `json:"en_pausa"`
}

type airingIn struct {
	Dias int `json:"dias,omitempty" jsonschema:"cuántos días desde hoy (por defecto 7, máximo 60)"`
}

type airingOut struct {
	Desde     string      `json:"desde"`
	Hasta     string      `json:"hasta"`
	Capitulos []airingRow `json:"capitulos"`
	Vuelven   []airingRow `json:"vuelven,omitempty"` // estrenos de temporada anunciados más adelante (hasta 90 días)
}

type airingRow struct {
	Serie string `json:"serie"`
	ID    int64  `json:"id"`
	epOut
	Visto bool `json:"visto,omitempty"`
}

type itemIn struct {
	Titulo     string `json:"titulo" jsonschema:"título"`
	Anio       int    `json:"anio,omitempty" jsonschema:"año de estreno o de salida"`
	Plataforma string `json:"plataforma,omitempty" jsonschema:"dónde: cine, Netflix, PS5, Switch… (opcional)"`
	Estado     string `json:"estado,omitempty" jsonschema:"pendiente (quiero ver/jugar), viendo/jugando, en pausa, visto/terminado o abandonado"`
	Valoracion int    `json:"valoracion,omitempty" jsonschema:"de 1 a 5"`
	Fecha      string `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD en que la vio o lo terminó (por defecto hoy si está terminado)"`
	Nota       string `json:"nota,omitempty" jsonschema:"nota corta"`
}

type itemAddOut struct {
	itemOut
	Actualizado bool `json:"ya_estaba_actualizado,omitempty"`
}

type listIn struct {
	Estado string `json:"estado,omitempty" jsonschema:"solo las de este estado (pendiente, viendo, en pausa, terminado, abandonado)"`
}

type listOut struct {
	Lista []itemOut `json:"lista"`
}

// ---- Tools ----

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "series_buscar", Description: "Busca series en TVmaze por nombre. Devuelve su tvmaze_id (para series_seguir), año, situación (en emisión, terminada), cadena y si la persona ya la sigue"},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, searchOut{}, err
			}
			list, err := svc.search(ctx, in.Consulta)
			if err != nil {
				return nil, searchOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, searchOut{}, err
			}
			out := searchOut{Resultados: []foundOut{}, Fuente: "TVmaze (CC BY-SA)"}
			for _, f := range list {
				o := foundOut{TVmazeID: f.TVmazeID, Nombre: f.Name, Anio: f.Year, Estado: showStatus(f.Status), Cadena: f.Network,
					Generos: strings.Join(f.Genres, ", "), Idioma: f.Language}
				if sv, ok := v.byTVmaze(f.TVmazeID); ok {
					o.LaSigues, o.ID = true, sv.ID
				}
				out.Resultados = append(out.Resultados, o)
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "series_seguir", Description: "Sigue una serie (con su tvmaze_id de series_buscar) o cambia el estado, la valoración o la nota de una que ya sigue. Con dejar_de_seguir la quita (pide antes confirmación). Estados: quiero ver, viendo, en pausa, terminada, abandonada"},
		func(ctx context.Context, req *mcp.CallToolRequest, in followIn) (*mcp.CallToolResult, followOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, followOut{}, err
			}
			state := ""
			if strings.TrimSpace(in.Estado) != "" {
				if state = parseState(in.Estado); state == "" {
					return nil, followOut{}, errors.New("estado no válido: quiero ver, viendo, en pausa, terminada o abandonada")
				}
			}
			var fid int64
			existed := false
			switch {
			case in.TVmazeID > 0:
				f, ex, err := svc.followShow(ctx, who.User, in.TVmazeID, state)
				if err != nil {
					return nil, followOut{}, err
				}
				fid, existed = f.ID, ex
			case strings.TrimSpace(in.Serie) != "":
				v, err := svc.load(ctx, who.User)
				if err != nil {
					return nil, followOut{}, err
				}
				sv, err := findShow(v, in.Serie)
				if err != nil {
					return nil, followOut{}, err
				}
				fid, existed = sv.ID, true
				if in.Dejar {
					if err := svc.unfollow(ctx, who.User, sv.ID); err != nil {
						return nil, followOut{}, err
					}
					return nil, followOut{showOut: showOut{ID: sv.ID, Serie: sv.Show.Name, Estado: stateLabel(kindShow, sv.State)}, Dejada: true}, nil
				}
			default:
				return nil, followOut{}, errors.New("falta tvmaze_id (de series_buscar) o el nombre de una serie que ya sigue")
			}
			if in.Valoracion != 0 || in.Nota != "" || (existed && state != "") {
				f, err := svc.store.follow(ctx, who.User, fid)
				if err != nil {
					return nil, followOut{}, err
				}
				st, rating, note := f.State, f.Rating, f.Note
				if state != "" {
					st = state
				}
				if in.Valoracion != 0 {
					rating = in.Valoracion
				}
				if in.Nota != "" {
					note = in.Nota
				}
				if _, err := svc.setMeta(ctx, who.User, fid, st, rating, note); err != nil {
					return nil, followOut{}, err
				}
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, followOut{}, err
			}
			sv, ok := v.show(fid)
			if !ok {
				return nil, followOut{}, errNotFound
			}
			return nil, followOut{showOut: showOutOf(sv, v.today), YaLaSeguia: existed}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "series_visto", Description: "Marca capítulos de una serie que sigue como vistos (o los desmarca): uno (temporada y episodio), hasta uno con los anteriores (hasta=true, «voy por el 3x04»), una temporada entera (solo temporada) o, sin temporada ni episodio, el siguiente que le toca. Devuelve el progreso y el siguiente por ver"},
		func(ctx context.Context, req *mcp.CallToolRequest, in watchedIn) (*mcp.CallToolResult, watchedOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, watchedOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, watchedOut{}, err
			}
			sv, err := findShow(v, in.Serie)
			if err != nil {
				return nil, watchedOut{}, err
			}
			mode, epID, season := markOne, int64(0), 0
			switch {
			case in.Temporada > 0 && in.Episodio > 0:
				e, ok := sv.Show.find(in.Temporada, in.Episodio)
				if !ok {
					return nil, watchedOut{}, errors.New("esa serie no tiene el " + strconv.Itoa(in.Temporada) + "x" + strconv.Itoa(in.Episodio))
				}
				epID = e.ID
				if in.Hasta {
					mode = markUpTo
				}
			case in.Temporada > 0:
				mode, season = markSeason, in.Temporada
			case in.Episodio > 0:
				return nil, watchedOut{}, errors.New("di también la temporada")
			default:
				if in.Desmarcar {
					if sv.Prog.Last == nil {
						return nil, watchedOut{}, errors.New("no tiene nada marcado como visto")
					}
					epID = sv.Prog.Last.ID
				} else {
					if sv.Prog.Next == nil {
						return nil, watchedOut{}, errors.New("va al día: no hay ningún capítulo emitido sin ver")
					}
					epID = sv.Prog.Next.ID
				}
			}
			_, n, err := svc.markWatched(ctx, who.User, sv.ID, mode, epID, season, !in.Desmarcar)
			if err != nil {
				return nil, watchedOut{}, err
			}
			v, err = svc.load(ctx, who.User)
			if err != nil {
				return nil, watchedOut{}, err
			}
			sv, _ = v.show(sv.ID)
			return nil, watchedOut{showOut: showOutOf(sv, v.today), Cambiados: n}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "series_pendientes", Description: "Qué le toca ver: de cada serie que está viendo, el siguiente capítulo emitido sin ver y cuántos le quedan; las que lleva al día (con el próximo en salir); las que quiere ver y las que tiene en pausa"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, pendingOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, pendingOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, pendingOut{}, err
			}
			out := pendingOut{TeToca: []showOut{}, AlDia: []showOut{}, QuieroVer: []showOut{}, EnPausa: []showOut{}}
			for _, sv := range v.shows {
				o := showOutOf(sv, v.today)
				switch sv.State {
				case stateWatching:
					if sv.Prog.Next != nil {
						out.TeToca = append(out.TeToca, o)
					} else {
						out.AlDia = append(out.AlDia, o)
					}
				case statePending:
					out.QuieroVer = append(out.QuieroVer, o)
				case statePaused:
					out.EnPausa = append(out.EnPausa, o)
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "series_estrenos", Description: "Capítulos de las series que sigue que salen en los próximos días (por defecto 7), con fecha y hora en Madrid y si es estreno de temporada; y las temporadas nuevas anunciadas para más adelante (hasta 90 días)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in airingIn) (*mcp.CallToolResult, airingOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, airingOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, airingOut{}, err
			}
			days := in.Dias
			if days <= 0 {
				days = 7
			}
			days = min(days, 60)
			to := v.today.Add(time.Duration(days-1) * dayUnit)
			out := airingOut{Desde: isoDay(v.today), Hasta: isoDay(to), Capitulos: []airingRow{}}
			for _, a := range v.week(v.today, to) {
				out.Capitulos = append(out.Capitulos, airingRow{Serie: a.View.Show.Name, ID: a.View.ID, epOut: *epOutOf(a.Episode, v.today), Visto: a.View.watched[a.Episode.ID]})
			}
			for _, a := range v.week(to.Add(dayUnit), v.today.Add(90*dayUnit)) {
				if a.Episode.premiere() {
					out.Vuelven = append(out.Vuelven, airingRow{Serie: a.View.Show.Name, ID: a.View.ID, epOut: *epOutOf(a.Episode, v.today)})
				}
			}
			return nil, out, nil
		})

	for _, k := range []string{kindFilm, kindGame} {
		kind := k
		name, what, plural := "pelis", "una peli", "pelis"
		if kind == kindGame {
			name, what, plural = "juegos", "un juego", "juegos"
		}
		mcp.AddTool(server, &mcp.Tool{Name: name + "_anadir", Description: "Apunta " + what + " en su lista (o la pone al día si ya estaba con ese título): título, año, plataforma, estado (pendiente, " +
			map[string]string{kindFilm: "viendo, en pausa, vista", kindGame: "jugando, en pausa, terminado"}[kind] + " o abandonado), valoración de 1 a 5, fecha y nota. Solo lo anota"},
			func(ctx context.Context, req *mcp.CallToolRequest, in itemIn) (*mcp.CallToolResult, itemAddOut, error) {
				who, err := identity(req)
				if err != nil {
					return nil, itemAddOut{}, err
				}
				state := ""
				if strings.TrimSpace(in.Estado) != "" {
					if state = parseState(in.Estado); state == "" {
						return nil, itemAddOut{}, errors.New("estado no válido: pendiente, viendo, en pausa, terminado o abandonado")
					}
				}
				it, existed, err := svc.addItem(ctx, who.User, kind, itemInput{Title: in.Titulo, Year: in.Anio, Platform: in.Plataforma, State: state,
					Rating: in.Valoracion, Day: in.Fecha, Note: in.Nota, Origin: "asistente"}, true)
				if err != nil {
					return nil, itemAddOut{}, err
				}
				return nil, itemAddOut{itemOut: itemOutOf(it), Actualizado: existed}, nil
			})
		mcp.AddTool(server, &mcp.Tool{Name: name + "_lista", Description: "Su lista de " + plural + ": lo que está en curso, lo pendiente y lo terminado (lo último primero), con valoración y nota"},
			func(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
				who, err := identity(req)
				if err != nil {
					return nil, listOut{}, err
				}
				want := ""
				if strings.TrimSpace(in.Estado) != "" {
					if want = parseState(in.Estado); want == "" {
						return nil, listOut{}, errors.New("estado no válido: pendiente, viendo, en pausa, terminado o abandonado")
					}
				}
				list, err := svc.store.items(ctx, who.User, kind)
				if err != nil {
					return nil, listOut{}, err
				}
				sortItems(list)
				out := listOut{Lista: []itemOut{}}
				for _, it := range list {
					if want == "" || it.State == want {
						out.Lista = append(out.Lista, itemOutOf(it))
					}
				}
				return nil, out, nil
			})
	}
}
