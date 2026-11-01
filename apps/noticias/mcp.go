package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools para el asistente: el digest de hoy y buscar entre lo recogido. Actúan
// en nombre de la persona del token.

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
	mcp.AddTool(server, &mcp.Tool{Name: "noticias_buscar", Description: "Busca noticias recogidas en los últimos 3 días (medios generales y los intereses de esta persona) que contengan todas las palabras"},
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
}
