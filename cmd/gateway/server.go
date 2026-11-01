package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
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

// Rutas propias de la pasarela; ninguna app puede llamarse así.
var reserved = map[string]bool{"static": true, "login": true, "logout": true, "panel": true, "healthz": true}

//go:embed web
var webFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"since": func(t time.Time) string { return humanDuration(time.Since(t)) },
	"date":  func(t time.Time) string { return t.Local().Format("02/01 15:04") },
	"short": func(s string) string { return truncate(s, 60) },
	"join":  strings.Join,
}).ParseFS(webFS, "web/*.html"))

type appSpec struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	// MaxUploadMB amplía el límite de cuerpo (32 MB por defecto) y da plazos
	// largos a la app, para subir y descargar ficheros grandes con datos móviles.
	MaxUploadMB int    `json:"max_upload_mb"`
	Title       string `json:"title"`
}

const defaultUploadMB = 32

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
		if !reserved[a.Name] {
			apps[a.Name] = a
		}
	}
	return apps, nil
}

type gateway struct {
	passwordHash string
	totp         *totpVerifier // nil: TOTP sin configurar, no se puede entrar
	recovery     *recoveryStore
	sessions     *sessionStore // panel (administrador)
	appSessions  *sessionStore // cuentas de las apps
	users        *userStore
	ipLimit      *limiter // fallos por IP
	globalLimit  *limiter // fallos en total, contra ataques repartidos entre IP
	userLimit    *limiter // fallos por cuenta de app
	tokens       *tokenStore
	apps         map[string]appSpec
	audit        *auditLog
	log          *slog.Logger
	srvDir       string
	svDir        string
	slowPhone    cached[phoneStatus]
	alerts       *alerter         // avisos por Telegram; nil en los tests que no los usan
	now          func() time.Time // sustituible en los tests
}

func (g *gateway) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *gateway) listen(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           g.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Sin ReadTimeout/WriteTimeout globales: cada petición recibe sus plazos
		// en deadlines(), y las apps con ficheros grandes los amplían.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}
	return srv.ListenAndServe()
}

func (g *gateway) handler() http.Handler {
	static, _ := fs.Sub(webFS, "web/static")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.Handle("GET /static/", cacheStatic(http.StripPrefix("/static/", http.FileServerFS(static))))
	mux.HandleFunc("GET /login", g.loginPage)
	mux.HandleFunc("POST /login", g.login)
	mux.Handle("POST /logout", g.session(http.HandlerFunc(g.logout)))
	mux.Handle("GET /{$}", g.session(http.HandlerFunc(g.dashboard)))
	mux.Handle("GET /panel/frag/{section}", g.session(http.HandlerFunc(g.fragment)))
	mux.Handle("POST /panel/restart", g.session(g.csrf(http.HandlerFunc(g.restart))))
	mux.Handle("POST /panel/sessions/revoke", g.session(g.csrf(http.HandlerFunc(g.revokeSession))))
	mux.Handle("POST /panel/tokens", g.session(g.csrf(http.HandlerFunc(g.createToken))))
	mux.Handle("POST /panel/tokens/revoke", g.session(g.csrf(http.HandlerFunc(g.revokeToken))))
	mux.Handle("POST /panel/users", g.session(g.csrf(http.HandlerFunc(g.userAction))))
	mux.Handle("POST /panel/asistente", g.session(g.csrf(http.HandlerFunc(g.assistantAction))))
	mux.Handle("POST /panel/avisos", g.session(g.csrf(http.HandlerFunc(g.alertsAction))))
	mux.Handle("POST /panel/copias", g.session(g.csrf(http.HandlerFunc(g.backupAction))))
	mux.Handle("/{app}", g.appAuth(http.HandlerFunc(g.addSlash)))
	mux.HandleFunc("/{app}/", g.appRoutes)
	return deadlines(securityHeaders(limitInflight(64, sameOrigin(mux))))
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// gatewayCSP es estricta: la pasarela no carga nada de fuera ni scripts en línea.
const gatewayCSP = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// securityHeaders se aplica a todo. Las apps pueden poner su propia CSP; si no
// la ponen, reciben una básica que impide incrustarlas y robar formularios.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// deadlines pone a cada petición 60 s para leer el cuerpo y 120 s para
// responder. proxy los amplía para las apps con max_upload_mb.
func deadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		now := time.Now()
		rc.SetReadDeadline(now.Add(60 * time.Second))
		rc.SetWriteDeadline(now.Add(120 * time.Second))
		next.ServeHTTP(w, r)
	})
}

// limitInflight rechaza con 503 lo que pase de n peticiones simultáneas, para
// que una avalancha no tumbe el móvil.
func limitInflight(n int, next http.Handler) http.Handler {
	slots := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "ocupado", http.StatusServiceUnavailable)
		}
	})
}

// sameOrigin rechaza peticiones que cambian cosas (no GET/HEAD) cuando vienen
// de otra web con la cookie del navegador. Los clientes con token Bearer no
// envían cookies y quedan fuera de esta comprobación.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safe := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		if !safe && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && !sameSite(r) {
			http.Error(w, "origen no permitido", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameSite usa Sec-Fetch-Site y Origin, que los navegadores ponen en todo POST.
// Un cliente sin ninguna de las dos (curl) no es un navegador engañado y pasa;
// sin cookie válida no podrá hacer nada.
func sameSite(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}

type ctxKey int

const sessionKey ctxKey = 0

// session exige una sesión web válida (el panel no acepta tokens).
func (g *gateway) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sess, ok := g.currentSession(r); ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
			return
		}
		g.denied(w, r)
	})
}

func (g *gateway) currentSession(r *http.Request) (*session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, false
	}
	return g.sessions.get(c.Value, clientIP(r), g.clock())
}

func sessionFrom(r *http.Request) *session {
	s, _ := r.Context().Value(sessionKey).(*session)
	return s
}

func (g *gateway) denied(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="homeserver"`)
	http.Error(w, "no autorizado", http.StatusUnauthorized)
}

// csrf exige el token de la sesión en los formularios del panel.
func (g *gateway) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if s := sessionFrom(r); s == nil || r.FormValue("csrf") != s.CSRF {
			http.Error(w, "formulario caducado, recarga la página", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *gateway) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Security-Policy", gatewayCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		g.log.Error("plantilla", "nombre", name, "err", err)
	}
}

func (g *gateway) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.currentSession(r); ok {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	g.render(w, "login.html", map[string]any{"Next": safeNext(r.URL.Query().Get("next")), "NoTOTP": g.totp == nil})
}

func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	ip, now := clientIP(r), g.clock()
	next := safeNext(r.FormValue("next"))
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		g.render(w, "login.html", map[string]any{"Next": next, "Error": msg, "NoTOTP": g.totp == nil})
	}
	if g.totp == nil {
		fail(http.StatusServiceUnavailable, "Falta configurar el segundo factor (gateway totp).")
		return
	}
	if !g.ipLimit.allowed(ip, now) || !g.globalLimit.allowed("*", now) {
		g.audit.add("login_bloqueado", ip, nil)
		fail(http.StatusTooManyRequests, "Demasiados intentos. Prueba dentro de un rato.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	okPass, err := checkPassword(ctx, g.passwordHash, r.FormValue("password"))
	if err != nil {
		fail(http.StatusServiceUnavailable, "Servidor ocupado, inténtalo de nuevo.")
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	how := "totp"
	okCode := okPass && g.totp.verify(code, now)
	if okPass && !okCode && strings.Contains(code, "-") && g.recovery.use(code) {
		okCode, how = true, "recuperación"
	}
	if !okPass || !okCode {
		g.ipLimit.fail(ip, now)
		g.globalLimit.fail("*", now)
		reason := "contraseña"
		if okPass {
			reason = "código"
		}
		g.audit.add("login_fallido", ip, map[string]string{"motivo": reason})
		fail(http.StatusUnauthorized, "Contraseña o código incorrectos.")
		return
	}
	cookie, _, err := g.sessions.create("", ip, r.UserAgent(), now)
	if err != nil {
		fail(http.StatusInternalServerError, "No se pudo crear la sesión.")
		return
	}
	detail := map[string]string{"factor": how}
	if how == "recuperación" {
		detail["quedan"] = fmt.Sprint(g.recovery.remaining())
	}
	g.audit.add("login", ip, detail)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: cookie, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (g *gateway) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		g.sessions.revokeCookie(c.Value, g.clock())
	}
	g.audit.add("logout", clientIP(r), nil)
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
	limitMB := defaultUploadMB
	if a.MaxUploadMB > limitMB {
		limitMB = a.MaxUploadMB
		rc := http.NewResponseController(w)
		rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
		rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(limitMB)<<20)
	prefix := "/" + name
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", a.Port)}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
			for _, h := range authHeaders {
				pr.Out.Header.Set(h, r.Header.Get(h))
			}
			// Las credenciales de la pasarela no llegan a las apps.
			pr.Out.Header.Del("Authorization")
			removeCookie(pr.Out, cookieName)
			removeCookie(pr.Out, appCookie)
		},
		ModifyResponse: func(resp *http.Response) error {
			// Las redirecciones absolutas de la app («/x») se corrigen al prefijo.
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
				resp.Header.Set("Location", prefix+loc)
			}
			if resp.Header.Get("Content-Security-Policy") == "" {
				resp.Header.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
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
