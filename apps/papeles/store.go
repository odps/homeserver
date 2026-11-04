package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

// store guarda los documentos cifrados (platform/seal). Cada consulta filtra
// por el dueño. Hay pocos cientos de documentos por persona: los filtros y el
// orden se hacen en Go, después de descifrar.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealDoc  = "papeles/docs"
	sealFile = "papeles/files"
)

func fold(s string) string { return seal.Fold(s) }

// docs son todos los documentos de la persona con cuántos archivos tiene
// cada uno, sin ordenar.
func (s store) docs(ctx context.Context, owner string) ([]doc, error) {
	rows, _ := s.db.Query(ctx, `SELECT d.id, d.sealed, d.created_at, d.updated_at,
		(SELECT count(*) FROM files f WHERE f.doc_id = d.id AND f.owner = $1)
		FROM docs d WHERE d.owner = $1`, owner)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (doc, error) {
		var d doc
		var sealed []byte
		if err := r.Scan(&d.ID, &sealed, &d.Created, &d.Updated, &d.Files); err != nil {
			return d, err
		}
		id, c, u, n := d.ID, d.Created, d.Updated, d.Files
		if err := s.box.OpenJSON(sealDoc, sealed, &d); err != nil {
			return d, err
		}
		d.ID, d.Created, d.Updated, d.Files = id, c, u, n
		return d, nil
	})
}

// doc es un documento de la persona (errNotFound si no es suyo).
func (s store) doc(ctx context.Context, owner string, id int64) (doc, error) {
	var d doc
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT d.sealed, d.created_at, d.updated_at,
		(SELECT count(*) FROM files f WHERE f.doc_id = d.id AND f.owner = $1)
		FROM docs d WHERE d.owner = $1 AND d.id = $2`, owner, id).Scan(&sealed, &d.Created, &d.Updated, &d.Files)
	if errors.Is(err, pgx.ErrNoRows) {
		return doc{}, errNotFound
	}
	if err != nil {
		return doc{}, err
	}
	c, u, n := d.Created, d.Updated, d.Files
	if err := s.box.OpenJSON(sealDoc, sealed, &d); err != nil {
		return doc{}, err
	}
	d.ID, d.Created, d.Updated, d.Files = id, c, u, n
	return d, nil
}

// saveDoc valida y guarda un documento (ID 0: nuevo). Devuelve su id.
func (s store) saveDoc(ctx context.Context, owner string, d doc) (int64, error) {
	if err := d.clean(); err != nil {
		return 0, err
	}
	if d.Alerts == nil {
		d.Alerts = []int{}
	}
	sealed, err := s.box.SealJSON(sealDoc, d)
	if err != nil {
		return 0, err
	}
	if d.ID == 0 {
		var id int64
		err := s.db.QueryRow(ctx, `INSERT INTO docs (owner, sealed) VALUES ($1, $2) RETURNING id`, owner, sealed).Scan(&id)
		return id, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE docs SET sealed = $3, updated_at = now() WHERE owner = $1 AND id = $2`, owner, d.ID, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return 0, errNotFound
	}
	return d.ID, err
}

// deleteDoc borra el documento y devuelve los ids de sus archivos (para
// borrarlos del disco).
func (s store) deleteDoc(ctx context.Context, owner string, id int64) ([]int64, error) {
	var files []int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, `SELECT id FROM files WHERE owner = $1 AND doc_id = $2`, owner, id)
		var err error
		if files, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM docs WHERE owner = $1 AND id = $2`, owner, id)
		if err == nil && tag.RowsAffected() == 0 {
			return errNotFound
		}
		return err
	})
	return files, err
}

// ---- Archivos (metadatos) ----

// fileMeta son los datos de un archivo; el contenido va en el disco.
type fileMeta struct {
	ID      int64  `json:"-"`
	DocID   int64  `json:"-"`
	Name    string `json:"nombre"`
	Type    string `json:"tipo"` // application/pdf o image/jpeg
	Size    int    `json:"tamano"`
	Mini    bool   `json:"mini,omitempty"`
	W       int    `json:"ancho,omitempty"`
	H       int    `json:"alto,omitempty"`
	Created string `json:"-"`
}

func (s store) files(ctx context.Context, owner string, docID int64) ([]fileMeta, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, sealed, created_at FROM files WHERE owner = $1 AND doc_id = $2 ORDER BY id`, owner, docID)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (fileMeta, error) {
		var f fileMeta
		var sealed []byte
		var created time.Time
		if err := r.Scan(&f.ID, &sealed, &created); err != nil {
			return f, err
		}
		id := f.ID
		if err := s.box.OpenJSON(sealFile, sealed, &f); err != nil {
			return f, err
		}
		f.ID, f.DocID, f.Created = id, docID, created.In(madrid).Format(dateFmt)
		return f, nil
	})
	return list, err
}

// file es un archivo de la persona (errNotFound si no es suyo).
func (s store) file(ctx context.Context, owner string, id int64) (fileMeta, error) {
	var f fileMeta
	var sealed []byte
	var created time.Time
	err := s.db.QueryRow(ctx, `SELECT doc_id, sealed, created_at FROM files WHERE owner = $1 AND id = $2`, owner, id).Scan(&f.DocID, &sealed, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return fileMeta{}, errNotFound
	}
	if err != nil {
		return fileMeta{}, err
	}
	docID := f.DocID
	if err := s.box.OpenJSON(sealFile, sealed, &f); err != nil {
		return fileMeta{}, err
	}
	f.ID, f.DocID, f.Created = id, docID, created.In(madrid).Format(dateFmt)
	return f, nil
}
