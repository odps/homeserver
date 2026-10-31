package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/app"
)

// Book son los metadatos de un libro tal como los ve una persona: su estado
// y su última posición son de quien pregunta. Los ficheros viven en DATA_DIR.
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
	Owner       string     `json:"owner"`
	Source      *string    `json:"source"`     // biblioteca pública de origen, p. ej. gutenberg:2000
	Visibility  string     `json:"visibility"` // privado o compartido
	AddedAt     time.Time  `json:"added_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Status      string     `json:"status"` // de quien pregunta
	FinishedAt  *time.Time `json:"finished_at"`
	// Last es la posición más reciente de quien pregunta en cualquiera de sus
	// dispositivos.
	Last *Progress `json:"last"`
	// CanEdit: dueño o admin (en libros compartidos).
	CanEdit bool `json:"can_edit"`
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
var errForbidden = errors.New("no permitido")

type store struct{ db *pgxpool.Pool }

const bookCols = `b.id, b.sha256, b.format, b.file_name, b.size, b.title, b.authors, b.genres,
	b.series, b.series_index, b.language, b.publisher, b.published, b.description, b.has_cover,
	b.owner, b.visibility, b.source, b.added_at, b.updated_at,
	coalesce(r.status, 'pendiente'), r.finished_at,
	p.device_id, p.device_name, p.location, p.fraction, p.label, p.updated_at`

// Estado y posición más reciente de la persona ($1).
const personal = `LEFT JOIN reading r ON r.book_id = b.id AND r.username = $1
	LEFT JOIN LATERAL (
		SELECT * FROM progress WHERE book_id = b.id AND username = $1 ORDER BY updated_at DESC LIMIT 1
	) p ON true`

// Lo que ve una persona: lo compartido y lo suyo.
const visible = `(b.visibility = 'compartido' OR b.owner = $1)`

func scanBook(row pgx.Row, who app.Identity) (Book, error) {
	var b Book
	var devID, devName, loc *string
	var frac *float32
	var label *string
	var at *time.Time
	err := row.Scan(&b.ID, &b.SHA256, &b.Format, &b.FileName, &b.Size, &b.Title, &b.Authors, &b.Genres,
		&b.Series, &b.SeriesIndex, &b.Language, &b.Publisher, &b.Published, &b.Description, &b.HasCover,
		&b.Owner, &b.Visibility, &b.Source, &b.AddedAt, &b.UpdatedAt, &b.Status, &b.FinishedAt,
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
	b.CanEdit = canEdit(b, who)
	return b, nil
}

// canEdit: el dueño siempre; un admin, en los compartidos y en los anteriores
// a las cuentas (dueño vacío).
func canEdit(b Book, who app.Identity) bool {
	return b.Owner == who.User || (who.IsAdmin() && (b.Visibility == "compartido" || b.Owner == ""))
}

type listQuery struct {
	Text   string
	Status string
	Sort   string // recent (último leído), added, title, author
	Scope  string // "" todos los visibles, mine (subidos por mí), shared (compartidos)
	Limit  int
}

func (s store) list(ctx context.Context, who app.Identity, q listQuery) ([]Book, error) {
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
	rows, err := s.db.Query(ctx, `SELECT `+bookCols+` FROM books b `+personal+`
		WHERE `+visible+`
		  AND ($2 = '' OR b.title ILIKE '%' || $2 || '%' OR array_to_string(b.authors, ' ') ILIKE '%' || $2 || '%'
		       OR array_to_string(b.genres, ' ') ILIKE '%' || $2 || '%' OR coalesce(b.series, '') ILIKE '%' || $2 || '%')
		  AND ($3 = '' OR coalesce(r.status, 'pendiente') = $3)
		  AND ($4 = '' OR ($4 = 'mine' AND b.owner = $1) OR ($4 = 'shared' AND b.visibility = 'compartido'))
		ORDER BY `+order+` LIMIT $5`, who.User, q.Text, q.Status, q.Scope, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	books := []Book{}
	for rows.Next() {
		b, err := scanBook(rows, who)
		if err != nil {
			return nil, err
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

// get devuelve el libro si la persona puede verlo.
func (s store) get(ctx context.Context, who app.Identity, id int64) (Book, error) {
	return scanBook(s.db.QueryRow(ctx, `SELECT `+bookCols+` FROM books b `+personal+`
		WHERE b.id = $2 AND `+visible, who.User, id), who)
}

// findCopy busca un libro con ese fichero que la persona ya pueda ver: el suyo
// o uno compartido. Así no se duplica al subirlo otra vez.
func (s store) findCopy(ctx context.Context, who app.Identity, sha string) (Book, error) {
	return scanBook(s.db.QueryRow(ctx, `SELECT `+bookCols+` FROM books b `+personal+`
		WHERE b.sha256 = $2 AND `+visible+` ORDER BY b.owner = $1 DESC LIMIT 1`, who.User, sha), who)
}

// idsBySource dice cuáles de esas procedencias («fuente:id») ya están en la
// biblioteca que ve la persona, con el id del libro.
func (s store) idsBySource(ctx context.Context, who app.Identity, keys []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT b.source, b.id FROM books b
		WHERE b.source = ANY($2) AND `+visible+` ORDER BY b.owner = $1`, who.User, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		var id int64
		if err := rows.Scan(&src, &id); err != nil {
			return nil, err
		}
		out[src] = id // el propio gana: va el último por el ORDER BY
	}
	return out, rows.Err()
}

// fileInUse indica si algún libro sigue usando el fichero.
func (s store) fileInUse(ctx context.Context, sha string) (bool, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM books WHERE sha256 = $1`, sha).Scan(&n)
	return n > 0, err
}

// insert guarda un libro nuevo. Los metadatos ya vienen saneados.
func (s store) insert(ctx context.Context, b Book) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO books
		(sha256, format, file_name, size, title, authors, genres, series, series_index,
		 language, publisher, published, description, has_cover, owner, visibility, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
		b.SHA256, b.Format, b.FileName, b.Size, b.Title, b.Authors, b.Genres, b.Series, b.SeriesIndex,
		b.Language, b.Publisher, b.Published, b.Description, b.HasCover, b.Owner, b.Visibility, b.Source).Scan(&id)
	return id, err
}

// update cambia los metadatos editables y la visibilidad. Quien llama ya ha
// comprobado CanEdit.
func (s store) update(ctx context.Context, b Book) error {
	tag, err := s.db.Exec(ctx, `UPDATE books SET title = $2, authors = $3, genres = $4, series = $5,
		series_index = $6, language = $7, publisher = $8, published = $9, description = $10,
		visibility = $11, updated_at = now() WHERE id = $1`,
		b.ID, b.Title, b.Authors, b.Genres, b.Series, b.SeriesIndex, b.Language, b.Publisher,
		b.Published, b.Description, b.Visibility)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// setStatus cambia el estado de lectura de la persona.
func (s store) setStatus(ctx context.Context, who app.Identity, id int64, status string) error {
	if _, err := s.get(ctx, who, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO reading (book_id, username, status, finished_at)
		VALUES ($1, $2, $3, CASE WHEN $3 = 'leido' THEN now() END)
		ON CONFLICT (book_id, username) DO UPDATE SET status = excluded.status, updated_at = now(),
			finished_at = CASE WHEN excluded.status = 'leido' THEN coalesce(reading.finished_at, now()) END`,
		id, who.User, status)
	return err
}

func (s store) delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM books WHERE id = $1`, id)
	return err
}

// saveProgress guarda la posición de un dispositivo de la persona si es más
// reciente que la que había (las colas sin conexión pueden llegar
// desordenadas). Un libro pendiente pasa a «leyendo». Devuelve la posición
// más reciente de la persona en ese libro.
func (s store) saveProgress(ctx context.Context, who app.Identity, bookID int64, p Progress) (*Progress, error) {
	if _, err := s.get(ctx, who, bookID); err != nil {
		return nil, err
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO progress (book_id, username, device_id, device_name, location, fraction, label, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (book_id, username, device_id) DO UPDATE SET device_name = excluded.device_name,
				location = excluded.location, fraction = excluded.fraction, label = excluded.label,
				updated_at = excluded.updated_at
			WHERE progress.updated_at < excluded.updated_at`,
			bookID, who.User, p.DeviceID, p.DeviceName, p.Location, p.Fraction, p.Label, p.UpdatedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO reading (book_id, username, status) VALUES ($1, $2, 'leyendo')
				ON CONFLICT (book_id, username) DO UPDATE SET status = 'leyendo', updated_at = now()
				WHERE reading.status = 'pendiente'`, bookID, who.User)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	b, err := s.get(ctx, who, bookID)
	return b.Last, err
}

// devices son las posiciones de cada dispositivo de la persona.
func (s store) devices(ctx context.Context, who app.Identity, bookID int64) ([]Progress, error) {
	rows, _ := s.db.Query(ctx, `SELECT device_id, device_name, location, fraction, label, updated_at
		FROM progress WHERE book_id = $1 AND username = $2 ORDER BY updated_at DESC`, bookID, who.User)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Progress])
}

// claimLegacy asigna a una persona los libros, estados y progreso de antes de
// las cuentas (usuario ”). Lo usa el primer admin que entra.
func (s store) claimLegacy(ctx context.Context, who app.Identity) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE books SET owner = $1 WHERE owner = ''
			AND NOT EXISTS (SELECT 1 FROM books o WHERE o.owner = $1 AND o.sha256 = books.sha256)`, who.User)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		if _, err := tx.Exec(ctx, `UPDATE reading SET username = $1 WHERE username = ''
			AND NOT EXISTS (SELECT 1 FROM reading o WHERE o.username = $1 AND o.book_id = reading.book_id)`, who.User); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE progress SET username = $1 WHERE username = ''
			AND NOT EXISTS (SELECT 1 FROM progress o WHERE o.username = $1 AND o.book_id = progress.book_id AND o.device_id = progress.device_id)`, who.User)
		return err
	})
	return n, err
}
