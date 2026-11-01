package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/dbtest"
)

// fakeTelegram guarda lo que el bot envía.
type fakeTelegram struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeTelegram) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botTOKEN/") {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch strings.TrimPrefix(r.URL.Path, "/botTOKEN/") {
		case "getMe":
			w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"mibot"}}`))
		case "sendMessage":
			f.mu.Lock()
			f.sent = append(f.sent, body["text"].(string))
			f.mu.Unlock()
			w.Write([]byte(`{"ok":true,"result":{}}`))
		default:
			w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeTelegram) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

// fakeLLM imita la API de chat: pide una herramienta según la pregunta y, con
// su resultado, contesta.
func fakeLLM(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gsk_prueba" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"whisper-large-v3"},{"id":"openai/gpt-oss-120b"},{"id":"llama-3.1-8b-instant"}]}`))
			return
		}
		var req struct {
			Model    string        `json:"model"`
			Messages []chatMessage `json:"messages"`
			Tools    []toolDef     `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1]
		reply := map[string]any{"role": "assistant"}
		call := func(name, args string) {
			reply["content"] = nil
			reply["tool_calls"] = []map[string]any{{"id": "c1", "type": "function", "function": map[string]string{"name": name, "arguments": args}}}
		}
		text, _ := last.Content.(string)
		switch {
		case last.Role == "tool":
			reply["content"] = "Según tu biblioteca: " + text
		case strings.Contains(text, "leyendo"):
			call("libros_leyendo", "{}")
		case strings.Contains(text, "apunta"):
			call("notas_guardar", `{"texto":"comprar pan"}`)
		default:
			reply["content"] = "**Hola**, ¿qué tal? Recuerdo " + itoa(len(req.Messages)) + " mensajes."
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": reply, "finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 20}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// fakeGateway sirve una app «libros» con una tool MCP, solo con el token.
func fakeGateway(t *testing.T) *httptest.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "libros", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "libros_leyendo", Description: "Libros que se están leyendo"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"libros": []string{"Crimen y castigo"}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hs_token" {
			http.Error(w, "no autorizado", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/libros/mcp" {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestAgent(t *testing.T) (*agent, *fakeTelegram, config) {
	t.Helper()
	pool := dbtest.Migrated(t, migrations)
	dir := t.TempDir()
	tg := &fakeTelegram{}
	tgSrv, llmSrv, gw := tg.server(t), fakeLLM(t), fakeGateway(t)
	a := newAgent(store{pool}, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(dir, "claves.env"), filepath.Join(dir, "estado.json"), gw.URL)
	a.telegramAt = tgSrv.URL
	c := config{TelegramToken: "TOKEN", TelegramUser: 42, Provider: "groq", BaseURL: llmSrv.URL, APIKey: "gsk_prueba",
		AppsToken: "hs_token", AppsUser: "ana", Apps: []string{"libros", "hola"}, DailyLimit: 50}
	a.config.Store(&c)
	return a, tg, c
}

func msg(from int64, text string) *tgMessage {
	m := &tgMessage{From: tgUser{ID: from}, Text: text}
	m.Chat.ID, m.Chat.Type = from, "private"
	return m
}

func TestAgentConversation(t *testing.T) {
	a, tgf, c := newTestAgent(t)
	ctx := context.Background()
	a.refresh(ctx, true)
	if a.model != "openai/gpt-oss-120b" || a.bot != "mibot" {
		t.Fatalf("modelo o bot: %q %q", a.model, a.bot)
	}
	var names []string
	for _, tl := range a.tools {
		names = append(names, tl.def.Function.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "libros_leyendo") || !strings.Contains(got, "notas_guardar") || !strings.Contains(got, "web_leer") || strings.Contains(got, "web_buscar") {
		t.Fatalf("herramientas: %s", got)
	}
	// «hola» no tiene MCP: es un problema, pero no impide usar libros.
	if p := strings.Join(a.problems, " "); !strings.Contains(p, "hola") {
		t.Fatalf("problemas: %v", a.problems)
	}
	tg := a.tg(c)

	// Alguien que no es el dueño: el bot es privado y no usa el modelo.
	a.handle(ctx, tg, c, msg(7, "hola"))
	if !strings.Contains(tgf.last(), "privado") {
		t.Fatalf("a un desconocido: %q", tgf.last())
	}
	n := len(tgf.sent)
	a.handle(ctx, tg, c, msg(7, "¿hola?"))
	if len(tgf.sent) != n {
		t.Fatal("se responde a un desconocido más de una vez por hora")
	}

	// Pregunta por la biblioteca: herramienta MCP de libros a través de la pasarela.
	a.handle(ctx, tg, c, msg(42, "¿qué estoy leyendo?"))
	if !strings.Contains(tgf.last(), "Crimen y castigo") {
		t.Fatalf("respuesta con herramienta: %q", tgf.last())
	}
	// Nota con herramienta y orden /notas (sin modelo).
	a.handle(ctx, tg, c, msg(42, "apunta que tengo que comprar pan"))
	a.handle(ctx, tg, c, msg(42, "/notas"))
	if !strings.Contains(tgf.last(), "comprar pan") {
		t.Fatalf("/notas: %q", tgf.last())
	}
	// La conversación se recuerda (sistema + 2 turnos por pregunta + la nueva) y /nuevo la olvida.
	a.handle(ctx, tg, c, msg(42, "hola"))
	if !strings.Contains(tgf.last(), "Recuerdo 6 mensajes") || !strings.HasPrefix(tgf.last(), "Hola, ") {
		t.Fatalf("historial o formato: %q", tgf.last())
	}
	a.handle(ctx, tg, c, msg(42, "/nuevo"))
	a.handle(ctx, tg, c, msg(42, "hola"))
	if !strings.Contains(tgf.last(), "Recuerdo 2 mensajes") {
		t.Fatalf("tras /nuevo: %q", tgf.last())
	}
	// Uso: 2 peticiones por pregunta con herramienta, 1 por las demás.
	if u, _ := a.store.usageToday(ctx); u.Requests != 6 || u.PromptTokens != 600 {
		t.Fatalf("uso: %+v", u)
	}
	// Límite diario.
	c.DailyLimit = 6
	a.handle(ctx, tg, c, msg(42, "hola"))
	if !strings.Contains(tgf.last(), "límite") {
		t.Fatalf("límite diario: %q", tgf.last())
	}
	// Estado para el panel.
	a.writeStatus(ctx)
	b, err := os.ReadFile(a.statusPath)
	if err != nil || !strings.Contains(string(b), `"bot": "mibot"`) || !strings.Contains(string(b), "libros_leyendo") || strings.Contains(string(b), "gsk_prueba") {
		t.Fatalf("estado: %v %s", err, b)
	}
}

func TestOwnerLinking(t *testing.T) {
	a, tgf, c := newTestAgent(t)
	c.TelegramUser = 0
	a.handle(context.Background(), a.tg(c), c, msg(12345, "/start"))
	if !strings.Contains(tgf.last(), "12345") || !strings.Contains(tgf.last(), "panel") {
		t.Fatalf("sin dueño, debe dar el ID para vincular: %q", tgf.last())
	}
}

func TestConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claves.env")
	os.WriteFile(path, []byte("# comentario\nTELEGRAM_TOKEN=1:abc\nTELEGRAM_USER_ID=42\nLLM_BASE_URL=https://x/v1/\nAPPS=libros, hola\n"), 0o600)
	c, mod := loadConfig(path)
	if c.TelegramToken != "1:abc" || c.TelegramUser != 42 || c.BaseURL != "https://x/v1" || len(c.Apps) != 2 || c.DailyLimit != 200 || mod.IsZero() {
		t.Fatalf("config: %+v", c)
	}
	if len(c.problems()) == 0 {
		t.Fatal("falta la clave del modelo y no se avisa")
	}
}
