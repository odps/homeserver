// series lleva la cuenta de las series, pelis y juegos de cada persona: las
// series se buscan y se siguen en TVmaze (pública, sin clave), con sus
// capítulos, cuál toca ver, los estrenos de la semana y avisos por Telegram
// cuando sale uno; las pelis y los juegos se apuntan a mano. Se usa desde la
// web y con el asistente. Todo lo de cada persona va cifrado (platform/seal).
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
		fmt.Fprintln(os.Stderr, "series: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "series", Title: "Series y pelis", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, tv: newTVmaze(defaultURLs, false), log: env.Log, now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
