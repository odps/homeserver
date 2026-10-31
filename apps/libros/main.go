// libros es la biblioteca personal: guarda los libros (EPUB, PDF, MOBI, AZW3,
// FB2, FBZ, CBZ) con sus metadatos, los sirve a un lector web basado en
// foliate-js y sincroniza el progreso de lectura entre dispositivos. La
// interfaz es una PWA que también funciona sin conexión.
package main

import (
	"embed"
	"os"

	"homeserver/platform/app"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	app.Run(app.App{Name: "libros", Title: "Libros", Migrations: migrations, Setup: setup})
}

func setup(env *app.Env) error {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	f, err := newFiles(dir)
	if err != nil {
		return err
	}
	a := &api{store: store{env.DB}, files: f, log: env.Log, catalog: newCatalog(defaultCatalogURLs, catalogHosts, false)}
	a.routes(env.Mux)
	serveWeb(env.Mux)
	addTools(env.MCP, a.store)
	return nil
}
