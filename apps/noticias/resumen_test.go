package main

import (
	"strings"
	"testing"
)

func TestSummaryInBlocks(t *testing.T) {
	d := digest{Sections: []digestSection{
		{Title: "Lo más importante", Kind: "espana", Items: []digestItem{{Title: "Presupuestos aprobados", Source: "El País", Others: []string{"a", "b"}, Link: "https://a.es/1"}}},
		{Title: "En el mundo", Kind: "mundo", Items: []digestItem{{Title: "Cumbre del clima", Source: "BBC", Link: "https://b.com/1"}}},
		{Title: "Salud mental", Kind: "interes", Items: []digestItem{{Title: "Nueva guía de terapia", Source: "X", Link: "https://x.es/1"}}},
		{Title: "Hacker News", Kind: "red", Items: []digestItem{{Title: "Show HN", Source: "HN", Link: "https://hn/1"}}},
	}}
	p := summaryPrompt(d)
	for _, want := range []string{"== ESPAÑA ==\n- Presupuestos aprobados (3 medios)", "== MUNDO ==\n- Cumbre del clima", "== INTERESES ==\n[Salud mental]\n- Nueva guía de terapia"} {
		if !strings.Contains(p, want) {
			t.Errorf("al modelo le falta %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Show HN") {
		t.Error("las redes no van al resumen")
	}
	if summaryPrompt(digest{Sections: []digestSection{d.Sections[3]}}) != "" {
		t.Error("sin noticias que resumir, no hay que llamar al modelo")
	}

	reply := "**ESPAÑA**\n• Se aprueban los presupuestos.\n\n• Otra cosa.\nMundo:\n- Cumbre del clima en Belém.\n\nINTERESES\n• Salud mental: nueva guía."
	blocks := parseBlocks(reply)
	if len(blocks) != 3 || blocks[0].Title != "España" || len(blocks[0].Lines) != 2 || blocks[1].Title != "En el mundo" ||
		blocks[1].Lines[0] != "Cumbre del clima en Belém." || blocks[2].Lines[0] != "Salud mental: nueva guía." {
		t.Fatalf("bloques: %+v", blocks)
	}
	if b := parseBlocks("Un párrafo sin marcas."); len(b) != 1 || b[0].Title != "" || b[0].Lines[0] != "Un párrafo sin marcas." {
		t.Fatalf("sin marcas: %+v", b)
	}

	d.Blocks = blocks
	text := plainDigest(d, 3)
	for _, want := range []string{"🗞 ESPAÑA\n• Se aprueban los presupuestos.\n• Otra cosa.\n\n🌍 EN EL MUNDO\n", "⭐ TUS INTERESES\n• Salud mental: nueva guía.\n\n━━━━━━━━━━\nTITULARES\n\n🗞 Lo más importante\n• Presupuestos aprobados (El País y 2 más)\n  https://a.es/1\n\n🌍 En el mundo", "⭐ Salud mental", "💬 Hacker News"} {
		if !strings.Contains(text, want) {
			t.Errorf("en Telegram falta %q:\n%s", want, text)
		}
	}
}
