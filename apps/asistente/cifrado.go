package main

import (
	"context"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/db"
	"homeserver/platform/seal"
)

// Contextos de cifrado: lo de una columna no se descifra como otra.
const (
	sealTurn = "asistente/messages"
	sealNote = "asistente/notes"
)

// steps son las migraciones en Go del asistente.
func steps(box *seal.Box) map[int]db.Step {
	return map[int]db.Step{
		// 002: cifra lo que había en claro; la 003 quita esas columnas y luego
		// se compactan las tablas para que no quede en disco.
		2: {Run: func(ctx context.Context, tx pgx.Tx) error { return sealExisting(ctx, tx, box) },
			Vacuum: []string{"messages", "notes"}},
	}
}

func sealExisting(ctx context.Context, tx pgx.Tx, box *seal.Box) error {
	batch := &pgx.Batch{}
	var id int64
	var text string
	rows, _ := tx.Query(ctx, `SELECT id, content FROM messages`)
	if _, err := pgx.ForEachRow(rows, []any{&id, &text}, func() error {
		batch.Queue(`UPDATE messages SET sealed = $2 WHERE id = $1`, id, box.SealString(sealTurn, text))
		return nil
	}); err != nil {
		return err
	}
	rows, _ = tx.Query(ctx, `SELECT id, text FROM notes`)
	if _, err := pgx.ForEachRow(rows, []any{&id, &text}, func() error {
		batch.Queue(`UPDATE notes SET sealed = $2, tokens = $3 WHERE id = $1`, id, box.SealString(sealNote, text), box.Index(nil, "t", text))
		return nil
	}); err != nil {
		return err
	}
	return tx.SendBatch(ctx, batch).Close()
}
