// updater mantiene el servidor al día: consulta la última Release de GitHub,
// la descarga, verifica su checksum, aprovisiona las apps nuevas (rol, schema y
// servicio runit), hace una copia de la base de datos, cambia de versión y
// comprueba /health de cada app. Si algo falla, vuelve a la versión anterior.
//
// Configuración por variables de entorno:
//
//	REPO                owner/nombre del repositorio en GitHub (obligatoria)
//	ADMIN_DATABASE_URL  conexión de superusuario para aprovisionar (obligatoria)
//	SRV_DIR             directorio base (por defecto ~/srv)
//	SVDIR               directorio de servicios runit (por defecto $PREFIX/var/service)
//	INTERVAL            intervalo entre consultas (por defecto 2m)
//	GITHUB_TOKEN        opcional, para repositorios privados o más cuota
//
// Estructura de SRV_DIR:
//
//	releases/<tag>/   bin/, apps.json, VERSION
//	current           enlace a la release activa
//	env/<app>.env     DATABASE_URL de cada app (permisos 600)
//	data/<app>/       ficheros de cada app (DATA_DIR)
//	backups/          pg_dump antes de cada cambio de versión
//	state/bad         tags que fallaron y no se reintentan
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "homeserver/platform/netfix"
	"homeserver/platform/version"
)

type config struct {
	Repo     string
	AdminDSN string
	Token    string
	SrvDir   string
	SvDir    string
	Prefix   string
	Interval time.Duration
}

func loadConfig() (config, error) {
	home, _ := os.UserHomeDir()
	prefix := env("PREFIX", "/data/data/com.termux/files/usr")
	c := config{
		Repo:     os.Getenv("REPO"),
		AdminDSN: os.Getenv("ADMIN_DATABASE_URL"),
		Token:    os.Getenv("GITHUB_TOKEN"),
		SrvDir:   env("SRV_DIR", filepath.Join(home, "srv")),
		SvDir:    env("SVDIR", filepath.Join(prefix, "var", "service")),
		Prefix:   prefix,
	}
	var err error
	if c.Interval, err = time.ParseDuration(env("INTERVAL", "2m")); err != nil {
		return c, err
	}
	if c.Repo == "" || c.AdminDSN == "" {
		return c, errors.New("faltan REPO o ADMIN_DATABASE_URL")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := loadConfig()
	if err != nil {
		log.Error("configuración", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	u := newUpdater(cfg, log)
	log.Info("arrancado", "version", version.Version, "repo", cfg.Repo, "actual", u.currentTag())

	// Al arrancar, deja la versión actual en marcha: apps aprovisionadas,
	// servicios creados y arrancados.
	if err := u.reconcile(ctx); err != nil {
		log.Error("reconciliar la versión actual", "err", err)
	}

	for {
		restart, err := u.tick(ctx)
		if err != nil {
			log.Error("actualización", "err", err)
		}
		if restart {
			log.Info("hay un updater nuevo; salgo para que runit lo arranque")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.Interval):
		}
	}
}
