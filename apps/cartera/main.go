// cartera es la cartera personal de inversión: acciones, ETF, fondos y
// cripto en varias plataformas (DEGIRO, Binance, Kraken…), con su histórico
// de precios, gráficas y alertas por Telegram. Solo mira precios públicos (sin
// cuentas ni claves de API) y hace las cuentas: nunca opera, ni mueve dinero,
// ni recomienda nada. Se usa desde la web y con el asistente. Todo lo de cada
// persona va cifrado (platform/seal).
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
		fmt.Fprintln(os.Stderr, "cartera: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "cartera", Title: "Cartera", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, market: newMarket(defaultURLs, false), log: env.Log, now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
