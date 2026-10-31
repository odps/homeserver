package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/version"
)

// Panel de inicio: apps, móvil, despliegues y seguridad. Cada sección es un
// fragmento HTML que la página refresca sola (web/static/app.js).

type tokenView struct {
	Name     string
	Apps     string
	Created  time.Time
	Expires  *time.Time
	LastUsed *time.Time
}

type panelData struct {
	Version   string
	CSRF      string
	SessionID string
	Flash     string
	NewToken  string // se muestra una sola vez tras crearlo
	Apps      []appStatus
	Services  []serviceStatus
	Phone     phoneStatus
	NetEvents []netEvent
	Deploys   deployStatus
	Sessions  []session
	Tokens    []tokenView
	Audit     []auditEvent
	Recovery  int
	AppNames  []string
}

// sections dice qué datos necesita cada fragmento, para no recoger de más.
var sections = map[string]func(g *gateway, r *http.Request, d *panelData){
	"apps": func(g *gateway, r *http.Request, d *panelData) {
		d.Apps = g.appStatuses(r.Context())
		d.Services = g.services(r.Context())
	},
	"phone": func(g *gateway, r *http.Request, d *panelData) {
		d.Phone = g.phone(r.Context())
		d.NetEvents = g.netEvents()
	},
	"deploys": func(g *gateway, r *http.Request, d *panelData) { d.Deploys = g.deploys() },
	"security": func(g *gateway, r *http.Request, d *panelData) {
		now := g.clock()
		d.Sessions = g.sessions.list(now)
		d.Audit = g.audit.recent(25)
		d.Recovery = g.recovery.remaining()
		tokens, _ := g.tokens.list()
		for name, e := range tokens {
			d.Tokens = append(d.Tokens, tokenView{Name: name, Apps: strings.Join(e.Apps, ", "), Created: e.Created, Expires: e.Expires, LastUsed: e.LastUsed})
		}
		sort.Slice(d.Tokens, func(i, j int) bool { return d.Tokens[i].Name < d.Tokens[j].Name })
		for n := range g.apps {
			d.AppNames = append(d.AppNames, n)
		}
		sort.Strings(d.AppNames)
	},
}

func (g *gateway) base(r *http.Request) *panelData {
	s := sessionFrom(r)
	return &panelData{Version: version.Version, CSRF: s.CSRF, SessionID: s.ID, Flash: flashText(r.URL.Query().Get("ok"))}
}

func (g *gateway) dashboard(w http.ResponseWriter, r *http.Request) {
	d := g.base(r)
	for _, fill := range sections {
		fill(g, r, d)
	}
	g.render(w, "panel.html", d)
}

func (g *gateway) fragment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("section")
	fill, ok := sections[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := g.base(r)
	fill(g, r, d)
	w.Header().Set("Cache-Control", "no-store")
	g.render(w, "sec-"+name, d)
}

var flashes = map[string]string{
	"reiniciado": "Servicio reiniciado.",
	"sesion":     "Sesión cerrada.",
	"sesiones":   "Se han cerrado todas las demás sesiones.",
	"revocado":   "Token revocado.",
	"error":      "No se pudo completar la acción; mira la auditoría.",
}

func flashText(k string) string { return flashes[k] }

func (g *gateway) back(w http.ResponseWriter, r *http.Request, flash, anchor string) {
	http.Redirect(w, r, "/?ok="+flash+"#"+anchor, http.StatusSeeOther)
}

func (g *gateway) restart(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	ip := clientIP(r)
	if err := g.restartService(r.Context(), name); err != nil {
		g.audit.add("reinicio_fallido", ip, map[string]string{"servicio": name, "error": err.Error()})
		g.back(w, r, "error", "apps")
		return
	}
	g.audit.add("reinicio", ip, map[string]string{"servicio": name})
	g.back(w, r, "reiniciado", "apps")
}

func (g *gateway) revokeSession(w http.ResponseWriter, r *http.Request) {
	now, ip := g.clock(), clientIP(r)
	if id := r.FormValue("id"); id != "" {
		g.sessions.revoke(id, now)
		g.audit.add("sesion_cerrada", ip, map[string]string{"sesion": id})
		g.back(w, r, "sesion", "security")
		return
	}
	// «Cerrar las demás»: todas menos la actual.
	current := sessionFrom(r).ID
	for _, s := range g.sessions.list(now) {
		if s.ID != current {
			g.sessions.revoke(s.ID, now)
		}
	}
	g.audit.add("sesiones_cerradas", ip, nil)
	g.back(w, r, "sesiones", "security")
}

func (g *gateway) createToken(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	apps := r.Form["apps"]
	days, _ := strconv.Atoi(r.FormValue("days"))
	for _, a := range apps {
		if _, ok := g.apps[a]; !ok && a != "*" {
			http.Error(w, "app desconocida", http.StatusBadRequest)
			return
		}
	}
	tok, err := g.tokens.create(name, apps, time.Duration(days)*24*time.Hour)
	ip := clientIP(r)
	if err != nil {
		g.audit.add("token_fallido", ip, map[string]string{"token": name, "error": err.Error()})
		g.back(w, r, "error", "security")
		return
	}
	g.audit.add("token_creado", ip, map[string]string{"token": name, "apps": strings.Join(apps, ","), "dias": strconv.Itoa(days)})
	// Sin redirección: el token solo existe en esta respuesta.
	d := g.base(r)
	for _, fill := range sections {
		fill(g, r, d)
	}
	d.NewToken, d.Flash = tok, "Token «"+name+"» creado. Cópialo ahora: no se vuelve a mostrar."
	w.Header().Set("Cache-Control", "no-store")
	g.render(w, "panel.html", d)
}

func (g *gateway) revokeToken(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	ip := clientIP(r)
	if err := g.tokens.revoke(name); err != nil {
		g.back(w, r, "error", "security")
		return
	}
	g.audit.add("token_revocado", ip, map[string]string{"token": name})
	g.back(w, r, "revocado", "security")
}
