package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Acceso a las apps con cuentas propias (sin 2FA) y sesiones largas. Es un
// ámbito aparte del panel: otra cookie y otro almacén de sesiones, así que
// una sesión de app nunca abre el panel, y la del panel no abre las apps.

const appCookie = "__Host-hs_app"

// Cabeceras que la pasarela fija para las apps. Las que mande el cliente se
// borran siempre: las apps solo escuchan en 127.0.0.1 y confían en ellas.
var authHeaders = []string{"X-Auth-User", "X-Auth-Name", "X-Auth-Role"}

func setAuthHeaders(r *http.Request, user, name, role string) {
	r.Header.Set("X-Auth-User", user)
	r.Header.Set("X-Auth-Name", url.QueryEscape(name)) // puede llevar tildes
	r.Header.Set("X-Auth-Role", role)
}

// appAuth deja pasar a una app con una sesión de app de una cuenta que la
// tenga permitida, o con un token válido para ella.
func (g *gateway) appAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app := r.PathValue("app")
		if _, ok := g.apps[app]; !ok {
			http.NotFound(w, r)
			return
		}
		for _, h := range authHeaders {
			r.Header.Del(h)
		}
		// El manifiesto y los iconos son públicos: el navegador los pide sin
		// cookies al instalar la app, y no revelan nada.
		if r.Method == http.MethodGet && publicAsset(strings.TrimPrefix(r.URL.Path, "/"+app+"/")) {
			next.ServeHTTP(w, r)
			return
		}
		now := g.clock()
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			name, entry, valid := g.tokens.check(tok, app, now)
			if valid && entry.User != "" {
				// Token de un usuario: el agente actúa en su nombre.
				if u, ok := g.users.get(entry.User); ok && u.allows(app) {
					setAuthHeaders(r, entry.User, u.Name, u.Role)
					next.ServeHTTP(w, r)
					return
				}
				valid = false
			}
			if valid {
				setAuthHeaders(r, "token:"+name, name, "servicio")
				next.ServeHTTP(w, r)
				return
			}
			g.audit.add("token_rechazado", clientIP(r), map[string]string{"app": app, "token": name})
			g.appDenied(w, r, app)
			return
		}
		if user, tok, ok := r.BasicAuth(); ok {
			// Clientes CalDAV/CardDAV (el calendario del móvil), que solo
			// saben de usuario y contraseña: el usuario y un token de la app
			// creado en su nombre.
			name, entry, valid := g.tokens.check(tok, app, now)
			if u, okUser := g.users.get(entry.User); valid && entry.User != "" && strings.EqualFold(user, entry.User) && okUser && u.allows(app) {
				setAuthHeaders(r, entry.User, u.Name, u.Role)
				next.ServeHTTP(w, r)
				return
			}
			g.audit.add("token_rechazado", clientIP(r), map[string]string{"app": app, "token": name, "usuario": user})
			g.appDenied(w, r, app)
			return
		}
		if c, err := r.Cookie(appCookie); err == nil {
			if sess, ok := g.appSessions.get(c.Value, clientIP(r), now); ok {
				if u, ok := g.users.get(sess.User); ok && u.allows(app) {
					setAuthHeaders(r, sess.User, u.Name, u.Role)
					// Renueva la cookie al navegar: la sesión dura mientras se use.
					if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
						setAppCookie(w, c.Value)
					}
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
					return
				}
			}
		}
		g.appDenied(w, r, app)
	})
}

// appRoutes atiende /<app>/…: el login, la salida y la cuenta van dentro de
// la ruta de cada app (para que una app instalada no salga de su ventana) y
// nunca llegan a la app; el resto pasa por appAuth al proxy.
func (g *gateway) appRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/"+r.PathValue("app")+"/")
	switch {
	case rest == "_login" && r.Method == http.MethodGet:
		g.appLoginPage(w, r)
	case rest == "_login" && r.Method == http.MethodPost:
		g.appLogin(w, r)
	case rest == "_logout" && r.Method == http.MethodPost:
		g.appLogout(w, r)
	case rest == "_cuenta" && r.Method == http.MethodGet:
		g.appAuth(http.HandlerFunc(g.accountPage)).ServeHTTP(w, r)
	case rest == "_cuenta" && r.Method == http.MethodPost:
		g.appAuth(http.HandlerFunc(g.accountPost)).ServeHTTP(w, r)
	case rest == "_cuenta/nombre" && r.Method == http.MethodPost:
		g.appAuth(http.HandlerFunc(g.accountName)).ServeHTTP(w, r)
	case strings.HasPrefix(rest, "_"):
		http.NotFound(w, r)
	default:
		g.appAuth(http.HandlerFunc(g.proxy)).ServeHTTP(w, r)
	}
}

func publicAsset(p string) bool {
	return p == "manifest.webmanifest" || p == "assets/icon.svg" ||
		(strings.HasPrefix(p, "assets/icon-") && strings.HasSuffix(p, ".png") && !strings.Contains(p, "/../"))
}

func setAppCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: appCookie, Value: value, Path: "/", MaxAge: int(appSessionIdle.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// appDenied manda al login de la app (dentro de su ámbito, para que una app
// instalada no salga de su ventana) o responde 401 a las peticiones de API.
func (g *gateway) appDenied(w http.ResponseWriter, r *http.Request, app string) {
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/"+app+"/_login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="homeserver"`)
	if strings.HasPrefix(r.URL.Path, "/"+app+"/dav/") || r.URL.Path == "/"+app+"/dav" {
		// CalDAV/CardDAV: el cliente pide usuario y contraseña (el token).
		w.Header().Add("WWW-Authenticate", `Basic realm="homeserver `+app+`", charset="UTF-8"`)
	}
	http.Error(w, "no autorizado", http.StatusUnauthorized)
}

// appNext solo admite rutas dentro de la misma app.
func appNext(app, next string) string {
	next = safeNext(next)
	if next != "/"+app && !strings.HasPrefix(next, "/"+app+"/") || strings.HasPrefix(next, "/"+app+"/_") {
		return "/" + app + "/"
	}
	return next
}

func (g *gateway) appTitle(app string) string {
	if t := g.apps[app].Title; t != "" {
		return t
	}
	return strings.ToUpper(app[:1]) + app[1:]
}

func (g *gateway) appLoginPage(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	if _, ok := g.apps[app]; !ok {
		http.NotFound(w, r)
		return
	}
	g.render(w, "app-login.html", map[string]any{"App": app, "Title": g.appTitle(app), "Next": appNext(app, r.URL.Query().Get("next"))})
}

func (g *gateway) appLogin(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	if _, ok := g.apps[app]; !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	ip, now := clientIP(r), g.clock()
	username := strings.ToLower(strings.TrimSpace(r.FormValue("username")))
	next := appNext(app, r.FormValue("next"))
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		g.render(w, "app-login.html", map[string]any{"App": app, "Title": g.appTitle(app), "Next": next, "Username": username, "Error": msg})
	}
	if !g.ipLimit.allowed(ip, now) || !g.globalLimit.allowed("*", now) || !g.userLimit.allowed(username, now) {
		g.audit.add("app_login_bloqueado", ip, map[string]string{"app": app, "usuario": username})
		fail(http.StatusTooManyRequests, "Demasiados intentos. Prueba dentro de un rato.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	u, ok, err := g.users.check(ctx, username, r.FormValue("password"))
	if err != nil {
		fail(http.StatusServiceUnavailable, "Servidor ocupado, inténtalo de nuevo.")
		return
	}
	if !ok || !u.allows(app) {
		g.ipLimit.fail(ip, now)
		g.globalLimit.fail("*", now)
		g.userLimit.fail(username, now)
		g.audit.add("app_login_fallido", ip, map[string]string{"app": app, "usuario": username})
		fail(http.StatusUnauthorized, "Usuario o contraseña incorrectos.")
		return
	}
	cookie, _, err := g.appSessions.create(username, ip, r.UserAgent(), now)
	if err != nil {
		fail(http.StatusInternalServerError, "No se pudo crear la sesión.")
		return
	}
	g.users.touchLogin(username, now)
	g.audit.add("app_login", ip, map[string]string{"app": app, "usuario": username})
	setAppCookie(w, cookie)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (g *gateway) appLogout(w http.ResponseWriter, r *http.Request) {
	app := r.PathValue("app")
	if c, err := r.Cookie(appCookie); err == nil {
		g.appSessions.revokeCookie(c.Value, g.clock())
	}
	http.SetCookie(w, &http.Cookie{Name: appCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/"+app+"/_login", http.StatusSeeOther)
}

// La página de cuenta pasa por appAuth: solo la ve quien ya ha entrado.
func (g *gateway) accountPage(w http.ResponseWriter, r *http.Request) {
	g.renderAccount(w, r, http.StatusOK, "", "")
}

func (g *gateway) renderAccount(w http.ResponseWriter, r *http.Request, status int, msg, errMsg string) {
	app := r.PathValue("app")
	sess := sessionFrom(r)
	if sess == nil {
		http.Error(w, "solo con sesión de usuario", http.StatusForbidden)
		return
	}
	u, _ := g.users.get(sess.User)
	var mine []session
	for _, s := range g.appSessions.list(g.clock()) {
		if s.User == sess.User {
			mine = append(mine, s)
		}
	}
	w.WriteHeader(status)
	g.render(w, "app-account.html", map[string]any{
		"App": app, "Title": g.appTitle(app), "Username": sess.User, "Name": u.Name, "CSRF": sess.CSRF,
		"SessionID": sess.ID, "Sessions": mine, "Msg": msg, "Error": errMsg, "MinLen": minAppPassword,
	})
}

// accountName cambia el nombre visible de la cuenta desde la propia app
// (JSON). Lo protege la comprobación de origen de todas las peticiones que no
// son GET; el usuario para entrar no cambia.
func (g *gateway) accountName(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess == nil {
		http.Error(w, "solo con sesión de usuario", http.StatusForbidden)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil {
		http.Error(w, "datos no válidos", http.StatusBadRequest)
		return
	}
	name := strings.Join(strings.Fields(strings.ToValidUTF8(in.Name, "")), " ")
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "el nombre debe tener entre 1 y 60 caracteres"})
		return
	}
	if err := g.users.update(sess.User, func(u *appUser) error { u.Name = name; return nil }); err != nil {
		http.Error(w, "no se pudo guardar", http.StatusInternalServerError)
		return
	}
	g.audit.add("app_nombre", clientIP(r), map[string]string{"usuario": sess.User})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": name})
}

func (g *gateway) accountPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	sess := sessionFrom(r)
	if sess == nil || r.FormValue("csrf") != sess.CSRF {
		http.Error(w, "formulario caducado, recarga la página", http.StatusForbidden)
		return
	}
	ip, now := clientIP(r), g.clock()
	switch r.FormValue("action") {
	case "password":
		if !g.userLimit.allowed(sess.User, now) {
			g.renderAccount(w, r, http.StatusTooManyRequests, "", "Demasiados intentos. Prueba dentro de un rato.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, ok, err := g.users.check(ctx, sess.User, r.FormValue("current")); err != nil || !ok {
			g.userLimit.fail(sess.User, now)
			g.renderAccount(w, r, http.StatusUnauthorized, "", "La contraseña actual no es correcta.")
			return
		}
		if r.FormValue("new") != r.FormValue("repeat") {
			g.renderAccount(w, r, http.StatusBadRequest, "", "Las contraseñas nuevas no coinciden.")
			return
		}
		if err := g.users.setPassword(sess.User, r.FormValue("new")); err != nil {
			g.renderAccount(w, r, http.StatusBadRequest, "", err.Error())
			return
		}
		g.audit.add("app_contraseña", ip, map[string]string{"usuario": sess.User})
		g.renderAccount(w, r, http.StatusOK, "Contraseña cambiada.", "")
	case "logout-others":
		for _, s := range g.appSessions.list(now) {
			if s.User == sess.User && s.ID != sess.ID {
				g.appSessions.revoke(s.ID, now)
			}
		}
		g.audit.add("app_sesiones_cerradas", ip, map[string]string{"usuario": sess.User})
		g.renderAccount(w, r, http.StatusOK, "Se han cerrado las sesiones de los demás dispositivos.", "")
	default:
		http.Error(w, "acción no válida", http.StatusBadRequest)
	}
}
