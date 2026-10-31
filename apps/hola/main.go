// hola es la app de prueba de la plataforma: una tabla, una página y dos tools
// MCP. Sirve para comprobar el ciclo push → CI → despliegue → vuelta atrás.
package main

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
	"homeserver/platform/version"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	panic("prueba de vuelta atrás: esta versión no arranca")
	app.Run(app.App{Name: "hola", Title: "Hola (app de prueba)", Migrations: migrations, Setup: setup})
}

var page = template.Must(template.New("").Parse(`<!doctype html>
<html lang="es"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Hola</title>
<h1>Hola</h1>
<p>Versión {{.Version}}</p>
<form method="post" action="saludos"><input name="texto" required maxlength="500"> <button>Guardar</button></form>
<ul>{{range .Saludos}}<li>{{.Texto}} <small>{{.Creado.Format "02/01/2006 15:04"}}</small></li>{{end}}</ul>
</html>`))

type saludarIn struct {
	Texto string `json:"texto" jsonschema:"texto del saludo, hasta 500 caracteres"`
}

type listarIn struct {
	Limite int `json:"limite,omitempty" jsonschema:"número máximo de saludos (por defecto 20)"`
}

type listarOut struct {
	Saludos []Saludo `json:"saludos"`
}

func setup(env *app.Env) error {
	s := store{env.DB}

	env.Mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		saludos, err := s.list(r.Context(), 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page.Execute(w, map[string]any{"Version": version.Version, "Saludos": saludos})
	})
	env.Mux.HandleFunc("POST /saludos", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.add(r.Context(), strings.TrimSpace(r.FormValue("texto"))); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "./", http.StatusSeeOther)
	})

	mcp.AddTool(env.MCP, &mcp.Tool{Name: "hola_saludar", Description: "Guarda un saludo nuevo"},
		func(ctx context.Context, _ *mcp.CallToolRequest, in saludarIn) (*mcp.CallToolResult, Saludo, error) {
			texto := strings.TrimSpace(in.Texto)
			if texto == "" || len([]rune(texto)) > 500 {
				return nil, Saludo{}, errors.New("el texto debe tener entre 1 y 500 caracteres")
			}
			sal, err := s.add(ctx, texto)
			return nil, sal, err
		})
	mcp.AddTool(env.MCP, &mcp.Tool{Name: "hola_listar", Description: "Lista los saludos más recientes"},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listarIn) (*mcp.CallToolResult, listarOut, error) {
			if in.Limite <= 0 || in.Limite > 200 {
				in.Limite = 20
			}
			saludos, err := s.list(ctx, in.Limite)
			if saludos == nil {
				saludos = []Saludo{}
			}
			return nil, listarOut{Saludos: saludos}, err
		})
	return nil
}
