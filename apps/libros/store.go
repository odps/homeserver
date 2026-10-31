package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Book son los metadatos de un libro. Los ficheros viven en DATA_DIR.
type Book struct {
	ID          int64      `json:"id"`
	SHA256      string     `json:"sha256"`
	Format      string     `json:"format"`
	FileName    string     `json:"file_name"`
	Size        int64      `json:"size"`
	Title       string     `json:"title"`
	Authors     []string   `json:"authors"`
	Genres      []string   `json:"genres"`
	Series      *string    `json:"series"`
	SeriesIndex *float32   `json:"series_index"`
	Language    *string    `json:"language"`
	Publisher   *string    `json:"publisher"`
	Published   *string    `json:"published"`
	Description *string    `json:"description"`
	HasCover    bool       `json:"has_cover"`
	Status      string     `json:"status"`
	AddedAt     time.Time  `json:"added_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	// Last es la posición más reciente de cualquier dispositivo.
	Last *Progress `json:"last"`
}

// Progress es dónde va un dispositivo en un libro.
type Progress struct {
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	Location   string    `json:"location"`
	Fraction   float32   `json:"fraction"`
	Label      *string   `json:"label"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var errNotFound = errors.New("no existe")

type store struct{ db *pgxpool.Pool }

const bookCols = `b.id, b.sha256, b.format, b.file_name, b.size, b.title, b.authors, b.genres,
	b.series, b.series_index, b.language, b.publisher, b.published, b.description, b.has_cover,
	b.status, b.added_at, b.updated_at, b.finished_at,
	p.device_id, p.device_name, p.location, p.fraction, p.label, p.updated_at`

// La posición más reciente de cada libro.
const lastProgress = `LEFT JOIN LATERAL (
	SELECT * FROM progress WHERE book_id = b.id ORDER BY updated_at DESC LIMIT 1
) p ON true`

func scanBook(row pgx.Row) (Book, error) {
	var b Book
	var devID, devName, loc *string
	var frac *float32
	var label *string
	var at *time.Time
	err := row.Scan(&b.ID, &b.SHA256, &b.Format, &b.FileName, &b.Size, &b.Title, &b.Authors, &b.Genres,
		&b.Series, &b.SeriesIndex, &b.Language, &b.Publisher, &b.Published, &b.Description, &b.HasCover,
		&b.Status, &b.AddedAt, &b.UpdatedAt, &b.FinishedAt,
		&devID, &devName, &loc, &frac, &label, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, errNotFound
	}
	if err != nil {
		return b, err
	}
	if devID != nil {
		b.Last = &Progress{DeviceID: *devID, DeviceName: *devName, Location: *loc, Fraction: *frac, Label: label, UpdatedAt: *at}
	}
	return b, nil
}

type listQuery struct {
	Text   string
	Status string
	Sort   string // recent (último leído), added, title, author
	Limit  int
}

func (s store) list(ctx context.Context, q listQuery) ([]Book, error) {
	order := map[string]string{
		"added":  "b.added_at DESC",
		"title":  "lower(b.title)",
		"author": "lower(coalesce(b.authors[1], '')), lower(b.title)",
	}[q.Sort]
	if order == "" {
		order = "coalesce(p.updated_at, b.added_at) DESC"
	}
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 1000
	}
	rows, err := s.db.Query(ctx, `SELECT `+bookCols+` FROM books b `+lastProgress+`
		WHERE ($1 = '' OR b.title ILIKE '%' || $1 || '%' OR array_to_string(b.authors, ' ') ILIKE '%' || $1 || '%'
		       OR array_to_string(b.genres, ' ') ILIKE '%' || $1 || '%' OR coalesce(b.series, '') ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR b.status = $2)
		ORDER BY `+order+` LIMIT $3`, q.Text, q.Status, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	books := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

func (s store) get(ctx context.Context, id int64) (Book, error) {
	return scanBook(s.db.QueryRow(ctx, `SELECT `+bookCols+` FROM books b `+lastProgress+` WHERE b.id = $1`, id))
}

func (s store) bySHA(ctx context.Context, sha string) (Book, error) {
	return scanBook(s.db.QueryRow(ctx, `SELECT `+bookCols+` FROM books b `+lastProgress+` WHERE b.sha256 = $1`, sha))
}

// insert guarda un libro nuevo. Los metadatos ya vienen saneados.
func (s store) insert(ctx context.Context, b Book) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO books
		(sha256, format, file_name, size, title, authors, genres, series, series_index,
		 language, publisher, published, description, has_cover)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
		b.SHA256, b.Format, b.FileName, b.Size, b.Title, b.Authors, b.Genres, b.Series, b.SeriesIndex,
		b.Language, b.Publisher, b.Published, b.Description, b.HasCover).Scan(&id)
	return id, err
}

// update cambia los metadatos editables y el estado.
func (s store) update(ctx context.Context, b Book) error {
	tag, err := s.db.Exec(ctx, `UPDATE books SET title = $2, authors = $3, genres = $4, series = $5,
		series_index = $6, language = $7, publisher = $8, published = $9, description = $10,
		status = $11, updated_at = now(),
		finished_at = CASE WHEN $11 = 'leido' THEN coalesce(finished_at, now()) ELSE NULL END
		WHERE id = $1`,
		b.ID, b.Title, b.Authors, b.Genres, b.Series, b.SeriesIndex, b.Language, b.Publisher,
		b.Published, b.Description, b.Status)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) setStatus(ctx context.Context, id int64, status string) error {
	tag, err := s.db.Exec(ctx, `UPDATE books SET status = $2, updated_at = now(),
		finished_at = CASE WHEN $2 = 'leido' THEN coalesce(finished_at, now()) ELSE NULL END
		WHERE id = $1`, id, status)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) delete(ctx context.Context, id int64) (Book, error) {
	b, err := s.get(ctx, id)
	if err != nil {
		return b, err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM books WHERE id = $1`, id)
	return b, err
}

// saveProgress guarda la posición de un dispositivo si es más reciente que la
// que había (las colas sin conexión pueden llegar desordenadas). Un libro
// pendiente pasa a «leyendo». Devuelve la posición más reciente del libro.
func (s store) saveProgress(ctx context.Context, bookID int64, p Progress) (*Progress, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO progress (book_id, device_id, device_name, location, fraction, label, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (book_id, device_id) DO UPDATE SET device_name = excluded.device_name,
				location = excluded.location, fraction = excluded.fraction, label = excluded.label,
				updated_at = excluded.updated_at
			WHERE progress.updated_at < excluded.updated_at`,
			bookID, p.DeviceID, p.DeviceName, p.Location, p.Fraction, p.Label, p.UpdatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err = tx.Exec(ctx, `UPDATE books SET status = 'leyendo' WHERE id = $1 AND status = 'pendiente'`, bookID)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	b, err := s.get(ctx, bookID)
	return b.Last, err
}

func (s store) devices(ctx context.Context, bookID int64) ([]Progress, error) {
	rows, _ := s.db.Query(ctx, `SELECT device_id, device_name, location, fraction, label, updated_at
		FROM progress WHERE book_id = $1 ORDER BY updated_at DESC`, bookID)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Progress])
}
