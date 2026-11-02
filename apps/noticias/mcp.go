package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools para el asistente: el digest de hoy, buscar entre lo recogido y ver
// o cambiar los ajustes, intereses y feeds propios. Actúan en nombre de la
// persona del token.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

type searchIn struct {
	Texto string `json:"texto" jsonschema:"palabras a buscar en los titulares y entradillas de los últimos días"`
}

type newsOut struct {
	Noticias []item `json:"noticias"`
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_hoy", Description: "El digest de noticias de hoy de esta persona: resumen, lo más importante y sus intereses, con enlaces"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, digest, error) {
			who, err := identity(req)
			if err != nil {
				return nil, digest{}, err
			}
			if err := svc.store.ensureUser(ctx, who.User); err != nil {
				return nil, digest{}, err
			}
			d, err := svc.today(ctx, who.User, time.Now())
			return nil, d, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_buscar", Description: "Busca noticias recogidas en los últimos 3 días (medios de España y del mundo, y las categorías, intereses, redes y subforos de esta persona) que contengan todas las palabras"},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, newsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, newsOut{}, err
			}
			want := words(in.Texto)
			if len(want) == 0 {
				return nil, newsOut{}, errors.New("faltan las palabras a buscar")
			}
			feeds := []string{}
			for _, g := range generalSources {
				feeds = append(feeds, g.URL)
			}
			topics, err := svc.store.topics(ctx, who.User)
			if err != nil {
				return nil, newsOut{}, err
			}
			for _, t := range topics {
				if t.Query != "" {
					feeds = append(feeds, topicFeedURL(t.Query))
				}
				for _, f := range t.Feeds {
					feeds = append(feeds, f.URL)
				}
			}
			if p, err := svc.store.prefs(ctx, who.User); err == nil {
				for _, k := range p.Presets {
					if pr, ok := presetByKey(k); ok {
						feeds = append(feeds, pr.Feeds()...)
					}
				}
				for _, r := range p.Reddit {
					feeds = append(feeds, redditFeedURL(r))
				}
			}
			items, err := svc.store.itemsSince(ctx, feeds, time.Now().Add(-72*time.Hour))
			if err != nil {
				return nil, newsOut{}, err
			}
			out := newsOut{Noticias: []item{}}
			seen := map[string]bool{}
			for _, it := range items {
				have := map[string]bool{}
				for _, w := range words(it.Title + " " + it.Summary) {
					have[w] = true
				}
				match := true
				for _, w := range want {
					if !have[w] {
						match = false
						break
					}
				}
				key := strings.ToLower(it.Title)
				if match && !seen[key] {
					seen[key] = true
					out.Noticias = append(out.Noticias, it)
					if len(out.Noticias) == 15 {
						break
					}
				}
			}
			return nil, out, nil
		})
	addSettingsTools(server, svc)
}

// --- Ajustes ---

type optionView struct {
	Clave       string `json:"clave"`
	Nombre      string `json:"nombre"`
	Tipo        string `json:"tipo"`
	Descripcion string `json:"descripcion,omitempty"`
	Activa      bool   `json:"activa"`
}

type feedView struct {
	ID     int64  `json:"id"`
	URL    string `json:"url"`
	Titulo string `json:"titulo,omitempty"`
	Falla  string `json:"falla,omitempty"`
}

type interestView struct {
	ID       int64      `json:"id"`
	Nombre   string     `json:"nombre"`
	Busqueda string     `json:"busqueda,omitempty"`
	Feeds    []feedView `json:"feeds,omitempty"`
}

type interestsOut struct {
	Intereses []interestView `json:"intereses"`
}

type prefsView struct {
	Hora          string         `json:"hora"`
	ResumenIA     bool           `json:"resumen_ia"`
	Importantes   int            `json:"importantes"`
	Mundo         bool           `json:"mundo"`
	NoticiasMundo int            `json:"noticias_mundo"`
	PorSeccion    int            `json:"por_seccion"`
	Secciones     []optionView   `json:"secciones"`
	Medios        []optionView   `json:"medios"`
	Subforos      []string       `json:"subforos"`
	Intereses     []interestView `json:"intereses"`
}

type prefsIn struct {
	Hora                *string  `json:"hora,omitempty" jsonschema:"hora de Madrid a la que se prepara el digest de cada día, como 07:00"`
	ResumenIA           *bool    `json:"resumen_ia,omitempty" jsonschema:"si el digest empieza con un resumen del día escrito por la IA"`
	Importantes         *int     `json:"importantes,omitempty" jsonschema:"noticias en «Lo más importante», de 3 a 30"`
	Mundo               *bool    `json:"mundo,omitempty" jsonschema:"si sale la sección «En el mundo»"`
	NoticiasMundo       *int     `json:"noticias_mundo,omitempty" jsonschema:"noticias en «En el mundo», de 3 a 30"`
	PorSeccion          *int     `json:"por_seccion,omitempty" jsonschema:"noticias de cada categoría, interés, red o subforo, de 1 a 15"`
	ActivarSecciones    []string `json:"activar_secciones,omitempty" jsonschema:"claves de categorías o redes que añadir al digest (las da noticias_ajustes_ver)"`
	DesactivarSecciones []string `json:"desactivar_secciones,omitempty" jsonschema:"claves de categorías o redes que quitar del digest"`
	ActivarMedios       []string `json:"activar_medios,omitempty" jsonschema:"claves de medios generales que volver a usar"`
	DesactivarMedios    []string `json:"desactivar_medios,omitempty" jsonschema:"claves de medios generales que dejar de usar"`
	AnadirSubforos      []string `json:"anadir_subforos,omitempty" jsonschema:"subforos de Reddit que añadir (golang o r/golang); como mucho 10 en total"`
	QuitarSubforos      []string `json:"quitar_subforos,omitempty" jsonschema:"subforos de Reddit que quitar"`
}

type interestIn struct {
	Nombre   string `json:"nombre" jsonschema:"nombre de la sección en el digest"`
	Busqueda string `json:"busqueda,omitempty" jsonschema:"qué buscar en Google News (vacío: solo sus feeds propios)"`
}

type interestChangeIn struct {
	ID       int64   `json:"id" jsonschema:"id del interés (de noticias_ajustes_ver)"`
	Nombre   *string `json:"nombre,omitempty" jsonschema:"nombre nuevo"`
	Busqueda *string `json:"busqueda,omitempty" jsonschema:"búsqueda nueva; vacío para dejar solo sus feeds"`
}

type interestIDIn struct {
	ID int64 `json:"id" jsonschema:"id del interés (de noticias_ajustes_ver)"`
}

type feedIn struct {
	InteresID int64  `json:"interes_id" jsonschema:"id del interés en cuya sección saldrá"`
	URL       string `json:"url" jsonschema:"dirección https:// del feed RSS o Atom"`
}

type feedIDIn struct {
	ID int64 `json:"id" jsonschema:"id del feed propio (de noticias_ajustes_ver)"`
}

const onlyAsked = " Cambia solo lo que la persona ha pedido de forma explícita; lo que no se indica se queda como está."

// interests pasa los intereses a la vista del asistente, con el estado de
// sus feeds.
func interests(ctx context.Context, st store, user string) ([]interestView, error) {
	topics, err := st.topics(ctx, user)
	if err != nil {
		return nil, err
	}
	states, err := st.feedStates(ctx, knownFeeds(topics))
	if err != nil {
		return nil, err
	}
	out := []interestView{}
	for _, t := range topics {
		v := interestView{ID: t.ID, Nombre: t.Name, Busqueda: t.Query}
		for _, f := range t.Feeds {
			v.Feeds = append(v.Feeds, feedView{ID: f.ID, URL: f.URL, Titulo: states[f.URL].Title, Falla: states[f.URL].Error})
		}
		out = append(out, v)
	}
	return out, nil
}

func prefsOf(ctx context.Context, st store, user string) (prefsView, error) {
	p, err := st.prefs(ctx, user)
	if err != nil {
		return prefsView{}, err
	}
	v := prefsView{Hora: p.DigestAt, ResumenIA: p.AISummary, Importantes: p.TopCount, Mundo: p.World, NoticiasMundo: p.WorldCount,
		PorSeccion: p.TopicCount, Secciones: []optionView{}, Medios: []optionView{}, Subforos: p.Reddit}
	for _, pr := range presets {
		tipo := "categoría"
		if pr.Group == "redes" {
			tipo = "red"
		}
		v.Secciones = append(v.Secciones, optionView{Clave: pr.Key, Nombre: pr.Name, Tipo: tipo, Descripcion: pr.Hint, Activa: slices.Contains(p.Presets, pr.Key)})
	}
	for _, g := range generalSources {
		tipo := "España («Lo más importante»)"
		if g.Group == "mundo" {
			tipo = "internacional («En el mundo»)"
		}
		v.Medios = append(v.Medios, optionView{Clave: g.Key, Nombre: g.Name, Tipo: tipo, Activa: !slices.Contains(p.Disabled, g.Key)})
	}
	v.Intereses, err = interests(ctx, st, user)
	return v, err
}

// toggle activa (on) y desactiva (off) claves de una lista y la deja en el
// orden de all. Una clave que no está en all es un error.
func toggle(list, on, off, all []string, what string) ([]string, error) {
	set := map[string]bool{}
	for _, k := range list {
		set[k] = true
	}
	for i, keys := range [][]string{on, off} {
		for _, k := range keys {
			if !slices.Contains(all, k) {
				return nil, fmt.Errorf("no existe %s «%s»: usa las claves de noticias_ajustes_ver", what, k)
			}
			set[k] = i == 0
		}
	}
	out := []string{}
	for _, k := range all {
		if set[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// changePrefs aplica a los ajustes solo lo que trae in, con las mismas
// comprobaciones que la web.
func changePrefs(p prefs, in prefsIn) (prefs, error) {
	if in.Hora != nil {
		p.DigestAt = *in.Hora
	}
	if in.ResumenIA != nil {
		p.AISummary = *in.ResumenIA
	}
	if in.Importantes != nil {
		p.TopCount = *in.Importantes
	}
	if in.Mundo != nil {
		p.World = *in.Mundo
	}
	if in.NoticiasMundo != nil {
		p.WorldCount = *in.NoticiasMundo
	}
	if in.PorSeccion != nil {
		p.TopicCount = *in.PorSeccion
	}
	var keys, sources []string
	for _, pr := range presets {
		keys = append(keys, pr.Key)
	}
	for _, g := range generalSources {
		sources = append(sources, g.Key)
	}
	var err error
	if p.Presets, err = toggle(p.Presets, in.ActivarSecciones, in.DesactivarSecciones, keys, "la sección"); err != nil {
		return p, err
	}
	// De los medios se guardan los desactivados: va al revés.
	if p.Disabled, err = toggle(p.Disabled, in.DesactivarMedios, in.ActivarMedios, sources, "el medio"); err != nil {
		return p, err
	}
	add, bad := subreddits(in.AnadirSubforos)
	drop, bad2 := subreddits(in.QuitarSubforos)
	if bad = append(bad, bad2...); len(bad) > 0 {
		return p, errors.New("estos subforos no tienen un nombre válido: " + strings.Join(bad, ", "))
	}
	reddit := []string{}
	for _, r := range append(slices.Clone(p.Reddit), add...) {
		same := func(d string) bool { return strings.EqualFold(d, r) }
		if !slices.ContainsFunc(drop, same) && !slices.ContainsFunc(reddit, same) {
			reddit = append(reddit, r)
		}
	}
	p.Reddit = reddit
	return p, p.check()
}

func addSettingsTools(server *mcp.Server, svc *service) {
	st := svc.store
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_ajustes_ver", Description: "Ajustes del digest de noticias de esta persona: hora, resumen de IA, cuántas noticias, categorías y redes, medios generales, subforos de Reddit y sus intereses con sus feeds propios (con las claves e ids para cambiarlos)"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, prefsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, prefsView{}, err
			}
			if err := st.ensureUser(ctx, who.User); err != nil {
				return nil, prefsView{}, err
			}
			v, err := prefsOf(ctx, st, who.User)
			return nil, v, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_ajustes_cambiar", Description: "Cambia los ajustes del digest de noticias. Las categorías, redes y medios se activan o desactivan por su clave (de noticias_ajustes_ver); los subforos se añaden o quitan." + onlyAsked + " Devuelve los ajustes como quedan."},
		func(ctx context.Context, req *mcp.CallToolRequest, in prefsIn) (*mcp.CallToolResult, prefsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, prefsView{}, err
			}
			p, err := st.prefs(ctx, who.User)
			if err != nil {
				return nil, prefsView{}, err
			}
			if p, err = changePrefs(p, in); err != nil {
				return nil, prefsView{}, err
			}
			if err := st.savePrefs(ctx, who.User, p); err != nil {
				return nil, prefsView{}, err
			}
			v, err := prefsOf(ctx, st, who.User)
			return nil, v, err
		})
	// Las de intereses y feeds devuelven los intereses como quedan.
	done := func(ctx context.Context, user string, err error) (*mcp.CallToolResult, interestsOut, error) {
		if errors.Is(err, errNotFound) {
			err = errors.New("no existe ese interés o feed: mira los ids con noticias_ajustes_ver")
		}
		if err != nil {
			return nil, interestsOut{}, err
		}
		l, err := interests(ctx, st, user)
		return nil, interestsOut{Intereses: l}, err
	}
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_interes_anadir", Description: "Añade un interés: una sección del digest con lo que encuentre Google News para su búsqueda y con los feeds propios que se le pongan." + onlyAsked},
		func(ctx context.Context, req *mcp.CallToolRequest, in interestIn) (*mcp.CallToolResult, interestsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, interestsOut{}, err
			}
			if err := st.ensureUser(ctx, who.User); err != nil {
				return nil, interestsOut{}, err
			}
			_, err = svc.addTopic(ctx, who.User, in.Nombre, in.Busqueda)
			return done(ctx, who.User, err)
		})
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_interes_cambiar", Description: "Renombra un interés o cambia lo que busca." + onlyAsked},
		func(ctx context.Context, req *mcp.CallToolRequest, in interestChangeIn) (*mcp.CallToolResult, interestsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, interestsOut{}, err
			}
			topics, err := st.topics(ctx, who.User)
			if err != nil {
				return nil, interestsOut{}, err
			}
			i := slices.IndexFunc(topics, func(t topic) bool { return t.ID == in.ID })
			if i < 0 {
				return done(ctx, who.User, errNotFound)
			}
			name, query := topics[i].Name, topics[i].Query
			if in.Nombre != nil {
				name = *in.Nombre
			}
			if in.Busqueda != nil {
				query = *in.Busqueda
			}
			return done(ctx, who.User, svc.updateTopic(ctx, who.User, in.ID, name, query))
		})
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_interes_borrar", Description: "Borra un interés y sus feeds propios. Solo si la persona lo ha pedido de forma explícita."},
		func(ctx context.Context, req *mcp.CallToolRequest, in interestIDIn) (*mcp.CallToolResult, interestsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, interestsOut{}, err
			}
			return done(ctx, who.User, st.deleteTopic(ctx, who.User, in.ID))
		})
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_feed_anadir", Description: "Añade un feed RSS o Atom propio a la sección de un interés. Lo lee antes de guardarlo: si no es un feed, da error." + onlyAsked},
		func(ctx context.Context, req *mcp.CallToolRequest, in feedIn) (*mcp.CallToolResult, interestsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, interestsOut{}, err
			}
			_, err = svc.addFeed(ctx, who.User, in.InteresID, in.URL)
			return done(ctx, who.User, err)
		})
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_feed_quitar", Description: "Quita un feed propio de su interés. Solo si la persona lo ha pedido de forma explícita."},
		func(ctx context.Context, req *mcp.CallToolRequest, in feedIDIn) (*mcp.CallToolResult, interestsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, interestsOut{}, err
			}
			return done(ctx, who.User, st.deleteOwnFeed(ctx, who.User, in.ID))
		})
}
