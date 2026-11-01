package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func writeCopy(t *testing.T, path string, rcpt age.Recipient, files map[string]string) {
	t.Helper()
	f, _ := os.Create(path)
	defer f.Close()
	enc, _ := age.Encrypt(f, rcpt)
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	enc.Close()
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	id, _ := age.GenerateX25519Identity()
	key := filepath.Join(dir, "clave.txt")
	os.WriteFile(key, []byte("# clave de las copias\n"+id.String()+"\n"), 0o600)
	good := filepath.Join(dir, "buena.age")
	writeCopy(t, good, id.Recipient(), map[string]string{"homeserver.dump": "PGDMP…", "data/libros/a.epub": "EPUB", "LEEME.txt": "hola"})

	if n, _, err := run(key, good, "", io.Discard); err != nil || n != 3 {
		t.Fatalf("comprobar: %d %v", n, err)
	}
	out := filepath.Join(dir, "fuera")
	if _, _, err := run(key, good, out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "data", "libros", "a.epub")); string(b) != "EPUB" {
		t.Fatalf("extraer: %q", b)
	}

	// Otra clave, sin base de datos o con rutas que salen del directorio: error.
	other, _ := age.GenerateX25519Identity()
	wrong := filepath.Join(dir, "otra.age")
	writeCopy(t, wrong, other.Recipient(), map[string]string{"homeserver.dump": "PGDMP"})
	nodump := filepath.Join(dir, "sindump.age")
	writeCopy(t, nodump, id.Recipient(), map[string]string{"LEEME.txt": "x"})
	evil := filepath.Join(dir, "mala.age")
	writeCopy(t, evil, id.Recipient(), map[string]string{"../fuera.txt": "x"})
	for file, want := range map[string]string{wrong: "otra clave", nodump: "falta la base de datos", evil: "no permitido"} {
		_, _, err := run(key, file, filepath.Join(dir, "x"), io.Discard)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", filepath.Base(file), err)
		}
	}
}
