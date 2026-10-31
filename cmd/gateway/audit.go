package main

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// audit guarda los eventos de seguridad (logins, fallos, tokens, acciones del
// panel) en un JSONL. Al pasar de maxAuditBytes rota a .1, así ocupa como
// mucho el doble.
const maxAuditBytes = 1 << 20

type auditEvent struct {
	Time   time.Time         `json:"time"`
	Event  string            `json:"event"`
	IP     string            `json:"ip,omitempty"`
	Detail map[string]string `json:"detail,omitempty"`
}

type auditLog struct {
	path string
	log  *slog.Logger
	mu   sync.Mutex
}

func (a *auditLog) add(event, ip string, detail map[string]string) {
	e := auditEvent{Time: time.Now().UTC(), Event: event, IP: ip, Detail: detail}
	a.log.Info("auditoría", "evento", event, "ip", ip, "detalle", detail)
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, err := os.Stat(a.path); err == nil && st.Size() > maxAuditBytes {
		os.Rename(a.path, a.path+".1")
	}
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		a.log.Error("auditoría", "err", err)
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(e)
}

// recent devuelve los últimos n eventos, del más nuevo al más antiguo.
func (a *auditLog) recent(n int) []auditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return tailJSONL[auditEvent](a.path, n)
}

// tailJSONL lee las últimas n líneas válidas de un JSONL (ficheros pequeños).
func tailJSONL[T any](path string, n int) []T {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []T
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var v T
		if json.Unmarshal(sc.Bytes(), &v) == nil {
			all = append(all, v)
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all
}
