package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// Avisos por Telegram, sin IA: comprobaciones programadas que mandan un
// mensaje con el bot del asistente al ID de Telegram de su dueño. Cada tipo se
// activa y se ajusta en el panel (sección Avisos). Las condiciones que duran
// (batería, temperatura, disco, una app caída) avisan una vez al empezar y,
// las que importan, otra al terminar; no se repiten mientras duran.
//
// Configuración: ~/srv/state/avisos.json. Lo que ya se ha avisado:
// ~/srv/state/avisos-estado.json.

type alertsConfig struct {
	Enabled   bool   `json:"enabled"`
	QuietFrom string `json:"quiet_from"` // «23:00»; vacío: sin horas de silencio
	QuietTo   string `json:"quiet_to"`
	User      string `json:"user"`       // cuenta de las apps cuyos cómics se vigilan
	PublicURL string `json:"public_url"` // para enlazar a las apps desde el aviso

	News struct {
		On         bool   `json:"on"`
		At         string `json:"at"`          // hora (Madrid) del mensaje
		PerSection int    `json:"per_section"` // titulares por sección
	} `json:"news"`
	Agenda struct {
		On bool `json:"on"` // recordatorios y resumen de la mañana de la app Agenda
	} `json:"agenda"`
	Cartera struct {
		On bool `json:"on"` // alertas de precios de la app Cartera
	} `json:"cartera"`
	// AppsOff son las apps de noticeApps con los avisos apagados: así una app
	// nueva llega encendida aunque la configuración sea anterior a ella.
	AppsOff  []string `json:"apps_off,omitempty"`
	Chapters struct {
		On        bool   `json:"on"`
		Favorites bool   `json:"favorites"` // solo series favoritas
		Daily     bool   `json:"daily"`     // resumen diario en vez de al momento
		DailyAt   string `json:"daily_at"`
	} `json:"chapters"`
	Deploys struct {
		Failed bool `json:"failed"`
		OK     bool `json:"ok"`
	} `json:"deploys"`
	Network struct {
		On         bool `json:"on"`
		MinMinutes int  `json:"min_minutes"`
	} `json:"network"`
	Battery struct {
		On        bool `json:"on"`
		Low       int  `json:"low"`
		High      int  `json:"high"` // 0: sin aviso
		Unplugged bool `json:"unplugged"`
	} `json:"battery"`
	Temp struct {
		On      bool    `json:"on"`
		Battery float64 `json:"battery"`
		CPU     float64 `json:"cpu"`
	} `json:"temp"`
	Apps struct {
		On      bool `json:"on"`
		Minutes int  `json:"minutes"`
	} `json:"apps"`
	Disk struct {
		On        bool    `json:"on"`
		MinFreeGB float64 `json:"min_free_gb"`
	} `json:"disk"`
	Backups struct {
		Failed bool `json:"failed"`
		PCDays int  `json:"pc_days"` // 0: sin aviso
	} `json:"backups"`
}

func defaultAlerts() alertsConfig {
	var c alertsConfig
	c.Enabled = true
	c.QuietFrom, c.QuietTo = "23:00", "08:00"
	c.Chapters.On, c.Chapters.DailyAt = true, "20:00"
	c.News.On, c.News.At, c.News.PerSection = true, "07:30", 3
	c.Agenda.On = true
	c.Cartera.On = true
	c.Deploys.Failed = true
	c.Network.On, c.Network.MinMinutes = true, 3
	c.Battery.On, c.Battery.Low, c.Battery.High, c.Battery.Unplugged = true, 25, 90, true
	c.Temp.On, c.Temp.Battery, c.Temp.CPU = true, 45, 80
	c.Apps.On, c.Apps.Minutes = true, 5
	c.Disk.On, c.Disk.MinFreeGB = true, 2
	c.Backups.Failed, c.Backups.PCDays = true, 3
	return c
}

type sentAlert struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Text   string    `json:"text"`
	Error  string    `json:"error,omitempty"`
	Silent bool      `json:"silent,omitempty"`
}

type alertsState struct {
	Init      bool                 `json:"init"`
	Active    map[string]bool      `json:"active"`     // condiciones avisadas que siguen activas
	Since     map[string]time.Time `json:"since"`      // desde cuándo falla (apps)
	DeploysAt time.Time            `json:"deploys_at"` // último evento de despliegue visto
	NetAt     time.Time            `json:"net_at"`
	Chapters  map[string]float32   `json:"chapters"` // serie → último capítulo avisado
	Pending   []string             `json:"pending"`  // capítulos para el resumen diario
	DailySent string               `json:"daily_sent"`
	NewsSent  string               `json:"news_sent"` // día del último digest de noticias enviado
	Log       []sentAlert          `json:"log"`       // últimos avisos, para el panel
	Outbox    []sentAlert          `json:"outbox"`    // avisos que no se pudieron enviar
}

type alerter struct {
	mu          sync.Mutex
	configPath  string
	statePath   string
	keysPath    string // claves del asistente (token del bot e ID de su dueño)
	telegramAPI string // https://api.telegram.org (sustituible en los tests)
	http        *http.Client
	chaptersAt  time.Time
	phone       func(context.Context) phoneStatus // nil: el del móvil (sustituible en los tests)
}

func newAlerter(srvDir string) *alerter {
	return &alerter{
		configPath:  filepath.Join(srvDir, "state", "avisos.json"),
		statePath:   filepath.Join(srvDir, "state", "avisos-estado.json"),
		keysPath:    filepath.Join(srvDir, "data", assistantApp, "claves.env"),
		telegramAPI: "https://api.telegram.org",
		http:        &http.Client{Timeout: 20 * time.Second},
	}
}

func (a *alerter) config() alertsConfig {
	c := defaultAlerts()
	if b, err := os.ReadFile(a.configPath); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func (a *alerter) saveConfig(c alertsConfig) error {
	return writeJSONFile(a.configPath, c)
}

func (a *alerter) state() alertsState {
	var s alertsState
	if b, err := os.ReadFile(a.statePath); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.Active == nil {
		s.Active = map[string]bool{}
	}
	if s.Since == nil {
		s.Since = map[string]time.Time{}
	}
	return s
}

func (a *alerter) readPhone(ctx context.Context, g *gateway) phoneStatus {
	if a.phone != nil {
		return a.phone(ctx)
	}
	return g.phone(ctx)
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runAlerts comprueba cada minuto; los cómics, cada 10 (libros los mira cada 30).
func (g *gateway) runAlerts(ctx context.Context) {
	if g.alerts == nil {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			g.checkAlerts(ctx, g.clock())
		}
	}
}

// checkAlerts hace una ronda de comprobaciones y envía lo que toque.
func (g *gateway) checkAlerts(ctx context.Context, now time.Time) {
	a := g.alerts
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.config()
	if c.User == "" {
		c.User = readEnvFile(a.keysPath)["APPS_USER"] // por defecto, la cuenta del asistente
	}
	s := a.state()
	var out []sentAlert
	add := func(kind, text string) { out = append(out, sentAlert{Time: now, Kind: kind, Text: text}) }
	if !s.Init {
		// La primera vez solo se marca por dónde van los eventos: nada de
		// avisar de lo que pasó antes.
		s.Init, s.DeploysAt, s.NetAt = true, now, now
	}

	// Con los avisos apagados se sigue leyendo lo nuevo, para no soltar
	// avisos viejos al encenderlos.
	g.alertEvents(c, &s, add)
	if c.Enabled {
		g.alertPhone(ctx, c, &s, add)
		g.alertApps(ctx, c, &s, now, add)
		g.alertBackups(c, &s, now, add)
		g.alertNews(ctx, c, &s, now, add)
		g.alertAgenda(ctx, c, add)
		g.alertCartera(ctx, c, add)
		for _, a := range noticeApps {
			if c.appOn(a.Name) {
				g.alertApp(ctx, c, a.Name, linkTo(c, a.Name), add)
			}
		}
		if now.Sub(a.chaptersAt) >= 10*time.Minute {
			a.chaptersAt = now
			g.alertChapters(ctx, c, &s, add)
		}
		if c.Chapters.On && c.Chapters.Daily && len(s.Pending) > 0 {
			day := now.In(madrid).Format("2006-01-02")
			if s.DailySent != day && now.In(madrid).Format("15:04") >= c.Chapters.DailyAt {
				add("capitulos", "Capítulos nuevos de hoy:\n"+strings.Join(s.Pending, "\n"))
				s.Pending, s.DailySent = nil, day
			}
		}
	}
	// Los que fallaron (sin red, Telegram caído) se reintentan durante 2 horas.
	var retry []sentAlert
	for _, al := range s.Outbox {
		if now.Sub(al.Time) > 2*time.Hour {
			continue
		}
		if err := a.send(ctx, al.Text, quiet(c, now), headings(al)...); err != nil {
			retry = append(retry, al)
			continue
		}
		for i := range s.Log {
			if s.Log[i].Time.Equal(al.Time) && s.Log[i].Text == al.Text {
				s.Log[i].Error = ""
			}
		}
	}
	s.Outbox = retry
	silent := quiet(c, now)
	for _, al := range out {
		al.Silent = silent
		if err := a.send(ctx, al.Text, silent, headings(al)...); err != nil {
			al.Error = err.Error()
			g.log.Warn("aviso", "tipo", al.Kind, "err", err)
			s.Outbox = append(s.Outbox, al)
		}
		s.Log = append(s.Log, al)
	}
	if len(s.Log) > 30 {
		s.Log = s.Log[len(s.Log)-30:]
	}
	if err := writeJSONFile(a.statePath, s); err != nil {
		g.log.Warn("estado de los avisos", "err", err)
	}
}

// alertEvents lee los eventos nuevos del updater y de netwatch.
func (g *gateway) alertEvents(c alertsConfig, s *alertsState, add func(kind, text string)) {
	for _, e := range tailJSONL[deployEvent](filepath.Join(g.srvDir, "state", "deploys.jsonl"), 50) {
		if !e.Time.After(s.DeploysAt) {
			continue
		}
		s.DeploysAt = e.Time
		if !c.Enabled {
			continue
		}
		switch {
		case e.Event == "descartada" && c.Deploys.Failed:
			add("despliegue", fmt.Sprintf("Despliegue fallido: la versión %s no arrancó bien y se ha vuelto a %s.\n%s", e.Tag, orDash(e.Prev), truncateRunes(e.Detail, 300)))
		case e.Event == "desplegada" && c.Deploys.OK:
			add("despliegue", "Desplegada la versión "+e.Tag+".")
		}
	}
	restarts := 0
	for _, e := range tailJSONL[netEvent](filepath.Join(g.srvDir, "state", "netwatch.jsonl"), 50) {
		if !e.Time.After(s.NetAt) {
			continue
		}
		s.NetAt = e.Time
		if !c.Enabled || !c.Network.On {
			continue
		}
		switch e.Event {
		case "wifi_reiniciada":
			restarts++
		case "recuperada":
			secs, _ := e.Detail["corte_s"].(float64)
			if secs >= float64(c.Network.MinMinutes*60) {
				from := e.Time.Add(-time.Duration(secs) * time.Second).In(madrid)
				text := fmt.Sprintf("El servidor estuvo sin red %s (de %s a %s).", humanDuration(time.Duration(secs)*time.Second), from.Format("15:04"), e.Time.In(madrid).Format("15:04"))
				if restarts > 0 {
					text += fmt.Sprintf(" Se reinició la wifi %d %s.", restarts, plural(restarts, "vez", "veces"))
				}
				add("red", text)
			}
			restarts = 0
		}
	}
}

// alertPhone vigila batería, temperatura y disco, con margen para no avisar
// una y otra vez cuando el valor baila alrededor del umbral.
func (g *gateway) alertPhone(ctx context.Context, c alertsConfig, s *alertsState, add func(kind, text string)) {
	p := g.alerts.readPhone(ctx, g)
	cond := func(key string, on, enter, leave bool, start, end string) {
		s.cond(key, on, enter, leave, start, end, add)
	}
	if p.HasBattery {
		b := c.Battery
		cond("bateria_baja", b.On && b.Low > 0, p.BatteryPct <= b.Low, p.BatteryPct >= b.Low+5,
			fmt.Sprintf("Batería baja: %d %% (%s).", p.BatteryPct, strings.ToLower(p.BatteryStatus)), "")
		cond("bateria_alta", b.On && b.High > 0, p.BatteryPct >= b.High, p.BatteryPct <= b.High-5,
			fmt.Sprintf("La batería está al %d %%: revisa el límite de carga (Ajustes → Batería → Control de carga).", p.BatteryPct), "")
		cond("desenchufado", b.On && b.Unplugged, !p.Plugged, p.Plugged,
			fmt.Sprintf("El móvil no está cargando (batería al %d %%). ¿Se ha ido la luz o se ha soltado el cable?", p.BatteryPct),
			fmt.Sprintf("El móvil vuelve a cargar (batería al %d %%).", p.BatteryPct))
		t := c.Temp
		cond("temp_bateria", t.On && t.Battery > 0, p.BatteryTemp >= t.Battery, p.BatteryTemp <= t.Battery-3,
			fmt.Sprintf("La batería está a %.0f °C.", p.BatteryTemp), fmt.Sprintf("La batería ha bajado a %.0f °C.", p.BatteryTemp))
	}
	if p.CPUTemp > 0 {
		t := c.Temp
		cond("temp_cpu", t.On && t.CPU > 0, p.CPUTemp >= t.CPU, p.CPUTemp <= t.CPU-5,
			fmt.Sprintf("El procesador está a %.0f °C.", p.CPUTemp), fmt.Sprintf("El procesador ha bajado a %.0f °C.", p.CPUTemp))
	}
	if p.DiskFreeGB > 0 || p.DiskUsedPct > 0 {
		d := c.Disk
		cond("disco", d.On && d.MinFreeGB > 0, p.DiskFreeGB < d.MinFreeGB, p.DiskFreeGB >= d.MinFreeGB*1.2,
			fmt.Sprintf("Queda poco espacio: %.1f GB libres.", p.DiskFreeGB), "")
	}
}

// cond avisa al empezar una condición (start) y, si end no está vacío, al
// acabar; entre medias no repite.
func (s *alertsState) cond(key string, on, enter, leave bool, start, end string, add func(kind, text string)) {
	switch {
	case !on:
		delete(s.Active, key)
	case !s.Active[key] && enter:
		s.Active[key] = true
		add(key, start)
	case s.Active[key] && leave:
		delete(s.Active, key)
		if end != "" {
			add(key, end)
		}
	}
}

// alertBackups avisa si falla la copia nocturna o si el PC lleva días sin
// recogerlas.
func (g *gateway) alertBackups(c alertsConfig, s *alertsState, now time.Time, add func(kind, text string)) {
	b := g.backupView()
	on := b.Config.Recipient != ""
	s.cond("copia_fallida", on && c.Backups.Failed, b.Status.Error != "", b.Status.Error == "",
		"Ha fallado la copia de seguridad: "+truncateRunes(b.Status.Error, 300), "Las copias de seguridad vuelven a funcionar.", add)
	ref := b.PickedUp
	if ref.IsZero() && len(b.Files) > 0 {
		ref = b.Files[len(b.Files)-1].Time // la más antigua sin recoger
	}
	late := !ref.IsZero() && now.Sub(ref) > time.Duration(c.Backups.PCDays)*24*time.Hour
	last := "nunca"
	if !b.PickedUp.IsZero() {
		last = "el " + b.PickedUp.In(madrid).Format("02/01 a las 15:04")
	}
	s.cond("copias_pc", on && c.Backups.PCDays > 0, late, !late,
		fmt.Sprintf("El PC lleva más de %d %s sin recoger las copias de seguridad (última vez: %s). Enciéndelo con Tailscale activo.", c.Backups.PCDays, plural(c.Backups.PCDays, "día", "días"), last),
		"El PC ha vuelto a recoger las copias de seguridad.", add)
}

// alertNews manda una vez al día, a su hora, los titulares del digest de
// noticias de la cuenta elegida (si tiene la app). Si la app aún no lo tiene
// listo, se reintenta cada minuto hasta mediodía.
func (g *gateway) alertNews(ctx context.Context, c alertsConfig, s *alertsState, now time.Time, add func(kind, text string)) {
	local := now.In(madrid)
	day, hm := local.Format("2006-01-02"), local.Format("15:04")
	if !c.News.On || c.User == "" || s.NewsSent == day || hm < c.News.At || hm >= "12:00" {
		return
	}
	app, ok := g.apps["noticias"]
	u, okUser := g.users.get(c.User)
	if !ok || !okUser || !u.allows("noticias") {
		return
	}
	per := c.News.PerSection
	if per <= 0 {
		per = 3
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/digest/texto?por_seccion=%d", app.Port, per), nil)
	setAuthHeaders(req, c.User, u.Name, u.Role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	text := strings.TrimSpace(string(b))
	if resp.StatusCode != http.StatusOK || text == "" {
		return
	}
	s.NewsSent = day
	// Telegram admite unos 4000 caracteres por mensaje: el resto, en la app.
	if r := []rune(text); len(r) > 3600 {
		text = strings.TrimSpace(string(r[:3600])) + "\n…"
	}
	if c.PublicURL != "" {
		text += "\n\nEl digest completo: " + c.PublicURL + "/noticias/"
	}
	add("noticias", "Buenos días. Tus noticias de hoy:\n\n"+text)
}

// alertAgenda recoge lo que la app Agenda tiene preparado para la cuenta
// elegida: los recordatorios de eventos y tareas y el resumen de la mañana
// (la hora y el contenido se ajustan en la propia app).
func (g *gateway) alertAgenda(ctx context.Context, c alertsConfig, add func(kind, text string)) {
	if !c.Agenda.On {
		return
	}
	g.alertApp(ctx, c, "agenda", func(n appNotice) string {
		if n.Kind == "resumen" && c.PublicURL != "" {
			return n.Text + "\n\nTu agenda: " + c.PublicURL + "/agenda/"
		}
		return n.Text
	}, add)
}

// alertCartera recoge las alertas de precios que han saltado en la app
// Cartera (se crean en la propia app o con Talia), con el enlace a ella.
func (g *gateway) alertCartera(ctx context.Context, c alertsConfig, add func(kind, text string)) {
	if !c.Cartera.On {
		return
	}
	g.alertApp(ctx, c, "cartera", func(n appNotice) string {
		if c.PublicURL != "" {
			return n.Text + "\n" + c.PublicURL + "/cartera/"
		}
		return n.Text
	}, add)
}

// noticeApp es una app cuyos avisos (GET /api/avisos) se reenvían tal cual,
// con el enlace a ella, y se encienden o apagan en Avisos.
type noticeApp struct {
	Name, Title, Desc string
}

var noticeApps = []noticeApp{
	{"gastos", "Gastos", "Cobros de suscripciones que se acercan y presupuestos superados"},
	{"casa", "Casa", "Mantenimiento de la casa y, si lo activas en la app, el precio de la luz"},
	{"coche", "Coche", "Mantenimiento del coche por fecha o por kilómetros"},
	{"habitos", "Hábitos", "Recordatorios de los hábitos que aún no has hecho"},
	{"leer", "Leer después", "Recordatorios de lo que tienes pendiente de leer, si los activas en la app"},
	{"series", "Series y pelis", "Episodios nuevos y estrenos de temporada de las series que sigues"},
}

// appOn dice si los avisos de una app de noticeApps están encendidos.
func (c alertsConfig) appOn(name string) bool {
	for _, n := range c.AppsOff {
		if n == name {
			return false
		}
	}
	return true
}

// linkTo añade al aviso el enlace a la app.
func linkTo(c alertsConfig, name string) func(appNotice) string {
	return func(n appNotice) string {
		if c.PublicURL != "" {
			return n.Text + "\n" + c.PublicURL + "/" + name + "/"
		}
		return n.Text
	}
}

// appNotice es un aviso que una app tiene preparado en GET /api/avisos.
type appNotice struct {
	Kind string `json:"tipo"`
	Text string `json:"texto"`
}

// alertApp recoge los avisos pendientes de una app para la cuenta elegida
// (la app los da por entregados al devolverlos); text pone el texto final.
func (g *gateway) alertApp(ctx context.Context, c alertsConfig, name string, text func(appNotice) string, add func(kind, text string)) {
	app, ok := g.apps[name]
	if !ok || c.User == "" {
		return
	}
	u, ok := g.users.get(c.User)
	if !ok || !u.allows(name) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/avisos", app.Port), nil)
	setAuthHeaders(req, c.User, u.Name, u.Role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var list []appNotice
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&list) != nil {
		return
	}
	for _, n := range list {
		// Telegram admite unos 4000 caracteres por mensaje.
		if r := []rune(n.Text); len(r) > 3800 {
			n.Text = strings.TrimSpace(string(r[:3800])) + "\n…"
		}
		add(name, text(n))
	}
}

// alertApps avisa de una app que lleva un rato sin responder, y cuando vuelve.
func (g *gateway) alertApps(ctx context.Context, c alertsConfig, s *alertsState, now time.Time, add func(kind, text string)) {
	for _, st := range g.appStatuses(ctx) {
		key := "app:" + st.Name
		if st.OK || !c.Apps.On {
			if s.Active[key] && c.Apps.On {
				add("app", fmt.Sprintf("La app %s vuelve a funcionar.", st.Name))
			}
			delete(s.Active, key)
			delete(s.Since, key)
			continue
		}
		since, ok := s.Since[key]
		if !ok {
			s.Since[key] = now
			since = now
		}
		if !s.Active[key] && now.Sub(since) >= time.Duration(c.Apps.Minutes)*time.Minute {
			s.Active[key] = true
			add("app", fmt.Sprintf("La app %s lleva %s sin funcionar: %s.", st.Name, humanDuration(now.Sub(since)), st.Error))
		}
	}
}

// alertChapters pregunta a libros por las series que sigue la cuenta elegida.
// Una serie que aparece por primera vez solo se apunta.
func (g *gateway) alertChapters(ctx context.Context, c alertsConfig, s *alertsState, add func(kind, text string)) {
	app, ok := g.apps["libros"]
	if !ok || !c.Chapters.On || c.User == "" {
		return
	}
	u, ok := g.users.get(c.User)
	if !ok || !u.allows("libros") {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/series", app.Port), nil)
	setAuthHeaders(req, c.User, u.Name, u.Role)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		g.log.Warn("avisos: series", "err", err)
		return
	}
	defer resp.Body.Close()
	var list []struct {
		ID           int64    `json:"id"`
		Title        string   `json:"title"`
		LatestNumber *float32 `json:"latest_number"`
		Favorite     bool     `json:"favorite"`
		Unread       int      `json:"unread"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&list) != nil {
		return
	}
	if s.Chapters == nil {
		s.Chapters = map[string]float32{}
	}
	for _, sr := range list {
		if sr.LatestNumber == nil {
			continue
		}
		key := fmt.Sprint(sr.ID)
		prev, seen := s.Chapters[key]
		s.Chapters[key] = *sr.LatestNumber
		if !seen || *sr.LatestNumber <= prev || (c.Chapters.Favorites && !sr.Favorite) {
			continue
		}
		line := fmt.Sprintf("%s: capítulo %s", sr.Title, chapterLabel(*sr.LatestNumber))
		if sr.Unread > 1 {
			line += fmt.Sprintf(" (%d sin leer)", sr.Unread)
		}
		if c.Chapters.Daily {
			s.Pending = append(s.Pending, "• "+line)
			continue
		}
		text := "Nuevo " + line
		if c.PublicURL != "" {
			text += "\n" + c.PublicURL + "/libros/#/comics/" + key
		}
		add("capitulos", text)
	}
}

func chapterLabel(n float32) string {
	if n == float32(math.Trunc(float64(n))) {
		return fmt.Sprint(int(n))
	}
	return fmt.Sprintf("%g", n)
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "la anterior"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// quiet dice si ahora es hora de silencio (los avisos llegan sin sonido).
func quiet(c alertsConfig, now time.Time) bool {
	if c.QuietFrom == "" || c.QuietTo == "" || c.QuietFrom == c.QuietTo {
		return false
	}
	t := now.In(madrid).Format("15:04")
	if c.QuietFrom < c.QuietTo {
		return t >= c.QuietFrom && t < c.QuietTo
	}
	return t >= c.QuietFrom || t < c.QuietTo // cruza la medianoche
}

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

var errNoBot = errors.New("el bot no está configurado (sección Asistente)")

// send manda el aviso con el bot del asistente a su dueño.
// tgEntity es un trozo con formato en un mensaje de Telegram: así no hace
// falta parse_mode ni escapar nada. Las posiciones van en unidades UTF-16.
type tgEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// headings pone en negrita los títulos del resumen de la mañana de la Agenda:
// la primera línea y cada una que va tras una línea en blanco. Los demás
// avisos (de un solo bloque) van sin formato.
func headings(al sentAlert) []tgEntity {
	if al.Kind != "agenda" || !strings.Contains(al.Text, "\n\n") {
		return nil
	}
	var out []tgEntity
	pos, prevBlank := 0, true
	for _, line := range strings.Split(al.Text, "\n") {
		n := len(utf16.Encode([]rune(line)))
		if prevBlank && strings.TrimSpace(line) != "" && !strings.Contains(line, "://") { // el enlace a la app, sin negrita
			out = append(out, tgEntity{Type: "bold", Offset: pos, Length: n})
		}
		prevBlank = strings.TrimSpace(line) == ""
		pos += n + 1
	}
	return out
}

func (a *alerter) send(ctx context.Context, text string, silent bool, entities ...tgEntity) error {
	m := readEnvFile(a.keysPath)
	token, chat := m["TELEGRAM_TOKEN"], m["TELEGRAM_USER_ID"]
	if token == "" || chat == "" {
		return errNoBot
	}
	msg := map[string]any{"chat_id": chat, "text": text, "disable_notification": silent,
		"link_preview_options": map[string]bool{"is_disabled": true}}
	if len(entities) > 0 {
		msg["entities"] = entities
	}
	body, _ := json.Marshal(msg)
	req, _ := http.NewRequestWithContext(ctx, "POST", a.telegramAPI+"/bot"+token+"/sendMessage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		// El error de red lleva la URL, y la URL el token: no se muestra.
		return errors.New("Telegram no responde")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("Telegram respondió %d: %s", resp.StatusCode, e.Description)
	}
	return nil
}

// recentAlerts devuelve los últimos avisos, el más reciente primero.
func (a *alerter) recentAlerts(n int) []sentAlert {
	s := a.state()
	out := append([]sentAlert(nil), s.Log...)
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
