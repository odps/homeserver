package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type api struct {
	store store
	files files
	log   *slog.Logger
}

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/books", a.list)
	mux.HandleFunc("POST /api/books", a.upload)
	mux.HandleFunc("GET /api/books/{id}", a.get)
	mux.HandleFunc("PATCH /api/books/{id}", a.patch)
	mux.HandleFunc("DELETE /api/books/{id}", a.remove)
	mux.HandleFunc("GET /api/books/{id}/file", a.file)
	mux.HandleFunc("GET /api/books/{id}/cover", a.cover)
	mux.HandleFunc("PUT /api/books/{id}/progress", a.progress)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (a *api) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "el libro no existe"})
	case errors.As(err, new(badRequest)):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		a.log.Error("libros", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "error interno"})
	}
}

type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

func bad(format string, args ...any) error {
	return badRequest{msg: fmt.Sprintf(format, args...)}
}

func bookID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound
	}
	return id, nil
}

func (a *api) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	books, err := a.store.list(r.Context(), listQuery{Text: q.Get("q"), Status: q.Get("status"), Sort: q.Get("sort")})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, books)
}

func (a *api) get(w http.ResponseWriter, r *http.Request) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	devices, err := a.store.devices(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"book": b, "devices": devices})
}

// metaIn son los metadatos que extrae el navegador con foliate-js al subir, o
// los que se editan después.
type metaIn struct {
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Genres      []string `json:"genres"`
	Series      string   `json:"series"`
	SeriesIndex *float32 `json:"series_index"`
	Language    string   `json:"language"`
	Publisher   string   `json:"publisher"`
	Published   string   `json:"published"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
}

// apply copia los metadatos saneados al libro.
func (m metaIn) apply(b *Book) {
	b.Title = clean(m.Title, 500)
	b.Authors = cleanList(m.Authors, 20, 200)
	b.Genres = cleanList(m.Genres, 30, 100)
	b.Series = optional(m.Series, 300)
	b.SeriesIndex = m.SeriesIndex
	b.Language = optional(m.Language, 35)
	b.Publisher = optional(m.Publisher, 300)
	b.Published = optional(m.Published, 40)
	b.Description = optional(m.Description, 20000)
}

func clean(s string, max int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	for utf8.RuneCountInString(s) > max {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}

func optional(s string, max int) *string {
	if s = clean(s, max); s == "" {
		return nil
	}
	return &s
}

func cleanList(in []string, maxItems, maxLen int) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		if s = clean(s, maxLen); s != "" && !seen[strings.ToLower(s)] && len(out) < maxItems {
			seen[strings.ToLower(s)] = true
			out = append(out, s)
		}
	}
	return out
}

// upload recibe multipart con «file» (obligatorio), «meta» (JSON) y «cover»
// (imagen), en cualquier orden. Si el libro ya estaba (mismo SHA-256),
// devuelve el existente con duplicate=true.
func (a *api) upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mr, err := r.MultipartReader()
	if err != nil {
		a.fail(w, bad("se esperaba un formulario multipart"))
		return
	}
	var meta metaIn
	var cover []byte
	var up *upload
	var fileName string
	defer func() {
		if up != nil {
			up.discard()
		}
	}()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			a.fail(w, bad("formulario incompleto: %s", err))
			return
		}
		switch part.FormName() {
		case "meta":
			if err := json.NewDecoder(io.LimitReader(part, 64<<10)).Decode(&meta); err != nil {
				a.fail(w, bad("metadatos no válidos"))
				return
			}
		case "cover":
			cover, err = io.ReadAll(io.LimitReader(part, maxCoverBytes+1))
			if err != nil || len(cover) > maxCoverBytes {
				cover = nil // una portada mala no impide subir el libro
			}
		case "file":
			if up != nil {
				a.fail(w, bad("solo un libro por petición"))
				return
			}
			fileName = filepath.Base(part.FileName())
			if up, err = a.files.receive(part, maxBookBytes); err != nil {
				a.fail(w, bad("%s", err))
				return
			}
		}
		part.Close()
	}
	if up == nil {
		a.fail(w, bad("falta el fichero"))
		return
	}
	if existing, err := a.store.bySHA(ctx, up.sha256); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"book": existing, "duplicate": true})
		return
	} else if !errors.Is(err, errNotFound) {
		a.fail(w, err)
		return
	}
	format, err := detectFormat(up, fileName)
	if err != nil {
		a.fail(w, bad("%s", err))
		return
	}
	b := Book{SHA256: up.sha256, Format: format, FileName: clean(fileName, 300), Size: up.size}
	meta.apply(&b)
	if b.Title == "" {
		b.Title = clean(strings.TrimSuffix(fileName, filepath.Ext(fileName)), 500)
	}
	if b.Title == "" {
		b.Title = "Sin título"
	}
	if err := a.files.commit(up, b); err != nil {
		a.fail(w, err)
		return
	}
	up = nil
	if cover != nil && a.files.saveCover(b, bytes.NewReader(cover)) == nil {
		b.HasCover = true
	}
	id, err := a.store.insert(ctx, b)
	if err != nil {
		a.files.remove(b)
		a.fail(w, err)
		return
	}
	saved, err := a.store.get(ctx, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.log.Info("libro añadido", "id", id, "titulo", b.Title, "formato", format, "bytes", b.Size)
	writeJSON(w, http.StatusCreated, map[string]any{"book": saved, "duplicate": false})
}

var statuses = map[string]bool{"pendiente": true, "leyendo": true, "leido": true}

func (a *api) patch(w http.ResponseWriter, r *http.Request) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	var m metaIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&m); err != nil {
		a.fail(w, bad("datos no válidos"))
		return
	}
	m.apply(&b)
	if b.Title == "" {
		a.fail(w, bad("el título no puede estar vacío"))
		return
	}
	if m.Status != "" {
		if !statuses[m.Status] {
			a.fail(w, bad("estado no válido"))
			return
		}
		b.Status = m.Status
	}
	if err := a.store.update(r.Context(), b); err != nil {
		a.fail(w, err)
		return
	}
	b, err = a.store.get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *api) remove(w http.ResponseWriter, r *http.Request) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.delete(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.files.remove(b)
	a.log.Info("libro borrado", "id", id, "titulo", b.Title)
	w.WriteHeader(http.StatusNoContent)
}

// file sirve el libro con soporte de rangos y ETag (el SHA-256 nunca cambia).
func (a *api) file(w http.ResponseWriter, r *http.Request) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.get(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	f, err := os.Open(a.files.bookPath(b))
	if err != nil {
		a.fail(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", contentTypes[b.Format])
	h.Set("ETag", `"`+b.SHA256+`"`)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": b.FileName}))
	http.ServeContent(w, r, "", b.AddedAt, f)
}

func (a *api) cover(w http.ResponseWriter, r *http.Request) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.get(r.Context(), id)
	if err != nil || !b.HasCover {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(a.files.coverPath(b))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("ETag", `"`+b.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", b.AddedAt, f)
}

type progressIn struct {
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	Location   string    `json:"location"`
	Fraction   float32   `json:"fraction"`
	Label      string    `json:"label"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (a *api) progress(w http.ResponseWriter, r *http.Request) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	var in progressIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		a.fail(w, bad("datos no válidos"))
		return
	}
	p := Progress{
		DeviceID: clean(in.DeviceID, 64), DeviceName: clean(in.DeviceName, 80),
		Location: strings.TrimSpace(in.Location), Fraction: in.Fraction, Label: optional(in.Label, 300),
		UpdatedAt: in.UpdatedAt,
	}
	if p.DeviceID == "" || p.DeviceName == "" || p.Location == "" || len(p.Location) > 2000 || p.Fraction < 0 || p.Fraction > 1 {
		a.fail(w, bad("progreso no válido"))
		return
	}
	// Un reloj adelantado no puede ganar a todas las lecturas futuras.
	if now := time.Now(); p.UpdatedAt.IsZero() || p.UpdatedAt.After(now.Add(5*time.Minute)) {
		p.UpdatedAt = now
	}
	last, err := a.store.saveProgress(r.Context(), id, p)
	if err != nil {
		if strings.Contains(err.Error(), "foreign key") {
			err = errNotFound
		}
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"last": last})
}
