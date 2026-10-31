// libros es la biblioteca personal: guarda los libros (EPUB, PDF, MOBI, AZW3,
// FB2, FBZ, CBZ) con sus metadatos, los sirve a un lector web basado en
// foliate-js y sincroniza el progreso de lectura entre dispositivos. La
// interfaz es una PWA que también funciona sin conexión.
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
	series, err := newSeriesService(env.DB, dir, defaultSeriesURLs, seriesHosts, false, env.Log)
	if err != nil {
		return err
	}
	// Comprueba cada 30 minutos los capítulos nuevos de las series seguidas.
	go series.watch(context.Background(), 30*time.Minute)
	a := &api{store: store{env.DB}, files: f, log: env.Log, catalog: newCatalog(defaultCatalogURLs, catalogHosts, false), series: series}
	a.routes(env.Mux)
	serveWeb(env.Mux)
	addTools(env.MCP, a.store, series.store)
	return nil
}
