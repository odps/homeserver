package main

import (
	"bytes"
	"context"
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
	"sync"
	"time"
	"unicode/utf8"

	"homeserver/platform/app"
)

type api struct {
	store   store
	files   files
	catalog *catalog
	series  *seriesService
	log     *slog.Logger
	legacy  sync.Once // asignación de los datos anteriores a las cuentas
}

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", a.user(a.me))
	mux.HandleFunc("GET /api/settings", a.user(a.getSettings))
	mux.HandleFunc("PUT /api/settings", a.user(a.putSettings))
	mux.HandleFunc("PUT /api/prefs", a.user(a.putPrefs))
	mux.HandleFunc("GET /api/books", a.user(a.list))
	mux.HandleFunc("POST /api/books", a.user(a.upload))
	mux.HandleFunc("GET /api/books/{id}", a.user(a.get))
	mux.HandleFunc("PATCH /api/books/{id}", a.user(a.patch))
	mux.HandleFunc("DELETE /api/books/{id}", a.user(a.remove))
	mux.HandleFunc("PUT /api/books/{id}/status", a.user(a.status))
	mux.HandleFunc("PUT /api/books/{id}/favorito", a.user(a.favorite))
	mux.HandleFunc("GET /api/books/{id}/file", a.user(a.file))
	mux.HandleFunc("GET /api/books/{id}/cover", a.user(a.cover))
	mux.HandleFunc("PUT /api/books/{id}/cover", a.user(a.putCover))
	mux.HandleFunc("GET /api/isbn/{isbn}", a.user(a.isbnLookup))
	mux.HandleFunc("PUT /api/books/{id}/progress", a.user(a.progress))
	a.catalogRoutes(mux)
	a.seriesRoutes(mux)
}

type handler func(w http.ResponseWriter, r *http.Request, who app.Identity)

// user exige una persona identificada por la pasarela. Los tokens de servicio
// (sin usuario) no tienen biblioteca.
func (a *api) user(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who := app.User(r)
		if !who.IsUser() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "hace falta una cuenta de usuario"})
			return
		}
		// El primer admin que entra hereda los libros y el progreso de antes
		// de que hubiera cuentas.
		if who.IsAdmin() {
			a.legacy.Do(func() {
				if n, err := a.store.claimLegacy(r.Context(), who); err != nil {
					a.log.Error("asignar datos anteriores", "err", err)
				} else if n > 0 {
					a.log.Info("datos anteriores asignados", "usuario", who.User, "libros", n)
				}
			})
		}
		h(w, r, who)
	}
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
	case errors.Is(err, errForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "solo quien lo subió (o un admin, si es compartido) puede cambiarlo"})
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

// load devuelve el libro si la persona puede verlo.
func (a *api) load(r *http.Request, who app.Identity) (Book, error) {
	id, err := bookID(r)
	if err != nil {
		return Book{}, err
	}
	return a.store.get(r.Context(), who, id)
}

func (a *api) me(w http.ResponseWriter, r *http.Request, who app.Identity) {
	writeJSON(w, http.StatusOK, map[string]any{"user": who.User, "name": who.Name, "admin": who.IsAdmin()})
}

func (a *api) list(w http.ResponseWriter, r *http.Request, who app.Identity) {
	q := r.URL.Query()
	books, err := a.store.list(r.Context(), who, listQuery{Text: q.Get("q"), Status: q.Get("status"), Sort: q.Get("sort"), Scope: q.Get("scope")})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, books)
}

func (a *api) get(w http.ResponseWriter, r *http.Request, who app.Identity) {
	b, err := a.load(r, who)
	if err != nil {
		a.fail(w, err)
		return
	}
	devices, err := a.store.devices(r.Context(), who, b.ID)
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
	ISBN        string   `json:"isbn"`
	Visibility  string   `json:"visibility"`
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
	b.ISBN = nil
	if isbn, ok := normalizeISBN(m.ISBN); ok {
		b.ISBN = &isbn
	}
}

var visibilities = map[string]bool{"privado": true, "compartido": true}

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

// upload recibe multipart con «file» (obligatorio), «meta» (JSON, con la
// visibilidad) y «cover» (imagen), en cualquier orden. Si la persona ya puede
// ver un libro con ese fichero (suyo o compartido), lo devuelve con
// duplicate=true. El fichero se guarda una sola vez en disco.
func (a *api) upload(w http.ResponseWriter, r *http.Request, who app.Identity) {
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
	if existing, err := a.store.findCopy(ctx, who, up.sha256); err == nil {
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
	visibility := meta.Visibility
	if !visibilities[visibility] {
		visibility = "privado"
	}
	b := Book{SHA256: up.sha256, Format: format, FileName: clean(fileName, 300), Size: up.size, Owner: who.User, Visibility: visibility}
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
	} else if a.files.hasCover(b) {
		b.HasCover = true // otra persona ya subió este fichero con portada
	}
	id, err := a.store.insert(ctx, b)
	if err != nil {
		a.removeFileIfUnused(ctx, b)
		a.fail(w, err)
		return
	}
	saved, err := a.store.get(ctx, who, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.log.Info("libro añadido", "id", id, "usuario", who.User, "titulo", b.Title, "formato", format, "visibilidad", visibility, "bytes", b.Size)
	writeJSON(w, http.StatusCreated, map[string]any{"book": saved, "duplicate": false})
}

func (a *api) removeFileIfUnused(ctx context.Context, b Book) {
	if used, err := a.store.fileInUse(ctx, b.SHA256); err == nil && !used {
		a.files.remove(b)
	}
}

func (a *api) patch(w http.ResponseWriter, r *http.Request, who app.Identity) {
	b, err := a.load(r, who)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !b.CanEdit {
		a.fail(w, errForbidden)
		return
	}
	var m metaIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&m); err != nil {
		a.fail(w, bad("datos no válidos"))
		return
	}
	if _, ok := normalizeISBN(m.ISBN); !ok && strings.TrimSpace(m.ISBN) != "" {
		a.fail(w, bad("el ISBN no es válido (revisa los dígitos)"))
		return
	}
	m.apply(&b)
	if b.Title == "" {
		a.fail(w, bad("el título no puede estar vacío"))
		return
	}
	if m.Visibility != "" {
		if !visibilities[m.Visibility] {
			a.fail(w, bad("visibilidad no válida"))
			return
		}
		// Hacer privado un libro es cosa de su dueño (un admin no se lo puede quedar).
		if m.Visibility == "privado" && b.Owner != who.User {
			a.fail(w, errForbidden)
			return
		}
		b.Visibility = m.Visibility
	}
	if err := a.store.update(r.Context(), b); err != nil {
		a.fail(w, err)
		return
	}
	b, err = a.store.get(r.Context(), who, b.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

var statuses = map[string]bool{"pendiente": true, "leyendo": true, "leido": true}

func (a *api) status(w http.ResponseWriter, r *http.Request, who app.Identity) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil || !statuses[in.Status] {
		a.fail(w, bad("estado no válido"))
		return
	}
	if err := a.store.setStatus(r.Context(), who, id, in.Status); err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.get(r.Context(), who, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *api) favorite(w http.ResponseWriter, r *http.Request, who app.Identity) {
	id, err := bookID(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	var in struct {
		Favorite bool `json:"favorite"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil {
		a.fail(w, bad("datos no válidos"))
		return
	}
	if err := a.store.setFavorite(r.Context(), who, id, in.Favorite); err != nil {
		a.fail(w, err)
		return
	}
	b, err := a.store.get(r.Context(), who, id)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *api) remove(w http.ResponseWriter, r *http.Request, who app.Identity) {
	b, err := a.load(r, who)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !b.CanEdit {
		a.fail(w, errForbidden)
		return
	}
	if err := a.store.delete(r.Context(), b.ID); err != nil {
		a.fail(w, err)
		return
	}
	a.removeFileIfUnused(r.Context(), b)
	a.log.Info("libro borrado", "id", b.ID, "usuario", who.User, "titulo", b.Title)
	w.WriteHeader(http.StatusNoContent)
}

// file sirve el libro con soporte de rangos y ETag (el SHA-256 nunca cambia).
func (a *api) file(w http.ResponseWriter, r *http.Request, who app.Identity) {
	b, err := a.load(r, who)
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

func (a *api) cover(w http.ResponseWriter, r *http.Request, who app.Identity) {
	b, err := a.load(r, who)
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
	// La portada se puede cambiar: la URL lleva la fecha del cambio (?v=) y el
	// ETag, la del fichero.
	mod := b.AddedAt
	if st, err := f.Stat(); err == nil {
		mod = st.ModTime()
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%s-%d"`, b.SHA256[:16], mod.Unix()))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", mod, f)
}

type progressIn struct {
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	Location   string    `json:"location"`
	Fraction   float32   `json:"fraction"`
	Label      string    `json:"label"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (a *api) progress(w http.ResponseWriter, r *http.Request, who app.Identity) {
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
	last, err := a.store.saveProgress(r.Context(), who, id, p)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"last": last})
}
