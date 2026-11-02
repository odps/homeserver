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
	"fmt"
	"net/http"
	"os"
	"path/filepath"

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
		fmt.Fprintln(os.Stderr, "asistente: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "asistente", Title: "Asistente", Migrations: migrations, Steps: steps(box),
		Setup: func(env *app.Env) error { return setup(env, dir, box) }})
}

func setup(env *app.Env, dir string, box *seal.Box) error {
	gateway := os.Getenv("GATEWAY_URL")
	if gateway == "" {
		gateway = "http://127.0.0.1:8100"
	}
	a := newAgent(store{env.DB, box}, env.Log, filepath.Join(dir, "claves.env"), filepath.Join(dir, "estado.json"), gateway)
	go a.run(context.Background())
	env.Mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("Asistente del homeserver: se usa desde Telegram y se configura en el panel.\n"))
	})
	env.Mux.HandleFunc("POST /interno/resumir", a.internalSummary)
	return nil
}
