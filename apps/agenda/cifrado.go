package main

import (
	"context"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/db"
	sealpkg "homeserver/platform/seal"
)

// steps son las migraciones en Go de la agenda.
func steps(box *sealpkg.Box) map[int]db.Step {
	return map[int]db.Step{
		// 002: cifra lo que ya había en claro (la 003 quita esas columnas y
		// luego se compactan las tablas para que no quede en disco).
		2: {Run: func(ctx context.Context, tx pgx.Tx) error { return sealExisting(ctx, tx, box) },
			Vacuum: []string{"users", "objects", "contacts", "notices"}},
		// 004: busca envíos en el correo que ya estaba guardado.
		4: {Run: func(ctx context.Context, tx pgx.Tx) error { return backfillShipments(ctx, tx, box) }},
	}
}

func sealExisting(ctx context.Context, tx pgx.Tx, box *sealpkg.Box) error {
	batch := &pgx.Batch{}

	rows, _ := tx.Query(ctx, `SELECT username, prefs::text FROM users`)
	var user, raw string
	if _, err := pgx.ForEachRow(rows, []any{&user, &raw}, func() error {
		batch.Queue(`UPDATE users SET prefs_sealed = $2 WHERE username = $1`, user, box.Seal(sealPrefs, []byte(raw)))
		return nil
	}); err != nil {
		return err
	}

	rows, _ = tx.Query(ctx, `SELECT id, data, summary, location, description FROM objects`)
	var id int64
	var data, summary, location, description string
	if _, err := pgx.ForEachRow(rows, []any{&id, &data, &summary, &location, &description}, func() error {
		batch.Queue(`UPDATE objects SET sealed = $2, tokens = $3 WHERE id = $1`, id,
			box.SealString(sealObject, data), objectTokens(box, summary, location, description))
		return nil
	}); err != nil {
		return err
	}

	rows, _ = tx.Query(ctx, `SELECT id, data, birthday, search FROM contacts`)
	var birthday, search string
	if _, err := pgx.ForEachRow(rows, []any{&id, &data, &birthday, &search}, func() error {
		batch.Queue(`UPDATE contacts SET sealed = $2, tokens = $3, birthday_token = $4 WHERE id = $1`, id,
			box.SealString(sealCard, data), box.Index(nil, "t", search), birthdayToken(box, birthday))
		return nil
	}); err != nil {
		return err
	}

	rows, _ = tx.Query(ctx, `SELECT id, text FROM notices`)
	var text string
	if _, err := pgx.ForEachRow(rows, []any{&id, &text}, func() error {
		batch.Queue(`UPDATE notices SET sealed = $2 WHERE id = $1`, id, box.SealString(sealNotice, text))
		return nil
	}); err != nil {
		return err
	}
	return tx.SendBatch(ctx, batch).Close()
}
