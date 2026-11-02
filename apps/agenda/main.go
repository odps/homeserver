// agenda es la agenda personal: calendario y tareas, contactos y correo, con
// avisos y un resumen cada mañana por Telegram. Se usa sobre todo a través
// del asistente (tools MCP); la web sirve para ver y retocar, y el móvil se
// sincroniza por CalDAV y CardDAV (DAVx5). El correo se lee por IMAP y la IA
// solo deja borradores: nunca envía.
package main

import (
	"context"
	"embed"
	"os"
	"time"

	"homeserver/platform/app"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	app.Run(app.App{Name: "agenda", Title: "Agenda", Migrations: migrations, Setup: setup})
}

func setup(env *app.Env) error {
	assistant := os.Getenv("ASSISTANT_URL")
	if assistant == "" {
		assistant = "http://127.0.0.1:8103"
	}
	seal, err := loadSealer(os.Getenv("DATA_DIR"))
	if err != nil {
		return err
	}
	st := store{env.DB}
	svc := &service{store: st, mail: newMailer(st, seal, env.Log), log: env.Log, summarizer: assistantSummarizer(assistant), now: time.Now}
	go svc.run(context.Background())
	(&web{svc: svc, dav: newDAV(st)}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
