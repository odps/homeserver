package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// config son las claves y ajustes que escribe el panel en claves.env.
type config struct {
	TelegramToken string
	TelegramUser  int64
	Provider      string
	BaseURL       string
	Model         string
	APIKey        string
	BraveKey      string
	AppsToken     string
	AppsUser      string
	Apps          []string
	DailyLimit    int
}

func loadConfig(path string) (config, time.Time) {
	c := config{DailyLimit: 200}
	st, err := os.Stat(path)
	if err != nil {
		return c, time.Time{}
	}
	f, err := os.Open(path)
	if err != nil {
		return c, time.Time{}
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			m[k] = v
		}
	}
	c.TelegramToken = m["TELEGRAM_TOKEN"]
	c.TelegramUser, _ = strconv.ParseInt(m["TELEGRAM_USER_ID"], 10, 64)
	c.Provider = m["LLM_PROVIDER"]
	c.BaseURL = strings.TrimRight(m["LLM_BASE_URL"], "/")
	c.Model = m["LLM_MODEL"]
	c.APIKey = m["LLM_API_KEY"]
	c.BraveKey = m["BRAVE_API_KEY"]
	c.AppsToken = m["APPS_TOKEN"]
	c.AppsUser = m["APPS_USER"]
	for _, a := range strings.Split(m["APPS"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			c.Apps = append(c.Apps, a)
		}
	}
	c.DailyLimit, _ = strconv.Atoi(m["DAILY_LIMIT"])
	if c.DailyLimit <= 0 {
		c.DailyLimit = 200
	}
	return c, st.ModTime()
}

// problems dice qué falta para que el asistente funcione.
func (c config) problems() []string {
	var p []string
	if c.TelegramToken == "" {
		p = append(p, "Falta el token del bot de Telegram.")
	}
	if c.TelegramUser == 0 {
		p = append(p, "Falta tu ID de Telegram: escribe al bot y te lo dirá.")
	}
	if c.BaseURL == "" || c.APIKey == "" {
		p = append(p, "Falta el proveedor del modelo o su clave.")
	}
	if c.AppsToken == "" {
		p = append(p, "Sin cuenta de las apps: no podrá consultar libros ni cómics.")
	}
	if c.BraveKey == "" {
		p = append(p, "Sin clave de Brave Search: no podrá buscar en internet (sí leer páginas).")
	}
	return p
}
