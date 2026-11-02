// Package app arranca una app de la plataforma: base de datos con su schema,
// migraciones, HTTP con /health, tools MCP en /mcp y parada ordenada.
//
// Configuración por variables de entorno:
//
//	DATABASE_URL  conexión con el rol de la app (obligatoria)
//	ADDR          dirección HTTP, por ejemplo "127.0.0.1:8101" (por defecto
//	              127.0.0.1:8080): solo local, se accede por la pasarela
//	DATA_DIR      carpeta para los ficheros de la app (la crea el updater)
package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/db"
	_ "homeserver/platform/netfix" // las apps también llaman a APIs externas
	"homeserver/platform/version"
)

// App describe una app. Name es también su schema de PostgreSQL.
type App struct {
	Name  string
	Title string
	// Migrations contiene los ficheros migrations/NNN_*.sql.
	Migrations embed.FS
	// Steps son migraciones en Go que acompañan a los ficheros (db.Step).
	Steps map[int]db.Step
	// Setup registra las rutas HTTP y las tools MCP.
	Setup func(*Env) error
}

// Env es lo que recibe Setup.
type Env struct {
	DB  *pgxpool.Pool
	Mux *http.ServeMux
	MCP *mcp.Server
	Log *slog.Logger
}

// Run arranca la app y termina el proceso cuando se detiene.
func Run(a App) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("app", a.Name)
	if err := run(a, log); err != nil {
		log.Error("detenida por error", "err", err)
		os.Exit(1)
	}
}

func run(a App, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("falta DATABASE_URL")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	pool, err := Connect(ctx, dsn, a.Name)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.MigrateSteps(ctx, pool, a.Name, a.Migrations, "migrations", a.Steps); err != nil {
		return err
	}

	env := &Env{
		DB:  pool,
		Mux: http.NewServeMux(),
		MCP: mcp.NewServer(&mcp.Implementation{Name: a.Name, Title: a.Title, Version: version.Version}, nil),
		Log: log,
	}
	if err := a.Setup(env); err != nil {
		return err
	}
	env.Mux.HandleFunc("GET /health", health(a.Name, pool))
	env.Mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return env.MCP },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))

	srv := &http.Server{Addr: addr, Handler: env.Mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("escuchando", "addr", addr, "version", version.Version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Connect abre un pool pequeño con search_path apuntando al schema de la app.
func Connect(ctx context.Context, dsn, schema string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	return pgxpool.NewWithConfig(ctx, cfg)
}

func health(name string, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		status, dbState := http.StatusOK, "ok"
		if err := pool.Ping(ctx); err != nil {
			status, dbState = http.StatusServiceUnavailable, err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"app": name, "version": version.Version, "db": dbState})
	}
}
