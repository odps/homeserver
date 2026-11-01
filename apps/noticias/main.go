// noticias prepara cada mañana un digest de noticias para cada cuenta: lo
// más importante del día (lo que cubren más medios generales) y una sección
// por cada interés (búsqueda en Google News y feeds RSS propios). La
// relevancia se calcula sin IA; el resumen del día, opcional, lo escribe el
// asistente con su modelo. Los titulares enlazan siempre al medio.
package main

import (
	"context"
	"embed"
	"os"

	"homeserver/platform/app"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	app.Run(app.App{Name: "noticias", Title: "Noticias", Migrations: migrations, Setup: setup})
}

func setup(env *app.Env) error {
	assistant := os.Getenv("ASSISTANT_URL")
	if assistant == "" {
		assistant = "http://127.0.0.1:8103"
	}
	svc := &service{store: store{env.DB}, fetch: newFetcher(false), log: env.Log, summarizer: assistantSummarizer(assistant)}
	go svc.run(context.Background())
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}
