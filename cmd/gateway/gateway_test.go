package main

import (
	"io"
	"log/slog"
	"net"
	"net/http"
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
	if !checkPassword(h, "una contraseña larga") {
		t.Fatal("la contraseña correcta no valida")
	}
	if checkPassword(h, "otra") || checkPassword("basura", "x") {
		t.Fatal("valida lo que no debe")
	}
}

func TestSessions(t *testing.T) {
	s := sessions{key: []byte("0123456789abcdef0123456789abcdef"), ttl: time.Hour}
	now := time.Now()
	c := s.issue(now)
	if !s.valid(c, now.Add(59*time.Minute)) {
		t.Fatal("sesión vigente no válida")
	}
	if s.valid(c, now.Add(61*time.Minute)) {
		t.Fatal("sesión caducada válida")
	}
	exp, sig, _ := strings.Cut(c, ".")
	if s.valid(exp+"9."+sig, now) {
		t.Fatal("acepta una caducidad manipulada")
	}
	other := sessions{key: []byte("otra-clave-otra-clave-otra-clave"), ttl: time.Hour}
	if other.valid(c, now) {
		t.Fatal("acepta una firma de otra clave")
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
		t.Fatal("el límite por IP no funciona")
	}
	if !l.allowed("a", now.Add(2*time.Minute)) {
		t.Fatal("la ventana no caduca")
	}
}

// newGateway prepara una pasarela de prueba delante de la app de backend.
func newGateway(t *testing.T, backend *httptest.Server) *gateway {
	t.Helper()
	u, _ := url.Parse(backend.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	return &gateway{
		passwordHash: hashPassword("correcta-y-larga"),
		sessions:     sessions{key: []byte("0123456789abcdef0123456789abcdef"), ttl: time.Hour},
		limiter:      newLimiter(3, time.Minute),
		tokens:       tokenStore{path: filepath.Join(t.TempDir(), "tokens.json")},
		apps:         map[string]appSpec{"hola": {Name: "hola", Port: p}},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// setup arranca una app falsa y la pasarela delante de ella.
func setup(t *testing.T) *httptest.Server {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/destino", http.StatusSeeOther)
			return
		}
		io.WriteString(w, "app:"+r.URL.Path)
	}))
	t.Cleanup(backend.Close)
	gw := httptest.NewServer(newGateway(t, backend).handler())
	t.Cleanup(gw.Close)
	return gw
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func TestUnauthenticated(t *testing.T) {
	gw := setup(t)
	c := &http.Client{CheckRedirect: noRedirect}

	req, _ := http.NewRequest("GET", gw.URL+"/hola/", nil)
	req.Header.Set("Accept", "text/html")
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=%2Fhola%2F") {
		t.Fatalf("navegador sin sesión: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = c.Post(gw.URL+"/hola/mcp", "application/json", strings.NewReader("{}"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API sin token: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", gw.URL+"/hola/", nil)
	req.Header.Set("Authorization", "Bearer hs_falso")
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token falso: %d", resp.StatusCode)
	}
}

func TestLoginAndProxy(t *testing.T) {
	gw := setup(t)
	c := &http.Client{CheckRedirect: noRedirect}

	resp, _ := c.PostForm(gw.URL+"/login", url.Values{"password": {"mala"}, "next": {"/hola/"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("contraseña mala: %d", resp.StatusCode)
	}
	resp, _ = c.PostForm(gw.URL+"/login", url.Values{"password": {"correcta-y-larga"}, "next": {"/hola/x"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/hola/x" {
		t.Fatalf("login: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			session = ck
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure {
		t.Fatalf("cookie de sesión: %+v", session)
	}

	req, _ := http.NewRequest("GET", gw.URL+"/hola/x?y=1", nil)
	req.AddCookie(session)
	req.AddCookie(&http.Cookie{Name: "otra", Value: "1"})
	req.Header.Set("X-Auth-User", "intruso")
	resp, _ = c.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "app:/x" {
		t.Fatalf("proxy: %d %q", resp.StatusCode, body)
	}

	req, _ = http.NewRequest("GET", gw.URL+"/hola/redir", nil)
	req.AddCookie(session)
	resp, _ = c.Do(req)
	if resp.Header.Get("Location") != "/hola/destino" {
		t.Fatalf("Location reescrita = %q", resp.Header.Get("Location"))
	}

	req, _ = http.NewRequest("GET", gw.URL+"/nada/", nil)
	req.AddCookie(session)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("app inexistente: %d", resp.StatusCode)
	}
}

func TestProxyHeaders(t *testing.T) {
	var got http.Header
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotPath = r.Header.Clone(), r.URL.Path
	}))
	defer backend.Close()
	g := newGateway(t, backend)
	tok, err := g.tokens.create("agente")
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(g.handler())
	defer gw.Close()

	req, _ := http.NewRequest("POST", gw.URL+"/hola/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Auth-User", "intruso")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "x"})
	req.AddCookie(&http.Cookie{Name: "app", Value: "1"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("token válido: %v %v", err, resp)
	}
	if gotPath != "/mcp" || got.Get("X-Auth-User") != "token:agente" || got.Get("Authorization") != "" ||
		got.Get("X-Forwarded-Prefix") != "/hola" || strings.Contains(got.Get("Cookie"), cookieName) || !strings.Contains(got.Get("Cookie"), "app=1") {
		t.Fatalf("cabeceras en la app: path=%s %v", gotPath, got)
	}

	if err := g.tokens.revoke("agente"); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("POST", gw.URL+"/hola/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token revocado: %d", resp.StatusCode)
	}
}
