package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools para el agente. Actúan en nombre de la persona del token (la pasarela
// pasa su identidad en las cabeceras) y devuelven resúmenes: sin rutas de
// fichero ni hashes.

var errNoUser = errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra == nil {
		return app.Identity{}, errNoUser
	}
	who := app.UserFrom(req.Extra.Header)
	if !who.IsUser() {
		return who, errNoUser
	}
	return who, nil
}

type bookSummary struct {
	ID          int64    `json:"id"`
	Title       string   `json:"titulo"`
	Authors     []string `json:"autores"`
	Genres      []string `json:"generos,omitempty"`
	Series      string   `json:"serie,omitempty"`
	Status      string   `json:"estado"`
	Owner       string   `json:"subido_por,omitempty"`
	Visibility  string   `json:"visibilidad"`
	Format      string   `json:"formato"`
	Percent     *int     `json:"porcentaje,omitempty"`
	Where       string   `json:"donde,omitempty"`
	LastRead    string   `json:"ultima_lectura,omitempty"`
	Description string   `json:"descripcion,omitempty"`
}

func summarize(b Book, withDescription bool) bookSummary {
	s := bookSummary{ID: b.ID, Title: b.Title, Authors: b.Authors, Genres: b.Genres, Status: b.Status, Format: b.Format, Owner: b.Owner, Visibility: b.Visibility}
	if b.Series != nil {
		s.Series = *b.Series
		if b.SeriesIndex != nil {
			s.Series += fmt.Sprintf(" #%g", *b.SeriesIndex)
		}
	}
	if b.Last != nil {
		pct := int(b.Last.Fraction*100 + 0.5)
		s.Percent = &pct
		s.Where = b.Last.DeviceName
		s.LastRead = b.Last.UpdatedAt.Format("2006-01-02 15:04")
		if b.Last.Label != nil {
			s.Where += " (" + *b.Last.Label + ")"
		}
	}
	if withDescription && b.Description != nil {
		s.Description = *b.Description
	}
	return s
}

type booksOut struct {
	Libros []bookSummary `json:"libros"`
}

type searchIn struct {
	Texto  string `json:"texto,omitempty" jsonschema:"palabras del título, autor, género o serie; sin acentos ni mayúsculas que importen (vacío: todos)"`
	Varios []any  `json:"varios,omitempty" jsonschema:"varias búsquedas a la vez, una por libro, como lista de textos: [\"Clausewitz\", \"Musashi\"]; sustituye a texto"`
	Estado string `json:"estado,omitempty" jsonschema:"pendiente, leyendo o leido"`
}

type searchGroup struct {
	Texto  string        `json:"texto"`
	Libros []bookSummary `json:"libros"`
}

type searchOut struct {
	Libros      []bookSummary `json:"libros,omitempty"`
	PorBusqueda []searchGroup `json:"por_busqueda,omitempty"`
	Nota        string        `json:"nota,omitempty"`
}

// searchSchema es el de searchIn con los elementos de «varios» explícitos
// (texto u objeto): algunos proveedores validan la llamada antes de enviarla.
func searchSchema() *jsonschema.Schema {
	s, err := jsonschema.For[searchIn](nil)
	if err != nil {
		panic(err)
	}
	v := s.Properties["varios"]
	v.Type, v.Types = "array", nil
	v.Items = &jsonschema.Schema{Types: []string{"string", "object"}}
	return s
}

// searchText admite cada búsqueda de «varios» como texto o, si el modelo
// manda objetos ({"titulo": …, "autor": …}), con sus textos juntos.
func searchText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			if s, ok := x[k].(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return fmt.Sprint(v)
}

const notFoundHint = "Sin resultados. Prueba con el apellido del autor o con una palabra del título en su idioma original. Si quiere conseguirlo, búscalo en las bibliotecas libres con libros_catalogo_buscar."

type catalogSearchIn struct {
	Texto  string `json:"texto" jsonschema:"título y/o autor (mejor en el idioma original o en inglés)"`
	Idioma string `json:"idioma,omitempty" jsonschema:"es, en, fr, de, it, pt o ca (vacío: cualquiera)"`
}

type catalogHit struct {
	Fuente   string   `json:"fuente"`
	ID       string   `json:"id"`
	Title    string   `json:"titulo"`
	Authors  []string `json:"autores"`
	Language string   `json:"idioma,omitempty"`
	Year     string   `json:"año,omitempty"`
	BookID   int64    `json:"ya_en_biblioteca,omitempty"`
}

type catalogOut struct {
	Resultados   []catalogHit `json:"resultados"`
	SinRespuesta []string     `json:"fuentes_sin_respuesta,omitempty"`
	Nota         string       `json:"nota,omitempty"`
}

type importIn struct {
	Fuente      string `json:"fuente" jsonschema:"gutenberg, wikisource o archive (de libros_catalogo_buscar)"`
	ID          string `json:"id" jsonschema:"id del resultado de libros_catalogo_buscar"`
	Visibilidad string `json:"visibilidad,omitempty" jsonschema:"privado (por defecto) o compartido"`
}

type importOut struct {
	Libro     bookSummary `json:"libro"`
	YaLoTenia bool        `json:"ya_lo_tenia"`
}

type idIn struct {
	ID int64 `json:"id" jsonschema:"id del libro"`
}

type statusIn struct {
	ID     int64  `json:"id" jsonschema:"id del libro"`
	Estado string `json:"estado" jsonschema:"pendiente, leyendo o leido"`
}

func addTools(server *mcp.Server, a *api, ss seriesStore) {
	s := a.store
	mcp.AddTool(server, &mcp.Tool{Name: "libros_leyendo", Description: "Libros que se están leyendo, con el porcentaje, el último dispositivo y cuándo"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, booksOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, booksOut{}, err
			}
			books, err := s.list(ctx, who, listQuery{Status: "leyendo", Sort: "recent", Limit: 20})
			return nil, toOut(books, false), err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "libros_buscar", Description: "Busca en la biblioteca de la persona por palabras (todas deben aparecer en título, autor, género o serie) y/o estado. Con «varios» comprueba varios libros en una sola llamada.",
		InputSchema: searchSchema()},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, searchOut{}, err
			}
			if in.Estado != "" && !statuses[in.Estado] {
				return nil, searchOut{}, errors.New("estado no válido: pendiente, leyendo o leido")
			}
			if len(in.Varios) > 0 {
				if len(in.Varios) > 15 {
					in.Varios = in.Varios[:15]
				}
				var out searchOut
				found := false
				for _, v := range in.Varios {
					t := searchText(v)
					books, err := s.list(ctx, who, listQuery{Text: t, Status: in.Estado, Sort: "recent", Limit: 5})
					if err != nil {
						return nil, searchOut{}, err
					}
					g := searchGroup{Texto: t, Libros: toOut(books, false).Libros}
					found = found || len(books) > 0
					out.PorBusqueda = append(out.PorBusqueda, g)
				}
				if !found {
					out.Nota = notFoundHint
				}
				return nil, out, nil
			}
			books, err := s.list(ctx, who, listQuery{Text: in.Texto, Status: in.Estado, Sort: "recent", Limit: 50})
			out := searchOut{Libros: toOut(books, false).Libros}
			if err == nil && len(books) == 0 {
				out.Nota = notFoundHint
			}
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "libros_catalogo_buscar", Description: "Busca libros gratuitos y legales (dominio público o licencia libre) en Project Gutenberg, Wikisource e Internet Archive. Es la única forma de conseguir un libro: si no aparece aquí, no está disponible gratis legalmente."},
		func(ctx context.Context, req *mcp.CallToolRequest, in catalogSearchIn) (*mcp.CallToolResult, catalogOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, catalogOut{}, err
			}
			if strings.TrimSpace(in.Texto) == "" {
				return nil, catalogOut{}, errors.New("falta el texto a buscar")
			}
			p, err := a.catalog.search(ctx, "todas", in.Texto, in.Idioma, 1)
			if err != nil {
				return nil, catalogOut{}, err
			}
			keys := make([]string, len(p.Items))
			for i, it := range p.Items {
				keys[i] = it.Source + ":" + it.ID
			}
			have, err := s.idsBySource(ctx, who, keys)
			if err != nil {
				return nil, catalogOut{}, err
			}
			out := catalogOut{Resultados: []catalogHit{}, SinRespuesta: p.Failed}
			for i, it := range p.Items {
				if i == 10 {
					break
				}
				out.Resultados = append(out.Resultados, catalogHit{Fuente: it.Source, ID: it.ID, Title: it.Title, Authors: it.Authors,
					Language: it.Language, Year: it.Year, BookID: have[keys[i]]})
			}
			if len(out.Resultados) == 0 {
				out.Nota = "No está en las bibliotecas libres: no se puede conseguir gratis de forma legal. Prueba con el título original o el apellido del autor antes de darlo por perdido."
			}
			return nil, out, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "libros_anadir", Description: "Descarga a la biblioteca de la persona un libro encontrado con libros_catalogo_buscar"},
		func(ctx context.Context, req *mcp.CallToolRequest, in importIn) (*mcp.CallToolResult, importOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, importOut{}, err
			}
			b, dup, err := a.importBook(ctx, who, in.Fuente, in.ID, in.Visibilidad)
			if err != nil {
				return nil, importOut{}, err
			}
			return nil, importOut{Libro: summarize(b, false), YaLoTenia: dup}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "libros_detalle", Description: "Metadatos y descripción de un libro"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, bookSummary, error) {
			who, err := identity(req)
			if err != nil {
				return nil, bookSummary{}, err
			}
			b, err := s.get(ctx, who, in.ID)
			return nil, summarize(b, true), err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "libros_estado", Description: "Cambia el estado de lectura de un libro para esta persona (pendiente, leyendo o leido)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, bookSummary, error) {
			who, err := identity(req)
			if err != nil {
				return nil, bookSummary{}, err
			}
			if !statuses[in.Estado] {
				return nil, bookSummary{}, errors.New("estado no válido: pendiente, leyendo o leido")
			}
			if err := s.setStatus(ctx, who, in.ID, in.Estado); err != nil {
				return nil, bookSummary{}, err
			}
			b, err := s.get(ctx, who, in.ID)
			return nil, summarize(b, false), err
		})
	addSeriesTools(server, ss)
	addSettingsTools(server, s)
}

// settingsView son los ajustes de la persona: los del lector (iguales en
// todos sus dispositivos) y las preferencias de la app.
type settingsView struct {
	TemaLector     string  `json:"tema_lector"`
	TamanoLetra    int     `json:"tamano_letra"`
	Interlineado   float64 `json:"interlineado"`
	Margenes       string  `json:"margenes"`
	Justificar     bool    `json:"justificar"`
	Modo           string  `json:"modo"`
	Letra          string  `json:"letra"`
	IdiomaBusqueda string  `json:"idioma_busqueda"`
	FuenteComics   string  `json:"fuente_comics"`
	Visibilidad    string  `json:"visibilidad"`
	TemaApp        string  `json:"tema_app"`
}

type settingsIn struct {
	TemaLector     *string  `json:"tema_lector,omitempty" jsonschema:"colores del lector: light (claro), sepia o dark (oscuro)"`
	TamanoLetra    *int     `json:"tamano_letra,omitempty" jsonschema:"tamaño de la letra en %, de 70 a 200"`
	Interlineado   *float64 `json:"interlineado,omitempty" jsonschema:"espacio entre líneas, de 1 a 2.5"`
	Margenes       *string  `json:"margenes,omitempty" jsonschema:"narrow (estrechos), normal o wide (anchos)"`
	Justificar     *bool    `json:"justificar,omitempty" jsonschema:"texto justificado"`
	Modo           *string  `json:"modo,omitempty" jsonschema:"paginated (por páginas) o scrolled (desplazamiento continuo)"`
	Letra          *string  `json:"letra,omitempty" jsonschema:"original (la del libro), serif o sans"`
	IdiomaBusqueda *string  `json:"idioma_busqueda,omitempty" jsonschema:"idioma al buscar en las bibliotecas libres: es, en, fr, de, it, pt o ca; vacío para todos"`
	FuenteComics   *string  `json:"fuente_comics,omitempty" jsonschema:"dónde buscar cómics: todas, asura o flame"`
	Visibilidad    *string  `json:"visibilidad,omitempty" jsonschema:"visibilidad de los libros que se añadan: privado o compartido"`
	TemaApp        *string  `json:"tema_app,omitempty" jsonschema:"colores de la app: system (los del dispositivo), light o dark"`
}

func viewSettings(d settingsDoc) settingsView {
	r, p := d.Reader, d.Prefs
	return settingsView{TemaLector: r.Theme, TamanoLetra: r.Size, Interlineado: r.Spacing, Margenes: r.Margin, Justificar: r.Justify,
		Modo: r.Flow, Letra: r.Font, IdiomaBusqueda: p.SearchLang, FuenteComics: p.ComicSource, Visibilidad: p.Visibility, TemaApp: p.Theme}
}

// set cambia *dst si v trae algo y dice si lo ha hecho.
func set[T any](dst *T, v *T) bool {
	if v == nil {
		return false
	}
	*dst = *v
	return true
}

func addSettingsTools(server *mcp.Server, s store) {
	mcp.AddTool(server, &mcp.Tool{Name: "libros_ajustes_ver", Description: "Ajustes de esta persona: los del lector (colores, letra, márgenes, modo; iguales en todos sus dispositivos) y las preferencias de la app (idioma de búsqueda, fuente de cómics, visibilidad al añadir, tema)"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, settingsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, settingsView{}, err
			}
			d, err := s.settings(ctx, who)
			return nil, viewSettings(d), err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "libros_ajustes_cambiar", Description: "Cambia los ajustes del lector o las preferencias de la app. Cambia solo lo que la persona ha pedido de forma explícita; lo que no se indica se queda como está. Devuelve los ajustes como quedan; los dispositivos los toman al abrir el lector."},
		func(ctx context.Context, req *mcp.CallToolRequest, in settingsIn) (*mcp.CallToolResult, settingsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, settingsView{}, err
			}
			d, err := s.settings(ctx, who)
			if err != nil {
				return nil, settingsView{}, err
			}
			r, p := d.Reader, d.Prefs
			reader := false
			for _, ch := range []bool{set(&r.Theme, in.TemaLector), set(&r.Size, in.TamanoLetra), set(&r.Spacing, in.Interlineado),
				set(&r.Margin, in.Margenes), set(&r.Justify, in.Justificar), set(&r.Flow, in.Modo), set(&r.Font, in.Letra)} {
				reader = reader || ch
			}
			prefs := false
			for _, ch := range []bool{set(&p.SearchLang, in.IdiomaBusqueda), set(&p.ComicSource, in.FuenteComics),
				set(&p.Visibility, in.Visibilidad), set(&p.Theme, in.TemaApp)} {
				prefs = prefs || ch
			}
			if reader && !r.valid() {
				return nil, settingsView{}, errors.New("ajustes del lector no válidos: tema light, sepia o dark; tamaño de 70 a 200; interlineado de 1 a 2.5; márgenes narrow, normal o wide; modo paginated o scrolled; letra original, serif o sans")
			}
			if prefs && !p.valid() {
				return nil, settingsView{}, errors.New("preferencias no válidas: idioma es, en, fr, de, it, pt, ca o vacío (todos); cómics todas, asura o flame; visibilidad privado o compartido; tema de la app system, light o dark")
			}
			if reader {
				// Más reciente que lo guardado, aunque un dispositivo vaya adelantado.
				at := time.Now()
				if d.UpdatedAt != nil && !at.After(*d.UpdatedAt) {
					at = d.UpdatedAt.Add(time.Millisecond)
				}
				if d, err = s.saveSettings(ctx, who, r, at); err != nil {
					return nil, settingsView{}, err
				}
			}
			if prefs {
				if d, err = s.savePrefs(ctx, who, p); err != nil {
					return nil, settingsView{}, err
				}
			}
			return nil, viewSettings(d), nil
		})
}

type seriesSummary struct {
	Title     string `json:"titulo"`
	Source    string `json:"fuente"`
	Latest    string `json:"ultimo_capitulo,omitempty"`
	LatestAt  string `json:"publicado,omitempty"`
	LastRead  string `json:"leido_hasta,omitempty"`
	Unread    int    `json:"sin_leer"`
	NextURL   string `json:"siguiente,omitempty"`
	NextLabel string `json:"siguiente_capitulo,omitempty"`
}

type seriesOut struct {
	Series []seriesSummary `json:"series"`
}

func addSeriesTools(server *mcp.Server, ss seriesStore) {
	mcp.AddTool(server, &mcp.Tool{Name: "libros_series", Description: "Series de cómic (manga, manhwa) que sigue esta persona, con los capítulos sin leer y el enlace al siguiente"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, seriesOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, seriesOut{}, err
			}
			list, err := ss.followed(ctx, who)
			out := seriesOut{Series: []seriesSummary{}}
			for _, s := range list {
				sum := seriesSummary{Title: s.Title, Source: s.Source, Unread: s.Unread}
				if s.LatestNumber != nil {
					sum.Latest = numberLabel(*s.LatestNumber)
				}
				if s.LatestAt != nil {
					sum.LatestAt = s.LatestAt.Format("2006-01-02 15:04")
				}
				if s.LastRead != nil {
					sum.LastRead = numberLabel(*s.LastRead)
				}
				if s.Next != nil {
					sum.NextURL, sum.NextLabel = s.Next.URL, numberLabel(s.Next.Number)
				}
				out.Series = append(out.Series, sum)
			}
			return nil, out, err
		})
}

func toOut(books []Book, desc bool) booksOut {
	out := booksOut{Libros: []bookSummary{}}
	for _, b := range books {
		out.Libros = append(out.Libros, summarize(b, desc))
	}
	return out
}
