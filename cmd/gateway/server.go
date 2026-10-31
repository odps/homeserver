package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

const cookieName = "__Host-hs_session"

type appSpec struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

func loadApps(path string) (map[string]appSpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m struct {
		Apps []appSpec `json:"apps"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	apps := map[string]appSpec{}
	for _, a := range m.Apps {
		apps[a.Name] = a
	}
	return apps, nil
}

type gateway struct {
	passwordHash string
	sessions     sessions
	limiter      *limiter
	tokens       tokenStore
	apps         map[string]appSpec
	log          *slog.Logger
	now          func() time.Time // sustituible en los tests
}

func (g *gateway) listen(addr string) error {
	srv := &http.Server{Addr: addr, Handler: g.handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func (g *gateway) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *gateway) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /login", g.loginPage)
	mux.HandleFunc("POST /login", g.login)
	mux.HandleFunc("POST /logout", g.logout)
	mux.Handle("GET /{$}", g.auth(http.HandlerFunc(g.index)))
	mux.Handle("/{app}", g.auth(http.HandlerFunc(g.addSlash)))
	mux.Handle("/{app}/", g.auth(http.HandlerFunc(g.proxy)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// auth deja pasar con una sesión válida o un token Bearer. Sin ellas, las
// peticiones de navegador van al login y el resto recibe 401.
func (g *gateway) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Nadie de fuera puede hacerse pasar por un usuario ante las apps.
		r.Header.Del("X-Auth-User")
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if name, valid := g.tokens.check(tok); valid {
				r.Header.Set("X-Auth-User", "token:"+name)
				next.ServeHTTP(w, r)
				return
			}
			g.log.Warn("token no válido", "ip", clientIP(r))
			unauthorized(w)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && g.sessions.valid(c.Value, g.clock()) {
			r.Header.Set("X-Auth-User", "owner")
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		unauthorized(w)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="homeserver"`)
	http.Error(w, "no autorizado", http.StatusUnauthorized)
}

var loginTmpl = template.Must(template.New("").Parse(`<!doctype html>
<html lang="es"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Entrar</title>
<style>body{font-family:system-ui,sans-serif;max-width:22rem;margin:15vh auto;padding:0 1rem}
input,button{font:inherit;width:100%;padding:.6rem;margin:.3rem 0;box-sizing:border-box}</style>
<h1>homeserver</h1>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/login">
<input type="hidden" name="next" value="{{.Next}}">
<input type="password" name="password" autocomplete="current-password" placeholder="Contraseña" required autofocus>
<button>Entrar</button>
</form>
</html>`))

func (g *gateway) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	loginTmpl.Execute(w, map[string]string{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	ip, now := clientIP(r), g.clock()
	next := safeNext(r.FormValue("next"))
	render := func(status int, msg string) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(status)
		loginTmpl.Execute(w, map[string]string{"Next": next, "Error": msg})
	}
	if !g.limiter.allowed(ip, now) {
		g.log.Warn("login bloqueado por intentos", "ip", ip)
		render(http.StatusTooManyRequests, "Demasiados intentos. Prueba dentro de un rato.")
		return
	}
	if !checkPassword(g.passwordHash, r.FormValue("password")) {
		g.limiter.fail(ip, now)
		g.log.Warn("contraseña incorrecta", "ip", ip)
		render(http.StatusUnauthorized, "Contraseña incorrecta.")
		return
	}
	g.log.Info("login", "ip", ip)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: g.sessions.issue(now), Path: "/",
		MaxAge: int(g.sessions.ttl.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (g *gateway) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext solo admite rutas locales, para no redirigir a otro dominio.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return "/"
	}
	return next
}

var indexTmpl = template.Must(template.New("").Parse(`<!doctype html>
<html lang="es"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>homeserver</title>
<style>body{font-family:system-ui,sans-serif;max-width:30rem;margin:2rem auto;padding:0 1rem}</style>
<h1>homeserver</h1>
<ul>{{range .}}<li><a href="/{{.Name}}/">{{.Name}}</a></li>{{end}}</ul>
<form method="post" action="/logout"><button>Salir</button></form>
</html>`))

func (g *gateway) index(w http.ResponseWriter, r *http.Request) {
	var list []appSpec
	for _, a := range g.apps {
		list = append(list, a)
	}
	indexTmpl.Execute(w, list)
}

func (g *gateway) addSlash(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.apps[r.PathValue("app")]; !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
}

// proxy reenvía /<app>/resto a 127.0.0.1:<puerto>/resto.
func (g *gateway) proxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("app")
	a, ok := g.apps[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	prefix := "/" + name
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", a.Port)}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
			pr.Out.Header.Set("X-Auth-User", r.Header.Get("X-Auth-User"))
			// Las credenciales de la pasarela no llegan a las apps.
			pr.Out.Header.Del("Authorization")
			removeCookie(pr.Out, cookieName)
		},
		// Las redirecciones absolutas de la app («/x») se corrigen al prefijo.
		ModifyResponse: func(resp *http.Response) error {
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
				resp.Header.Set("Location", prefix+loc)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.log.Error("app no responde", "app", name, "err", err)
			http.Error(w, "la app no responde", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func removeCookie(r *http.Request, name string) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}

// clientIP usa X-Forwarded-For cuando la petición llega de un proxy local
// (Tailscale Funnel); si no, la dirección de la conexión.
func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}
