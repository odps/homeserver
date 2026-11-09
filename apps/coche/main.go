// coche es el cuaderno del coche (o de los coches) de cada persona:
// repostajes con su consumo y su gasto, el mantenimiento por km o por tiempo
// (aceite, ITV, neumáticos…) con avisos por Telegram, los demás gastos
// (seguro, parking, peajes, multas) y las gasolineras más baratas de su
// municipio con los precios públicos del Ministerio. Se usa desde la web y
// con el asistente. Todo lo de cada persona va cifrado (platform/seal).
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
		fmt.Fprintln(os.Stderr, "coche: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "coche", Title: "Coche", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, fuel: newFuelSource(defaultFuelURL, false), log: env.Log, now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
