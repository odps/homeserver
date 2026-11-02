// noticias prepara cada mañana un digest de noticias para cada cuenta: lo
// más importante del día (lo que cubren más medios generales) y una sección
// por cada interés (búsqueda en Google News y feeds RSS propios). La
// relevancia se calcula sin IA; el resumen del día, opcional, lo escribe el
// asistente con su modelo. Los titulares enlazan siempre al medio.
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
		fmt.Fprintln(os.Stderr, "noticias: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "noticias", Title: "Noticias", Migrations: migrations, Steps: steps(box),
		Setup: func(env *app.Env) error { return setup(env, box) }})
}

func setup(env *app.Env, box *seal.Box) error {
	assistant := os.Getenv("ASSISTANT_URL")
	if assistant == "" {
		assistant = "http://127.0.0.1:8103"
	}
	svc := &service{store: store{env.DB, box}, fetch: newFetcher(false), log: env.Log, summarizer: assistantSummarizer(assistant),
		redditPause: 8 * time.Second}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
