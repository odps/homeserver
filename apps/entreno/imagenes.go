package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homeserver/platform/app"
)

// Imágenes de las fichas: se bajan de wger o de free-exercise-db la primera
// vez que se ven y se guardan en el móvil (DATA_DIR/imagenes). Así la web no
// carga nada de fuera (la CSP no lo deja) y esas webs no saben cuándo se mira
// una ficha.

var imageHosts = map[string]bool{"wger.de": true, "raw.githubusercontent.com": true}

const maxImage = 3 << 20

type imageCache struct {
	dir    string
	client *http.Client
	mu     sync.Mutex // una descarga a la vez
}

func newImageCache() *imageCache {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	return &imageCache{dir: filepath.Join(dir, "imagenes"), client: &http.Client{Timeout: 20 * time.Second}}
}

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

// get devuelve la ruta local de la imagen, bajándola si hace falta.
func (c *imageCache) get(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !imageHosts[u.Host] {
		return "", "", errors.New("imagen de un sitio no permitido")
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	ctype, ok := imageTypes[ext]
	if !ok {
		return "", "", errors.New("tipo de imagen no permitido")
	}
	sum := sha256.Sum256([]byte(raw))
	path := filepath.Join(c.dir, hex.EncodeToString(sum[:12])+ext)
	if _, err := os.Stat(path); err == nil {
		return path, ctype, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return path, ctype, nil
	}
	req, _ := http.NewRequest("GET", raw, nil)
	req.Header.Set("User-Agent", "homeserver-personal/1.0 (fichas de ejercicios)")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", errors.New("no se ha podido bajar la imagen")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return "", "", errors.New("la imagen no está disponible")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImage+1))
	if err != nil || len(b) > maxImage {
		return "", "", errors.New("la imagen es demasiado grande")
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return "", "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", "", err
	}
	return path, ctype, os.Rename(tmp, path)
}

// imagePage sirve la imagen n de la ficha de un ejercicio.
func (w *web) imagePage(rw http.ResponseWriter, r *http.Request, _ app.Identity) {
	e, ok := lookup(r.PathValue("key"))
	n, err := strconv.Atoi(r.PathValue("n"))
	if !ok || err != nil || n < 0 {
		notFound(rw)
		return
	}
	imgs := details(e).Images
	if n >= len(imgs) {
		notFound(rw)
		return
	}
	path, ctype, err := w.images.get(imgs[n].URL)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		notFound(rw)
		return
	}
	defer f.Close()
	rw.Header().Set("Content-Type", ctype)
	rw.Header().Set("Cache-Control", "private, max-age=2592000")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	io.Copy(rw, f)
}
