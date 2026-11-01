package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type tgMessage struct {
	Text   string `json:"text"`
	Silent bool   `json:"disable_notification"`
}

// fakeBot guarda lo que la pasarela envía a Telegram.
type fakeBot struct {
	mu   sync.Mutex
	sent []tgMessage
}

func (f *fakeBot) take() []tgMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

func appendJSONL(t *testing.T, path string, v any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(v)
}

func portOf(t *testing.T, srv *httptest.Server) int {
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	return p
}

func TestAlerts(t *testing.T) {
	h := newHarness(t)
	g := h.g
	bot := &fakeBot{}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123:abc/sendMessage" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var m tgMessage
		json.NewDecoder(r.Body).Decode(&m)
		bot.mu.Lock()
		bot.sent = append(bot.sent, m)
		bot.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	g.alerts.telegramAPI = tg.URL
	writeEnvFile(g.assistantPath("claves.env"), map[string]string{"TELEGRAM_TOKEN": "123:abc", "TELEGRAM_USER_ID": "42", "APPS_USER": "ana"})

	// Una app de libros con una serie seguida, y una app que no responde.
	latest := float32(10)
	var mu sync.Mutex
	libros := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"version":"x","db":"ok"}`))
			return
		}
		if r.URL.Path != "/api/series" || r.Header.Get("X-Auth-User") != "ana" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "title": "Solo Leveling", "latest_number": latest, "favorite": false, "unread": 2}})
	}))
	defer libros.Close()
	g.apps["libros"] = appSpec{Name: "libros", Port: portOf(t, libros)}
	g.apps["rota"] = appSpec{Name: "rota", Port: 1}
	g.users.create("ana", "Ana", []string{"libros"}, "usuario")

	phone := phoneStatus{HasBattery: true, BatteryPct: 80, Plugged: true, BatteryStatus: "Cargando", BatteryTemp: 30, CPUTemp: 50, DiskFreeGB: 20, DiskUsedPct: 40}
	g.alerts.phone = func(context.Context) phoneStatus { return phone }
	c := defaultAlerts()
	c.User, c.PublicURL, c.QuietFrom, c.QuietTo = "ana", "https://srv.example", "", ""
	g.alerts.saveConfig(c)

	ctx := context.Background()
	deploys := filepath.Join(g.srvDir, "state", "deploys.jsonl")
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	appendJSONL(t, deploys, map[string]any{"time": t0.Add(-time.Hour), "event": "descartada", "tag": "vieja"})

	// Primera vuelta: solo apunta (ni el despliegue viejo ni la serie).
	g.checkAlerts(ctx, t0)
	if got := bot.take(); len(got) != 0 {
		t.Fatalf("la primera vuelta no debe avisar: %+v", got)
	}

	// Un despliegue fallido, un capítulo nuevo, batería baja sin cargar y la
	// app rota ya pasados los 5 minutos.
	appendJSONL(t, deploys, map[string]any{"time": t0.Add(time.Minute), "event": "descartada", "tag": "v2", "prev": "v1", "detail": "health falló"})
	mu.Lock()
	latest = 11
	mu.Unlock()
	phone.BatteryPct, phone.Plugged, phone.BatteryStatus = 20, false, "Descargando"
	g.checkAlerts(ctx, t0.Add(11*time.Minute))
	all := ""
	for _, m := range bot.take() {
		all += m.Text + "\n"
		if m.Silent {
			t.Errorf("sin horas de silencio llegó en silencio: %q", m.Text)
		}
	}
	for _, want := range []string{"Despliegue fallido: la versión v2", "health falló", "Nuevo Solo Leveling: capítulo 11 (2 sin leer)", "https://srv.example/libros/#/comics/7",
		"Batería baja: 20 %", "no está cargando", "La app rota lleva 11 min sin funcionar"} {
		if !strings.Contains(all, want) {
			t.Errorf("falta %q en:\n%s", want, all)
		}
	}
	if strings.Contains(all, "vieja") {
		t.Error("avisa de un despliegue anterior a los avisos")
	}

	// Sigue igual: no se repite nada.
	g.checkAlerts(ctx, t0.Add(12*time.Minute))
	if got := bot.take(); len(got) != 0 {
		t.Fatalf("se repiten avisos: %+v", got)
	}

	// Vuelve a cargar (aviso) y la batería sube un poco, sin pasar el margen.
	phone.Plugged, phone.BatteryPct = true, 22
	g.checkAlerts(ctx, t0.Add(13*time.Minute))
	if got := bot.take(); len(got) != 1 || !strings.Contains(got[0].Text, "vuelve a cargar") {
		t.Fatalf("al volver a cargar: %+v", got)
	}

	// Horas de silencio (en Madrid, las 23:30 son las 22:30 UTC en noviembre).
	c.QuietFrom, c.QuietTo = "23:00", "08:00"
	g.alerts.saveConfig(c)
	phone.CPUTemp = 85
	g.checkAlerts(ctx, time.Date(2026, 11, 1, 22, 30, 0, 0, time.UTC))
	if got := bot.take(); len(got) != 1 || !got[0].Silent || !strings.Contains(got[0].Text, "procesador está a 85") {
		t.Fatalf("aviso en horas de silencio: %+v", got)
	}

	// Un tipo apagado no avisa; con los avisos apagados, nada.
	c.Temp.On = false
	c.Enabled = false
	g.alerts.saveConfig(c)
	phone.BatteryTemp, phone.Plugged = 50, false
	g.checkAlerts(ctx, t0.Add(30*time.Minute))
	if got := bot.take(); len(got) != 0 {
		t.Fatalf("con los avisos apagados: %+v", got)
	}

	// El panel enseña los últimos avisos.
	if r := g.alerts.recentAlerts(3); len(r) != 3 || !strings.Contains(r[0].Text, "procesador") {
		t.Fatalf("últimos avisos: %+v", r)
	}
}

func TestAlertsDailySummary(t *testing.T) {
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
	g.alerts.phone = func(context.Context) phoneStatus { return phoneStatus{} }
	latest := float32(1)
	libros := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"id": 3, "title": "Omniscient Reader", "latest_number": latest, "favorite": true, "unread": 1}})
	}))
	defer libros.Close()
	g.apps = map[string]appSpec{"libros": {Name: "libros", Port: portOf(t, libros)}}
	g.users.create("ana", "Ana", []string{"libros"}, "usuario")
	c := defaultAlerts()
	c.User, c.QuietFrom, c.QuietTo = "ana", "", ""
	c.Chapters.Daily, c.Chapters.DailyAt, c.Chapters.Favorites = true, "20:00", true
	c.Apps.On = false
	g.alerts.saveConfig(c)

	ctx := context.Background()
	day := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) // 11:00 en Madrid
	g.checkAlerts(ctx, day)
	latest = 2
	g.checkAlerts(ctx, day.Add(15*time.Minute))
	if got := bot.take(); len(got) != 0 {
		t.Fatalf("en modo resumen no se avisa al momento: %+v", got)
	}
	g.checkAlerts(ctx, day.Add(9*time.Hour)) // 20:00 en Madrid
	got := bot.take()
	if len(got) != 1 || !strings.Contains(got[0].Text, "Capítulos nuevos de hoy") || !strings.Contains(got[0].Text, "Omniscient Reader: capítulo 2") {
		t.Fatalf("resumen diario: %+v", got)
	}
	g.checkAlerts(ctx, day.Add(10*time.Hour))
	if got := bot.take(); len(got) != 0 {
		t.Fatalf("el resumen se repite: %+v", got)
	}
}

func TestAlertsPanel(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	h.login(t, c)
	s := h.g.sessions.list(time.Now())[0]
	h.g.users.create("ana", "Ana", []string{"hola"}, "usuario")
	post := func(form url.Values) *http.Response {
		form.Set("csrf", s.CSRF)
		req, _ := http.NewRequest("POST", h.gw.URL+"/panel/avisos", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", h.gw.URL)
		resp, _ := c.Do(req)
		return resp
	}
	form := url.Values{"action": {"save"}, "enabled": {"1"}, "quiet_from": {"22:30"}, "quiet_to": {"07:00"}, "user": {"ana"},
		"chapters": {"1"}, "chapters_mode": {"daily"}, "chapters_at": {"21:00"}, "apps": {"1"}, "apps_minutes": {"10"},
		"network_minutes": {"5"}, "battery": {"1"}, "battery_low": {"30"}, "battery_high": {"0"}, "temp_battery": {"44"},
		"temp_cpu": {"75,5"}, "disk_gb": {"1.5"}}
	if resp := post(form); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("guardar: %d %s", resp.StatusCode, body(t, resp))
	}
	got := h.g.alerts.config()
	if !got.Enabled || got.QuietFrom != "22:30" || !got.Chapters.Daily || got.Chapters.DailyAt != "21:00" || got.Apps.Minutes != 10 ||
		got.Battery.Low != 30 || got.Battery.Unplugged || got.Deploys.Failed || got.Network.On || got.Temp.CPU != 75.5 || got.Disk.MinFreeGB != 1.5 || got.User != "ana" {
		t.Fatalf("configuración guardada: %+v", got)
	}
	page := body(t, func() *http.Response { r, _ := c.Get(h.gw.URL + "/panel/frag/avisos"); return r }())
	if !strings.Contains(page, "Falta configurar el bot") || !strings.Contains(page, `value="22:30"`) {
		t.Fatalf("sección de avisos: %s", page)
	}
	for _, bad := range []url.Values{
		{"action": {"save"}, "quiet_from": {"25:00"}, "chapters_at": {"20:00"}, "apps_minutes": {"5"}, "network_minutes": {"3"}, "battery_low": {"20"}, "battery_high": {"90"}, "temp_battery": {"45"}, "temp_cpu": {"80"}, "disk_gb": {"2"}},
		{"action": {"save"}, "chapters_at": {"20:00"}, "apps_minutes": {"0"}, "network_minutes": {"3"}, "battery_low": {"20"}, "battery_high": {"90"}, "temp_battery": {"45"}, "temp_cpu": {"80"}, "disk_gb": {"2"}},
		{"action": {"save"}, "user": {"nadie"}, "chapters_at": {"20:00"}, "apps_minutes": {"5"}, "network_minutes": {"3"}, "battery_low": {"20"}, "battery_high": {"90"}, "temp_battery": {"45"}, "temp_cpu": {"80"}, "disk_gb": {"2"}},
	} {
		if resp := post(bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("acepta %v: %d", bad, resp.StatusCode)
		}
	}
	// Sin bot, la prueba falla con un mensaje claro.
	if resp := post(url.Values{"action": {"test"}}); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body(t, resp), "bot no está configurado") {
		t.Fatal("prueba sin bot")
	}
}
