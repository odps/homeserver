package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/version"
)

// Herramientas de las apps (libros, cómics…) por MCP, a través de la
// pasarela con el token «asistente», que actúa en nombre de la cuenta elegida
// en el panel. Cada app expone sus tools en /<app>/mcp.

type appTools struct {
	gateway string
	log     *slog.Logger

	mu       sync.Mutex
	token    string
	sessions map[string]*mcp.ClientSession
}

// bearer añade el token a las peticiones hacia la pasarela.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (m *appTools) session(ctx context.Context, app, token string) (*mcp.ClientSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if token != m.token {
		for _, s := range m.sessions {
			s.Close()
		}
		m.sessions, m.token = map[string]*mcp.ClientSession{}, token
	}
	if s, ok := m.sessions[app]; ok {
		return s, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "asistente", Version: version.Version}, nil)
	hc := &http.Client{Timeout: 40 * time.Second, Transport: bearer{token, http.DefaultTransport}}
	s, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: m.gateway + "/" + app + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return nil, err
	}
	m.sessions[app] = s
	return s, nil
}

func (m *appTools) drop(app string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[app]; ok {
		s.Close()
		delete(m.sessions, app)
	}
}

// tools descubre las herramientas de cada app. Una app que falla no impide
// usar las demás.
func (m *appTools) tools(ctx context.Context, apps []string, token string) ([]tool, []string) {
	var out []tool
	var problems []string
	if token == "" {
		return nil, nil
	}
	for _, app := range apps {
		s, err := m.session(ctx, app, token)
		var res *mcp.ListToolsResult
		if err == nil {
			res, err = s.ListTools(ctx, nil)
		}
		if err != nil {
			m.drop(app)
			problems = append(problems, fmt.Sprintf("No se pudieron leer las herramientas de %s: %v", app, err))
			continue
		}
		for _, t := range res.Tools {
			if !validToolName.MatchString(t.Name) {
				continue
			}
			nt := newTool(t.Name, t.Description, schemaFor(t.InputSchema), m.caller(app, t.Name, token))
			nt.app = app
			out = append(out, nt)
		}
	}
	return out, problems
}

// schemaFor adapta el esquema de una tool MCP al formato de funciones: los
// proveedores exigen un objeto con «properties».
func schemaFor(s any) map[string]any {
	var m map[string]any
	b, _ := json.Marshal(s)
	json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["type"] = "object"
	if _, ok := m["properties"]; !ok {
		m["properties"] = map[string]any{}
	}
	delete(m, "$schema")
	return m
}

func (m *appTools) caller(app, name, token string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var a map[string]any
		json.Unmarshal(args, &a)
		s, err := m.session(ctx, app, token)
		if err != nil {
			return "", fmt.Errorf("%s no responde: %w", app, err)
		}
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: a})
		if err != nil {
			m.drop(app) // la próxima vez se vuelve a conectar
			return "", err
		}
		if res.StructuredContent != nil {
			b, _ := json.Marshal(res.StructuredContent)
			return string(b), nil
		}
		var parts []string
		for _, c := range res.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				parts = append(parts, t.Text)
			}
		}
		text := strings.Join(parts, "\n")
		if res.IsError {
			return "", fmt.Errorf("%s", text)
		}
		return text, nil
	}
}
