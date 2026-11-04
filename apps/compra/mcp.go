package main

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools de Talia. El texto lo interpreta el programa (como en la web); las
// respuestas, en JSON con claves en español.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

type addIn struct {
	Texto string `json:"texto" jsonschema:"lo que hay que comprar, tal cual lo dice la persona: «leche, huevos y 2 kg de patatas». Lo interpreta el programa (separa las cosas y saca las cantidades): no lo cambies"`
}

// cosa es una cosa de la lista, legible.
type cosa struct {
	ID       int64  `json:"id"`
	Nombre   string `json:"nombre"`
	Cantidad string `json:"cantidad,omitempty"`
	Nota     string `json:"nota,omitempty"`
	Seccion  string `json:"seccion"`
	Tachada  bool   `json:"tachada"`
	Anadida  string `json:"anadida,omitempty"` // AAAA-MM-DD
	Origen   string `json:"origen"`
}

func cosaOf(it item) cosa {
	c := cosa{ID: it.ID, Nombre: it.Name, Cantidad: it.Qty, Nota: it.Note, Seccion: sectionLabel(it.Section), Tachada: it.Done, Origen: it.Source}
	if !it.Added.IsZero() {
		c.Anadida = it.Added.In(madrid).Format("2006-01-02")
	}
	return c
}

type addOut struct {
	Anadidas  []cosa `json:"anadidas"`
	Juntadas  []cosa `json:"juntadas,omitempty"`   // ya estaban: se ha sumado la cantidad
	YaEstaban []cosa `json:"ya_estaban,omitempty"` // ya estaban sin tachar: no se repiten
}

type listIn struct {
	SinTachadas bool `json:"sin_tachadas,omitempty" jsonschema:"true para no incluir lo ya tachado"`
}

type seccionOut struct {
	Seccion string `json:"seccion"`
	Cosas   []cosa `json:"cosas"`
}

type listOut struct {
	Secciones []seccionOut `json:"secciones"`
	Total     int          `json:"total"`
	Tachadas  int          `json:"tachadas"`
}

type pickIn struct {
	ID     int64  `json:"id,omitempty" jsonschema:"id de la cosa (de compra_lista)"`
	Nombre string `json:"nombre,omitempty" jsonschema:"o su nombre, como lo diga la persona («leche»)"`
}

type markIn2 struct {
	ID        int64  `json:"id,omitempty" jsonschema:"id de la cosa (de compra_lista)"`
	Nombre    string `json:"nombre,omitempty" jsonschema:"o su nombre, como lo diga la persona («leche»)"`
	Destachar bool   `json:"destachar,omitempty" jsonschema:"true para volver a dejarla sin tachar"`
}

type changedOut struct {
	Cosas []cosa `json:"cosas"`
}

type finishOut struct {
	AlHistorial int `json:"al_historial"`
	Quedan      int `json:"quedan"`
}

// pick elige las cosas por id o por nombre. wantDone filtra por estado si
// se busca por nombre.
func pick(ctx context.Context, svc *service, owner string, in pickIn, wantDone *bool) ([]item, error) {
	if in.ID > 0 {
		it, err := svc.store.item(ctx, owner, in.ID)
		if err != nil {
			return nil, errors.New("esa cosa no está en la lista")
		}
		return []item{it}, nil
	}
	if strings.TrimSpace(in.Nombre) == "" {
		return nil, errors.New("di el id o el nombre")
	}
	list, err := svc.findByName(ctx, owner, in.Nombre, wantDone)
	switch {
	case errors.Is(err, errAmbiguous):
		var names []string
		for _, it := range list {
			names = append(names, it.Name)
		}
		return nil, errors.New("hay varias que encajan: " + strings.Join(names, ", ") + ". Usa el id (de compra_lista)")
	case errors.Is(err, errNotFound):
		return nil, errors.New("no está en la lista")
	}
	return list, err
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "compra_anadir", Description: "Añade cosas a la lista de la compra de la persona. Pasa el texto tal cual lo dice («leche, 2 kg de patatas, huevos y pan»): el programa separa las cosas, saca las cantidades y les pone su sección. Lo que ya está sin tachar no se repite (se junta la cantidad si se puede)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, addOut{}, err
			}
			res, err := svc.addText(ctx, who.User, in.Texto, sourceAssistant)
			if err != nil {
				return nil, addOut{}, err
			}
			out := addOut{Anadidas: []cosa{}}
			for _, r := range res {
				switch r.Result {
				case addNew:
					out.Anadidas = append(out.Anadidas, cosaOf(r.Item))
				case addMerged:
					out.Juntadas = append(out.Juntadas, cosaOf(r.Item))
				default:
					out.YaEstaban = append(out.YaEstaban, cosaOf(r.Item))
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "compra_lista", Description: "La lista de la compra de la persona, por secciones del súper en su orden, con lo tachado al final de cada sección"},
		func(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, listOut{}, err
			}
			list, err := svc.store.items(ctx, who.User)
			if err != nil {
				return nil, listOut{}, err
			}
			order, err := svc.store.order(ctx, who.User)
			if err != nil {
				return nil, listOut{}, err
			}
			byID := map[int64]item{}
			for _, it := range list {
				byID[it.ID] = it
			}
			groups, total, done := groupItems(list, order)
			out := listOut{Secciones: []seccionOut{}, Total: total, Tachadas: done}
			for _, g := range groups {
				sec := seccionOut{Seccion: g.Label, Cosas: []cosa{}}
				for _, v := range g.Items {
					if in.SinTachadas && v.Done {
						continue
					}
					sec.Cosas = append(sec.Cosas, cosaOf(byID[v.ID]))
				}
				if len(sec.Cosas) > 0 {
					out.Secciones = append(out.Secciones, sec)
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "compra_tachar", Description: "Tacha una cosa de la lista (ya está en el carro), por id o por nombre. Con destachar: true la vuelve a dejar pendiente. Para quitar lo tachado de la lista, compra_terminar"},
		func(ctx context.Context, req *mcp.CallToolRequest, in markIn2) (*mcp.CallToolResult, changedOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, changedOut{}, err
			}
			p := pickIn{ID: in.ID, Nombre: in.Nombre}
			from := in.Destachar // por nombre, se buscan las que estén en el otro estado
			list, err := pick(ctx, svc, who.User, p, &from)
			if err != nil && in.ID == 0 {
				// Si ya estaba como se pide, no es un error: es idempotente.
				to := !in.Destachar
				if already, err2 := pick(ctx, svc, who.User, p, &to); err2 == nil {
					list, err = already, nil
				}
			}
			if err != nil {
				return nil, changedOut{}, err
			}
			out := changedOut{Cosas: []cosa{}}
			for _, it := range list {
				if err := svc.store.setDone(ctx, who.User, it.ID, !in.Destachar); err != nil {
					return nil, changedOut{}, err
				}
				it.Done = !in.Destachar
				out.Cosas = append(out.Cosas, cosaOf(it))
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "compra_quitar", Description: "Quita una cosa de la lista sin comprarla (no pasa al historial), por id o por nombre"},
		func(ctx context.Context, req *mcp.CallToolRequest, in pickIn) (*mcp.CallToolResult, changedOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, changedOut{}, err
			}
			list, err := pick(ctx, svc, who.User, in, nil)
			if err != nil {
				return nil, changedOut{}, err
			}
			out := changedOut{Cosas: []cosa{}}
			for _, it := range list {
				if err := svc.store.remove(ctx, who.User, it.ID); err != nil {
					return nil, changedOut{}, err
				}
				out.Cosas = append(out.Cosas, cosaOf(it))
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "compra_terminar", Description: "Termina la compra: lo tachado sale de la lista y pasa al historial (para las sugerencias). Lo no tachado se queda"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, finishOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, finishOut{}, err
			}
			n, err := svc.store.finish(ctx, who.User, svc.clock())
			if err != nil {
				return nil, finishOut{}, err
			}
			left, err := svc.store.items(ctx, who.User)
			if err != nil {
				return nil, finishOut{}, err
			}
			return nil, finishOut{AlHistorial: n, Quedan: len(left)}, nil
		})
}
