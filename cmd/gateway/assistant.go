package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Asistente (bot de Telegram): sus claves se escriben desde el panel en
// ~/srv/data/asistente/claves.env (600), que el asistente relee solo al
// cambiar. Las claves nunca se vuelven a mostrar: solo sus 4 últimos
// caracteres. El token para las apps se crea aquí, en nombre de la cuenta
// elegida, y va directo al fichero.

const (
	assistantApp   = "asistente"
	assistantToken = "asistente" // nombre del token de la pasarela que usa
)

// Proveedores de modelos con API compatible con la de OpenAI.
var assistantProviders = map[string]string{
	"groq":       "https://api.groq.com/openai/v1",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
	"openrouter": "https://openrouter.ai/api/v1",
}

var (
	reTelegramToken = regexp.MustCompile(`^[0-9]{5,15}:[A-Za-z0-9_-]{30,64}$`)
	reTelegramUser  = regexp.MustCompile(`^[0-9]{3,15}$`)
	reModel         = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,120}$`)
	reAPIKey        = regexp.MustCompile(`^[\x21-\x7e]{8,300}$`) // ASCII visible, sin espacios
)

func (g *gateway) assistantPath(name string) string {
	return filepath.Join(g.srvDir, "data", assistantApp, name)
}

// readEnvFile lee un fichero CLAVE=valor (sin comillas ni escapes).
func readEnvFile(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m
}

func writeEnvFile(path string, m map[string]string) error {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Claves del asistente. Las escribe el panel; no editar a mano.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// assistantStatus es lo que el asistente publica en estado.json.
type assistantStatus struct {
	UpdatedAt time.Time `json:"updated_at"`
	Bot       string    `json:"bot"`      // @usuario del bot
	Model     string    `json:"model"`    // modelo en uso
	Models    []string  `json:"models"`   // modelos que ofrece el proveedor
	Tools     []string  `json:"tools"`    // herramientas disponibles
	Problems  []string  `json:"problems"` // configuración incompleta o errores
	Today     int       `json:"today"`    // peticiones al modelo hoy
	Limit     int       `json:"limit"`
	LastChat  time.Time `json:"last_chat"`
}

type assistantView struct {
	Installed bool              // la app está desplegada
	Masked    map[string]string // claves: «…1234» o vacío
	Values    map[string]string // datos no secretos (proveedor, modelo, usuario…)
	Status    *assistantStatus
	Stale     bool // el estado no se actualiza (asistente parado)
	Pending   bool // hay claves nuevas que el asistente aún no ha aplicado
	Providers []string
}

func mask(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "…"
	}
	return "…" + v[len(v)-4:]
}

func (g *gateway) assistantView() assistantView {
	_, installed := g.apps[assistantApp]
	m := readEnvFile(g.assistantPath("claves.env"))
	var saved time.Time
	if st, err := os.Stat(g.assistantPath("claves.env")); err == nil {
		saved = st.ModTime()
	}
	v := assistantView{Installed: installed, Masked: map[string]string{}, Values: map[string]string{}, Providers: []string{"groq", "gemini", "openrouter", "custom"}}
	for _, k := range []string{"TELEGRAM_TOKEN", "LLM_API_KEY", "BRAVE_API_KEY", "APPS_TOKEN"} {
		v.Masked[k] = mask(m[k])
	}
	for _, k := range []string{"TELEGRAM_USER_ID", "LLM_PROVIDER", "LLM_BASE_URL", "LLM_MODEL", "APPS_USER", "DAILY_LIMIT"} {
		v.Values[k] = m[k]
	}
	if v.Values["DAILY_LIMIT"] == "" {
		v.Values["DAILY_LIMIT"] = "200"
	}
	if b, err := os.ReadFile(g.assistantPath("estado.json")); err == nil {
		var s assistantStatus
		if json.Unmarshal(b, &s) == nil {
			v.Status = &s
			// El asistente relee las claves cada pocos segundos: el estado de
			// antes de guardar no vale. Si tarda más de un minuto, algo pasa.
			v.Pending = s.UpdatedAt.Before(saved)
			v.Stale = g.clock().Sub(s.UpdatedAt) > 5*time.Minute || (v.Pending && g.clock().Sub(saved) > time.Minute)
			v.Pending = v.Pending && !v.Stale
		}
	}
	return v
}

// assistantAction guarda o borra la configuración del asistente.
func (g *gateway) assistantAction(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	path := g.assistantPath("claves.env")
	if r.FormValue("action") == "clear" {
		os.Remove(path)
		g.tokens.revoke(assistantToken)
		g.audit.add("asistente_borrado", ip, nil)
		g.back(w, r, "asistente", "asistente")
		return
	}
	m := readEnvFile(path)
	changed, err := applyAssistantForm(m, r.Form)
	if err == nil {
		err = g.assistantAppsToken(m, r.FormValue("apps_user"), &changed)
	}
	if err == nil {
		err = writeEnvFile(path, m)
	}
	if err != nil {
		g.audit.add("asistente_fallido", ip, map[string]string{"error": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Se apunta qué cambió, nunca los valores.
	g.audit.add("asistente_config", ip, map[string]string{"campos": strings.Join(changed, ",")})
	g.back(w, r, "asistente", "asistente")
}

// applyAssistantForm valida el formulario y actualiza m. Los campos secretos
// vacíos no cambian (la casilla «quitar» los borra).
func applyAssistantForm(m map[string]string, f url.Values) ([]string, error) {
	var changed []string
	set := func(k, v string) {
		if m[k] != v {
			m[k] = v
			changed = append(changed, k)
		}
	}
	secret := func(k, field string, re *regexp.Regexp, what string) error {
		v := strings.TrimSpace(f.Get(field))
		if f.Get("clear_"+field) != "" {
			set(k, "")
			return nil
		}
		if v == "" {
			return nil
		}
		if !re.MatchString(v) {
			return fmt.Errorf("%s no tiene el formato esperado", what)
		}
		set(k, v)
		return nil
	}
	if err := secret("TELEGRAM_TOKEN", "telegram_token", reTelegramToken, "el token del bot"); err != nil {
		return nil, err
	}
	if err := secret("LLM_API_KEY", "llm_key", reAPIKey, "la clave del proveedor"); err != nil {
		return nil, err
	}
	if err := secret("BRAVE_API_KEY", "brave_key", reAPIKey, "la clave de Brave"); err != nil {
		return nil, err
	}
	if u := strings.TrimSpace(f.Get("telegram_user")); u == "" || reTelegramUser.MatchString(u) {
		set("TELEGRAM_USER_ID", u)
	} else {
		return nil, errors.New("el ID de Telegram son solo números")
	}
	provider := f.Get("provider")
	base, ok := assistantProviders[provider]
	if provider == "custom" {
		base = strings.TrimRight(strings.TrimSpace(f.Get("llm_base_url")), "/")
		if u, err := url.Parse(base); err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, errors.New("la dirección del proveedor debe ser https://…")
		}
	} else if !ok {
		return nil, errors.New("proveedor no válido")
	}
	set("LLM_PROVIDER", provider)
	set("LLM_BASE_URL", base)
	if model := strings.TrimSpace(f.Get("llm_model")); model == "" || reModel.MatchString(model) {
		set("LLM_MODEL", model)
	} else {
		return nil, errors.New("nombre de modelo no válido")
	}
	limit, err := strconv.Atoi(f.Get("daily_limit"))
	if err != nil || limit < 10 || limit > 5000 {
		return nil, errors.New("el límite diario debe estar entre 10 y 5000")
	}
	set("DAILY_LIMIT", strconv.Itoa(limit))
	return changed, nil
}

// assistantAppsToken crea (o cambia) el token con el que el asistente usa las
// apps en nombre de la cuenta elegida: solo las apps que esa cuenta tiene.
func (g *gateway) assistantAppsToken(m map[string]string, user string, changed *[]string) error {
	if user == m["APPS_USER"] && (user == "" || m["APPS_TOKEN"] != "") {
		return nil
	}
	g.tokens.revoke(assistantToken) // puede no existir
	m["APPS_TOKEN"], m["APPS_USER"], m["APPS"] = "", "", ""
	*changed = append(*changed, "APPS_USER")
	if user == "" {
		return nil
	}
	u, ok := g.users.get(user)
	if !ok || u.Disabled {
		return errors.New("cuenta de las apps desconocida o desactivada")
	}
	var apps []string
	for _, a := range u.Apps {
		if a != assistantApp {
			apps = append(apps, a)
		}
	}
	if len(apps) == 0 {
		return errors.New("esa cuenta no tiene ninguna app")
	}
	tok, err := g.tokens.create(assistantToken, user, apps, 0)
	if err != nil {
		return err
	}
	m["APPS_TOKEN"], m["APPS_USER"], m["APPS"] = tok, user, strings.Join(apps, ",")
	return nil
}
