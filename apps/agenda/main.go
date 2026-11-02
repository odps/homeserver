// agenda es la agenda personal: calendario y tareas, contactos y correo, con
// avisos y un resumen cada mañana por Telegram. Se usa sobre todo a través
// del asistente (tools MCP); la web sirve para ver y retocar, y el móvil se
// sincroniza por CalDAV y CardDAV (DAVx5). El correo se lee por IMAP y la IA
// solo deja borradores: nunca envía.
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"time"

	"homeserver/platform/app"
	sealpkg "homeserver/platform/seal"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	box, err := sealpkg.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agenda: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "agenda", Title: "Agenda", Migrations: migrations, Steps: steps(box),
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *sealpkg.Box) error {
	assistant := os.Getenv("ASSISTANT_URL")
	if assistant == "" {
		assistant = "http://127.0.0.1:8103"
	}
	seal, err := loadSealer(os.Getenv("DATA_DIR"))
	if err != nil {
		return err
	}
	st := store{env.DB, box}
	svc := &service{store: st, mail: newMailer(st, seal, env.Log), log: env.Log, summarizer: assistantSummarizer(assistant), now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc, dav: newDAV(st)}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
