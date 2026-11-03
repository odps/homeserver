package main

import (
	"context"
	"strings"
	"testing"
)

func TestSpeechText(t *testing.T) {
	text, cut := speechText("📅 HOY\n• 09:00 · Dentista\n• 18:00 · Cena 🔁\nMapa: https://www.google.com/maps/search/?api=1&query=x")
	if cut || strings.Contains(text, "http") || strings.ContainsAny(text, "📅🔁•·") || !strings.Contains(text, "09:00") ||
		!strings.Contains(text, "te dejo el enlace por escrito") {
		t.Fatalf("%q", text)
	}
	long := strings.Repeat("Una frase bastante larga para leer. ", 60)
	text, cut = speechText(long)
	if !cut || len([]rune(text)) > maxSpoken+40 || !strings.HasSuffix(text, "El resto te lo dejo por escrito.") {
		t.Fatalf("recorte: %v %d %q", cut, len([]rune(text)), text[len(text)-60:])
	}
}

func TestVoiceModeAndSend(t *testing.T) {
	a, tgf, c := newTestAgent(t)
	ctx := context.Background()
	tg := a.tg(c)
	a.speak = speaker{bin: "no-existe-tts", voiceDir: t.TempDir(), ffmpeg: "no-existe-ffmpeg"}

	if a.store.voiceMode(ctx) != voiceAuto {
		t.Fatal("por defecto, auto")
	}
	a.handle(ctx, tg, c, msg(42, "/voz siempre"))
	if a.store.voiceMode(ctx) != voiceAlways || !strings.Contains(tgf.last(), "siempre también con voz") || !strings.Contains(tgf.last(), "falta instalar la voz") {
		t.Fatalf("/voz siempre: %q", tgf.last())
	}
	a.handle(ctx, tg, c, msg(42, "/voz no"))
	if a.store.voiceMode(ctx) != voiceNever {
		t.Fatal("/voz no")
	}
	if !strings.Contains(a.voiceCommand(ctx, "quizá"), "Usa /voz") {
		t.Fatal("opción no válida")
	}
	// Sin la voz instalada, contesta solo por escrito.
	a.store.setVoiceMode(ctx, voiceAlways)
	a.speakReply(ctx, tg, 42, "Hola", true)
	if len(tgf.voices) != 0 {
		t.Fatal("manda voz sin tener la voz instalada")
	}
	// El envío de la nota de voz.
	if err := tg.sendVoice(ctx, 42, []byte("OggS-prueba")); err != nil || len(tgf.voices) != 1 || string(tgf.voices[0]) != "OggS-prueba" {
		t.Fatalf("sendVoice: %v %d", err, len(tgf.voices))
	}
}
