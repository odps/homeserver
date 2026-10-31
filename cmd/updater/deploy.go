package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

const keepReleases, keepBackups = 3, 5

type manifest struct {
	Apps []appSpec `json:"apps"`
}

type appSpec struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type updater struct {
	cfg  config
	log  *slog.Logger
	http *http.Client
	etag string
	last *release
}

func newUpdater(cfg config, log *slog.Logger) *updater {
	for _, d := range []string{"releases", "env", "backups", "state"} {
		os.MkdirAll(filepath.Join(cfg.SrvDir, d), 0o700)
	}
	return &updater{cfg: cfg, log: log, http: &http.Client{Timeout: 5 * time.Minute}}
}

func assetName() string { return "homeserver_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz" }

func (u *updater) path(p ...string) string {
	return filepath.Join(append([]string{u.cfg.SrvDir}, p...)...)
}

func (u *updater) currentTag() string {
	target, err := os.Readlink(u.path("current"))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// tick despliega la última release si es nueva. Devuelve true cuando el
// binario del updater ha cambiado y conviene reiniciarlo.
func (u *updater) tick(ctx context.Context) (bool, error) {
	r, err := u.latest(ctx)
	if err != nil || r == nil || r.Tag == u.currentTag() || u.isBad(r.Tag) {
		return false, err
	}
	u.log.Info("nueva versión", "tag", r.Tag, "actual", u.currentTag())
	if err := u.deploy(ctx, r); err != nil {
		return false, err
	}
	u.log.Info("versión desplegada", "tag", r.Tag)
	u.prune()
	return u.updaterChanged(), nil
}

func (u *updater) deploy(ctx context.Context, r *release) error {
	if !filepath.IsLocal(r.Tag) || strings.ContainsAny(r.Tag, `/\`) {
		return fmt.Errorf("tag no válido: %q", r.Tag)
	}
	if err := u.fetch(ctx, r, u.path("releases", r.Tag)); err != nil {
		return err
	}
	m, err := u.manifest(r.Tag)
	if err != nil {
		return err
	}
	if err := u.ensureApps(ctx, m); err != nil {
		return err
	}
	prev := u.currentTag()
	if prev != "" {
		if err := u.backup(ctx, prev); err != nil {
			return fmt.Errorf("copia de seguridad: %w", err)
		}
	}
	if err := u.switchTo(r.Tag); err != nil {
		return err
	}
	err = u.restartAndCheck(ctx, m, r.Tag)
	if err == nil {
		return nil
	}

	u.markBad(r.Tag)
	if prev == "" {
		return err
	}
	u.log.Error("la versión nueva falla; vuelvo a la anterior", "tag", r.Tag, "anterior", prev, "err", err)
	if serr := u.switchTo(prev); serr != nil {
		return errors.Join(err, serr)
	}
	if pm, merr := u.manifest(prev); merr == nil {
		// Las apps que solo existen en la versión descartada se paran: sin su
		// binario, runit las reiniciaría sin fin.
		for _, a := range m.Apps {
			if !slices.ContainsFunc(pm.Apps, func(p appSpec) bool { return p.Name == a.Name }) {
				u.sv(ctx, "down", a.Name)
			}
		}
		if rerr := u.restartAndCheck(ctx, pm, prev); rerr != nil {
			return errors.Join(err, fmt.Errorf("la versión anterior tampoco responde: %w", rerr))
		}
	}
	return fmt.Errorf("%s descartada, sigue %s: %w", r.Tag, prev, err)
}

// reconcile deja la versión actual aprovisionada y en marcha.
func (u *updater) reconcile(ctx context.Context) error {
	tag := u.currentTag()
	if tag == "" {
		return nil
	}
	m, err := u.manifest(tag)
	if err != nil {
		return err
	}
	if err := u.ensureApps(ctx, m); err != nil {
		return err
	}
	for _, a := range m.Apps {
		if err := u.sv(ctx, "up", a.Name); err != nil {
			return err
		}
	}
	return u.checkAll(ctx, m, tag)
}

func (u *updater) manifest(tag string) (manifest, error) {
	var m manifest
	b, err := os.ReadFile(u.path("releases", tag, "apps.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("apps.json de %s: %w", tag, err)
	}
	for _, a := range m.Apps {
		if !validName.MatchString(a.Name) || a.Port < 1024 || a.Port > 65535 {
			return m, fmt.Errorf("apps.json de %s: app no válida %+v", tag, a)
		}
	}
	return m, nil
}

// switchTo apunta current a la release de forma atómica.
func (u *updater) switchTo(tag string) error {
	tmp := u.path("current.tmp")
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join("releases", tag), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, u.path("current"))
}

func (u *updater) restartAndCheck(ctx context.Context, m manifest, tag string) error {
	for _, a := range m.Apps {
		if err := u.sv(ctx, "restart", a.Name); err != nil {
			return err
		}
	}
	return u.checkAll(ctx, m, tag)
}

func (u *updater) checkAll(ctx context.Context, m manifest, tag string) error {
	var errs []error
	for _, a := range m.Apps {
		if err := u.waitHealthy(ctx, a, tag); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name, err))
		}
	}
	return errors.Join(errs...)
}

// waitHealthy espera hasta 45 s a que /health responda 200 con la versión tag.
func (u *updater) waitHealthy(ctx context.Context, a appSpec, tag string) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", a.Port)
	deadline := time.Now().Add(45 * time.Second)
	last := errors.New("sin respuesta")
	for time.Now().Before(deadline) {
		if h, err := getHealth(ctx, url); err != nil {
			last = err
		} else if h.Version != tag {
			last = fmt.Errorf("responde la versión %s", h.Version)
		} else {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return last
}

type healthResp struct {
	Version string `json:"version"`
	DB      string `json:"db"`
}

func getHealth(ctx context.Context, url string) (healthResp, error) {
	var h healthResp
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(&h)
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("/health %s (db: %s)", resp.Status, h.DB)
	}
	return h, nil
}

// sv envía una orden a un servicio runit. Si el servicio es nuevo, runsvdir
// tarda unos segundos en verlo, así que espera a que su supervisor exista.
func (u *updater) sv(ctx context.Context, cmd, name string) error {
	dir := filepath.Join(u.cfg.SvDir, name)
	for i := 0; i < 15; i++ {
		if _, err := os.Stat(filepath.Join(dir, "supervise", "ok")); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	out, err := exec.CommandContext(ctx, "sv", cmd, dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sv %s %s: %v: %s", cmd, name, err, out)
	}
	return nil
}

func (u *updater) backup(ctx context.Context, tag string) error {
	file := u.path("backups", time.Now().Format("20060102-150405")+"_"+tag+".dump")
	out, err := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--file="+file, "--dbname="+u.cfg.AdminDSN).CombinedOutput()
	if err != nil {
		os.Remove(file)
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

func (u *updater) isBad(tag string) bool {
	f, err := os.Open(u.path("state", "bad"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() == tag {
			return true
		}
	}
	return false
}

func (u *updater) markBad(tag string) {
	f, err := os.OpenFile(u.path("state", "bad"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		u.log.Error("guardar tag fallido", "err", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, tag)
}

// updaterChanged indica si el updater de la versión activa no es el binario
// que está corriendo.
func (u *updater) updaterChanged() bool {
	self, err1 := os.Executable()
	next, err2 := filepath.EvalSymlinks(u.path("current", "bin", "updater"))
	if err1 != nil || err2 != nil {
		return false
	}
	self, _ = filepath.EvalSymlinks(self)
	return self != next
}

// prune borra releases y copias antiguas, sin tocar nunca la activa.
func (u *updater) prune() {
	cur := u.currentTag()
	entries, _ := os.ReadDir(u.path("releases"))
	var tags []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != cur && !strings.HasSuffix(e.Name(), ".tmp") {
			tags = append(tags, e.Name())
		}
	}
	sort.Strings(tags) // los tags empiezan por fecha y hora
	for len(tags) > keepReleases-1 {
		os.RemoveAll(u.path("releases", tags[0]))
		tags = tags[1:]
	}

	dumps, _ := filepath.Glob(u.path("backups", "*.dump"))
	slices.Sort(dumps)
	for len(dumps) > keepBackups {
		os.Remove(dumps[0])
		dumps = dumps[1:]
	}
}
