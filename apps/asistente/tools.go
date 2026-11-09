package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Herramientas que el modelo puede usar. Lista cerrada: nada ejecuta órdenes
// en el sistema ni toca ficheros. Las de las apps llegan por MCP.

type tool struct {
	def toolDef
	app string // app de la que viene; vacía: propia del asistente
	run func(ctx context.Context, args json.RawMessage) (string, error)
}

func newTool(name, description string, params map[string]any, run func(context.Context, json.RawMessage) (string, error)) tool {
	var t tool
	t.def.Type = "function"
	t.def.Function.Name = name
	t.def.Function.Description = description
	t.def.Function.Parameters = params
	t.run = run
	return t
}

// object construye un JSON Schema de objeto con propiedades de texto o número.
func object(required []string, props map[string]map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *agent) builtinTools() []tool {
	tools := append(a.baseTools(), a.geoTools()...)
	tools = append(tools, a.transitTools()...)
	if a.cfg().BraveKey != "" {
		tools = append(tools, a.searchTool())
	}
	return tools
}

// searchTool busca en internet; solo se ofrece si hay clave de Brave.
func (a *agent) searchTool() tool {
	return newTool("web_buscar", "Busca en internet. Úsala para noticias, datos actuales o cosas que no sabes con seguridad. Devuelve títulos, enlaces y extractos.",
		object([]string{"consulta"}, map[string]map[string]any{"consulta": str("Lo que hay que buscar, como en un buscador.")}),
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct{ Consulta string }
			if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Consulta) == "" {
				return "", fmt.Errorf("falta la consulta")
			}
			cfg := a.cfg()
			res, err := a.web.search(ctx, strings.TrimSpace(in.Consulta), cfg.BraveKey)
			return toJSON(map[string]any{"resultados": res}), err
		})
}

func (a *agent) baseTools() []tool {
	return []tool{
		newTool("notas_guardar", "Guarda una nota para recordarla más tarde (ideas, listas, datos que la persona pide apuntar o recordar).",
			object([]string{"texto"}, map[string]map[string]any{"texto": str("El texto de la nota, completo y claro.")}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ Texto string }
				if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Texto) == "" {
					return "", fmt.Errorf("falta el texto de la nota")
				}
				t := strings.TrimSpace(in.Texto)
				if len(t) > 4000 {
					t = t[:4000]
				}
				n, err := a.store.addNote(ctx, t)
				return toJSON(map[string]any{"guardada": n}), err
			}),
		newTool("notas_buscar", "Busca en las notas guardadas. Sin texto devuelve las más recientes.",
			object(nil, map[string]map[string]any{"texto": str("Palabras a buscar (opcional).")}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ Texto string }
				json.Unmarshal(raw, &in)
				ns, err := a.store.notes(ctx, strings.TrimSpace(in.Texto), 20)
				return toJSON(map[string]any{"notas": ns}), err
			}),
		newTool("notas_borrar", "Borra una nota por su id (búscala antes para saber el id).",
			object([]string{"id"}, map[string]map[string]any{"id": integer("Id de la nota.")}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ ID int64 }
				if err := json.Unmarshal(raw, &in); err != nil || in.ID <= 0 {
					return "", fmt.Errorf("falta el id de la nota")
				}
				if err := a.store.deleteNote(ctx, in.ID); err != nil {
					return "", err
				}
				return `{"borrada":true}`, nil
			}),
		newTool("web_leer", "Lee el texto de una página web (para resumirla o sacar datos). Úsala con enlaces de una búsqueda o que dé la persona.",
			object([]string{"url"}, map[string]map[string]any{"url": str("Dirección completa, con https://.")}),
			func(ctx context.Context, raw json.RawMessage) (string, error) {
				var in struct{ URL string }
				if err := json.Unmarshal(raw, &in); err != nil || in.URL == "" {
					return "", fmt.Errorf("falta la dirección")
				}
				title, text, err := a.web.read(ctx, in.URL)
				return toJSON(map[string]any{"titulo": title, "texto": text}), err
			}),
	}
}

// runTool ejecuta una herramienta con un plazo y devuelve el resultado como
// texto para el modelo (también los errores: así puede corregirse).
func (a *agent) runTool(ctx context.Context, tools map[string]tool, call toolCall) string {
	t, ok := tools[call.Function.Name]
	if !ok {
		return toJSON(map[string]string{"error": "herramienta desconocida: " + call.Function.Name})
	}
	args := json.RawMessage(call.Function.Arguments)
	if len(strings.TrimSpace(call.Function.Arguments)) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return toJSON(map[string]string{"error": "argumentos no válidos (JSON mal formado)"})
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	out, err := t.run(ctx, args)
	if err != nil {
		a.log.Warn("herramienta", "nombre", call.Function.Name, "err", err)
		return toJSON(map[string]string{"error": err.Error()})
	}
	if len(out) > 12000 {
		out = out[:12000] + "…(recortado)"
	}
	return out
}
