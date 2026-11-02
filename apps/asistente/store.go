package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

// store guarda conversaciones y notas cifradas (platform/seal); de las notas
// quedan además huellas de sus palabras para buscar.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// keepTurns es lo que se conserva de las conversaciones: el asistente solo
// usa los últimos turnos y no hace falta guardar más.
const keepTurns = 30 * 24 * time.Hour

type turn struct {
	Role    string
	Content string
}

// history devuelve los últimos turnos desde el último /nuevo, del más antiguo
// al más reciente.
func (s store) history(ctx context.Context, chat int64, limit int) ([]turn, error) {
	rows, err := s.db.Query(ctx, `SELECT role, sealed FROM (
			SELECT m.id, m.role, m.sealed FROM messages m LEFT JOIN chats c ON c.chat_id = m.chat_id
			WHERE m.chat_id = $1 AND m.created_at > coalesce(c.reset_at, '-infinity')
			ORDER BY m.id DESC LIMIT $2) t ORDER BY id`, chat, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (turn, error) {
		var t turn
		var sealed []byte
		if err := r.Scan(&t.Role, &sealed); err != nil {
			return t, err
		}
		var err error
		t.Content, err = s.box.OpenString(sealTurn, sealed)
		return t, err
	})
}

func (s store) addTurns(ctx context.Context, chat int64, turns ...turn) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, t := range turns {
			c := t.Content
			if len(c) > 20000 {
				c = c[:20000]
			}
			if _, err := tx.Exec(ctx, `INSERT INTO messages (chat_id, role, sealed) VALUES ($1, $2, $3)`, chat, t.Role, s.box.SealString(sealTurn, c)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE created_at < $1`, time.Now().Add(-keepTurns)); err != nil {
			return err
		}
		if err := s.prunePseudonyms(ctx, tx); err != nil {
			return err
		}
		if err := pruneLocations(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO chats (chat_id, reset_at, last_at) VALUES ($1, '-infinity', now())
			ON CONFLICT (chat_id) DO UPDATE SET last_at = now()`, chat)
		return err
	})
}

func (s store) reset(ctx context.Context, chat int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO chats (chat_id, reset_at) VALUES ($1, now())
		ON CONFLICT (chat_id) DO UPDATE SET reset_at = now()`, chat)
	return err
}

func (s store) lastChat(ctx context.Context) time.Time {
	var t *time.Time
	s.db.QueryRow(ctx, `SELECT max(last_at) FROM chats`).Scan(&t)
	if t == nil {
		return time.Time{}
	}
	return *t
}

// --- Notas ---

type note struct {
	ID        int64     `json:"id"`
	Text      string    `json:"texto"`
	CreatedAt time.Time `json:"fecha"`
}

func (s store) addNote(ctx context.Context, text string) (note, error) {
	if l := len([]rune(text)); l < 1 || l > 4000 {
		return note{}, errors.New("la nota debe tener entre 1 y 4000 caracteres")
	}
	n := note{Text: text}
	err := s.db.QueryRow(ctx, `INSERT INTO notes (sealed, tokens) VALUES ($1, $2) RETURNING id, created_at`,
		s.box.SealString(sealNote, text), s.box.Index(nil, "t", text)).Scan(&n.ID, &n.CreatedAt)
	return n, err
}

// notes busca en las notas por palabras, enteras o por su comienzo (sin
// texto: las más recientes).
func (s store) notes(ctx context.Context, q string, limit int) ([]note, error) {
	rows, err := s.db.Query(ctx, `SELECT id, sealed, created_at FROM notes
		WHERE tokens @> $1 ORDER BY id DESC LIMIT $2`, s.box.Query("t", q), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (note, error) {
		var n note
		var sealed []byte
		if err := r.Scan(&n.ID, &sealed, &n.CreatedAt); err != nil {
			return n, err
		}
		var err error
		n.Text, err = s.box.OpenString(sealNote, sealed)
		return n, err
	})
}

func (s store) deleteNote(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM notes WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("no existe esa nota")
	}
	return err
}

// --- Uso del modelo ---

func (s store) today(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT requests FROM usage WHERE day = current_date), 0)`).Scan(&n)
	return n, err
}

func (s store) addUsage(ctx context.Context, prompt, completion int) error {
	_, err := s.db.Exec(ctx, `INSERT INTO usage (day, requests, prompt_tokens, completion_tokens) VALUES (current_date, 1, $1, $2)
		ON CONFLICT (day) DO UPDATE SET requests = usage.requests + 1, prompt_tokens = usage.prompt_tokens + excluded.prompt_tokens,
			completion_tokens = usage.completion_tokens + excluded.completion_tokens`, prompt, completion)
	return err
}

type usageDay struct {
	Requests         int
	PromptTokens     int64
	CompletionTokens int64
}

func (s store) usageToday(ctx context.Context) (usageDay, error) {
	var u usageDay
	err := s.db.QueryRow(ctx, `SELECT requests, prompt_tokens, completion_tokens FROM usage WHERE day = current_date`).Scan(&u.Requests, &u.PromptTokens, &u.CompletionTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, nil
	}
	return u, err
}

// --- Valores sueltos ---

func (s store) offset(ctx context.Context) int64 {
	var v string
	s.db.QueryRow(ctx, `SELECT value FROM kv WHERE key = 'telegram_offset'`).Scan(&v)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s store) setOffset(ctx context.Context, n int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO kv (key, value) VALUES ('telegram_offset', $1)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(n, 10))
	return err
}
