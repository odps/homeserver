package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Sección «Avisos» del panel: qué se avisa, con qué umbrales y cuándo en
// silencio.

type alertsView struct {
	Config alertsConfig
	Bot    bool // hay token del bot e ID de su dueño
	Recent []sentAlert
}

func (g *gateway) alertsView() alertsView {
	if g.alerts == nil {
		return alertsView{Config: defaultAlerts()}
	}
	m := readEnvFile(g.alerts.keysPath)
	v := alertsView{Config: g.alerts.config(), Bot: m["TELEGRAM_TOKEN"] != "" && m["TELEGRAM_USER_ID"] != "", Recent: g.alerts.recentAlerts(8)}
	if v.Config.User == "" {
		v.Config.User = m["APPS_USER"] // por defecto, la cuenta del asistente
	}
	return v
}

var reClock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func (g *gateway) alertsAction(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if r.FormValue("action") == "test" {
		err := g.alerts.send(r.Context(), "Aviso de prueba del homeserver: si lees esto, los avisos llegan bien.", false)
		if err != nil {
			http.Error(w, "No se pudo enviar: "+err.Error(), http.StatusBadGateway)
			return
		}
		g.back(w, r, "aviso_prueba", "avisos")
		return
	}
	g.alerts.mu.Lock()
	c := g.alerts.config()
	err := applyAlertsForm(&c, r)
	if err == nil && c.User != "" {
		if _, ok := g.users.get(c.User); !ok {
			err = errors.New("cuenta de las apps desconocida")
		}
	}
	if err == nil {
		c.PublicURL = "https://" + r.Host
		err = g.alerts.saveConfig(c)
	}
	g.alerts.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.audit.add("avisos_config", ip, nil)
	g.back(w, r, "avisos", "avisos")
}

// applyAlertsForm valida el formulario. Las casillas sin marcar no llegan:
// cuentan como apagadas.
func applyAlertsForm(c *alertsConfig, r *http.Request) error {
	on := func(name string) bool { return r.FormValue(name) != "" }
	var errs []error
	get := func(name, label string, min, max float64) float64 {
		v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(r.FormValue(name)), ",", "."), 64)
		if err != nil || v < min || v > max {
			errs = append(errs, fmt.Errorf("%s: entre %g y %g", label, min, max))
		}
		return v
	}
	getClock := func(name, label string, empty bool) string {
		v := strings.TrimSpace(r.FormValue(name))
		if !(v == "" && empty) && !reClock.MatchString(v) {
			errs = append(errs, fmt.Errorf("%s: una hora como 23:00", label))
		}
		return v
	}
	c.Enabled = on("enabled")
	c.QuietFrom, c.QuietTo = getClock("quiet_from", "Silencio desde", true), getClock("quiet_to", "Silencio hasta", true)
	c.User = strings.TrimSpace(r.FormValue("user"))
	c.Chapters.On, c.Chapters.Favorites, c.Chapters.Daily = on("chapters"), on("chapters_favorites"), r.FormValue("chapters_mode") == "daily"
	c.Chapters.DailyAt = getClock("chapters_at", "Hora del resumen", false)
	c.Deploys.Failed, c.Deploys.OK = on("deploys_failed"), on("deploys_ok")
	c.Network.On, c.Network.MinMinutes = on("network"), int(get("network_minutes", "Minutos sin red", 0, 600))
	c.Battery.On, c.Battery.Unplugged = on("battery"), on("battery_unplugged")
	c.Battery.Low, c.Battery.High = int(get("battery_low", "Batería baja", 0, 90)), int(get("battery_high", "Batería alta", 0, 100))
	c.Temp.On, c.Temp.Battery, c.Temp.CPU = on("temp"), get("temp_battery", "Temperatura de la batería", 0, 80), get("temp_cpu", "Temperatura del procesador", 0, 110)
	c.Apps.On, c.Apps.Minutes = on("apps"), int(get("apps_minutes", "Minutos con una app caída", 1, 600))
	c.Disk.On, c.Disk.MinFreeGB = on("disk"), get("disk_gb", "Espacio libre mínimo", 0, 500)
	return errors.Join(errs...)
}
