package main

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"homeserver/platform/db"
	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// Las conversaciones y notas en claro de antes se cifran al actualizar.
func TestSealExistingData(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	before := fstest.MapFS{}
	all, _ := fs.Glob(migrations, "migrations/*.sql")
	for _, p := range all {
		if b, _ := fs.ReadFile(migrations, p); p < "migrations/002" {
			before[p] = &fstest.MapFile{Data: b}
		}
	}
	if err := db.Migrate(ctx, pool, schema, before, "migrations"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO messages (chat_id, role, content) VALUES (7, 'user', 'Mi médico es el doctor Ruiz'), (7, 'assistant', 'Apuntado')`,
		`INSERT INTO notes (text) VALUES ('Código del trastero: azul')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	box, _ := seal.Load("")
	if err := db.MigrateSteps(ctx, pool, schema, migrations, "migrations", steps(box)); err != nil {
		t.Fatal(err)
	}
	st := store{pool, box}
	h, err := st.history(ctx, 7, 10)
	if err != nil || len(h) != 2 || h[0].Content != "Mi médico es el doctor Ruiz" {
		t.Fatalf("historial: %+v %v", h, err)
	}
	if n, err := st.notes(ctx, "trast", 5); err != nil || len(n) != 1 || n[0].Text != "Código del trastero: azul" {
		t.Fatalf("notas: %+v %v", n, err)
	}
	if n, _ := st.notes(ctx, "", 5); len(n) != 1 {
		t.Fatalf("todas las notas: %+v", n)
	}
	for _, table := range []string{"messages", "notes"} {
		var dump string
		pool.QueryRow(ctx, `SELECT string_agg(x::text, ' ') FROM `+table+` x`).Scan(&dump)
		for _, s := range []string{"Ruiz", "trastero", "Apuntado"} {
			if strings.Contains(dump, s) {
				t.Errorf("%s guarda «%s» en claro", table, s)
			}
		}
	}
}
