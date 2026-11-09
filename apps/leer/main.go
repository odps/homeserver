// leer es «Leer después»: enlaces que cada persona guarda para leerlos con
// calma, desde la web o mandándoselos a Talia. El servidor descarga la página
// (solo HTTPS, nunca de la red local), saca el texto limpio con
// go-readability y lo guarda como bloques de texto: nunca se sirve el HTML
// descargado. Listas de pendientes, favoritos y archivo, etiquetas y
// búsqueda. Todo lo de cada persona va cifrado (platform/seal).
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"time"

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
		fmt.Fprintln(os.Stderr, "leer: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "leer", Title: "Leer después", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := newService(store{env.DB, box}, newFetcher(), env.Log, time.Now)
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
