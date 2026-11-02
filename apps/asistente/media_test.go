package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestSplitButtons(t *testing.T) {
	text, opts := splitButtons("¿Lo borro?\n[[botones: Sí, bórralo | No | Más tarde ]]")
	if text != "¿Lo borro?" || len(opts) != 3 || opts[0] != "Sí, bórralo" || opts[2] != "Más tarde" {
		t.Fatalf("%q %q", text, opts)
	}
	if text, opts := splitButtons("Sin botones [[otra cosa]]"); opts != nil || text != "Sin botones [[otra cosa]]" {
		t.Fatalf("sin botones: %q %q", text, opts)
	}
	if _, opts := splitButtons("x\n[[botones: a|b|c|d|e]]"); len(opts) != 4 {
		t.Fatalf("como mucho 4: %q", opts)
	}
}

func TestButtonsPhotosAndVoice(t *testing.T) {
	a, tgf, c := newTestAgent(t)
	ctx := context.Background()
	a.refresh(ctx, true)
	tg := a.tg(c)

	// Botones: salen bajo el mensaje y pulsarlos sigue la conversación.
	a.handle(ctx, tg, c, msg(42, "borra la nota 3"))
	if tgf.last() != "¿Seguro que la borro?" || !strings.Contains(fmt.Sprint(tgf.markup[len(tgf.markup)-1]), "Sí, bórrala") {
		t.Fatalf("botones: %q %v", tgf.last(), tgf.markup[len(tgf.markup)-1])
	}
	cb := &tgCallback{ID: "q1", From: tgUser{ID: 42}, Data: "b:Sí, bórrala", Message: msg(42, "")}
	a.pressed(ctx, tg, c, cb)
	if tgf.last() != "Borrada." || tgf.edited != 1 || !strings.Contains(strings.Join(tgf.sent, "|"), "👉 Sí, bórrala") {
		t.Fatalf("al pulsar: %q, editados %d", tgf.last(), tgf.edited)
	}
	// De otro, no se hace caso.
	n := len(tgf.sent)
	a.pressed(ctx, tg, c, &tgCallback{ID: "q2", From: tgUser{ID: 7}, Data: "b:Sí, bórrala", Message: msg(7, "")})
	if len(tgf.sent) != n {
		t.Fatal("se atiende un botón de otra persona")
	}

	// Foto: llega al modelo como imagen; al historial, solo «[foto]».
	tgf.files = map[string][]byte{"foto-grande": []byte("JPEGDATA"), "doc.pdf": []byte("%PDF-1.4")}
	m := msg(42, "")
	m.Photo = []tgPhotoSize{{FileID: "foto-peque", Width: 90, Height: 120}, {FileID: "foto-grande", Width: 960, Height: 1280}, {FileID: "foto-enorme", Width: 2560, Height: 3413}}
	m.Caption = "cita del médico"
	a.handle(ctx, tg, c, m)
	if !strings.Contains(tgf.last(), "Recibido: text image_url jpeg") || tgf.markup[len(tgf.markup)-1] == nil {
		t.Fatalf("foto: %q", tgf.last())
	}
	h, _ := a.store.history(ctx, 42, 30)
	last := h[len(h)-2]
	if last.Content != "[foto] cita del médico" || strings.Contains(fmt.Sprint(h), "JPEGDATA") {
		t.Fatalf("historial de la foto: %+v", last)
	}
	// PDF.
	m = msg(42, "")
	m.Document = &tgFileRef{FileID: "doc.pdf", FileName: "factura.pdf", MimeType: "application/pdf"}
	a.handle(ctx, tg, c, m)
	if !strings.Contains(tgf.last(), "Recibido: text file") {
		t.Fatalf("pdf: %q", tgf.last())
	}
	// Otros documentos, no.
	m.Document = &tgFileRef{FileID: "x", FileName: "hoja.xlsx", MimeType: "application/vnd.ms-excel"}
	a.handle(ctx, tg, c, m)
	if !strings.Contains(tgf.last(), "solo sé leer imágenes y PDF") {
		t.Fatalf("xlsx: %q", tgf.last())
	}

	// Voz sin whisper instalado: lo dice, sin llamar al modelo.
	a.voice = transcriber{ffmpeg: "no-existe-ffmpeg", whisper: "no-existe-whisper", model: "no-existe.bin"}
	m = msg(42, "")
	m.Voice = &tgFileRef{FileID: "nota", Duration: 5}
	a.handle(ctx, tg, c, m)
	if !strings.Contains(tgf.last(), "falta instalar whisper") {
		t.Fatalf("voz: %q", tgf.last())
	}
}

func TestBestPhoto(t *testing.T) {
	got := bestPhoto([]tgPhotoSize{{FileID: "a", Width: 90, Height: 90}, {FileID: "b", Width: 1280, Height: 960}, {FileID: "c", Width: 2560, Height: 1920}})
	if got.FileID != "b" {
		t.Fatalf("%+v", got)
	}
}
