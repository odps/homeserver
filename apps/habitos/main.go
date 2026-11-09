// habitos son los hábitos y un diario corto de cada día. Hábitos de sí o no
// o de cantidad con objetivo (8 vasos de agua, 20 min de lectura), diarios,
// de unos días de la semana o unas veces por semana, con su racha, lo
// cumplido por semana y por mes, su calendario y un recordatorio por Telegram
// a la hora que se elija si aún no está hecho. El diario apunta el ánimo, la
// energía, las horas de sueño, una nota y etiquetas, y cruza esos datos con
// los hábitos con medias simples (sin IA). Se usa desde la web y con el
// asistente. Todo lo de cada persona va cifrado (platform/seal). Los
// entrenamientos van en Entreno: aquí los hábitos son genéricos.
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
		fmt.Fprintln(os.Stderr, "habitos: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "habitos", Title: "Hábitos", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	svc := &service{store: store{env.DB, box}, log: env.Log, now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
