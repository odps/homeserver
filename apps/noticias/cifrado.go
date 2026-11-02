package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/db"
	"homeserver/platform/seal"
)

// Contextos de cifrado: lo de una columna no se descifra como otra.
const (
	sealTopic  = "noticias/topics"
	sealFeed   = "noticias/own_feeds"
	sealDigest = "noticias/digests"
)

// topicData es lo cifrado de un interés.
type topicData struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

// digestData es lo cifrado de un digest (el día y la hora quedan en claro).
type digestData struct {
	Summary  string          `json:"summary"`
	Blocks   []summaryBlock  `json:"blocks,omitempty"`
	Sections []digestSection `json:"sections"`
}

// topicToken es la huella del nombre de un interés: sin mayúsculas ni
// tildes, para no tener «Política» y «politica» a la vez.
func topicToken(box *seal.Box, name string) int64 {
	return box.Token("tema", seal.Fold(strings.TrimSpace(name)))
}

func feedToken(box *seal.Box, url string) int64 { return box.Token("feed", url) }

// feedKey identifica un feed en items y feeds sin guardar su URL.
func feedKey(box *seal.Box, url string) int64 { return box.Token("feed-url", url) }

const sealFeedState = "noticias/feeds"

// feedData es lo cifrado del estado de un feed.
type feedData struct {
	Title string `json:"title,omitempty"`
	Error string `json:"error,omitempty"`
}

// sealFeedKeys pasa items y feeds de la URL a su huella (paso 005).
func sealFeedKeys(ctx context.Context, tx pgx.Tx, box *seal.Box) error {
	batch := &pgx.Batch{}
	var url, title, msg string
	rows, _ := tx.Query(ctx, `SELECT DISTINCT feed FROM items`)
	if _, err := pgx.ForEachRow(rows, []any{&url}, func() error {
		batch.Queue(`UPDATE items SET feed_key = $2 WHERE feed = $1`, url, feedKey(box, url))
		return nil
	}); err != nil {
		return err
	}
	rows, _ = tx.Query(ctx, `SELECT url, title, error FROM feeds`)
	if _, err := pgx.ForEachRow(rows, []any{&url, &title, &msg}, func() error {
		sealed, err := box.SealJSON(sealFeedState, feedData{Title: title, Error: msg})
		if err != nil {
			return err
		}
		batch.Queue(`UPDATE feeds SET key = $2, sealed = $3 WHERE url = $1`, url, feedKey(box, url), sealed)
		return nil
	}); err != nil {
		return err
	}
	return tx.SendBatch(ctx, batch).Close()
}

// steps son las migraciones en Go de noticias.
func steps(box *seal.Box) map[int]db.Step {
	return map[int]db.Step{
		// 003: cifra lo que había en claro; la 004 quita esas columnas y luego
		// se compactan las tablas para que no quede en disco.
		3: {Run: func(ctx context.Context, tx pgx.Tx) error { return sealExisting(ctx, tx, box) },
			Vacuum: []string{"topics", "own_feeds", "digests"}},
		// 005: los feeds, por su huella; la 006 quita las URL.
		5: {Run: func(ctx context.Context, tx pgx.Tx) error { return sealFeedKeys(ctx, tx, box) },
			Vacuum: []string{"items", "feeds"}},
	}
}

func sealExisting(ctx context.Context, tx pgx.Tx, box *seal.Box) error {
	batch := &pgx.Batch{}
	var id int64
	var user, name, query string
	taken := map[string]bool{}
	rows, _ := tx.Query(ctx, `SELECT id, username, name, query FROM topics ORDER BY id`)
	if _, err := pgx.ForEachRow(rows, []any{&id, &user, &name, &query}, func() error {
		sealed, err := box.SealJSON(sealTopic, topicData{Name: name, Query: query})
		if err != nil {
			return err
		}
		// Antes «Golang» y «golang» podían convivir: el segundo se queda con
		// una huella propia hasta que se renombre.
		tok := topicToken(box, name)
		if k := user + "\x00" + strconv.FormatInt(tok, 10); taken[k] {
			tok = box.Token("tema-id", strconv.FormatInt(id, 10))
		} else {
			taken[k] = true
		}
		batch.Queue(`UPDATE topics SET sealed = $2, name_token = $3 WHERE id = $1`, id, sealed, tok)
		return nil
	}); err != nil {
		return err
	}

	var url string
	rows, _ = tx.Query(ctx, `SELECT id, url FROM own_feeds`)
	if _, err := pgx.ForEachRow(rows, []any{&id, &url}, func() error {
		batch.Queue(`UPDATE own_feeds SET sealed = $2, url_token = $3 WHERE id = $1`, id, box.SealString(sealFeed, url), feedToken(box, url))
		return nil
	}); err != nil {
		return err
	}

	var day, content, summary string
	rows, _ = tx.Query(ctx, `SELECT username, day::text, content::text, summary FROM digests`)
	if _, err := pgx.ForEachRow(rows, []any{&user, &day, &content, &summary}, func() error {
		d := digestData{Summary: summary}
		if err := json.Unmarshal([]byte(content), &d.Sections); err != nil {
			return err
		}
		sealed, err := box.SealJSON(sealDigest, d)
		if err != nil {
			return err
		}
		batch.Queue(`UPDATE digests SET sealed = $3 WHERE username = $1 AND day = $2::date`, user, day, sealed)
		return nil
	}); err != nil {
		return err
	}
	return tx.SendBatch(ctx, batch).Close()
}
