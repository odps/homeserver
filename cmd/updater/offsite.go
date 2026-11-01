package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"homeserver/platform/version"
)

// Copias completas para sacar del móvil: base de datos entera (pg_dump),
// ficheros de las apps, estado, entornos y el nodo de Tailscale, en un
// tar.gz cifrado con age para la clave pública que se elige en el panel. La
// clave privada no está en el móvil: quien lo robe no puede leer las copias.
// El PC las recoge por SSH (deploy/pc/recoger-copias.ps1) y deja una marca.
//
//	state/copias.json         configuración (la escribe el panel)
//	state/copias-estado.json  resultado de la última copia (lo lee el panel)
//	state/copia-ahora         si existe, se hace una copia en la próxima vuelta
//	backups/copias/*.age      las copias; .recogida es la marca del PC

type offsiteConfig struct {
	Recipient  string `json:"recipient"` // clave pública age1…
	Hour       int    `json:"hour"`      // hora (Madrid) a partir de la que se hace la del día
	KeepDaily  int    `json:"keep_daily"`
	KeepWeekly int    `json:"keep_weekly"`
}

type offsiteStatus struct {
	LastTry time.Time `json:"last_try"`
	LastOK  time.Time `json:"last_ok"`
	File    string    `json:"file,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Took    string    `json:"took,omitempty"`
	Error   string    `json:"error,omitempty"`
}

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

func (u *updater) offsiteConfig() (offsiteConfig, bool) {
	c := offsiteConfig{Hour: 4, KeepDaily: 7, KeepWeekly: 4}
	b, err := os.ReadFile(u.path("state", "copias.json"))
	if err != nil || json.Unmarshal(b, &c) != nil || c.Recipient == "" {
		return c, false
	}
	return c, true
}

func (u *updater) offsiteStatus() offsiteStatus {
	var s offsiteStatus
	if b, err := os.ReadFile(u.path("state", "copias-estado.json")); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

// maybeOffsite hace la copia del día si toca (o si se ha pedido desde el
// panel). Tras un fallo espera media hora antes de reintentar.
func (u *updater) maybeOffsite(ctx context.Context, now time.Time) {
	c, ok := u.offsiteConfig()
	if !ok {
		return
	}
	s := u.offsiteStatus()
	_, err := os.Stat(u.path("state", "copia-ahora"))
	asked := err == nil
	local := now.In(madrid)
	due := local.Hour() >= c.Hour && s.LastOK.In(madrid).Format("2006-01-02") != local.Format("2006-01-02") &&
		now.Sub(s.LastTry) > 30*time.Minute
	if !asked && !due {
		return
	}
	os.Remove(u.path("state", "copia-ahora"))
	s.LastTry = now
	start := time.Now()
	file, size, err := u.offsite(ctx, c, now)
	if err != nil {
		s.Error = err.Error()
		u.log.Error("copia", "err", err)
	} else {
		s.LastOK, s.File, s.Size, s.Error = now, filepath.Base(file), size, ""
		s.Took = time.Since(start).Round(time.Second).String()
		u.log.Info("copia hecha", "fichero", s.File, "bytes", size, "tardó", s.Took)
		u.pruneOffsite(c)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	writeFile(u.path("state", "copias-estado.json"), string(b), 0o600)
}

// offsite escribe la copia cifrada y devuelve su ruta y tamaño.
func (u *updater) offsite(ctx context.Context, c offsiteConfig, now time.Time) (string, int64, error) {
	rcpt, err := age.ParseX25519Recipient(c.Recipient)
	if err != nil {
		return "", 0, fmt.Errorf("clave pública no válida: %w", err)
	}
	dir := u.path("backups", "copias")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	dump := filepath.Join(dir, ".homeserver.dump.tmp")
	defer os.Remove(dump)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--file="+dump, "--dbname="+u.cfg.AdminDSN).CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("pg_dump: %v: %s", err, out)
	}

	final := filepath.Join(dir, "homeserver-"+now.In(madrid).Format("20060102-1504")+".tar.gz.age")
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp)
	err = writeArchive(ctx, f, rcpt, dump, u.cfg.SrvDir, now)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return "", 0, err
	}
	return final, st.Size(), os.Rename(tmp, final)
}

// Lo que entra en la copia, relativo a SRV_DIR. releases/ se baja de GitHub
// y backups/ son las propias copias.
var offsiteDirs = []string{"data", "state", "env", "tailscale"}

func writeArchive(ctx context.Context, w io.Writer, rcpt age.Recipient, dump, srv string, now time.Time) error {
	enc, err := age.Encrypt(w, rcpt)
	if err != nil {
		return err
	}
	// Los libros y las imágenes ya van comprimidos: compresión rápida.
	gz, _ := gzip.NewWriterLevel(enc, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	readme := fmt.Sprintf("Copia de homeserver del %s (versión %s).\n\n"+
		"homeserver.dump: toda la base de datos (pg_restore).\n"+
		"data/, state/, env/, tailscale/: el contenido de ~/srv con esos nombres.\n"+
		"Cómo restaurar: RUNBOOK.md, apartado «Copias de seguridad».\n", now.In(madrid).Format("2006-01-02 15:04"), version.Version)
	if err := addBytes(tw, "LEEME.txt", []byte(readme), now); err != nil {
		return err
	}
	if err := addFile(tw, dump, "homeserver.dump"); err != nil {
		return err
	}
	for _, d := range offsiteDirs {
		root := filepath.Join(srv, d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Solo ficheros normales: ni sockets ni enlaces; los .tmp están a medio escribir.
			if !e.Type().IsRegular() || strings.HasSuffix(p, ".tmp") {
				return nil
			}
			rel, _ := filepath.Rel(srv, p)
			return addFile(tw, p, filepath.ToSlash(rel))
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return enc.Close()
}

func addBytes(tw *tar.Writer, name string, b []byte, mod time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: mod}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // borrado mientras se copiaba
		}
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(st.Mode().Perm()), Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	// Si el fichero crece mientras se lee, se copia solo lo que medía.
	_, err = io.CopyN(tw, f, st.Size())
	return err
}

// pruneOffsite deja las últimas KeepDaily copias de días distintos y la más
// reciente de cada una de las últimas KeepWeekly semanas.
func (u *updater) pruneOffsite(c offsiteConfig) {
	files, _ := filepath.Glob(u.path("backups", "copias", "homeserver-*.tar.gz.age"))
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // el nombre empieza por la fecha
	keep := map[string]bool{}
	days, weeks := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		t, err := time.ParseInLocation("20060102-1504", strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "homeserver-"), ".tar.gz.age"), madrid)
		if err != nil {
			continue
		}
		day := t.Format("2006-01-02")
		y, w := t.ISOWeek()
		week := fmt.Sprintf("%d-%02d", y, w)
		if !days[day] && len(days) < c.KeepDaily {
			days[day], keep[f] = true, true
		}
		if !weeks[week] && len(weeks) < c.KeepWeekly {
			weeks[week], keep[f] = true, true
		}
	}
	for _, f := range files {
		if !keep[f] {
			os.Remove(f)
		}
	}
}
