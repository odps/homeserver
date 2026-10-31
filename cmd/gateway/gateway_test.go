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
	"path/filepath"
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
	s, _ := newSessionStore(filepath.Join(t.TempDir(), "s.json"))
	now := time.Now()
	cookie, sess, err := s.create("1.2.3.4", "test", now)
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
	s2, _ := newSessionStore(s.path)
	if _, ok := s2.get(cookie, "", now.Add(2*time.Hour)); !ok {
		t.Fatal("la sesión no sobrevive a un reinicio")
	}
	if _, ok := s.get(cookie, "", now.Add(sessionIdle+2*time.Hour)); ok {
		t.Fatal("acepta una sesión inactiva demasiado tiempo")
	}
	cookie, sess, _ = s.create("", "", now)
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
	tok, err := s.create("agente", []string{"hola"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.check(tok, "hola", now); !ok {
		t.Fatal("token válido rechazado")
	}
	if _, ok := s.check(tok, "otra", now); ok {
		t.Fatal("token usado fuera de su app")
	}
	if _, ok := s.check(tok, "hola", now.Add(2*time.Hour)); ok {
		t.Fatal("token caducado aceptado")
	}
	if _, err := s.create("mal nombre", []string{"*"}, 0); err == nil {
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
		io.WriteString(w, "app:"+r.URL.Path+" user:"+r.Header.Get("X-Auth-User")+" cookie:"+r.Header.Get("Cookie")+" auth:"+r.Header.Get("Authorization"))
	}))
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)

	dir := t.TempDir()
	secret := newTOTPSecret()
	v, _ := newTOTPVerifier(secret)
	sessions, _ := newSessionStore(filepath.Join(dir, "s.json"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := &gateway{
		passwordHash: hashPassword("correcta-y-larga"),
		totp:         v,
		recovery:     &recoveryStore{path: filepath.Join(dir, "r.json")},
		sessions:     sessions,
		ipLimit:      newLimiter(5, time.Minute),
		globalLimit:  newLimiter(30, time.Minute),
		tokens:       &tokenStore{path: filepath.Join(dir, "t.json")},
		apps:         map[string]appSpec{"hola": {Name: "hola", Port: p}},
		audit:        &auditLog{path: filepath.Join(dir, "audit.jsonl"), log: log},
		log:          log,
		srvDir:       dir,
		svDir:        filepath.Join(dir, "sv"),
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
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=%2Fhola%2F") {
		t.Fatalf("sin sesión: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	h.login(t, c)
	req, _ = http.NewRequest("GET", h.gw.URL+"/hola/x", nil)
	req.Header.Set("X-Auth-User", "intruso")
	req.AddCookie(&http.Cookie{Name: "otra", Value: "1"})
	resp, _ = c.Do(req)
	got := body(t, resp)
	if !strings.HasPrefix(got, "app:/x user:owner") || strings.Contains(got, cookieName) || !strings.Contains(got, "otra=1") {
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
	if got := body(t, resp); !strings.Contains(got, "user:token:agente") || !strings.HasSuffix(got, "auth:") {
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
