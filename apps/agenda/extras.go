package main

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
)

// extra es la sección de otra app del servidor en el resumen de la mañana:
// sus líneas llegan por POST /interno/hoy (appToday). Cada persona puede
// quitarla en Ajustes o con Talia; si no usa la app, no sale nada.
type extra struct {
	Name  string // app y clave del ajuste
	Title string // título de la sección en el resumen
	Label string // en Ajustes
	Meta  string
	today func(ctx context.Context, owner string) ([]string, error)
}

// extraApps son las apps con sección propia, en el orden del resumen.
var extraApps = []struct {
	name, title, label, meta string
	port                     int
}{
	{"habitos", "✅ HÁBITOS", "Con tus hábitos de hoy", "Los que tocan hoy y sus rachas, si usas la app Hábitos", 8113},
	{"gastos", "💶 GASTOS", "Con tus cobros y presupuestos", "Cobros de hoy y de los próximos días, y presupuestos superados, si usas la app Gastos", 8110},
	{"casa", "🏠 CASA", "Con el precio de la luz y la casa", "Las horas más baratas de la luz y el mantenimiento pendiente, si usas la app Casa", 8111},
	{"coche", "🚗 COCHE", "Con el mantenimiento del coche", "Solo si hay algo pendiente, si usas la app Coche", 8112},
	{"series", "📺 SERIES", "Con los estrenos de tus series", "Los episodios que se estrenan hoy, si usas la app Series y pelis", 8115},
	{"leer", "📖 PARA LEER", "Con tus lecturas pendientes", "Cuántos artículos tienes guardados y una sugerencia, si usas Leer después", 8114},
}

// newExtras prepara las secciones; <APP>_URL cambia la dirección de cada app.
func newExtras() []extra {
	var out []extra
	for _, a := range extraApps {
		base := os.Getenv(strings.ToUpper(a.name) + "_URL")
		if base == "" {
			base = "http://127.0.0.1:" + strconv.Itoa(a.port)
		}
		out = append(out, extra{Name: a.name, Title: a.title, Label: a.label, Meta: a.meta, today: appToday(a.name, base)})
	}
	return out
}

// extraOn dice si la sección de una app sale en el resumen de esta persona.
func (p prefs) extraOn(name string) bool { return !slices.Contains(p.NoSections, name) }

// setExtra enciende o apaga la sección de una app.
func (p *prefs) setExtra(name string, on bool) {
	p.NoSections = slices.DeleteFunc(p.NoSections, func(n string) bool { return n == name })
	if !on {
		p.NoSections = append(p.NoSections, name)
	}
}

// extraView es una sección para la página de Ajustes.
type extraView struct {
	Name, Label, Meta string
	On                bool
}

func (s *service) extraViews(p prefs) []extraView {
	var out []extraView
	for _, e := range s.extras {
		out = append(out, extraView{e.Name, e.Label, e.Meta, p.extraOn(e.Name)})
	}
	return out
}
