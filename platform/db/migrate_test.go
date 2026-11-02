package db_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/db"
	"homeserver/platform/dbtest"
)

func TestMigrateAppliesOnceInOrder(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	files := fstest.MapFS{
		"m/002_insert.sql": {Data: []byte("INSERT INTO t VALUES (1)")},
		"m/001_create.sql": {Data: []byte("CREATE TABLE t (n int)")},
	}
	for range 2 {
		if err := db.Migrate(ctx, pool, schema, files, "m"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("filas en t = %d, quiero 1", n)
	}
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	files := fstest.MapFS{
		"m/001_ok.sql":  {Data: []byte("CREATE TABLE a (n int)")},
		"m/002_bad.sql": {Data: []byte("CREATE TABLE b (n int); SELECT no_existe()")},
	}
	if err := db.Migrate(ctx, pool, schema, files, "m"); err == nil {
		t.Fatal("quería un error")
	}
	var b *string
	pool.QueryRow(ctx, "SELECT to_regclass('b')::text").Scan(&b)
	if b != nil {
		t.Fatal("la tabla b no debería existir tras el fallo")
	}
	var v int
	pool.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&v)
	if v != 1 {
		t.Fatalf("última migración = %d, quiero 1", v)
	}
}

func TestMigrateRejectsBadNames(t *testing.T) {
	_, pool := dbtest.Schema(t)
	files := fstest.MapFS{"m/sin_numero.sql": {Data: []byte("SELECT 1")}}
	if err := db.Migrate(context.Background(), pool, "x", files, "m"); err == nil {
		t.Fatal("quería un error por el nombre")
	}
}

func TestMigrateSteps(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	files := fstest.MapFS{
		"m/001_create.sql": {Data: []byte("CREATE TABLE t (n int); INSERT INTO t VALUES (1), (2)")},
		"m/002_double.sql": {Data: []byte("ALTER TABLE t ADD COLUMN d int")},
	}
	runs := 0
	steps := map[int]db.Step{2: {Run: func(ctx context.Context, tx pgx.Tx) error {
		runs++
		_, err := tx.Exec(ctx, "UPDATE t SET d = n * 2")
		return err
	}, Vacuum: []string{"t"}}}
	for range 2 {
		if err := db.MigrateSteps(ctx, pool, schema, files, "m", steps); err != nil {
			t.Fatal(err)
		}
	}
	var sum int
	pool.QueryRow(ctx, "SELECT sum(d) FROM t").Scan(&sum)
	if runs != 1 || sum != 6 {
		t.Fatalf("pasos = %d, suma = %d", runs, sum)
	}

	// Si el paso falla, tampoco queda el SQL de su migración.
	_, pool2 := dbtest.Schema(t)
	bad := map[int]db.Step{2: {Run: func(context.Context, pgx.Tx) error { return errors.New("no") }}}
	if err := db.MigrateSteps(ctx, pool2, "x", files, "m", bad); err == nil {
		t.Fatal("quería un error")
	}
	var col *string
	pool2.QueryRow(ctx, "SELECT column_name::text FROM information_schema.columns WHERE table_name = 't' AND column_name = 'd' AND table_schema = current_schema()").Scan(&col)
	if col != nil {
		t.Fatal("la columna d no debería existir tras el fallo del paso")
	}
	if err := db.MigrateSteps(ctx, pool2, "x", files, "m", map[int]db.Step{7: {}}); err == nil {
		t.Fatal("quería un error por un paso sin fichero")
	}
}
