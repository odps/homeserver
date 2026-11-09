package main

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools de Talia. Respuestas en JSON con claves en español. La dirección de
// cada artículo solo sale al guardarlo y en leer_articulo (lo justo para
// pasársela a la persona); el texto, solo en leer_articulo y recortado.

const (
	maxToolText = 20000 // letras del texto que se le pasan a la IA
	toolLimit   = 20
	toolMax     = 100
)

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

// ---- Salidas ----

type itemOut struct {
	ID        int64    `json:"id"`
	Titulo    string   `json:"titulo"`
	Sitio     string   `json:"sitio"`
	Minutos   int      `json:"minutos,omitempty"`
	Etiquetas []string `json:"etiquetas,omitempty"`
	Guardado  string   `json:"guardado"` // AAAA-MM-DD
	Leido     bool     `json:"leido"`
	Favorito  bool     `json:"favorito"`
	Archivado bool     `json:"archivado"`
	Error     string   `json:"error_descarga,omitempty"`
	Extracto  string   `json:"extracto,omitempty"`
}

func itemOf(a article, excerpt bool) itemOut {
	o := itemOut{ID: a.ID, Titulo: a.title(), Sitio: a.site(), Minutos: a.Minutes, Etiquetas: a.Tags, Guardado: a.Created.Format("2006-01-02"),
		Leido: a.Read, Favorito: a.Favorite, Archivado: a.Archived}
	switch a.State {
	case stateError:
		o.Error = a.Err
	case stateQueued:
		o.Error = "aún descargándose"
	}
	if excerpt {
		o.Extracto = oneLine(a.Excerpt, 200)
	}
	return o
}

type listOut struct {
	Lista      string    `json:"lista"`
	Total      int       `json:"total"`
	PorLeer    int       `json:"pendientes_sin_leer"`
	Articulos  []itemOut `json:"articulos"`
	Etiquetas  []string  `json:"etiquetas_usadas,omitempty"`
	Recortados bool      `json:"hay_mas,omitempty"`
}

// ---- Entradas ----

type saveIn struct {
	URL       string   `json:"url" jsonschema:"la dirección de la página (https://…)"`
	Etiquetas []string `json:"etiquetas,omitempty" jsonschema:"etiquetas opcionales, por ejemplo [\"cocina\", \"viajes\"]"`
}

type saveOut struct {
	ID        int64    `json:"id"`
	YaEstaba  bool     `json:"ya_estaba"`
	Titulo    string   `json:"titulo"`
	Sitio     string   `json:"sitio"`
	Minutos   int      `json:"minutos,omitempty"`
	Etiquetas []string `json:"etiquetas,omitempty"`
	URL       string   `json:"url"`
	Leido     bool     `json:"texto_extraido"`
	Error     string   `json:"error_descarga,omitempty"`
}

type listIn struct {
	Lista    string `json:"lista,omitempty" jsonschema:"pendientes (por defecto), favoritos, archivo o todos"`
	Etiqueta string `json:"etiqueta,omitempty" jsonschema:"solo los que llevan esta etiqueta"`
	Limite   int    `json:"limite,omitempty" jsonschema:"cuántos, los más recientes (por defecto 20, máximo 100)"`
}

type searchIn struct {
	Texto  string `json:"texto" jsonschema:"palabras que buscar en el título, el sitio, el autor, las etiquetas y el texto (todas tienen que estar)"`
	Lista  string `json:"lista,omitempty" jsonschema:"todos (por defecto), pendientes, favoritos o archivo"`
	Limite int    `json:"limite,omitempty" jsonschema:"cuántos como mucho (por defecto 20, máximo 100)"`
}

type idIn struct {
	ID int64 `json:"id" jsonschema:"el id del artículo (de leer_lista o leer_buscar)"`
}

type articleIn struct {
	ID          int64 `json:"id" jsonschema:"el id del artículo (de leer_lista o leer_buscar)"`
	MaxCaracter int   `json:"max_caracteres,omitempty" jsonschema:"recorta el texto a estas letras (por defecto y como mucho 20000)"`
}

type articleOut struct {
	itemOut
	Autor     string `json:"autor,omitempty"`
	Fecha     string `json:"fecha_publicacion,omitempty"`
	URL       string `json:"url"`
	Texto     string `json:"texto"`
	Recortado bool   `json:"texto_recortado,omitempty"`
}

type markIn struct {
	ID        int64     `json:"id" jsonschema:"el id del artículo"`
	Leido     *bool     `json:"leido,omitempty" jsonschema:"true para marcarlo leído, false para no leído"`
	Archivado *bool     `json:"archivado,omitempty" jsonschema:"true para archivarlo (cuenta como leído), false para devolverlo a pendientes"`
	Favorito  *bool     `json:"favorito,omitempty" jsonschema:"true para ponerlo en favoritos, false para quitarlo"`
	Etiquetas *[]string `json:"etiquetas,omitempty" jsonschema:"si se da, sustituye todas sus etiquetas"`
}

type deleteOut struct {
	Borrado itemOut `json:"borrado"`
}

func limitOf(n int) int {
	switch {
	case n <= 0:
		return toolLimit
	case n > toolMax:
		return toolMax
	}
	return n
}

func listName(l, def string) (string, error) {
	l = strings.ToLower(strings.TrimSpace(l))
	switch l {
	case "":
		return def, nil
	case "archivados", "archivadas", "archivo":
		return listArchive, nil
	case "pendiente", "pendientes":
		return listPending, nil
	case "favorito", "favoritos":
		return listFavorite, nil
	case "todo", "todos":
		return listAll, nil
	}
	return "", errors.New("la lista es pendientes, favoritos, archivo o todos")
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "leer_guardar", Description: "Guarda un enlace en «Leer después» para leerlo más tarde. El servidor descarga la página y saca el texto limpio y el tiempo de lectura; si no puede, el enlace queda guardado igualmente con el error. Si ya estaba, lo devuelve (y lo saca del archivo)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, saveOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, saveOut{}, err
			}
			a, existed, err := svc.save(ctx, who.User, in.URL, in.Etiquetas, "asistente")
			if err != nil {
				return nil, saveOut{}, err
			}
			out := saveOut{ID: a.ID, YaEstaba: existed, Titulo: a.title(), Sitio: a.site(), Minutos: a.Minutes, Etiquetas: a.Tags, URL: a.URL,
				Leido: a.State == stateOK}
			if a.State == stateError {
				out.Error = a.Err
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "leer_lista", Description: "Los artículos guardados en «Leer después» de una lista (pendientes por defecto, favoritos, archivo o todos), del más reciente al más antiguo, con su tiempo de lectura y etiquetas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, listOut{}, err
			}
			list, err := listName(in.Lista, listPending)
			if err != nil {
				return nil, listOut{}, err
			}
			return listResult(ctx, svc, who.User, list, filter{List: list, Tag: in.Etiqueta, Limit: limitOf(in.Limite)}, true)
		})

	mcp.AddTool(server, &mcp.Tool{Name: "leer_buscar", Description: "Busca en «Leer después» por palabras (en el título, el sitio, el autor, las etiquetas y el texto de cada artículo)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, listOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, listOut{}, err
			}
			if strings.TrimSpace(in.Texto) == "" {
				return nil, listOut{}, errors.New("di qué buscar")
			}
			list, err := listName(in.Lista, listAll)
			if err != nil {
				return nil, listOut{}, err
			}
			return listResult(ctx, svc, who.User, list, filter{List: list, Query: in.Texto, Limit: limitOf(in.Limite)}, true)
		})

	mcp.AddTool(server, &mcp.Tool{Name: "leer_articulo", Description: "El texto limpio de un artículo guardado (recortado a 20.000 letras), con su título, sitio, autor, fecha y dirección: para resumirlo o responder preguntas sobre él. No lo marca como leído"},
		func(ctx context.Context, req *mcp.CallToolRequest, in articleIn) (*mcp.CallToolResult, articleOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, articleOut{}, err
			}
			a, err := svc.store.get(ctx, who.User, in.ID)
			if errors.Is(err, errNotFound) {
				return nil, articleOut{}, errors.New("ese artículo no existe")
			}
			if err != nil {
				return nil, articleOut{}, err
			}
			blocks, err := svc.store.body(ctx, who.User, a.ID)
			if err != nil {
				return nil, articleOut{}, err
			}
			max := in.MaxCaracter
			if max <= 0 || max > maxToolText {
				max = maxToolText
			}
			out := articleOut{itemOut: itemOf(a, false), Autor: a.Author, Fecha: a.Published, URL: a.URL}
			if a.Final != "" {
				out.URL = a.Final
			}
			out.Texto, out.Recortado = plainText(blocks, max)
			out.Recortado = out.Recortado || a.Truncated
			if out.Texto == "" {
				out.Texto = "(no hay texto: la página no se pudo leer)"
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "leer_marcar", Description: "Marca un artículo como leído o no leído, lo archiva o lo devuelve a pendientes, lo pone o quita de favoritos, o cambia sus etiquetas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in markIn) (*mcp.CallToolResult, itemOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, itemOut{}, err
			}
			if in.Leido == nil && in.Archivado == nil && in.Favorito == nil && in.Etiquetas == nil {
				return nil, itemOut{}, errors.New("di qué cambiar: leido, archivado, favorito o etiquetas")
			}
			if in.Etiquetas != nil {
				if _, err := svc.setTags(ctx, who.User, in.ID, *in.Etiquetas); err != nil {
					return nil, itemOut{}, toolErr(err)
				}
			}
			a, err := svc.mark(ctx, who.User, in.ID, flags{Read: in.Leido, Archived: in.Archivado, Favorite: in.Favorito})
			if err != nil {
				return nil, itemOut{}, toolErr(err)
			}
			return nil, itemOf(a, false), nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "leer_borrar", Description: "Borra un artículo guardado por su id. Pide antes confirmación a la persona (para quitarlo de pendientes sin borrarlo, mejor archivarlo con leer_marcar)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, deleteOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, deleteOut{}, err
			}
			a, err := svc.store.get(ctx, who.User, in.ID)
			if err == nil {
				err = svc.store.delete(ctx, who.User, in.ID)
			}
			if err != nil {
				return nil, deleteOut{}, toolErr(err)
			}
			return nil, deleteOut{Borrado: itemOf(a, false)}, nil
		})
}

func toolErr(err error) error {
	if errors.Is(err, errNotFound) {
		return errors.New("ese artículo no existe")
	}
	return err
}

func listResult(ctx context.Context, svc *service, owner, name string, f filter, excerpt bool) (*mcp.CallToolResult, listOut, error) {
	list, total, err := svc.store.list(ctx, owner, f)
	if err != nil {
		return nil, listOut{}, err
	}
	c, err := svc.store.counts(ctx, owner)
	if err != nil {
		return nil, listOut{}, err
	}
	out := listOut{Lista: name, Total: total, PorLeer: c.Unread, Articulos: []itemOut{}, Recortados: total > len(list)}
	for _, a := range list {
		out.Articulos = append(out.Articulos, itemOf(a, excerpt))
	}
	if f.Query == "" && f.Tag == "" {
		tags, err := svc.allTags(ctx, owner)
		if err != nil {
			return nil, listOut{}, err
		}
		for i, t := range tags {
			if i == 30 {
				break
			}
			out.Etiquetas = append(out.Etiquetas, t.Name)
		}
	}
	return nil, out, nil
}
