package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools para Talia. Los archivos (escaneos, fotos, PDF) no salen nunca por
// aquí: solo cuántos hay. El número del documento va en la clave «numero»,
// que el asistente esconde tras un seudónimo, y solo en papeles_ver,
// papeles_guardar y papeles_renovar (la lista no lo lleva).

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

func typeKeys() string {
	keys := make([]string, len(docTypes))
	for i, t := range docTypes {
		keys[i] = t.Key + " (" + t.Label + ")"
	}
	return strings.Join(keys, ", ")
}

const filesNote = "Los archivos (escaneos, fotos, PDF) solo se ven en la web de Papeles."

type listIn struct {
	Todos bool   `json:"todos,omitempty" jsonschema:"true para todos los documentos; por defecto, solo los caducados y los que caducan en los próximos días"`
	Tipo  string `json:"tipo,omitempty" jsonschema:"solo los de este tipo (su clave, como dni o itv)"`
	Dias  int    `json:"dias,omitempty" jsonschema:"para los próximos vencimientos: cuántos días hacia delante (por defecto 90, como mucho 3650)"`
}

// docItem es un documento en una lista (sin número ni notas).
type docItem struct {
	ID         int64  `json:"id"`
	Tipo       string `json:"tipo"`
	TipoNombre string `json:"tipo_nombre"`
	Titulo     string `json:"titulo"`
	Titular    string `json:"titular,omitempty"`
	Emisor     string `json:"emisor,omitempty"`
	Caducidad  string `json:"caducidad,omitempty"`
	Estado     string `json:"estado"`
	Dias       *int   `json:"dias_restantes,omitempty"`
	Archivos   int    `json:"archivos"`
}

type listOut struct {
	Documentos []docItem `json:"documentos"`
	Total      int       `json:"total_documentos"`
}

// docFull es la ficha completa (con el número).
type docFull struct {
	docItem
	Numero      string   `json:"numero,omitempty"`
	Emision     string   `json:"emision,omitempty"`
	CadaMeses   int      `json:"cada_meses,omitempty"`
	Avisos      []int    `json:"avisos"`
	Importe     *float64 `json:"importe,omitempty"`
	Notas       string   `json:"notas,omitempty"`
	ArchivosVer string   `json:"archivos_nota,omitempty"`
}

type idIn struct {
	ID int64 `json:"id" jsonschema:"id del documento (de papeles_lista)"`
}

type saveIn struct {
	ID        int64    `json:"id,omitempty" jsonschema:"id del documento que se cambia; sin él, se crea uno nuevo"`
	Tipo      *string  `json:"tipo,omitempty" jsonschema:"clave del tipo (obligatorio al crear)"`
	Titulo    *string  `json:"titulo,omitempty" jsonschema:"título corto, como «DNI» o «Seguro del coche» (obligatorio al crear)"`
	Titular   *string  `json:"titular,omitempty" jsonschema:"de quién es, si no es de la persona (un familiar); vacío para quitarlo"`
	Numero    *string  `json:"numero,omitempty" jsonschema:"número del documento, de la póliza o del contrato"`
	Emisor    *string  `json:"emisor,omitempty" jsonschema:"quién lo emite: organismo, compañía o tienda"`
	Emision   *string  `json:"emision,omitempty" jsonschema:"fecha de emisión AAAA-MM-DD; vacío para quitarla"`
	Caducidad *string  `json:"caducidad,omitempty" jsonschema:"fecha de caducidad AAAA-MM-DD; vacío para quitarla"`
	CadaMeses *int     `json:"cada_meses,omitempty" jsonschema:"si se renueva cada cierto tiempo (12 para un seguro anual); 0 para quitarlo"`
	Avisos    []int    `json:"avisos,omitempty" jsonschema:"días de antelación de los avisos (como mucho 5, de 1 a 365); [] sin avisos; al crear, si falta, los del tipo"`
	Importe   *float64 `json:"importe,omitempty" jsonschema:"prima o precio en euros; 0 para quitarlo"`
	Notas     *string  `json:"notas,omitempty" jsonschema:"notas; vacío para quitarlas"`
}

type renewIn struct {
	ID    int64  `json:"id" jsonschema:"id del documento"`
	Fecha string `json:"nueva_caducidad,omitempty" jsonschema:"fecha de caducidad nueva AAAA-MM-DD; sin ella, la caducidad avanza lo que diga cada_meses"`
}

type deleteOut struct {
	Borrado bool   `json:"borrado"`
	Titulo  string `json:"titulo"`
}

func (s *service) item(d doc) docItem {
	state, days := d.status(s.today())
	it := docItem{ID: d.ID, Tipo: d.Type, TipoNombre: typeLabel(d.Type), Titulo: d.Title, Titular: d.Holder, Emisor: d.Issuer,
		Caducidad: d.Expires, Estado: state, Archivos: d.Files}
	if state != stNone {
		it.Dias = &days
	}
	return it
}

func (s *service) full(d doc) docFull {
	f := docFull{docItem: s.item(d), Numero: d.Number, Emision: d.Issued, CadaMeses: d.Months, Avisos: d.Alerts, Notas: d.Notes}
	if f.Avisos == nil {
		f.Avisos = []int{}
	}
	if d.Amount != nil {
		e := float64(*d.Amount) / 100
		f.Importe = &e
	}
	if d.Files > 0 {
		f.ArchivosVer = fmt.Sprintf("Tiene %d archivo%s. %s", d.Files, map[bool]string{true: "", false: "s"}[d.Files == 1], filesNote)
	}
	return f
}

// merge aplica a d solo los campos que llegan.
func (in saveIn) merge(d *doc) error {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	typeChanged := in.Tipo != nil && *in.Tipo != d.Type
	set(&d.Type, in.Tipo)
	set(&d.Title, in.Titulo)
	set(&d.Holder, in.Titular)
	set(&d.Number, in.Numero)
	set(&d.Issuer, in.Emisor)
	set(&d.Issued, in.Emision)
	set(&d.Expires, in.Caducidad)
	set(&d.Notes, in.Notas)
	if in.CadaMeses != nil {
		d.Months = *in.CadaMeses
	}
	if _, ok := typeOf(d.Type); !ok {
		return fmt.Errorf("tipo no válido: usa una de estas claves: %s", typeKeys())
	}
	switch {
	case in.Avisos != nil:
		d.Alerts = in.Avisos
	case d.ID == 0 || (typeChanged && d.Alerts == nil):
		d.Alerts = defaultAlerts(d.Type)
	}
	if in.Importe != nil {
		switch v := *in.Importe; {
		case v == 0:
			d.Amount = nil
		case v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v*100 > maxAmount:
			return errors.New("importe no válido")
		default:
			c := int64(math.Round(v * 100))
			d.Amount = &c
		}
	}
	return nil
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "papeles_lista", Description: "Documentos personales de la persona (DNI, pasaporte, carné, ITV, seguros, garantías, contratos…) con su caducidad y estado (caducado, caduca_pronto, vigente, sin_caducidad). Por defecto, los próximos vencimientos y lo ya caducado; con todos=true, todos. Sin números de documento: para eso, papeles_ver. Tipos: " + typeKeys()},
		func(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, listOut{}, err
			}
			if in.Tipo != "" {
				if _, ok := typeOf(in.Tipo); !ok {
					return nil, listOut{}, fmt.Errorf("tipo no válido: usa una de estas claves: %s", typeKeys())
				}
			}
			days := in.Dias
			if days <= 0 {
				days = soonDays
			}
			days = min(days, 3650)
			list, err := svc.store.docs(ctx, who.User)
			if err != nil {
				return nil, listOut{}, err
			}
			out := listOut{Documentos: []docItem{}, Total: len(list)}
			for _, d := range list {
				if in.Tipo != "" && d.Type != in.Tipo {
					continue
				}
				it := svc.item(d)
				if !in.Todos && (it.Dias == nil || *it.Dias > days) {
					continue
				}
				out.Documentos = append(out.Documentos, it)
			}
			sort.SliceStable(out.Documentos, func(i, j int) bool {
				a, b := out.Documentos[i], out.Documentos[j]
				if (a.Caducidad == "") != (b.Caducidad == "") {
					return b.Caducidad == ""
				}
				if a.Caducidad != b.Caducidad {
					return a.Caducidad < b.Caducidad
				}
				return a.ID < b.ID
			})
			return nil, out, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "papeles_ver", Description: "La ficha completa de un documento, con su número, fechas, renovación, avisos, importe y notas. " + filesNote},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, docFull, error) {
			who, err := identity(req)
			if err != nil {
				return nil, docFull{}, err
			}
			d, err := svc.store.doc(ctx, who.User, in.ID)
			if errors.Is(err, errNotFound) {
				return nil, docFull{}, errors.New("no existe ese documento: búscalo con papeles_lista")
			}
			if err != nil {
				return nil, docFull{}, err
			}
			return nil, svc.full(d), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "papeles_guardar", Description: "Crea un documento (sin id; tipo y título obligatorios) o cambia uno (con id: solo los campos que mandes, lo demás se queda igual). La Agenda avisará antes de la caducidad. Los archivos se suben en la web. Tipos: " + typeKeys()},
		func(ctx context.Context, req *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, docFull, error) {
			who, err := identity(req)
			if err != nil {
				return nil, docFull{}, err
			}
			var d doc
			if in.ID != 0 {
				if d, err = svc.store.doc(ctx, who.User, in.ID); errors.Is(err, errNotFound) {
					return nil, docFull{}, errors.New("no existe ese documento: búscalo con papeles_lista")
				} else if err != nil {
					return nil, docFull{}, err
				}
			} else if in.Tipo == nil {
				return nil, docFull{}, fmt.Errorf("falta el tipo: usa una de estas claves: %s", typeKeys())
			}
			if err := in.merge(&d); err != nil {
				return nil, docFull{}, err
			}
			id, err := svc.saveDoc(ctx, who.User, d)
			if err != nil {
				return nil, docFull{}, err
			}
			d, err = svc.store.doc(ctx, who.User, id)
			if err != nil {
				return nil, docFull{}, err
			}
			return nil, svc.full(d), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "papeles_renovar", Description: "Renueva un documento: con nueva_caducidad, esa fecha; sin ella, la caducidad avanza lo que diga su cada_meses (ITV, seguros anuales). Si no tiene cada_meses, pide la fecha nueva a la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in renewIn) (*mcp.CallToolResult, docFull, error) {
			who, err := identity(req)
			if err != nil {
				return nil, docFull{}, err
			}
			d, err := svc.renewDoc(ctx, who.User, in.ID, strings.TrimSpace(in.Fecha))
			if errors.Is(err, errNotFound) {
				return nil, docFull{}, errors.New("no existe ese documento: búscalo con papeles_lista")
			}
			if err != nil {
				return nil, docFull{}, err
			}
			return nil, svc.full(d), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "papeles_borrar", Description: "Borra un documento con todos sus archivos y su aviso en la Agenda. No se puede deshacer: antes de llamarla, di qué documento es y espera a que la persona lo confirme de forma explícita"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, deleteOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, deleteOut{}, err
			}
			d, err := svc.store.doc(ctx, who.User, in.ID)
			if err == nil {
				err = svc.deleteDoc(ctx, who.User, in.ID)
			}
			if errors.Is(err, errNotFound) {
				return nil, deleteOut{}, errors.New("no existe ese documento: búscalo con papeles_lista")
			}
			if err != nil {
				return nil, deleteOut{}, err
			}
			return nil, deleteOut{Borrado: true, Titulo: d.Title}, nil
		})
}
