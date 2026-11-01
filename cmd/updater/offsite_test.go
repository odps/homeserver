package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestOffsiteArchive(t *testing.T) {
	srv := t.TempDir()
	for name, body := range map[string]string{
		"data/libros/libro.epub": "EPUB", "state/gateway-users.json": "{}", "env/libros.env": "DATABASE_URL=x",
		"tailscale/tailscaled.state": "{}", "state/medio.tmp": "no", "releases/v1/bin/x": "no", "backups/viejo.dump": "no",
	} {
		p := filepath.Join(srv, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(body), 0o600)
	}
	dump := filepath.Join(t.TempDir(), "d.dump")
	os.WriteFile(dump, []byte("PGDMP-contenido"), 0o600)
	id, _ := age.GenerateX25519Identity()

	var buf strings.Builder
	f, _ := os.Create(filepath.Join(t.TempDir(), "c.age"))
	if err := writeArchive(context.Background(), f, id.Recipient(), dump, srv, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, io.SeekStart)
	// Sin la clave privada no se puede leer; con ella, está todo lo que debe.
	other, _ := age.GenerateX25519Identity()
	if _, err := age.Decrypt(f, other); err == nil {
		t.Fatal("se descifra con otra clave")
	}
	f.Seek(0, io.SeekStart)
	dec, err := age.Decrypt(f, id)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := gzip.NewReader(dec)
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "homeserver.dump" {
			b, _ := io.ReadAll(tr)
			buf.Write(b)
		}
	}
	f.Close()
	sort.Strings(names)
	got := strings.Join(names, " ")
	if got != "LEEME.txt data/libros/libro.epub env/libros.env homeserver.dump state/gateway-users.json tailscale/tailscaled.state" || buf.String() != "PGDMP-contenido" {
		t.Fatalf("contenido de la copia: %s %q", got, buf.String())
	}
}

func TestOffsitePrune(t *testing.T) {
	srv := t.TempDir()
	dir := filepath.Join(srv, "backups", "copias")
	os.MkdirAll(dir, 0o700)
	// Una copia diaria durante 40 días, y dos el último día.
	start := time.Date(2026, 9, 1, 4, 0, 0, 0, madrid)
	for d := 0; d < 40; d++ {
		os.WriteFile(filepath.Join(dir, "homeserver-"+start.AddDate(0, 0, d).Format("20060102-1504")+".tar.gz.age"), nil, 0o600)
	}
	os.WriteFile(filepath.Join(dir, "homeserver-"+start.AddDate(0, 0, 39).Add(time.Hour).Format("20060102-1504")+".tar.gz.age"), nil, 0o600)
	u := &updater{cfg: config{SrvDir: srv}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	u.pruneOffsite(offsiteConfig{KeepDaily: 7, KeepWeekly: 4})
	files, _ := filepath.Glob(filepath.Join(dir, "*.age"))
	// 7 días distintos (el último, con su copia más reciente) + las semanas
	// anteriores que no cubren esos 7 días.
	if len(files) < 8 || len(files) > 10 {
		t.Fatalf("quedan %d copias: %v", len(files), files)
	}
	sort.Strings(files)
	if !strings.Contains(files[len(files)-1], start.AddDate(0, 0, 39).Add(time.Hour).Format("20060102-1504")) {
		t.Fatal("no se queda la copia más reciente")
	}
	for _, f := range files {
		if strings.Contains(f, "202609") && !strings.Contains(f, "2026092") && !strings.Contains(f, "2026093") {
			t.Fatalf("queda una copia de hace más de 4 semanas: %s", f)
		}
	}
}
