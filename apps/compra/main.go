// compra es la lista de la compra: una por persona, por secciones del súper
// (decididas con un diccionario, sin IA), con lo de siempre y lo que se
// estará acabando según el historial. Se usa desde la web, sobre todo en el
// móvil y en el súper (tachar funciona sin cobertura), y con el asistente.
// Todo lo de cada persona va cifrado (platform/seal).
package main

import (
	"context"
	"embed"
	"fmt"
	"os"

	"homeserver/platform/app"
	"homeserver/platform/seal"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	box, err := seal.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compra: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "compra", Title: "Compra", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, log: env.Log}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
