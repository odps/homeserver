package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func kg(v float64) *float64 { return &v }

func TestBodyLogsTrendAndPrivacy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	today := time.Date(2026, 11, 4, 9, 0, 0, 0, madrid)
	svc.now = func() time.Time { return today }
	// Cinco semanas de pesajes: baja 0,5 kg por semana.
	for d := 35; d >= 0; d-- {
		if d%2 == 1 {
			continue
		}
		w := 80 - 0.5*float64(35-d)/7
		if _, err := svc.store.saveBody(ctx, "ana", today.AddDate(0, 0, -d), bodyLog{Kg: kg(w)}, true); err != nil {
			t.Fatal(err)
		}
	}
	// Talia suma medidas al día sin quitar el peso.
	b, err := svc.store.saveBody(ctx, "ana", today, bodyLog{Measures: map[string]float64{"cintura": 84.5}, Note: "Tras el desayuno"}, true)
	if err != nil || b.Kg == nil || b.Measures["cintura"] != 84.5 {
		t.Fatalf("mezclar: %+v %v", b, err)
	}
	svc.store.saveBody(ctx, "ana", today.AddDate(0, 0, -28), bodyLog{Kg: kg(78.0), Measures: map[string]float64{"cintura": 88, "brazo": 36}}, true)
	logs, _ := svc.store.bodyLogs(ctx, "ana", time.Time{}, today)
	tr := trend(logs, today)
	if tr == nil || tr.Last != 77.5 || tr.Week == nil || *tr.Week > -0.3 || *tr.Week < -0.7 || tr.Month == nil {
		t.Fatalf("tendencia: %+v", tr)
	}
	rows := measureRows(logs)
	if len(rows) != 2 || rows[0].Key != "cintura" || rows[0].Last != 84.5 || rows[0].First != 88 || rows[1].Key != "brazo" {
		t.Fatalf("medidas: %+v", rows)
	}
	if got := bodyText(b); got != "77.5 kg · cintura 84.5 cm" {
		t.Fatalf("texto: %s", got)
	}
	// El formulario del día sustituye; vacío, se borra.
	if _, err := svc.store.saveBody(ctx, "ana", today, bodyLog{}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.store.bodyOn(ctx, "ana", today); ok {
		t.Fatal("el día vacío no se borra")
	}
	// Validación.
	for _, bad := range []bodyLog{{Kg: kg(5)}, {Fat: kg(90)}, {Measures: map[string]float64{"nariz": 4}}, {Measures: map[string]float64{"cintura": 900}}} {
		if _, err := svc.store.saveBody(ctx, "ana", today, bad, true); err == nil {
			t.Fatalf("acepta %+v", bad)
		}
	}
	noPlain(t, svc.store.db, "body_logs", "Tras el desayuno", "cintura", "84.5", "medidas")
	if l, _ := svc.store.bodyLogs(ctx, "luis", time.Time{}, today); len(l) != 0 {
		t.Fatal("otro ve el peso")
	}
	out, err := svc.bodySummary(ctx, "ana", 7)
	if err != nil || out.Peso == nil || len(out.Registros) != 3 || len(out.Medidas) != 2 || out.Medidas[0].Ultima != 88 || out.Fotos != 0 {
		t.Fatalf("resumen: %+v %v", out, err)
	}
}

func TestCardio(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 4, 19, 0, 0, 0, madrid)
	id, err := svc.store.addCardio(ctx, "ana", cardioLog{Start: at, Type: "correr", Seconds: 28*60 + 30, Meters: 5200, HR: 152, Note: "Por el río"})
	if err != nil {
		t.Fatal(err)
	}
	svc.store.addCardio(ctx, "ana", cardioLog{Start: at.AddDate(0, 0, -8), Type: "remo", Seconds: 600, Meters: 2400})
	c, err := svc.store.cardioOne(ctx, "ana", id)
	if err != nil || cardioText(c) != "Correr — 5.2 km en 28:30 · 5:29 min/km · 152 ppm" {
		t.Fatalf("texto: %q %v", cardioText(c), err)
	}
	if _, err := svc.store.cardioOne(ctx, "luis", id); !errors.Is(err, errNotFound) {
		t.Fatal("otro ve el cardio")
	}
	if err := svc.store.deleteCardio(ctx, "luis", id); err == nil {
		t.Fatal("otro borra el cardio")
	}
	list, _ := svc.store.cardioList(ctx, "ana", time.Time{}, at, 0)
	weeks := cardioWeeks(list, at, 3)
	if len(weeks) != 3 || weeks[2].Count != 1 || weeks[2].Seconds != 1710 || weeks[1].Count != 1 || weeks[1].Meters != 2400 {
		t.Fatalf("semanas: %+v", weeks)
	}
	noPlain(t, svc.store.db, "cardio", "correr", "Por el río", `"metros"`, `"pulso"`) // números cortos no: podrían salir en el hexadecimal
	for _, bad := range []cardioLog{{Start: at, Type: "volar", Seconds: 600}, {Start: at, Type: "correr", Seconds: 10}, {Start: at, Type: "correr", Seconds: 600, HR: 300}} {
		if _, err := svc.store.addCardio(ctx, "ana", bad); err == nil {
			t.Fatalf("acepta %+v", bad)
		}
	}
}

func TestCardioText(t *testing.T) {
	for in, want := range map[string]int{"45": 2700, "45,5": 2730, "45:30": 2730, "1:05:30": 3930, "30 min": 1800} {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("duración %q: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "45:75", "1:75:00", "-3"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("acepta la duración %q", bad)
		}
	}
	cases := map[string]string{
		"bici": "24 km/h", "remo": "2:05 /500 m", "natacion": "1:55 /100 m", "eliptica": "",
	}
	for typ, want := range cases {
		c := cardioLog{Type: typ}
		switch typ {
		case "bici":
			c.Seconds, c.Meters = 3600, 24000
		case "remo":
			c.Seconds, c.Meters = 500, 2000
		case "natacion":
			c.Seconds, c.Meters = 1150, 1000
		case "eliptica":
			c.Seconds, c.Meters = 1200, 3000
		}
		if got := paceText(c); got != want {
			t.Errorf("ritmo %s: %q", typ, got)
		}
	}
	for in, want := range map[string]string{"Bici estática": "bici_estatica", "bici": "bici", "nadar": "natacion", "salir a correr": "correr", "cinta": "cinta", "bici_estatica": "bici_estatica"} {
		if got, ok := cardioTypeFor(in); !ok || got.Key != want {
			t.Errorf("tipo %q: %s", in, got.Key)
		}
	}
	if _, ok := cardioTypeFor("parapente"); ok {
		t.Error("reconoce un tipo que no existe")
	}
	if durText(3900) != "1 h 5 min" || durText(2700) != "45 min" || distText(800) != "800 m" {
		t.Error("textos de duración o distancia")
	}
}

// jpegWithExif hace un JPEG con la mitad izquierda roja y la derecha azul,
// con un EXIF de orientación o y un texto «secreto» dentro.
func jpegWithExif(t *testing.T, w, h, o int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= w/2 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	// TIFF «II», un IFD con la orientación, y luego texto de relleno.
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00")
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:], 3)
	binary.LittleEndian.PutUint32(entry[4:], 1)
	binary.LittleEndian.PutUint16(entry[8:], uint16(o))
	tiff = append(tiff, entry...)
	tiff = append(tiff, 0, 0, 0, 0)
	tiff = append(tiff, []byte("GPS 39.4699 secreto")...)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	app1 = append(app1, seg...)
	raw := buf.Bytes()
	return append(append(append([]byte{}, raw[:2]...), app1...), raw[2:]...)
}

func TestProcessPhoto(t *testing.T) {
	raw := jpegWithExif(t, 2400, 1200, 6)
	if exifOrientation(raw) != 6 {
		t.Fatal("no lee la orientación")
	}
	full, thumb, w, h, err := processPhoto(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Girada 90° a la derecha: vertical, y lo de la izquierda (rojo) arriba.
	if w != 800 || h != 1600 {
		t.Fatalf("tamaño: %dx%d", w, h)
	}
	if bytes.Contains(full, []byte("secreto")) || bytes.Contains(full, []byte("Exif")) || bytes.Contains(thumb, []byte("Exif")) {
		t.Fatal("quedan metadatos")
	}
	img, err := jpeg.Decode(bytes.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	top, bottom := img.At(400, 200), img.At(400, 1400)
	if r, _, b, _ := top.RGBA(); r < b {
		t.Fatal("arriba no es rojo: no se ha girado bien")
	}
	if r, _, b, _ := bottom.RGBA(); b < r {
		t.Fatal("abajo no es azul")
	}
	if ti, _ := jpeg.Decode(bytes.NewReader(thumb)); ti.Bounds().Dy() != 480 {
		t.Fatalf("miniatura: %v", ti.Bounds())
	}
	for _, bad := range [][]byte{[]byte("hola"), []byte("<svg></svg>"), raw[:200]} {
		if _, _, _, _, err := processPhoto(bad); err == nil {
			t.Fatal("acepta algo que no es una foto")
		}
	}
	// Todas las orientaciones dejan el tamaño correcto.
	for o := 1; o <= 8; o++ {
		_, _, w, h, err := processPhoto(jpegWithExif(t, 60, 30, o))
		vertical := o >= 5
		if err != nil || (vertical && (w != 30 || h != 60)) || (!vertical && (w != 60 || h != 30)) {
			t.Fatalf("orientación %d: %dx%d %v", o, w, h, err)
		}
	}
}

func TestPhotosStoredEncrypted(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 4, 12, 0, 0, 0, madrid)
	id, err := svc.savePhoto(ctx, "ana", day, "perfil", "Semana 1 de definición", jpegWithExif(t, 300, 400, 1))
	if err != nil {
		t.Fatal(err)
	}
	// En disco, cifrada: ni la cabecera JPEG ni nada legible.
	for _, mini := range []bool{false, true} {
		b, err := os.ReadFile(svc.photos.path(id, mini))
		if err != nil || bytes.Contains(b, []byte("JFIF")) || bytes.HasPrefix(b, []byte{0xFF, 0xD8}) {
			t.Fatalf("fichero en claro (mini=%v): %v", mini, err)
		}
	}
	img, err := svc.photoImage(ctx, "ana", id, true)
	if err != nil || !bytes.HasPrefix(img, []byte{0xFF, 0xD8}) {
		t.Fatalf("leer: %v", err)
	}
	// Un fichero no se descifra como otro.
	id2, _ := svc.savePhoto(ctx, "ana", day, "frente", "", jpegWithExif(t, 300, 400, 1))
	a, _ := os.ReadFile(svc.photos.path(id, false))
	os.WriteFile(svc.photos.path(id2, false), a, 0o600)
	if _, err := svc.photoImage(ctx, "ana", id2, false); err == nil {
		t.Fatal("un fichero cambiado de sitio se descifra")
	}
	if _, err := svc.photoImage(ctx, "luis", id, true); !errors.Is(err, errNotFound) {
		t.Fatal("otro ve la foto")
	}
	if err := svc.deletePhoto(ctx, "luis", id); err == nil {
		t.Fatal("otro borra la foto")
	}
	if _, err := svc.photoImage(ctx, "ana", "../../clave-datos", false); err == nil {
		t.Fatal("acepta un id que no es un id")
	}
	list, _ := svc.store.photoList(ctx, "ana")
	if len(list) != 2 || list[0].Day.Format("2006-01-02") != "2026-11-04" {
		t.Fatalf("lista: %+v", list)
	}
	noPlain(t, svc.store.db, "photos", "perfil", "Semana 1", "definición")
	if err := svc.deletePhoto(ctx, "ana", id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(svc.photos.path(id, false)); !os.IsNotExist(err) {
		t.Fatal("la foto borrada sigue en disco")
	}
	if files, _ := filepath.Glob(filepath.Join(svc.photos.dir, ".tmp-*")); len(files) != 0 {
		t.Fatal("quedan temporales")
	}
	if strings.Contains(svc.photos.dir, "..") {
		t.Fatal("directorio raro")
	}
}
