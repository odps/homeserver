// gastos son los gastos e ingresos de cada persona, apuntados a mano (en la
// web o con el asistente): categorías con reglas para clasificar sin IA,
// presupuesto mensual por categoría, suscripciones y cargos que se repiten, y
// avisos por Telegram (un cobro que llega, un presupuesto superado). Solo
// datos: ni mueve dinero ni aconseja. Todo lo de cada persona va cifrado
// (platform/seal).
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
		fmt.Fprintln(os.Stderr, "gastos: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "gastos", Title: "Gastos", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, log: env.Log, now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
