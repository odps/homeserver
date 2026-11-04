package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testJPEG es una foto de w×h con un EXIF que dice orientación 6 (girar 90°)
// y lleva texto que no debe sobrevivir (como unas coordenadas GPS).
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	// TIFF little-endian con una sola entrada: orientación (0x0112) = 6.
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	payload := append([]byte("Exif\x00\x00"), tiff...)
	payload = append(payload, []byte("GPS 40.4168N 3.7038W Pixel-Ejemplo")...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	b := buf.Bytes()
	return append(append(append([]byte{}, b[:2]...), seg...), b[2:]...)
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 30, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 30; x++ {
			if x < 15 {
				img.Set(x, y, color.NRGBA{0, 0, 0, 255})
			} // la mitad derecha, transparente
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// testWebP es un WebP sin pérdida de 1×1.
var testWebP, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

const testPDF = "%PDF-1.4\n1 0 obj << /Title (Escritura de Ana Pérez 12345678Z) >> endobj\ntrailer << >>\n%%EOF\n"

var testHEIC = []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")

func TestPrepare(t *testing.T) {
	m, full, thumb, err := prepare("C:\\fotos\\dni-delante.JPG", testJPEG(t, 80, 40))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != typeJPEG || m.Name != "dni-delante.jpg" || !m.Mini || thumb == nil || m.Size != len(full) {
		t.Fatalf("jpeg: %+v", m)
	}
	// Sin EXIF y ya girada.
	if bytes.Contains(full, []byte("Exif")) || bytes.Contains(full, []byte("GPS")) || bytes.Contains(full, []byte("Pixel-Ejemplo")) {
		t.Fatal("la imagen guarda el EXIF")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(full))
	if err != nil || cfg.Width != 40 || cfg.Height != 80 || m.W != 40 || m.H != 80 {
		t.Fatalf("orientación: %+v %v", cfg, err)
	}

	// PNG con transparencia: JPEG sobre blanco.
	m, full, _, err = prepare("captura.png", testPNG(t))
	if err != nil || m.Type != typeJPEG || m.Name != "captura.jpg" {
		t.Fatalf("png: %+v %v", m, err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(full))
	if r, g, b, _ := img.At(25, 10).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("lo transparente no es blanco: %d %d %d", r>>8, g>>8, b>>8)
	}
	if r, _, _, _ := img.At(3, 10).RGBA(); r>>8 > 20 {
		t.Errorf("lo negro no es negro: %d", r>>8)
	}

	if m, _, _, err := prepare("foto.webp", testWebP); err != nil || m.Type != typeJPEG || m.W != 1 {
		t.Fatalf("webp: %+v %v", m, err)
	}

	// El PDF, tal cual y sin miniatura.
	m, full, thumb, err = prepare("escritura.pdf", []byte(testPDF))
	if err != nil || m.Type != typePDF || m.Mini || thumb != nil || string(full) != testPDF || m.Name != "escritura.pdf" {
		t.Fatalf("pdf: %+v %v", m, err)
	}

	// Lo demás, con un mensaje claro.
	for name, c := range map[string]struct {
		raw  []byte
		want error
	}{
		"heic":      {testHEIC, errHEIC},
		"texto":     {[]byte("hola, esto es un txt"), errFileType},
		"html":      {[]byte("<html><script>alert(1)</script>"), errFileType},
		"gif":       {[]byte("GIF89a\x01\x00\x01\x00"), errFileType},
		"pdf falso": {[]byte("PDF-1.4 sin el tanto por ciento"), errFileType},
		"vacío":     {nil, errEmptyFile},
		"grande":    {append([]byte("%PDF-"), make([]byte, maxFile)...), errTooBig},
	} {
		if _, _, _, err := prepare(name, c.raw); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, _, err := prepare("rota.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}); err == nil {
		t.Error("acepta un JPEG roto")
	}
	if got := cleanName("../../etc/\"passwd\".exe", ".pdf"); got != "passwd.pdf" {
		t.Errorf("nombre: %q", got)
	}
	if got := cleanName("", ".jpg"); got != "archivo.jpg" {
		t.Errorf("sin nombre: %q", got)
	}
}

func TestFilesStoreAndOwner(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	id, err := svc.store.saveDoc(ctx, "ana", sampleDNI())
	if err != nil {
		t.Fatal(err)
	}
	pdfID, err := svc.addFile(ctx, "ana", id, "escaneo-pasaporte.pdf", []byte(testPDF))
	if err != nil {
		t.Fatal(err)
	}
	jpgID, err := svc.addFile(ctx, "ana", id, "dni-delante.jpg", testJPEG(t, 60, 30))
	if err != nil {
		t.Fatal(err)
	}

	// En el disco, cifrado.
	raw, err := os.ReadFile(svc.files.path(pdfID, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"%PDF", "12345678Z", "Escritura", "Ana"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("el fichero del disco guarda «%s» en claro", s)
		}
	}
	if _, err := os.Stat(svc.files.path(jpgID, true)); err != nil {
		t.Fatal("falta la miniatura")
	}
	// Un fichero no se descifra como otro.
	b, _ := os.ReadFile(svc.files.path(jpgID, false))
	os.WriteFile(svc.files.path(pdfID, false), b, 0o600)
	if _, _, err := svc.fileContent(ctx, "ana", pdfID, false); err == nil {
		t.Fatal("descifra un fichero cambiado de sitio")
	}
	os.WriteFile(svc.files.path(pdfID, false), raw, 0o600)
	m, content, err := svc.fileContent(ctx, "ana", pdfID, false)
	if err != nil || string(content) != testPDF || m.Name != "escaneo-pasaporte.pdf" || m.DocID != id {
		t.Fatalf("leer: %+v %v", m, err)
	}
	if _, _, err := svc.fileContent(ctx, "ana", pdfID, true); !errors.Is(err, errNotFound) {
		t.Fatal("un PDF con miniatura")
	}
	noPlain(t, svc.store.db, "files", "escaneo-pasaporte", "dni-delante", "application/pdf", "image/jpeg")

	// Cada uno lo suyo.
	if _, _, err := svc.fileContent(ctx, "luis", pdfID, false); !errors.Is(err, errNotFound) {
		t.Fatal("luis lee un archivo de ana")
	}
	if _, err := svc.addFile(ctx, "luis", id, "mio.pdf", []byte(testPDF)); !errors.Is(err, errNotFound) {
		t.Fatalf("luis sube a un documento de ana: %v", err)
	}
	if _, err := svc.deleteFile(ctx, "luis", pdfID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra un archivo de ana")
	}
	if l, _ := svc.store.files(ctx, "luis", id); len(l) != 0 {
		t.Fatal("luis ve los archivos de ana")
	}

	// Como mucho 10 por documento.
	for i := 2; i < maxDocFiles; i++ {
		if _, err := svc.addFile(ctx, "ana", id, "p.pdf", []byte(testPDF)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.addFile(ctx, "ana", id, "once.pdf", []byte(testPDF)); !errors.Is(err, errTooMany) {
		t.Fatalf("el undécimo: %v", err)
	}
	if d, _ := svc.store.doc(ctx, "ana", id); d.Files != maxDocFiles {
		t.Fatalf("cuenta: %d", d.Files)
	}

	// Borrar un archivo lo quita del disco; borrar el documento, todos.
	if doc, err := svc.deleteFile(ctx, "ana", jpgID); err != nil || doc != id {
		t.Fatal(err)
	}
	for _, mini := range []bool{false, true} {
		if _, err := os.Stat(svc.files.path(jpgID, mini)); !os.IsNotExist(err) {
			t.Fatalf("sigue en el disco (mini %v)", mini)
		}
	}
	if err := svc.deleteDoc(ctx, "ana", id); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(svc.files.dir)
	if len(left) != 0 {
		t.Fatalf("quedan ficheros: %v", left)
	}
	var n int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM files`).Scan(&n)
	if n != 0 {
		t.Fatalf("quedan %d filas", n)
	}
	if !strings.HasSuffix(filepath.ToSlash(svc.files.dir), "/archivos") {
		t.Fatalf("directorio: %s", svc.files.dir)
	}
}
