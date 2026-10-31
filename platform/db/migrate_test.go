package db_test

import (
	"context"
	"testing"
	"testing/fstest"

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
