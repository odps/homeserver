package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// assistantSummarizer pide el resumen al asistente (que tiene el modelo y sus
// claves); responde solo a peticiones del propio servidor.
func assistantSummarizer(base string) func(context.Context, string) (string, error) {
	client := &http.Client{Timeout: 100 * time.Second}
	return func(ctx context.Context, prompt string) (string, error) {
		body, _ := json.Marshal(map[string]string{"prompt": prompt})
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/interno/resumir", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("el asistente no responde: %w", err)
		}
		defer resp.Body.Close()
		var out struct{ Texto, Error string }
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("asistente: %s %s", resp.Status, out.Error)
		}
		return strings.TrimSpace(out.Texto), nil
	}
}
