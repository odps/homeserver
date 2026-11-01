package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cliente de la API de chat compatible con la de OpenAI (/chat/completions),
// que ofrecen Groq, Gemini, OpenRouter y otros. Así se cambia de proveedor
// desde el panel sin tocar código.

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // texto, o nil junto a tool_calls
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type llmUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type llm struct {
	base, key, model string
	http             *http.Client
}

type badToolCall struct{ msg string }

func (e badToolCall) Error() string { return "llamada a herramienta no válida: " + e.msg }

var errRateLimited = errors.New("el proveedor del modelo está limitando las peticiones; prueba en un rato")

func (l llm) text(m chatMessage) string {
	s, _ := m.Content.(string)
	return s
}

// chat pide la siguiente respuesta. choice es «auto» o «none» (contestar ya,
// sin más herramientas).
func (l llm) chat(ctx context.Context, msgs []chatMessage, tools []toolDef, choice string) (chatMessage, llmUsage, error) {
	req := map[string]any{"model": l.model, "messages": msgs, "max_tokens": 2048, "temperature": 0.3}
	if len(tools) > 0 {
		req["tools"], req["tool_choice"] = tools, choice
	}
	var out struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage llmUsage `json:"usage"`
	}
	for attempt := 0; ; attempt++ {
		err := l.post(ctx, "/chat/completions", req, &out)
		var rl rateLimit
		if errors.As(err, &rl) && attempt == 0 && rl.wait <= 10*time.Second {
			select {
			case <-time.After(rl.wait):
				continue
			case <-ctx.Done():
				return chatMessage{}, llmUsage{}, ctx.Err()
			}
		}
		if errors.As(err, &rl) {
			return chatMessage{}, llmUsage{}, errRateLimited
		}
		if err != nil {
			return chatMessage{}, llmUsage{}, err
		}
		break
	}
	if len(out.Choices) == 0 {
		return chatMessage{}, out.Usage, errors.New("el modelo no ha devuelto respuesta")
	}
	return out.Choices[0].Message, out.Usage, nil
}

type rateLimit struct{ wait time.Duration }

func (r rateLimit) Error() string { return "429" }

func (l llm) post(ctx context.Context, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", l.base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.key)
	return l.do(req, out)
}

func (l llm) do(req *http.Request, out any) error {
	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("el proveedor del modelo no responde: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return rateLimit{time.Duration(max(wait, 1)) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		json.Unmarshal(data, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if len(msg) > 600 {
			msg = msg[:600]
		}
		// El proveedor rechaza una llamada a herramienta mal formada (Groq la
		// valida contra el esquema): el modelo puede corregirla.
		if resp.StatusCode == http.StatusBadRequest && (e.Error.Code == "tool_use_failed" || strings.Contains(strings.ToLower(msg), "tool call")) {
			return badToolCall{msg}
		}
		return fmt.Errorf("el proveedor del modelo respondió %s: %s", resp.Status, msg)
	}
	return json.Unmarshal(data, out)
}

// models lista los modelos que ofrece el proveedor.
func (l llm) models(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", l.base+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+l.key)
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := l.do(req, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, strings.TrimPrefix(m.ID, "models/"))
	}
	sort.Strings(ids)
	return ids, nil
}

// Modelos preferidos por proveedor cuando el panel no fija uno: abiertos o
// gratuitos, buenos usando herramientas. Se elige el primero que exista.
var preferredModels = map[string][]string{
	"groq":       {"gpt-oss-120b", "llama-3.3-70b", "llama-4-maverick", "llama-4-scout", "gpt-oss-20b"},
	"gemini":     {"flash"},
	"openrouter": {"gpt-oss-120b:free", "llama-3.3-70b-instruct:free"},
}

// Familias que no sirven para chatear (voz, imagen, moderación, embeddings…).
var notChat = []string{"whisper", "tts", "guard", "embed", "image", "audio", "live", "vision", "moderation", "lite", "native", "preview-tts"}

func pickModel(provider string, available []string) string {
	usable := func(id string) bool {
		for _, bad := range notChat {
			if strings.Contains(strings.ToLower(id), bad) {
				return false
			}
		}
		return true
	}
	for _, want := range preferredModels[provider] {
		// Entre varios que coinciden (p. ej. versiones de Gemini Flash), el de
		// nombre mayor: suele ser la versión más nueva.
		best := ""
		for _, id := range available {
			if usable(id) && strings.Contains(id, want) && id > best {
				best = id
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}
