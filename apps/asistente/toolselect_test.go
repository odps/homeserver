package main

import (
	"fmt"
	"testing"
)

// fakeTools hace n herramientas de una app («» para las propias).
func fakeTools(app, prefix string, n int) []tool {
	var out []tool
	for i := 0; i < n; i++ {
		t := newTool(fmt.Sprintf("%s_%d", prefix, i), "x", map[string]any{"type": "object"}, nil)
		t.app = app
		out = append(out, t)
	}
	return out
}

func count(tools []tool) map[string]int {
	m := map[string]int{}
	for _, t := range tools {
		m[t.app]++
	}
	return m
}

func TestPickTools(t *testing.T) {
	var all []tool
	all = append(all, fakeTools("", "notas", 14)...)
	all = append(all, fakeTools("agenda", "agenda", 30)...)
	all = append(all, fakeTools("entreno", "entreno", 20)...)
	all = append(all, fakeTools("compra", "compra", 6)...)
	all = append(all, fakeTools("papeles", "papeles", 6)...)
	all = append(all, fakeTools("gastos", "gastos", 6)...)
	all = append(all, fakeTools("habitos", "habitos", 6)...)
	all = append(all, fakeTools("cartera", "cartera", 9)...)
	all = append(all, fakeTools("casa", "casa", 6)...)
	all = append(all, fakeTools("coche", "coche", 6)...)
	all = append(all, fakeTools("series", "series", 9)...)
	all = append(all, fakeTools("leer", "leer", 6)...)
	all = append(all, fakeTools("libros", "libros", 12)...)
	if len(all) <= maxTools {
		t.Fatalf("el caso de prueba debe pasar del máximo: %d", len(all))
	}

	// Sin pistas: las propias siempre, y las apps por orden hasta el máximo.
	got := pickTools(all, "vale, borra todas las notas", nil)
	c := count(got)
	if len(got) > maxTools || c[""] != 14 || c["agenda"] != 30 || c["entreno"] != 20 {
		t.Fatalf("sin pistas: %d %v", len(got), c)
	}
	for app, n := range c {
		if app != "" && n != map[string]int{"agenda": 30, "entreno": 20, "compra": 6, "papeles": 6, "gastos": 6, "habitos": 6, "cartera": 9, "casa": 6, "coche": 6, "series": 9, "leer": 6, "libros": 12}[app] {
			t.Fatalf("app a medias: %s %d", app, n)
		}
	}

	// Lo que se nombra entra aunque vaya al final del orden.
	if c := count(pickTools(all, "¿Qué libro estoy leyendo?", nil)); c["libros"] != 12 {
		t.Fatalf("libros: %v", c)
	}
	if c := count(pickTools(all, "He repostado 40 litros", nil)); c["coche"] != 6 {
		t.Fatalf("coche: %v", c)
	}
	// Lo usado hace poco, también («bórralo»).
	recent := []string{"Hecho.\n[Herramientas: libros_3]"}
	if c := count(pickTools(all, "bórralo", recent)); c["libros"] != 12 {
		t.Fatalf("usado hace poco: %v", c)
	}
	// Por debajo del máximo, todas tal cual.
	if got := pickTools(all[:50], "hola", nil); len(got) != 50 {
		t.Fatalf("pocas: %d", len(got))
	}
}
