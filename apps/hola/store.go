package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Saludo struct {
	ID     int64     `json:"id"`
	Texto  string    `json:"texto"`
	Creado time.Time `json:"creado"`
}

type store struct{ db *pgxpool.Pool }

func (s store) add(ctx context.Context, texto string) (Saludo, error) {
	rows, _ := s.db.Query(ctx, "INSERT INTO saludos (texto) VALUES ($1) RETURNING id, texto, creado", texto)
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Saludo])
}

func (s store) list(ctx context.Context, limit int) ([]Saludo, error) {
	rows, _ := s.db.Query(ctx, "SELECT id, texto, creado FROM saludos ORDER BY id DESC LIMIT $1", limit)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Saludo])
}
