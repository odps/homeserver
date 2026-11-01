package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h := hashPassword("una contraseña larga")
	ctx := context.Background()
	if ok, _ := checkPassword(ctx, h, "una contraseña larga"); !ok {
		t.Fatal("la contraseña correcta no valida")
	}
	if ok, _ := checkPassword(ctx, h, "otra"); ok {
		t.Fatal("valida una contraseña incorrecta")
	}
	if ok, _ := checkPassword(ctx, "basura", "x"); ok {
		t.Fatal("valida un hash corrupto")
	}
}

func TestTOTP(t *testing.T) {
	// Vector de la RFC 6238 (SHA1, T=59 s): 94287082 → últimos 6 dígitos.
	if got := totpCode([]byte("12345678901234567890"), 59/30); got != "287082" {
		t.Fatalf("totpCode = %s, quiero 287082", got)
	}
	secret := newTOTPSecret()
	v, err := newTOTPVerifier(secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code := totpCode(v.secret, uint64(now.Unix()/totpStep))
	if !v.verify(code, now) {
		t.Fatal("código actual no válido")
	}
	if v.verify(code, now) {
		t.Fatal("acepta el mismo código dos veces")
	}
	old := totpCode(v.secret, uint64(now.Unix()/totpStep)-3)
	if v.verify(old, now) {
		t.Fatal("acepta un código de hace 90 s")
	}
}

func TestRecovery(t *testing.T) {
	r := &recoveryStore{path: filepath.Join(t.TempDir(), "rec.json")}
	codes, err := r.generate(3)
	if err != nil || len(codes) != 3 {
		t.Fatal(codes, err)
	}
	if !r.use(strings.ToUpper(codes[1])) || r.use(codes[1]) || r.remaining() != 2 {
		t.Fatal("los códigos de recuperación no son de un solo uso")
	}
	if r.use("aaaa-bbbb") {
		t.Fatal("acepta un código inventado")
	}
}

func TestSessions(t *testing.T) {
	s, _ := newSessionStore(filepath.Join(t.TempDir(), "s.json"), sessionTTL, sessionIdle)
	now := time.Now()
	cookie, sess, err := s.create("", "1.2.3.4", "test", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.get(cookie, "1.2.3.4", now.Add(time.Hour)); !ok {
		t.Fatal("sesión vigente no válida")
	}
	if _, ok := s.get("inventada", "", now); ok {
		t.Fatal("acepta una cookie inventada")
	}
	// Persiste: otro proceso la ve.
	s2, _ := newSessionStore(s.path, sessionTTL, sessionIdle)
	if _, ok := s2.get(cookie, "", now.Add(2*time.Hour)); !ok {
		t.Fatal("la sesión no sobrevive a un reinicio")
	}
	if _, ok := s.get(cookie, "", now.Add(sessionIdle+2*time.Hour)); ok {
		t.Fatal("acepta una sesión inactiva demasiado tiempo")
	}
	cookie, sess, _ = s.create("", "", "", now)
	if s.revoke(sess.ID, now) != 1 {
		t.Fatal("no cierra la sesión por ID")
	}
	if _, ok := s.get(cookie, "", now); ok {
		t.Fatal("sesión cerrada sigue válida")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/hola/": "/hola/", "": "/", "https://evil.com": "/", "//evil.com": "/", `/\evil.com`: "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, quiero %q", in, got, want)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(2, time.Minute)
	now := time.Now()
	l.fail("a", now)
	l.fail("a", now)
	if l.allowed("a", now) || !l.allowed("b", now) {
		t.Fatal("el límite por clave no funciona")
	}
	if !l.allowed("a", now.Add(2*time.Minute)) {
		t.Fatal("la ventana no caduca")
	}
}

func TestTokenScope(t *testing.T) {
	s := &tokenStore{path: filepath.Join(t.TempDir(), "t.json")}
	now := time.Now()
	tok, err := s.create("agente", "", []string{"hola"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.check(tok, "hola", now); !ok {
		t.Fatal("token válido rechazado")
	}
	if _, _, ok := s.check(tok, "otra", now); ok {
		t.Fatal("token usado fuera de su app")
	}
	if _, _, ok := s.check(tok, "hola", now.Add(2*time.Hour)); ok {
		t.Fatal("token caducado aceptado")
	}
	if _, err := s.create("mal nombre", "", []string{"*"}, 0); err == nil {
		t.Fatal("acepta un nombre con espacios")
	}
}

// harness: una app falsa y la pasarela delante, con TOTP conocido.
type harness struct {
	g      *gateway
	gw     *httptest.Server
	secret []byte
	step   uint64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/destino", http.StatusSeeOther)
			return
		}
		io.WriteString(w, "app:"+r.URL.Path+" user:"+r.Header.Get("X-Auth-User")+" role:"+r.Header.Get("X-Auth-Role")+" name:"+r.Header.Get("X-Auth-Name")+" cookie:"+r.Header.Get("Cookie")+" auth:"+r.Header.Get("Authorization"))
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)

	dir := t.TempDir()
	secret := newTOTPSecret()
	v, _ := newTOTPVerifier(secret)
	sessions, _ := newSessionStore(filepath.Join(dir, "s.json"), sessionTTL, sessionIdle)
	appSessions, _ := newSessionStore(filepath.Join(dir, "as.json"), 0, appSessionIdle)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := &gateway{
		passwordHash: hashPassword("correcta-y-larga"),
		totp:         v,
		recovery:     &recoveryStore{path: filepath.Join(dir, "r.json")},
		sessions:     sessions,
		appSessions:  appSessions,
		users:        &userStore{path: filepath.Join(dir, "u.json")},
		ipLimit:      newLimiter(5, time.Minute),
		globalLimit:  newLimiter(30, time.Minute),
		userLimit:    newLimiter(10, time.Minute),
		tokens:       &tokenStore{path: filepath.Join(dir, "t.json")},
		apps:         map[string]appSpec{"hola": {Name: "hola", Port: p}},
		audit:        &auditLog{path: filepath.Join(dir, "audit.jsonl"), log: log},
		log:          log,
		srvDir:       dir,
		svDir:        filepath.Join(dir, "sv"),
		alerts:       newAlerter(dir),
	}
	gw := httptest.NewServer(g.handler())
	t.Cleanup(gw.Close)
	return &harness{g: g, gw: gw, secret: v.secret, step: uint64(time.Now().Unix() / totpStep)}
}

// code devuelve un código TOTP aún no usado (pasos sucesivos dentro del margen).
func (h *harness) code() string {
	c := totpCode(h.secret, h.step)
	h.step++
	return c
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: noRedirect}
}

func (h *harness) login(t *testing.T, c *http.Client) {
	t.Helper()
	resp, err := c.PostForm(h.gw.URL+"/login", url.Values{"password": {"correcta-y-larga"}, "code": {h.code()}, "next": {"/"}})
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %v %v", err, resp)
	}
	// La cookie es Secure y httptest va por HTTP: se copia a mano.
	u, _ := url.Parse(h.gw.URL)
	c.Jar.SetCookies(u, resp.Cookies())
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName && (!ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode) {
			t.Fatalf("cookie de sesión sin las protecciones: %+v", ck)
		}
	}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

func TestLoginNeedsBothFactors(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for name, form := range map[string]url.Values{
		"sin código":        {"password": {"correcta-y-larga"}},
		"código malo":       {"password": {"correcta-y-larga"}, "code": {"000000"}},
		"contraseña mala":   {"password": {"mala"}, "code": {h.code()}},
		"recuperación mala": {"password": {"correcta-y-larga"}, "code": {"abcd-efgh"}},
	} {
		resp, _ := c.PostForm(h.gw.URL+"/login", form)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	codes, _ := h.g.recovery.generate(2)
	resp, _ := c.PostForm(h.gw.URL+"/login", url.Values{"password": {"correcta-y-larga"}, "code": {codes[0]}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login con código de recuperación: %d", resp.StatusCode)
	}
	// Tras 4 fallos más la IP queda bloqueada incluso con datos correctos.
	resp, _ = c.PostForm(h.gw.URL+"/login", url.Values{"password": {"mala"}, "code": {"1"}})
	resp, _ = c.PostForm(h.gw.URL+"/login", url.Values{"password": {"correcta-y-larga"}, "code": {h.code()}})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sin bloqueo tras 5 fallos: %d", resp.StatusCode)
	}
}

func TestLoginWithoutTOTPConfigured(t *testing.T) {
	h := newHarness(t)
	h.g.totp = nil
	resp, _ := h.client().PostForm(h.gw.URL+"/login", url.Values{"password": {"correcta-y-larga"}, "code": {"123456"}})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sin TOTP configurado: %d", resp.StatusCode)
	}
}

func TestProxyAndPanel(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	req, _ := http.NewRequest("GET", h.gw.URL+"/hola/", nil)
	req.Header.Set("Accept", "text/html")
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/hola/_login?next=%2Fhola%2F" {
		t.Fatalf("sin sesión: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	h.login(t, c)
	// La sesión del panel no abre las apps.
	req, _ = http.NewRequest("GET", h.gw.URL+"/hola/x", nil)
	if resp, _ := c.Do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("panel abriendo una app: %d", resp.StatusCode)
	}
	h.appLogin(t, c, "maria", []string{"hola"}, "usuario")
	req, _ = http.NewRequest("GET", h.gw.URL+"/hola/x", nil)
	req.Header.Set("X-Auth-User", "intruso")
	req.AddCookie(&http.Cookie{Name: "otra", Value: "1"})
	resp, _ = c.Do(req)
	got := body(t, resp)
	if !strings.HasPrefix(got, "app:/x user:maria role:usuario name:Mar%C3%ADa") || strings.Contains(got, cookieName) || strings.Contains(got, appCookie) || !strings.Contains(got, "otra=1") {
		t.Fatalf("proxy: %q", got)
	}
	resp, _ = c.Get(h.gw.URL + "/hola/redir")
	if resp.Header.Get("Location") != "/hola/destino" {
		t.Fatalf("Location reescrita = %q", resp.Header.Get("Location"))
	}
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("faltan cabeceras de seguridad en la app")
	}

	resp, _ = c.Get(h.gw.URL + "/")
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `id="security"`) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("panel: %d", resp.StatusCode)
	}
	for _, sec := range []string{"apps", "phone", "deploys", "security"} {
		if resp, _ := c.Get(h.gw.URL + "/panel/frag/" + sec); resp.StatusCode != http.StatusOK {
			t.Errorf("fragmento %s: %d", sec, resp.StatusCode)
		}
	}
}

func TestPanelForms(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.login(t, c)
	s := h.g.sessions.list(time.Now())[0]

	post := func(path string, form url.Values, origin string) *http.Response {
		req, _ := http.NewRequest("POST", h.gw.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, _ := c.Do(req)
		return resp
	}
	form := url.Values{"csrf": {s.CSRF}, "name": {"agente"}, "apps": {"hola"}, "days": {"30"}}
	if resp := post("/panel/tokens", form, "https://evil.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("otro origen: %d", resp.StatusCode)
	}
	if resp := post("/panel/tokens", url.Values{"name": {"agente"}, "apps": {"hola"}}, h.gw.URL); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("sin CSRF: %d", resp.StatusCode)
	}
	resp := post("/panel/tokens", form, h.gw.URL)
	page := body(t, resp)
	i := strings.Index(page, `id="new-token">`)
	if resp.StatusCode != http.StatusOK || i < 0 {
		t.Fatalf("crear token: %d", resp.StatusCode)
	}
	tok := page[i+len(`id="new-token">`):]
	tok = tok[:strings.Index(tok, "<")]

	// El token abre la app pero no el panel, y no llega a la app.
	req, _ := http.NewRequest("GET", h.gw.URL+"/hola/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	if got := body(t, resp); !strings.Contains(got, "user:token:agente role:servicio") || !strings.HasSuffix(got, "auth:") {
		t.Fatalf("token en la app: %q", got)
	}
	req, _ = http.NewRequest("GET", h.gw.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token en el panel: %d", resp.StatusCode)
	}

	if resp := post("/panel/tokens/revoke", url.Values{"csrf": {s.CSRF}, "name": {"agente"}}, h.gw.URL); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revocar: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.gw.URL+"/hola/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token revocado: %d", resp.StatusCode)
	}

	events := h.g.audit.recent(10)
	if len(events) < 3 || events[0].Event != "token_rechazado" {
		t.Fatalf("auditoría: %+v", events)
	}
}

// appLogin crea una cuenta de app y entra con ella por /<app>/_login.
func (h *harness) appLogin(t *testing.T, c *http.Client, user string, apps []string, role string) {
	t.Helper()
	pass, err := h.g.users.create(user, "María", apps, role)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.PostForm(h.gw.URL+"/"+apps[0]+"/_login", url.Values{"username": {user}, "password": {pass}, "next": {"/" + apps[0] + "/x"}})
	if err != nil || resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/"+apps[0]+"/x" {
		t.Fatalf("login de app: %v %v", err, resp)
	}
	u, _ := url.Parse(h.gw.URL)
	c.Jar.SetCookies(u, resp.Cookies())
}

func TestAppAccounts(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	// Sin sesión: al login de la propia app, dentro de su ruta.
	req, _ := http.NewRequest("GET", h.gw.URL+"/hola/libro", nil)
	req.Header.Set("Accept", "text/html")
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/hola/_login?next=%2Fhola%2Flibro" {
		t.Fatalf("sin sesión: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// El manifiesto y los iconos son públicos (instalación como app).
	if resp, _ := c.Get(h.gw.URL + "/hola/manifest.webmanifest"); resp.StatusCode != http.StatusOK {
		t.Fatalf("manifiesto: %d", resp.StatusCode)
	}

	pass, _ := h.g.users.create("pepe", "Pepe", []string{"hola"}, "usuario")
	bad, _ := c.PostForm(h.gw.URL+"/hola/_login", url.Values{"username": {"pepe"}, "password": {"mala"}})
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("contraseña mala: %d", bad.StatusCode)
	}
	resp, _ = c.PostForm(h.gw.URL+"/hola/_login", url.Values{"username": {"pepe"}, "password": {pass}, "next": {"https://evil.example"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/hola/" {
		t.Fatalf("login con next externo: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var appC *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == appCookie {
			appC = ck
		}
	}
	if appC == nil || !appC.HttpOnly || !appC.Secure || appC.MaxAge < 80*24*3600 {
		t.Fatalf("cookie de app: %+v", appC)
	}
	u, _ := url.Parse(h.gw.URL)
	c.Jar.SetCookies(u, resp.Cookies())

	// La sesión de app no abre el panel ni otras apps.
	req, _ = http.NewRequest("GET", h.gw.URL+"/", nil)
	req.Header.Set("Accept", "text/html")
	if resp, _ := c.Do(req); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("app abriendo el panel: %d", resp.StatusCode)
	}
	h.g.apps["otra"] = h.g.apps["hola"]
	if resp, _ := c.Get(h.gw.URL + "/otra/x"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("app sin permiso: %d", resp.StatusCode)
	}

	// Las cabeceras de identidad que mande el cliente no llegan a la app.
	req, _ = http.NewRequest("GET", h.gw.URL+"/hola/x", nil)
	req.Header.Set("X-Auth-User", "admin")
	req.Header.Set("X-Auth-Role", "admin")
	resp, _ = c.Do(req)
	if got := body(t, resp); !strings.Contains(got, "user:pepe role:usuario") {
		t.Fatalf("cabeceras falsificadas: %q", got)
	}

	// Desactivar la cuenta corta el acceso al momento.
	h.g.users.update("pepe", func(u *appUser) error { u.Disabled = true; return nil })
	if resp, _ := c.Get(h.gw.URL + "/hola/x"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cuenta desactivada: %d", resp.StatusCode)
	}
}

func TestAccountPasswordChange(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.appLogin(t, c, "ana", []string{"hola"}, "usuario")
	resp, _ := c.Get(h.gw.URL + "/hola/_cuenta")
	page := body(t, resp)
	i := strings.Index(page, `name="csrf" value="`)
	if resp.StatusCode != http.StatusOK || i < 0 {
		t.Fatalf("cuenta: %d", resp.StatusCode)
	}
	csrf := page[i+len(`name="csrf" value="`):]
	csrf = csrf[:strings.Index(csrf, `"`)]
	post := func(v url.Values) int {
		req, _ := http.NewRequest("POST", h.gw.URL+"/hola/_cuenta", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", h.gw.URL)
		resp, _ := c.Do(req)
		return resp.StatusCode
	}
	if st := post(url.Values{"csrf": {"x"}, "action": {"password"}}); st != http.StatusForbidden {
		t.Fatalf("sin CSRF: %d", st)
	}
	if st := post(url.Values{"csrf": {csrf}, "action": {"password"}, "current": {"mala"}, "new": {"nuevacontraseña"}, "repeat": {"nuevacontraseña"}}); st != http.StatusUnauthorized {
		t.Fatalf("actual mala: %d", st)
	}
	h.g.users.setPassword("ana", "contraseña-de-ana")
	if st := post(url.Values{"csrf": {csrf}, "action": {"password"}, "current": {"contraseña-de-ana"}, "new": {"corta"}, "repeat": {"corta"}}); st != http.StatusBadRequest {
		t.Fatalf("contraseña corta: %d", st)
	}
	if st := post(url.Values{"csrf": {csrf}, "action": {"password"}, "current": {"contraseña-de-ana"}, "new": {"otra-mas-larga"}, "repeat": {"otra-mas-larga"}}); st != http.StatusOK {
		t.Fatalf("cambio válido: %d", st)
	}
	if _, ok, _ := h.g.users.check(context.Background(), "ana", "otra-mas-larga"); !ok {
		t.Fatal("la contraseña nueva no vale")
	}
}

func TestUsers(t *testing.T) {
	s := &userStore{path: filepath.Join(t.TempDir(), "u.json")}
	if _, err := s.create("Mal Nombre", "", nil, "usuario"); err == nil {
		t.Fatal("acepta un usuario con espacios")
	}
	pass, err := s.create("luis", "Luis", []string{"libros"}, "admin")
	if err != nil || len(pass) < 16 {
		t.Fatal(pass, err)
	}
	if _, err := s.create("luis", "", nil, "usuario"); err == nil {
		t.Fatal("acepta un usuario repetido")
	}
	ctx := context.Background()
	if _, ok, _ := s.check(ctx, "luis", pass); !ok {
		t.Fatal("la contraseña temporal no vale")
	}
	if _, ok, _ := s.check(ctx, "nadie", pass); ok {
		t.Fatal("valida un usuario inexistente")
	}
	if err := s.setPassword("luis", "corta"); err == nil {
		t.Fatal("acepta una contraseña corta")
	}
}

func TestAccountName(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.appLogin(t, c, "ana", []string{"hola"}, "usuario")
	post := func(origin, body string) int {
		req, _ := http.NewRequest("POST", h.gw.URL+"/hola/_cuenta/nombre", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, _ := c.Do(req)
		return resp.StatusCode
	}
	if st := post("https://malicioso.example", `{"name":"Hackeado"}`); st != http.StatusForbidden {
		t.Fatalf("desde otro origen: %d", st)
	}
	if st := post(h.gw.URL, `{"name":"   "}`); st != http.StatusBadRequest {
		t.Fatalf("nombre vacío: %d", st)
	}
	if st := post(h.gw.URL, `{"name":"  Ana   García "}`); st != http.StatusOK {
		t.Fatalf("cambio válido: %d", st)
	}
	if u, _ := h.g.users.get("ana"); u.Name != "Ana García" {
		t.Fatalf("nombre guardado: %q", u.Name)
	}
	// La app recibe el nombre nuevo en la siguiente petición.
	resp, _ := c.Get(h.gw.URL + "/hola/algo")
	if page := body(t, resp); !strings.Contains(page, "name:"+url.QueryEscape("Ana García")) {
		t.Fatalf("la app no recibe el nombre nuevo: %s", page)
	}
	// Sin sesión de usuario (sin cookie) no hay nada que cambiar.
	req, _ := http.NewRequest("POST", h.gw.URL+"/hola/_cuenta/nombre", strings.NewReader(`{"name":"X"}`))
	req.Header.Set("Origin", h.gw.URL)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sin sesión: %d", resp.StatusCode)
	}
}

func TestAssistantConfig(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.login(t, c)
	s := h.g.sessions.list(time.Now())[0]
	h.g.users.create("ana", "Ana", []string{"hola"}, "usuario")
	post := func(form url.Values) *http.Response {
		form.Set("csrf", s.CSRF)
		req, _ := http.NewRequest("POST", h.gw.URL+"/panel/asistente", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", h.gw.URL)
		resp, _ := c.Do(req)
		return resp
	}
	path := h.g.assistantPath("claves.env")
	tgToken := "123456789:" + strings.Repeat("A", 35)
	valid := url.Values{"action": {"save"}, "telegram_token": {tgToken}, "telegram_user": {"987654321"}, "provider": {"groq"},
		"llm_key": {"gsk_clave-de-prueba-123"}, "daily_limit": {"200"}, "apps_user": {"ana"}}
	if resp := post(valid); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("guardar: %d %s", resp.StatusCode, body(t, resp))
	}
	m := readEnvFile(path)
	if m["TELEGRAM_TOKEN"] != tgToken || m["LLM_BASE_URL"] != "https://api.groq.com/openai/v1" || m["TELEGRAM_USER_ID"] != "987654321" ||
		!strings.HasPrefix(m["APPS_TOKEN"], "hs_") || m["APPS_USER"] != "ana" {
		t.Fatalf("claves guardadas: %v", m)
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Fatalf("permisos del fichero de claves: %v", st.Mode())
	}
	// El token de las apps actúa en nombre de ana, solo en sus apps.
	if user, e, ok := h.g.tokens.check(m["APPS_TOKEN"], "hola", time.Now()); !ok || e.User != "ana" || user == "" {
		t.Fatalf("token del asistente: %v %+v", ok, e)
	}
	// Recién guardado, el estado anterior no vale: el panel dice que se está aplicando.
	os.WriteFile(h.g.assistantPath("estado.json"), []byte(`{"updated_at":"`+time.Now().Add(-time.Minute/2).UTC().Format(time.RFC3339Nano)+`","bot":"","problems":["Falta el token"]}`), 0o600)
	if page := body(t, func() *http.Response { r, _ := c.Get(h.gw.URL + "/panel/frag/asistente"); return r }()); !strings.Contains(page, "Aplicando") || strings.Contains(page, "Falta el token") {
		t.Fatalf("estado de antes de guardar: %s", page)
	}
	// El panel muestra el estado que publica el asistente.
	os.WriteFile(h.g.assistantPath("estado.json"), []byte(`{"updated_at":"`+time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)+`","bot":"mibot","model":"openai/gpt-oss-120b",
		"models":["openai/gpt-oss-120b"],"tools":["libros_leyendo","notas_guardar"],"problems":["Falta algo"],"today":3,"limit":200}`), 0o600)
	// Los secretos no se muestran en el panel; un formulario sin ellos no los borra.
	page := body(t, func() *http.Response { r, _ := c.Get(h.gw.URL + "/panel/frag/asistente"); return r }())
	for _, want := range []string{"@mibot", "openai/gpt-oss-120b", "Hoy 3 de 200", "Falta algo", "libros_leyendo"} {
		if !strings.Contains(page, want) {
			t.Errorf("el panel no muestra %q", want)
		}
	}
	if strings.Contains(page, tgToken) || strings.Contains(page, "gsk_clave") || strings.Contains(page, m["APPS_TOKEN"]) || !strings.Contains(page, "…AAAA") {
		t.Fatal("el panel muestra un secreto o no lo enmascara")
	}
	keep := url.Values{"action": {"save"}, "telegram_user": {"987654321"}, "provider": {"gemini"}, "daily_limit": {"50"}, "apps_user": {"ana"}}
	post(keep)
	m2 := readEnvFile(path)
	if m2["TELEGRAM_TOKEN"] != tgToken || m2["LLM_API_KEY"] != "gsk_clave-de-prueba-123" || m2["APPS_TOKEN"] != m["APPS_TOKEN"] || !strings.Contains(m2["LLM_BASE_URL"], "generativelanguage") {
		t.Fatalf("guardar sin secretos: %v", m2)
	}
	for _, bad := range []url.Values{
		{"action": {"save"}, "telegram_token": {"no-es-un-token"}, "provider": {"groq"}, "daily_limit": {"200"}},
		{"action": {"save"}, "provider": {"custom"}, "llm_base_url": {"http://interno:8080"}, "daily_limit": {"200"}},
		{"action": {"save"}, "provider": {"groq"}, "telegram_user": {"abc"}, "daily_limit": {"200"}},
		{"action": {"save"}, "provider": {"groq"}, "llm_key": {"con espacio dentro"}, "daily_limit": {"200"}},
		{"action": {"save"}, "provider": {"groq"}, "daily_limit": {"200"}, "apps_user": {"nadie"}},
	} {
		if resp := post(bad); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("formulario no válido aceptado %v: %d", bad, resp.StatusCode)
		}
	}
	// Borrar: fichero fuera y token revocado.
	post(url.Values{"action": {"clear"}})
	if _, err := os.Stat(path); err == nil {
		t.Fatal("no se borró el fichero de claves")
	}
	if _, _, ok := h.g.tokens.check(m["APPS_TOKEN"], "hola", time.Now()); ok {
		t.Fatal("el token del asistente sigue valiendo")
	}
}
