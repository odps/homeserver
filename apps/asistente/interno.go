package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

// Peticiones de otras apps del propio servidor (no pasan por la pasarela):
// de momento, que noticias pida un resumen del día con el modelo del
// asistente, sin tener copia de sus claves. Cuenta para el límite diario.

const summarySystem = `Eres un redactor de un resumen de noticias matinal. Escribes en español de España, en texto plano (sin Markdown), con frases claras y neutrales. Usa solo la información que se te da; no inventes nada ni opines.`

// internalOnly rechaza lo que llega por la pasarela (que siempre pone
// X-Forwarded-Prefix) o desde fuera del propio servidor.
func internalOnly(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback() && r.Header.Get("X-Forwarded-Prefix") == ""
}

func (a *agent) internalSummary(w http.ResponseWriter, r *http.Request) {
	if !internalOnly(r) {
		http.Error(w, "solo para las apps del servidor", http.StatusForbidden)
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		http.Error(w, "falta el texto", http.StatusBadRequest)
		return
	}
	text, err := a.summarize(r, in.Prompt)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"texto": text})
}

func (a *agent) summarize(r *http.Request, prompt string) (string, error) {
	ctx := r.Context()
	c := a.cfg()
	if c.BaseURL == "" || c.APIKey == "" {
		return "", errors.New("el asistente no tiene modelo configurado")
	}
	if used, err := a.store.today(ctx); err != nil || used >= c.DailyLimit {
		return "", errors.New("el asistente ha llegado a su límite diario")
	}
	l := a.llm(c)
	if l.model == "" {
		return "", errors.New("el asistente no tiene modelo elegido")
	}
	reply, usage, err := l.chat(ctx, []chatMessage{{Role: "system", Content: summarySystem}, {Role: "user", Content: prompt}}, nil, "auto")
	a.store.addUsage(ctx, usage.PromptTokens, usage.CompletionTokens)
	if err != nil {
		return "", err
	}
	return plainText(l.text(reply)), nil
}
