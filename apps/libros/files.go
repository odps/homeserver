package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// files guarda los libros como DATA_DIR/books/<sha256>.<formato> y las
// portadas como DATA_DIR/covers/<sha256>.
type files struct{ dir string }

const (
	maxBookBytes  = 250 << 20
	maxCoverBytes = 5 << 20
)

func newFiles(dir string) (files, error) {
	for _, d := range []string{"books", "covers", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return files{}, err
		}
	}
	// Restos de subidas interrumpidas.
	tmp, _ := filepath.Glob(filepath.Join(dir, "tmp", "*"))
	for _, t := range tmp {
		os.Remove(t)
	}
	return files{dir: dir}, nil
}

func (f files) bookPath(b Book) string  { return filepath.Join(f.dir, "books", b.SHA256+"."+b.Format) }
func (f files) coverPath(b Book) string { return filepath.Join(f.dir, "covers", b.SHA256) }

// upload es un fichero recibido y ya en disco (temporal) con su hash.
type upload struct {
	tmp    string
	sha256 string
	size   int64
	head   []byte // primeros bytes, para reconocer el formato
}

// receive copia r a un temporal calculando el SHA-256, hasta max bytes.
func (f files) receive(r io.Reader, max int64) (*upload, error) {
	t, err := os.CreateTemp(filepath.Join(f.dir, "tmp"), "up-*")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	head := &headBuffer{max: 4096}
	n, err := io.Copy(io.MultiWriter(t, h, head), io.LimitReader(r, max+1))
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > max {
		err = fmt.Errorf("el fichero supera %d MB", max>>20)
	}
	if err == nil && n == 0 {
		err = errors.New("fichero vacío")
	}
	if err != nil {
		os.Remove(t.Name())
		return nil, err
	}
	return &upload{tmp: t.Name(), sha256: hex.EncodeToString(h.Sum(nil)), size: n, head: head.b}, nil
}

func (u *upload) discard() { os.Remove(u.tmp) }

type headBuffer struct {
	b   []byte
	max int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// detectFormat reconoce el formato por el contenido; el nombre solo desempata
// entre variantes del mismo tipo (EPUB, CBZ y FBZ son zip; MOBI y AZW3, PalmDB).
func detectFormat(u *upload, fileName string) (string, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))
	head := u.head
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf", nil
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return zipFormat(u.tmp, ext)
	case len(head) >= 68 && (string(head[60:68]) == "BOOKMOBI" || string(head[60:68]) == "TEXtREAd"):
		if ext == "azw3" || ext == "azw" {
			return "azw3", nil
		}
		return "mobi", nil
	case bytes.Contains(head[:min(len(head), 1024)], []byte("<FictionBook")):
		return "fb2", nil
	}
	return "", errors.New("formato no reconocido (EPUB, PDF, MOBI, AZW3, FB2, FBZ o CBZ)")
}

func zipFormat(path, ext string) (string, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return "", errors.New("zip dañado")
	}
	defer z.Close()
	images := 0
	for _, f := range z.File {
		name := strings.ToLower(f.Name)
		switch {
		case name == "mimetype":
			rc, err := f.Open()
			if err == nil {
				b, _ := io.ReadAll(io.LimitReader(rc, 64))
				rc.Close()
				if strings.TrimSpace(string(b)) == "application/epub+zip" {
					return "epub", nil
				}
			}
		case name == "meta-inf/container.xml":
			return "epub", nil
		case strings.HasSuffix(name, ".fb2"):
			return "fbz", nil
		case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"), strings.HasSuffix(name, ".png"),
			strings.HasSuffix(name, ".webp"), strings.HasSuffix(name, ".gif"):
			images++
		}
	}
	if images > 0 && (ext == "cbz" || ext == "zip") {
		return "cbz", nil
	}
	return "", errors.New("zip sin libro reconocible")
}

// commit mueve el temporal a su sitio definitivo. Si el fichero ya estaba
// (otra persona subió el mismo libro), se sustituye por una copia idéntica.
func (f files) commit(u *upload, b Book) error {
	if err := os.Rename(u.tmp, f.bookPath(b)); err != nil {
		os.Remove(u.tmp)
		return err
	}
	return nil
}

// saveCover guarda la portada si es una imagen reconocible.
func (f files) saveCover(b Book, r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, maxCoverBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxCoverBytes {
		return errors.New("portada demasiado grande")
	}
	switch http.DetectContentType(data) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return errors.New("la portada no es una imagen")
	}
	return os.WriteFile(f.coverPath(b), data, 0o600)
}

func (f files) hasCover(b Book) bool {
	_, err := os.Stat(f.coverPath(b))
	return err == nil
}

// remove borra el fichero y la portada; quien llama comprueba antes que
// ningún otro libro (de otra persona) los usa.
func (f files) remove(b Book) {
	os.Remove(f.bookPath(b))
	os.Remove(f.coverPath(b))
}

var contentTypes = map[string]string{
	"epub": "application/epub+zip",
	"pdf":  "application/pdf",
	"mobi": "application/x-mobipocket-ebook",
	"azw3": "application/vnd.amazon.ebook",
	"fb2":  "application/x-fictionbook+xml",
	"fbz":  "application/x-zip-compressed-fb2",
	"cbz":  "application/vnd.comicbook+zip",
}
