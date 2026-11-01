package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBackupsPanel(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.login(t, c)
	s := h.g.sessions.list(time.Now())[0]
	post := func(form url.Values) *http.Response {
		form.Set("csrf", s.CSRF)
		req, _ := http.NewRequest("POST", h.gw.URL+"/panel/copias", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", h.gw.URL)
		resp, _ := c.Do(req)
		return resp
	}
	frag := func() string {
		return body(t, func() *http.Response { r, _ := c.Get(h.gw.URL + "/panel/frag/copias"); return r }())
	}
	if !strings.Contains(frag(), "Crear la clave") {
		t.Fatal("sin clave, el panel no ofrece crearla")
	}
	if resp := post(url.Values{"action": {"now"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("copia sin clave: %d", resp.StatusCode)
	}

	// La clave privada sale una vez en la página y no se guarda en ningún sitio.
	page := body(t, post(url.Values{"action": {"generate"}}))
	secret := regexp.MustCompile(`AGE-SECRET-KEY-1[0-9A-Z]+`).FindString(page)
	if secret == "" {
		t.Fatalf("no se muestra la clave privada: %s", page)
	}
	cfg := h.g.backupConfig()
	if !strings.HasPrefix(cfg.Recipient, "age1") {
		t.Fatalf("clave pública: %+v", cfg)
	}
	filepath.Walk(h.g.srvDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secret) {
				t.Errorf("la clave privada está en %s", p)
			}
		}
		return nil
	})
	if strings.Contains(frag(), secret) {
		t.Fatal("la clave privada se vuelve a mostrar")
	}

	if resp := post(url.Values{"action": {"save"}, "hour": {"3"}, "keep_daily": {"10"}, "keep_weekly": {"6"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("guardar: %d", resp.StatusCode)
	}
	if cfg := h.g.backupConfig(); cfg.Hour != 3 || cfg.KeepDaily != 10 || cfg.KeepWeekly != 6 || !strings.HasPrefix(cfg.Recipient, "age1") {
		t.Fatalf("configuración: %+v", cfg)
	}
	if resp := post(url.Values{"action": {"save"}, "hour": {"24"}, "keep_daily": {"7"}, "keep_weekly": {"4"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("hora no válida: %d", resp.StatusCode)
	}
	if resp := post(url.Values{"action": {"now"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("copia ahora: %d", resp.StatusCode)
	}
	if _, err := os.Stat(h.g.backupPath("state", "copia-ahora")); err != nil || !strings.Contains(frag(), "copia pedida") {
		t.Fatal("no queda pedida la copia")
	}
}

func TestBackupAlerts(t *testing.T) {
	h := newHarness(t)
	g := h.g
	bot := &fakeBot{}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m tgMessage
		json.NewDecoder(r.Body).Decode(&m)
		bot.mu.Lock()
		bot.sent = append(bot.sent, m)
		bot.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	g.alerts.telegramAPI = tg.URL
	writeEnvFile(g.assistantPath("claves.env"), map[string]string{"TELEGRAM_TOKEN": "123:abc", "TELEGRAM_USER_ID": "42"})
	g.apps = map[string]appSpec{}
	g.alerts.phone = func(context.Context) phoneStatus { return phoneStatus{} }
	writeJSONFile(g.backupPath("state", "copias.json"), backupConfig{Recipient: "age1x"})
	dir := g.backupPath("backups", "copias")
	os.MkdirAll(dir, 0o700)
	t0 := time.Now()
	copyFile := filepath.Join(dir, "homeserver-20261101-0400.tar.gz.age")
	os.WriteFile(copyFile, nil, 0o600)
	os.Chtimes(copyFile, t0, t0)
	writeJSONFile(g.backupPath("state", "copias-estado.json"), backupStatus{LastTry: t0, Error: "pg_dump: sin espacio"})
	ctx := context.Background()

	g.checkAlerts(ctx, t0)
	got := bot.take()
	if len(got) != 1 || !strings.Contains(got[0].Text, "Ha fallado la copia de seguridad: pg_dump: sin espacio") {
		t.Fatalf("copia fallida: %+v", got)
	}
	// Vuelve a ir bien; cuatro días después, el PC no la ha recogido.
	writeJSONFile(g.backupPath("state", "copias-estado.json"), backupStatus{LastTry: t0, LastOK: t0})
	g.checkAlerts(ctx, t0.Add(4*24*time.Hour))
	all := ""
	for _, m := range bot.take() {
		all += m.Text + "\n"
	}
	if !strings.Contains(all, "vuelven a funcionar") || !strings.Contains(all, "El PC lleva más de 3 días sin recoger las copias de seguridad (última vez: nunca)") {
		t.Fatalf("copias sin recoger: %s", all)
	}
	// El PC las recoge (deja la marca).
	os.WriteFile(filepath.Join(dir, ".recogida"), nil, 0o600)
	now := t0.Add(4*24*time.Hour + time.Minute)
	os.Chtimes(filepath.Join(dir, ".recogida"), now, now)
	g.checkAlerts(ctx, now)
	if got := bot.take(); len(got) != 1 || !strings.Contains(got[0].Text, "ha vuelto a recoger") {
		t.Fatalf("al recogerlas: %+v", got)
	}
}
