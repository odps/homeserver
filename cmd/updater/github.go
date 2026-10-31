package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type release struct {
	Tag    string  `json:"tag_name"`
	Assets []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func (r *release) asset(name string) (asset, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return asset{}, fmt.Errorf("la release %s no tiene %s", r.Tag, name)
}

// latest consulta la última release. Usa ETag: las respuestas 304 no gastan
// cuota de la API de GitHub.
func (u *updater) latest(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+u.cfg.Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if u.etag != "" {
		req.Header.Set("If-None-Match", u.etag)
	}
	u.auth(req)
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return u.last, nil
	case http.StatusNotFound:
		return nil, nil // todavía no hay releases
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("GitHub respondió %s", resp.Status)
	}
	var r release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	u.etag, u.last = resp.Header.Get("ETag"), &r
	return &r, nil
}

func (u *updater) auth(req *http.Request) {
	if u.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	}
}

// fetch descarga el paquete de esta plataforma, verifica su SHA-256 contra
// checksums.txt y lo extrae en dir.
func (u *updater) fetch(ctx context.Context, r *release, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "apps.json")); err == nil {
		return nil // ya descargada
	}
	pkg, err := r.asset(assetName())
	if err != nil {
		return err
	}
	sums, err := r.asset("checksums.txt")
	if err != nil {
		return err
	}
	want, err := u.checksum(ctx, sums.URL, pkg.Name)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(u.cfg.SrvDir, "download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := u.get(ctx, pkg.URL, tmp); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, tmp); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum de %s: %s, esperaba %s", pkg.Name, got, want)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	staging := dir + ".tmp"
	os.RemoveAll(staging)
	if err := extract(tmp, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	return os.Rename(staging, dir)
}

func (u *updater) checksum(ctx context.Context, url, name string) (string, error) {
	var b strings.Builder
	if err := u.get(ctx, url, &b); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt no incluye %s", name)
}

func (u *updater) get(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	u.auth(req)
	resp, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("descargar %s: %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// extract descomprime un .tar.gz en dir, rechazando rutas que salgan de él.
func extract(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if !filepath.IsLocal(hdr.Name) {
			return fmt.Errorf("ruta no permitida en el paquete: %s", hdr.Name)
		}
		dst := filepath.Join(dir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}
