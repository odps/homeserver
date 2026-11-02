// Package dbtest da a cada test un schema propio y temporal en el PostgreSQL de
// TEST_DATABASE_URL. Sin esa variable, los tests que lo usan se saltan.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/app"
	"homeserver/platform/db"
)

// Schema crea un schema vacío, devuelve su nombre y un pool que lo usa como
// search_path, y lo borra al terminar el test.
func Schema(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("sin TEST_DATABASE_URL")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	rand.Read(b)
	name := "test_" + hex.EncodeToString(b)

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	pool, err := app.Connect(ctx, dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE")
		admin.Close(ctx)
	})
	return name, pool
}

// Migrated es Schema más las migraciones de la app ya aplicadas.
func Migrated(t *testing.T, files fs.FS) *pgxpool.Pool {
	t.Helper()
	return MigratedSteps(t, files, nil)
}

// MigratedSteps es Migrated con los pasos en Go de la app.
func MigratedSteps(t *testing.T, files fs.FS, steps map[int]db.Step) *pgxpool.Pool {
	t.Helper()
	name, pool := Schema(t)
	if err := db.MigrateSteps(context.Background(), pool, name, files, "migrations", steps); err != nil {
		t.Fatal(err)
	}
	return pool
}
