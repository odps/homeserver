// asistente es el bot de Telegram del homeserver: responde solo a su dueño,
// consulta las apps (libros, cómics…) con sus tools MCP a través de la
// pasarela, busca en internet y guarda notas. El modelo de IA va por una API
// compatible con la de OpenAI (Groq, Gemini, OpenRouter…).
//
// Las claves las escribe el panel en DATA_DIR/claves.env y se releen solas al
// cambiar. El asistente publica su estado en DATA_DIR/estado.json para que el
// panel lo muestre. Avisos y estado del servidor no pasan por aquí: son tareas
// programadas normales.
package main

import (
	"context"
	"embed"
	"net/http"
	"os"
	"path/filepath"

	"homeserver/platform/app"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	app.Run(app.App{Name: "asistente", Title: "Asistente", Migrations: migrations, Setup: setup})
}

func setup(env *app.Env) error {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	gateway := os.Getenv("GATEWAY_URL")
	if gateway == "" {
		gateway = "http://127.0.0.1:8100"
	}
	a := newAgent(store{env.DB}, env.Log, filepath.Join(dir, "claves.env"), filepath.Join(dir, "estado.json"), gateway)
	go a.run(context.Background())
	env.Mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("Asistente del homeserver: se usa desde Telegram y se configura en el panel.\n"))
	})
	return nil
}
