// entreno es el seguimiento del gimnasio: sesiones con sus ejercicios y
// series (en el formato de las notas de siempre: «Lat machine 3x10 50kg rir
// 2»), el historial de cada ejercicio y la importación de las notas antiguas
// de WhatsApp. Se usa desde la web y con el asistente. Todo lo de cada
// persona va cifrado (platform/seal).
package main

import (
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
		fmt.Fprintln(os.Stderr, "entreno: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "entreno", Title: "Entreno", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box, dir) }})
}

func setup(env *app.Env, box *seal.Box, dir string) error {
	svc := &service{store: store{env.DB, box}, log: env.Log, imports: newImports(), photos: newPhotoFiles(dir, box), off: newOFF()}
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
