package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/encoding/charmap"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Ayudas de los tests. Nada sale a la red: las páginas las sirve un
// servidor HTTPS local (httptest) al que solo el fetcher de los tests puede
// conectarse. Los textos de ejemplo son inventados y genéricos (el
// repositorio es público).

// testNow es un domingo a las 10:30 (para el aviso semanal).
func testNow() time.Time { return time.Date(2026, 11, 8, 10, 30, 0, 0, time.Local) }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// articleHTML es una página de ejemplo con su artículo, rodeada de lo que
// suele haber alrededor (menú, scripts, anuncios, pie) y de trampas que no
// deben llegar al texto guardado.
func articleHTML(title, site, author, topic string, paragraphs int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html><html lang="es"><head><meta charset="utf-8"><title>%s | %s</title>
<meta property="og:site_name" content="%s"><meta name="author" content="%s">
<meta property="article:published_time" content="2026-10-20T08:00:00Z">
<meta property="og:image" content="https://cdn.ejemplo.invalid/portada.jpg">
<script>window.rastreo = "no-debe-salir";</script><style>.x{color:red}</style></head><body>
<nav class="menu"><a href="/">Inicio</a> <a href="/secciones">Secciones</a> <a href="/contacto">Contacto del menú</a></nav>
<div class="publicidad">Anuncio: compra ya</div>
<article><h1>%s</h1>`, title, site, site, author, title)
	for i := 1; i <= paragraphs; i++ {
		fmt.Fprintf(&b, `<p>Párrafo %d sobre %s. Es un texto de ejemplo, escrito para las pruebas, que habla con calma de %s, de cómo empezar, de los errores más comunes y de lo que conviene tener en cuenta antes de lanzarse; también recuerda que cada caso es distinto y que la paciencia ayuda <a href="javascript:alert(1)">mucho</a>.</p>`, i, topic, topic)
		if i == 2 {
			fmt.Fprintf(&b, `<h2>Lo básico de %s</h2><ul><li>Primero, mirar bien.</li><li>Después, <strong>probar</strong> poco a poco.</li></ul>`, topic)
			b.WriteString(`<img src="https://rastreo.ejemplo.invalid/pixel.gif" onerror="alert(2)">`)
			b.WriteString(`<blockquote>Lo que bien empieza<br>bien acaba.</blockquote>`)
			b.WriteString(`<iframe src="https://video.ejemplo.invalid/"></iframe><script>alert(3)</script>`)
		}
		if i == 3 {
			b.WriteString(`<pre>paso 1:  regar
paso 2:  esperar</pre><p>Un poema corto:<br>primera línea<br>segunda línea</p>`)
		}
	}
	b.WriteString(`<p>&lt;script&gt;alert("escapado")&lt;/script&gt; aparece como texto, sin más.</p></article>
<footer>Pie de la página con aviso legal y cookies</footer></body></html>`)
	return b.String()
}

// site es el servidor de páginas de ejemplo.
type site struct {
	*httptest.Server
	flaky atomic.Int32 // /cambia falla mientras sea > 0
	hits  atomic.Int32
}

func newSite(t *testing.T) *site {
	t.Helper()
	s := &site{}
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/articulo/tomates", html(articleHTML("Cómo cultivar tomates en el balcón", "Huerto de Ejemplo", "Ana Ejemplo", "los tomates del balcón", 6)))
	mux.HandleFunc("/articulo/pan", html(articleHTML("Pan de masa madre en casa", "Cocina de Ejemplo", "Luis Ejemplo", "la masa madre", 8)))
	mux.HandleFunc("/articulo/bicis", html(articleHTML("Rutas en bicicleta por la sierra", "Rutas de Ejemplo", "Eva Ejemplo", "las rutas en bicicleta", 30)))
	mux.HandleFunc("/latin1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=ISO-8859-1")
		body, _ := charmap.ISO8859_1.NewEncoder().String(articleHTML("Canción de otoño", "Música de Ejemplo", "Pía Ejemplo", "la canción española", 5))
		w.Write([]byte(body))
	})
	mux.HandleFunc("/sin-tipo", func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		w.Write([]byte(articleHTML("Página sin tipo", "Sitio de Ejemplo", "", "las páginas sin tipo", 5)))
	})
	mux.HandleFunc("/grande", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>"))
		w.Write(bytes.Repeat([]byte("relleno "), (maxPage/8)+10))
	})
	mux.HandleFunc("/pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4 ejemplo"))
	})
	mux.HandleFunc("/vacia", html(`<html><head><title>Solo un título</title></head><body><p>Hola.</p></body></html>`))
	mux.HandleFunc("/no-existe", http.NotFound)
	mux.HandleFunc("/cambia", func(w http.ResponseWriter, r *http.Request) {
		if s.flaky.Add(-1) >= 0 {
			http.Error(w, "ocupado", http.StatusServiceUnavailable)
			return
		}
		html(articleHTML("Página que se arregla", "Sitio de Ejemplo", "", "las páginas que vuelven", 5))(w, r)
	})
	mux.HandleFunc("/redir/tomates", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/articulo/tomates", http.StatusFound)
	})
	mux.HandleFunc("/redir/http", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/articulo/pan", http.StatusFound)
	})
	mux.HandleFunc("/redir/privada", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.0.0.1/admin", http.StatusFound)
	})
	mux.HandleFunc("/redir/metadatos", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	mux.HandleFunc("/redir/ftp", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://ejemplo.invalid/fichero", http.StatusFound)
	})
	mux.HandleFunc("/redir/bucle", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redir/bucle", http.StatusFound)
	})
	mux.HandleFunc("/redir/", func(w http.ResponseWriter, r *http.Request) {
		// /redir/a/<host:puerto>: a otro servidor local
		if to, ok := strings.CutPrefix(r.URL.Path, "/redir/a/"); ok {
			http.Redirect(w, r, "https://"+to+"/articulo/tomates", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		mux.ServeHTTP(w, r)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0) // el test del certificado no válido
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// addr es host:puerto del servidor.
func (s *site) addr() string { return s.Listener.Addr().String() }

// Port es su puerto.
func (s *site) Port() string { return s.addr()[strings.LastIndex(s.addr(), ":")+1:] }

// url es la dirección de una página del servidor.
func (s *site) url(path string) string { return "https://" + s.addr() + path }

// testFetcher se puede conectar a esos servidores locales (y a nada más
// local).
func testFetcher(sites ...*site) *fetcher {
	roots := x509.NewCertPool()
	for _, s := range sites {
		roots.AddCert(s.Certificate())
	}
	f := newFetcherWith(roots, 5*time.Second)
	for _, s := range sites {
		f.allowURL[s.addr()] = true
		f.allowDial[s.addr()] = true
	}
	return f
}

// newTestService da un servicio con la base de datos de pruebas y el
// servidor de páginas de ejemplo.
func newTestService(t *testing.T) (*service, *site, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := &clock{t: testNow()}
	s := newSite(t)
	return newService(store{pool, box}, testFetcher(s), slog.New(slog.DiscardHandler), c.now), s, c
}

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	if dump == "" {
		t.Errorf("%s está vacía: la prueba no comprueba nada", table)
	}
	for _, s := range texts {
		if strings.Contains(strings.ToLower(dump), strings.ToLower(s)) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}
