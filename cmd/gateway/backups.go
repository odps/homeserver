package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"filippo.io/age"
)

// Copias de seguridad para sacar del móvil. Las hace el updater cada noche
// (cmd/updater/offsite.go) y el PC las recoge; aquí se configuran y se ve
// cómo van. La clave privada se genera aquí y se muestra una sola vez: en el
// móvil solo queda la pública.

type backupConfig struct {
	Recipient  string `json:"recipient"`
	Hour       int    `json:"hour"`
	KeepDaily  int    `json:"keep_daily"`
	KeepWeekly int    `json:"keep_weekly"`
}

type backupStatus struct {
	LastTry time.Time `json:"last_try"`
	LastOK  time.Time `json:"last_ok"`
	File    string    `json:"file"`
	Size    int64     `json:"size"`
	Took    string    `json:"took"`
	Error   string    `json:"error"`
}

type backupView struct {
	Config   backupConfig
	Status   backupStatus
	PickedUp time.Time // última vez que el PC las recogió
	Asked    bool      // hay una copia pedida que aún no se ha hecho
	Files    []fileInfo
}

func (g *gateway) backupPath(p ...string) string {
	return filepath.Join(append([]string{g.srvDir}, p...)...)
}

func (g *gateway) backupConfig() backupConfig {
	c := backupConfig{Hour: 4, KeepDaily: 7, KeepWeekly: 4}
	if b, err := os.ReadFile(g.backupPath("state", "copias.json")); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func (g *gateway) backupView() backupView {
	v := backupView{Config: g.backupConfig()}
	if b, err := os.ReadFile(g.backupPath("state", "copias-estado.json")); err == nil {
		json.Unmarshal(b, &v.Status)
	}
	if st, err := os.Stat(g.backupPath("backups", "copias", ".recogida")); err == nil {
		v.PickedUp = st.ModTime()
	}
	_, err := os.Stat(g.backupPath("state", "copia-ahora"))
	v.Asked = err == nil
	files, _ := filepath.Glob(g.backupPath("backups", "copias", "homeserver-*.tar.gz.age"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			v.Files = append(v.Files, fileInfo{Name: filepath.Base(f), Size: humanBytes(st.Size()), Time: st.ModTime()})
		}
	}
	return v
}

func (g *gateway) backupAction(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	c := g.backupConfig()
	path := g.backupPath("state", "copias.json")
	switch r.FormValue("action") {
	case "generate":
		id, err := age.GenerateX25519Identity()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		c.Recipient = id.Recipient().String()
		if err := writeJSONFile(path, c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		g.audit.add("copias_clave", ip, map[string]string{"publica": c.Recipient})
		// Sin redirección: la clave privada solo existe en esta respuesta.
		d := g.base(r)
		for _, fill := range sections {
			fill(g, r, d)
		}
		d.NewBackupKey = id.String()
		d.Flash = "Clave de las copias creada. Guarda la clave privada ahora en tu gestor de contraseñas: no se vuelve a mostrar y sin ella no se pueden abrir las copias."
		w.Header().Set("Cache-Control", "no-store")
		g.render(w, "panel.html", d)
		return
	case "save":
		hour, err1 := strconv.Atoi(r.FormValue("hour"))
		daily, err2 := strconv.Atoi(r.FormValue("keep_daily"))
		weekly, err3 := strconv.Atoi(r.FormValue("keep_weekly"))
		if err := errors.Join(err1, err2, err3); err != nil || hour < 0 || hour > 23 || daily < 1 || daily > 60 || weekly < 0 || weekly > 52 {
			http.Error(w, "hora entre 0 y 23, de 1 a 60 copias diarias y de 0 a 52 semanales", http.StatusBadRequest)
			return
		}
		c.Hour, c.KeepDaily, c.KeepWeekly = hour, daily, weekly
		g.audit.add("copias_config", ip, nil)
	case "now":
		if c.Recipient == "" {
			http.Error(w, "primero crea la clave de las copias", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(g.backupPath("state", "copia-ahora"), nil, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		g.audit.add("copias_ahora", ip, nil)
		g.back(w, r, "copia_pedida", "copias")
		return
	default:
		http.Error(w, "acción desconocida", http.StatusBadRequest)
		return
	}
	if err := writeJSONFile(path, c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	g.back(w, r, "copias", "copias")
}
