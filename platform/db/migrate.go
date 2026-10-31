// Package db aplica las migraciones SQL de cada app dentro de su schema.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate aplica en orden los ficheros "NNN_descripcion.sql" de dir que aún no
// constan en <schema>.schema_migrations. Cada migración va en su transacción.
// El schema debe existir: lo crea el aprovisionamiento con el rol de la app.
func Migrate(ctx context.Context, pool *pgxpool.Pool, schema string, files fs.FS, dir string) error {
	migs, err := load(files, dir)
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Evita que dos instancias migren a la vez.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", schema); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", schema)

	table := pgx.Identifier{schema, "schema_migrations"}.Sanitize()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	applied := map[int]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM "+table)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO "+table+" (version, name) VALUES ($1, $2)", m.version, m.name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migración %s: %w", m.name, err)
		}
	}
	return nil
}

func load(files fs.FS, dir string) ([]migration, error) {
	names, err := fs.Glob(files, path.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	var migs []migration
	seen := map[int]string{}
	for _, p := range names {
		name := path.Base(p)
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migración %s: el nombre debe empezar por NNN_", name)
		}
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("migraciones %s y %s con el mismo número", other, name)
		}
		seen[v] = name
		body, err := fs.ReadFile(files, p)
		if err != nil {
			return nil, err
		}
		migs = append(migs, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}
