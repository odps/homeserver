// casa es la app de la casa y sus gastos corrientes (fase 1): el precio de
// la luz por horas (PVPC de Red Eléctrica) con las horas y los bloques más
// baratos, el plan de mantenimiento (caldera, filtros, revisión del gas…)
// con avisos cuando toca, y las lecturas de los contadores de luz, agua y
// gas con lo consumido. Se usa desde la web y con el asistente. Todo lo de
// cada persona va cifrado (platform/seal); los precios, que son públicos,
// no.
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
		fmt.Fprintln(os.Stderr, "casa: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "casa", Title: "Casa", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, ree: newREE(defaultREE, false), log: env.Log, now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
