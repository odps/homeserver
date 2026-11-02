package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"homeserver/platform/seal"
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
// llmSaw es todo lo que ha recibido el modelo falso (para comprobar que no
// le llegan datos personales).
var llmSaw strings.Builder

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
		raw, _ := io.ReadAll(r.Body)
		llmSaw.Write(raw)
		json.Unmarshal(raw, &req)
		last := req.Messages[len(req.Messages)-1]
		reply := map[string]any{"role": "assistant"}
		call := func(name, args string) {
			reply["content"] = nil
			reply["tool_calls"] = []map[string]any{{"id": "c1", "type": "function", "function": map[string]string{"name": name, "arguments": args}}}
		}
		text, _ := last.Content.(string)
		// Como Groq: una llamada que no cumple el esquema se rechaza con un 400.
		if strings.Contains(text, "esquema") && last.Role == "user" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"tool call validation failed: /varios/0: expected string, but got object","code":"tool_use_failed"}}`))
			return
		}
		switch {
		case last.Role == "system" && strings.Contains(text, "no era válida"):
			reply["content"] = "Corregido."
		case last.Role == "tool":
			reply["content"] = "Según tu biblioteca: " + text
		case strings.Contains(text, "leyendo"):
			call("libros_leyendo", "{}")
		case strings.Contains(text, "apunta"):
			call("notas_guardar", `{"texto":"comprar pan"}`)
		case strings.Contains(text, "guarda el número"):
			b, _ := json.Marshal(map[string]string{"texto": text})
			call("notas_guardar", string(b))
		case strings.Contains(text, "Lucía"):
			call("contactos_buscar", `{"texto":"Lucía"}`)
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
	mcp.AddTool(server, &mcp.Tool{Name: "contactos_buscar", Description: "Contactos"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct {
			Texto string `json:"texto"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"contactos": []map[string]any{{"nombre": "Lucía Pérez",
				"telefonos":   []map[string]string{{"etiqueta": "móvil", "valor": "+34 600 111 222"}},
				"correos":     []map[string]string{{"valor": "lucia@example.com"}},
				"direcciones": []map[string]string{{"valor": "Calle Pez 3, 28013 Madrid", "mapa": "https://www.google.com/maps/search/?api=1&query=Calle+Pez+3"}},
				"notas":       "Su IBAN es ES91 2100 0418 4502 0005 1332"}}}, nil
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
	box, _ := seal.Load("")
	pool := dbtest.MigratedSteps(t, migrations, steps(box))
	dir := t.TempDir()
	tg := &fakeTelegram{}
	tgSrv, llmSrv, gw := tg.server(t), fakeLLM(t), fakeGateway(t)
	a := newAgent(store{pool, box}, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(dir, "claves.env"), filepath.Join(dir, "estado.json"), gw.URL)
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
	// Una llamada rechazada por el proveedor se corrige en vez de fallar.
	a.handle(ctx, tg, c, msg(42, "algo que rompe el esquema"))
	if tgf.last() != "Corregido." {
		t.Fatalf("llamada rechazada: %q", tgf.last())
	}
	// Datos personales: el modelo solo ve etiquetas; la persona, los datos.
	llmSaw.Reset()
	a.handle(ctx, tg, c, msg(42, "¿cuál es el teléfono de Lucía?"))
	got := tgf.last()
	for _, want := range []string{"+34 600 111 222", "Calle Pez 3, 28013 Madrid", "lucia@example.com", "ES91 2100 0418 4502 0005 1332", "https://www.google.com/maps/search"} {
		if !strings.Contains(got, want) {
			t.Errorf("a la persona le falta %q: %s", want, got)
		}
	}
	a.handle(ctx, tg, c, msg(42, "guarda el número 612 345 678 del fontanero"))
	if ns, _ := a.store.notes(ctx, "fontanero", 5); len(ns) != 1 || !strings.Contains(ns[0].Text, "612 345 678") {
		t.Fatalf("la nota debe guardar el dato real: %+v", ns)
	}
	if !strings.Contains(tgf.last(), "612 345 678") {
		t.Errorf("respuesta de la nota: %q", tgf.last())
	}
	saw := llmSaw.String()
	for _, leak := range []string{"600 111", "Calle Pez", "lucia@", "ES91", "612 345", "google.com/maps"} {
		if strings.Contains(saw, leak) {
			t.Errorf("al modelo le ha llegado %q", leak)
		}
	}
	if !strings.Contains(saw, "TEL_1") || !strings.Contains(saw, "DIR_1:Madrid") {
		t.Errorf("al modelo no le han llegado las etiquetas: %s", saw)
	}
	if h, _ := a.store.history(ctx, 42, 20); strings.Contains(fmt.Sprint(h), "612 345") {
		t.Errorf("el historial guarda el dato real: %v", h)
	}
	// Uso: 2 peticiones por pregunta con herramienta, 1 por las demás; la rechazada
	// también cuenta como petición (sin tokens).
	if u, _ := a.store.usageToday(ctx); u.Requests != 12 || u.PromptTokens != 1100 {
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

func TestInternalSummary(t *testing.T) {
	a, _, _ := newTestAgent(t)
	a.refresh(context.Background(), true)
	call := func(remote, prefix string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/interno/resumir", strings.NewReader(`{"prompt":"Resume: hola"}`))
		req.RemoteAddr = remote
		if prefix != "" {
			req.Header.Set("X-Forwarded-Prefix", prefix)
		}
		rec := httptest.NewRecorder()
		a.internalSummary(rec, req)
		return rec
	}
	rec := call("127.0.0.1:5555", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"texto":"Hola, ¿qué tal?`) {
		t.Fatalf("resumen: %d %s", rec.Code, rec.Body)
	}
	if rec := call("127.0.0.1:5555", "/asistente"); rec.Code != http.StatusForbidden {
		t.Fatalf("a través de la pasarela: %d", rec.Code)
	}
	if rec := call("192.168.1.20:5555", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("desde fuera: %d", rec.Code)
	}
}
