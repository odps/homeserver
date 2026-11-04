package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/seal"
)

// Archivos de los documentos: escaneos, fotos y PDF. Son lo más delicado de
// la app, así que:
//   - Se leen en memoria con límite (web.go), nunca pasan por un temporal en
//     claro.
//   - Las imágenes se vuelven a codificar (imagen.go): sin EXIF, con
//     miniatura. Los PDF se guardan tal cual (se comprueba la firma %PDF-).
//   - Se guardan cifrados (platform/seal) en DATA_DIR/archivos/<id>, con el id
//     en el contexto: un fichero no se descifra como si fuera otro.
//   - Solo se sirven a su dueño, sin caché. No salen nunca por MCP.

const (
	maxFile     = 15 << 20 // por archivo
	maxDocFiles = 10       // por documento
	maxFileName = 120
)

const (
	typePDF  = "application/pdf"
	typeJPEG = "image/jpeg"
)

// fileErr es un error del archivo que se puede enseñar tal cual.
type fileErr string

func (e fileErr) Error() string { return string(e) }

var (
	errTooBig    = fileErr("El archivo pasa de 15 MB.")
	errFileType  = fileErr("Solo se admiten PDF y fotos en JPEG, PNG o WebP.")
	errHEIC      = fileErr("Las fotos HEIC (las del iPhone) no se admiten: cámbialas a JPEG en Ajustes > Cámara > Formatos > «Más compatible», o sube una captura.")
	errEmptyFile = fileErr("El archivo está vacío.")
	errTooMany   = fileErr("Este documento ya tiene 10 archivos: borra alguno antes.")
)

// fileStore guarda el contenido cifrado en un directorio.
type fileStore struct {
	dir string
	box *seal.Box
}

func newFileStore(dataDir string, box *seal.Box) *fileStore {
	return &fileStore{dir: filepath.Join(dataDir, "archivos"), box: box}
}

func (f *fileStore) path(id int64, mini bool) string {
	name := strconv.FormatInt(id, 10)
	if mini {
		name += ".mini"
	}
	return filepath.Join(f.dir, name)
}

func fileContext(id int64, mini bool) string {
	c := "papeles/archivo/" + strconv.FormatInt(id, 10)
	if mini {
		c += "/mini"
	}
	return c
}

func (f *fileStore) write(id int64, mini bool, b []byte) error {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(f.box.Seal(fileContext(id, mini), b)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path(id, mini))
}

func (f *fileStore) read(id int64, mini bool) ([]byte, error) {
	b, err := os.ReadFile(f.path(id, mini))
	if err != nil {
		return nil, err
	}
	return f.box.Open(fileContext(id, mini), b)
}

func (f *fileStore) remove(id int64) {
	os.Remove(f.path(id, false))
	os.Remove(f.path(id, true))
}

// ---- Comprobar y preparar lo subido ----

// sniff dice qué es el fichero por sus primeros bytes (no por el nombre ni
// por lo que diga el navegador).
func sniff(b []byte) (string, error) {
	switch {
	case bytes.HasPrefix(b, []byte("%PDF-")):
		return typePDF, nil
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return typeJPEG, nil
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", nil
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", nil
	case isHEIC(b):
		return "", errHEIC
	}
	return "", errFileType
}

// isHEIC reconoce HEIC/HEIF (y AVIF): una caja «ftyp» con su marca.
func isHEIC(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	switch string(b[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "avif":
		return true
	}
	return false
}

// prepare comprueba lo subido y devuelve los metadatos, el contenido que se
// guarda y la miniatura (nil para los PDF).
func prepare(name string, raw []byte) (fileMeta, []byte, []byte, error) {
	if len(raw) == 0 {
		return fileMeta{}, nil, nil, errEmptyFile
	}
	if len(raw) > maxFile {
		return fileMeta{}, nil, nil, errTooBig
	}
	kind, err := sniff(raw)
	if err != nil {
		return fileMeta{}, nil, nil, err
	}
	if kind == typePDF {
		m := fileMeta{Name: cleanName(name, ".pdf"), Type: typePDF, Size: len(raw)}
		return m, raw, nil, nil
	}
	full, thumb, w, h, err := reencode(raw, kind)
	if err != nil {
		return fileMeta{}, nil, nil, err
	}
	m := fileMeta{Name: cleanName(name, ".jpg"), Type: typeJPEG, Size: len(full), Mini: true, W: w, H: h}
	return m, full, thumb, nil
}

// cleanName deja el nombre original legible y sin rutas, con la extensión
// de lo que se guarda.
func cleanName(name, ext string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimSuffix(name, path.Ext(name)))
	if name == "" || name == "." {
		name = "archivo"
	}
	if utf8.RuneCountInString(name) > maxFileName-len(ext) {
		name = string([]rune(name)[:maxFileName-len(ext)])
	}
	return name + ext
}

// ---- Guardar, leer y borrar ----

// addFile guarda un archivo en un documento de la persona.
func (s *service) addFile(ctx context.Context, owner string, docID int64, name string, raw []byte) (int64, error) {
	meta, content, thumb, err := prepare(name, raw)
	if err != nil {
		return 0, err
	}
	sealed, err := s.store.box.SealJSON(sealFile, meta)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.store.db, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM docs WHERE owner = $1 AND id = $2 FOR UPDATE`, owner, docID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM files WHERE owner = $1 AND doc_id = $2`, owner, docID).Scan(&n); err != nil {
			return err
		}
		if n >= maxDocFiles {
			return errTooMany
		}
		if err := tx.QueryRow(ctx, `INSERT INTO files (owner, doc_id, sealed) VALUES ($1, $2, $3) RETURNING id`, owner, docID, sealed).Scan(&id); err != nil {
			return err
		}
		// El fichero se escribe dentro de la transacción: si falla, no queda
		// la fila; si falla el commit, se borra abajo.
		if err := s.files.write(id, false, content); err != nil {
			return err
		}
		if thumb != nil {
			if err := s.files.write(id, true, thumb); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE docs SET updated_at = now() WHERE owner = $1 AND id = $2`, owner, docID)
		return err
	})
	if err != nil {
		if id != 0 {
			s.files.remove(id)
		}
		return 0, err
	}
	return id, nil
}

// fileContent es el contenido (o la miniatura) de un archivo de la persona.
func (s *service) fileContent(ctx context.Context, owner string, id int64, mini bool) (fileMeta, []byte, error) {
	m, err := s.store.file(ctx, owner, id)
	if err != nil {
		return m, nil, err
	}
	if mini && !m.Mini {
		return m, nil, errNotFound
	}
	b, err := s.files.read(id, mini)
	return m, b, err
}

// deleteFile borra un archivo de la persona, también del disco. Devuelve el
// documento al que pertenecía.
func (s *service) deleteFile(ctx context.Context, owner string, id int64) (int64, error) {
	var docID int64
	err := s.store.db.QueryRow(ctx, `DELETE FROM files WHERE owner = $1 AND id = $2 RETURNING doc_id`, owner, id).Scan(&docID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errNotFound
	}
	if err != nil {
		return 0, err
	}
	s.files.remove(id)
	s.store.db.Exec(ctx, `UPDATE docs SET updated_at = now() WHERE owner = $1 AND id = $2`, owner, docID)
	return docID, nil
}
