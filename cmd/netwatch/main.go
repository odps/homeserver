// netwatch vigila la conexión del servidor a la red local. Si deja de llegar
// a la puerta de enlace durante un rato, reinicia la wifi. Hace falta en el
// móvil: con la pantalla apagada, Android puede tardar muchos minutos en
// reconectar tras un corte del router, o quedarse «conectado» sin rutas.
//
// Configuración por variables de entorno:
//
//	GATEWAY     IP a la que hacer ping (por defecto 192.168.1.1)
//	INTERVAL    intervalo entre comprobaciones (por defecto 30s)
//	FAILS       fallos seguidos antes de actuar (por defecto 4)
//	WIFI_CMD    orden para apagar/encender la wifi (por defecto termux-wifi-enable)
//	EVENTS      fichero JSONL de eventos (por defecto ~/srv/state/netwatch.jsonl)
//
// Entre reinicios de la wifi espera cada vez más (5, 10, 20 y 30 min) para no
// pelearse con un router caído; el contador vuelve a cero al recuperar la red.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	firstBackoff = 5 * time.Minute
	maxBackoff   = 30 * time.Minute
)

type watcher struct {
	gateway  string
	wifiCmd  string
	events   string
	failsMax int
	log      *slog.Logger
	// reach y toggle son sustituibles en los tests.
	reach  func(context.Context) bool
	toggle func(context.Context) error

	fails   int
	downAt  time.Time // inicio del corte actual; cero si hay red
	toggled time.Time // último reinicio de la wifi
	backoff time.Duration
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	home, _ := os.UserHomeDir()
	interval, err := time.ParseDuration(env("INTERVAL", "30s"))
	if err != nil {
		log.Error("INTERVAL", "err", err)
		os.Exit(1)
	}
	failsMax, err := strconv.Atoi(env("FAILS", "4"))
	if err != nil || failsMax < 1 {
		log.Error("FAILS no válido")
		os.Exit(1)
	}
	w := &watcher{
		gateway:  env("GATEWAY", "192.168.1.1"),
		wifiCmd:  env("WIFI_CMD", "termux-wifi-enable"),
		events:   env("EVENTS", filepath.Join(home, "srv", "state", "netwatch.jsonl")),
		failsMax: failsMax,
		log:      log,
	}
	w.reach, w.toggle = w.ping, w.restartWifi
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("vigilando", "gateway", w.gateway, "intervalo", interval, "fallos", failsMax)

	for {
		w.check(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (w *watcher) check(ctx context.Context, now time.Time) {
	if w.reach(ctx) {
		if !w.downAt.IsZero() {
			d := now.Sub(w.downAt).Round(time.Second)
			w.log.Info("red recuperada", "corte", d)
			w.event(now, "recuperada", map[string]any{"corte_s": int(d.Seconds())})
		}
		w.fails, w.downAt, w.backoff = 0, time.Time{}, 0
		return
	}

	w.fails++
	if w.downAt.IsZero() {
		w.downAt = now
		w.log.Warn("sin respuesta de la puerta de enlace", "gateway", w.gateway)
	}
	if w.fails < w.failsMax || now.Sub(w.toggled) < w.backoff {
		return
	}

	w.log.Warn("reinicio la wifi", "fallos", w.fails, "sin_red", now.Sub(w.downAt).Round(time.Second))
	err := w.toggle(ctx)
	detail := map[string]any{"fallos": w.fails, "sin_red_s": int(now.Sub(w.downAt).Seconds())}
	if err != nil {
		w.log.Error("reiniciar la wifi", "err", err)
		detail["error"] = err.Error()
	}
	w.event(now, "wifi_reiniciada", detail)
	w.toggled = now
	w.backoff = min(max(w.backoff*2, firstBackoff), maxBackoff)
}

// ping hace hasta dos pings a la puerta de enlace.
func (w *watcher) ping(ctx context.Context) bool {
	for range 2 {
		if exec.CommandContext(ctx, "ping", "-c", "1", "-W", "3", w.gateway).Run() == nil {
			return true
		}
	}
	return false
}

func (w *watcher) restartWifi(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, w.wifiCmd, "false").CombinedOutput(); err != nil {
		return &cmdError{err, out}
	}
	time.Sleep(5 * time.Second)
	if out, err := exec.CommandContext(ctx, w.wifiCmd, "true").CombinedOutput(); err != nil {
		return &cmdError{err, out}
	}
	return nil
}

// event deja constancia para avisos futuros (por ejemplo, el bot de Telegram).
func (w *watcher) event(now time.Time, kind string, detail map[string]any) {
	f, err := os.OpenFile(w.events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		w.log.Error("guardar evento", "err", err)
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(map[string]any{"time": now.Format(time.RFC3339), "event": kind, "detail": detail})
}

type cmdError struct {
	err error
	out []byte
}

func (e *cmdError) Error() string { return e.err.Error() + ": " + string(e.out) }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
