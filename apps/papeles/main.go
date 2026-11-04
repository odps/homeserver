// papeles guarda los documentos personales y sus vencimientos: DNI,
// pasaporte, carné, ITV, seguros, garantías, contratos… con sus escaneos. Se
// usa desde la web (sobre todo en el móvil) y con el asistente. Los avisos
// los manda la Agenda: tras cada cambio se le pasa la lista de caducidades.
// Todo lo de cada persona va cifrado (platform/seal), también los archivos.
package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"time"

	"homeserver/platform/app"
	"homeserver/platform/seal"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	box, err := seal.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "papeles: clave de datos:", err)
		os.Exit(1)
	}
	app.Run(app.App{Name: "papeles", Title: "Papeles", Migrations: migrations,
		Setup: func(env *app.Env) error { return setup(env, box, dir) }})
}

func setup(env *app.Env, box *seal.Box, dir string) error {
	svc := &service{store: store{env.DB, box}, files: newFileStore(dir, box), log: env.Log, agenda: newAgenda()}
	(&web{svc: svc}).routes(env.Mux)
	addTools(env.MCP, svc)
	return nil
}

type service struct {
	store  store
	files  *fileStore
	log    *slog.Logger
	agenda *agendaClient    // nil: sin agenda (tests)
	now    func() time.Time // nil: time.Now
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *service) today() time.Time { return s.clock().In(madrid) }

// saveDoc guarda el documento y avisa a la Agenda.
func (s *service) saveDoc(ctx context.Context, owner string, d doc) (int64, error) {
	id, err := s.store.saveDoc(ctx, owner, d)
	if err == nil {
		s.syncAgendaQuiet(ctx, owner)
	}
	return id, err
}

// renewDoc renueva el documento (fecha nueva o, sin ella, cada_meses) y
// avisa a la Agenda.
func (s *service) renewDoc(ctx context.Context, owner string, id int64, newDate string) (doc, error) {
	d, err := s.store.doc(ctx, owner, id)
	if err != nil {
		return d, err
	}
	if err := d.renew(newDate); err != nil {
		return d, err
	}
	if _, err := s.store.saveDoc(ctx, owner, d); err != nil {
		return d, err
	}
	s.syncAgendaQuiet(ctx, owner)
	return d, nil
}

// deleteDoc borra el documento con sus archivos (también del disco) y avisa
// a la Agenda.
func (s *service) deleteDoc(ctx context.Context, owner string, id int64) error {
	files, err := s.store.deleteDoc(ctx, owner, id)
	if err != nil {
		return err
	}
	for _, f := range files {
		s.files.remove(f)
	}
	s.syncAgendaQuiet(ctx, owner)
	return nil
}
