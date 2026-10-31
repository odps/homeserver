package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"homeserver/platform/version"
)

// web contiene la interfaz (PWA) y foliate-js en web/vendor/foliate (MIT).
//
//go:embed web
var webFS embed.FS

// csp permite lo que necesita foliate-js: el libro se pinta en iframes con
// URL blob: que heredan esta política, los estilos del propio libro van en
// línea, y pdf.js usa un worker y WebAssembly. Los scripts de los libros no
// se ejecutan (no hay 'unsafe-inline' en script-src).
const csp = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline' blob:; " +
	"img-src 'self' blob: data:; font-src 'self' blob: data:; media-src 'self' blob:; frame-src 'self' blob:; " +
	"worker-src 'self' blob:; connect-src 'self' blob: data:; manifest-src 'self'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

func serveWeb(mux *http.ServeMux) {
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)
	serve := func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			h.Set("Cache-Control", "public, max-age=604800")
		} else {
			// La interfaz y el service worker, siempre frescos: de ellos
			// depende detectar versiones nuevas.
			h.Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	}
	// Rutas concretas: un «GET /» genérico chocaría con /mcp de la plataforma.
	for _, p := range []string{"GET /{$}", "GET /sw.js", "GET /manifest.webmanifest", "GET /assets/", "GET /vendor/"} {
		mux.HandleFunc(p, serve)
	}
	// precache.json lista los ficheros que el service worker guarda para que la
	// app funcione sin conexión. La versión cambia en cada despliegue.
	mux.HandleFunc("GET /precache.json", func(w http.ResponseWriter, r *http.Request) {
		var list []string
		fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && p != "sw.js" {
				if p == "index.html" {
					p = "./"
				}
				list = append(list, p)
			}
			return nil
		})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"version": version.Version, "files": list})
	})
}
