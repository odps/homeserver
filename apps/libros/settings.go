package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/app"
)

// Ajustes de lectura de cada persona. Se guardan en el servidor para que se
// apliquen a todos los libros en cualquier dispositivo; el navegador guarda
// una copia para abrir sin conexión y gana el cambio más reciente.

type readerSettings struct {
	Theme   string  `json:"theme"`
	Size    int     `json:"size"`
	Spacing float64 `json:"spacing"`
	Margin  string  `json:"margin"`
	Justify bool    `json:"justify"`
	Flow    string  `json:"flow"`
	Font    string  `json:"font"`
}

var defaultReader = readerSettings{Theme: "light", Size: 100, Spacing: 1.5, Margin: "normal", Justify: true, Flow: "paginated", Font: "original"}

func (s readerSettings) valid() bool {
	in := func(v string, opts ...string) bool {
		for _, o := range opts {
			if v == o {
				return true
			}
		}
		return false
	}
	return in(s.Theme, "light", "sepia", "dark") && s.Size >= 70 && s.Size <= 200 && s.Spacing >= 1 && s.Spacing <= 2.5 &&
		in(s.Margin, "narrow", "normal", "wide") && in(s.Flow, "paginated", "scrolled") && in(s.Font, "original", "serif", "sans")
}

type settingsDoc struct {
	Reader    readerSettings `json:"reader"`
	UpdatedAt *time.Time     `json:"updated_at"` // nil: nunca se han guardado
}

func (s store) settings(ctx context.Context, who app.Identity) (settingsDoc, error) {
	d := settingsDoc{Reader: defaultReader}
	var raw []byte
	var at time.Time
	err := s.db.QueryRow(ctx, `SELECT reader, updated_at FROM settings WHERE username = $1`, who.User).Scan(&raw, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	// Los campos que falten (ajustes nuevos) se quedan con su valor por defecto.
	if json.Unmarshal(raw, &d.Reader) != nil || !d.Reader.valid() {
		d.Reader = defaultReader
	}
	d.UpdatedAt = &at
	return d, nil
}

// saveSettings guarda los ajustes si son más recientes que los que había (un
// dispositivo que estuvo sin conexión no pisa un cambio posterior).
func (s store) saveSettings(ctx context.Context, who app.Identity, r readerSettings, at time.Time) (settingsDoc, error) {
	raw, _ := json.Marshal(r)
	if _, err := s.db.Exec(ctx, `INSERT INTO settings (username, reader, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (username) DO UPDATE SET reader = excluded.reader, updated_at = excluded.updated_at
		WHERE settings.updated_at < excluded.updated_at`, who.User, raw, at); err != nil {
		return settingsDoc{}, err
	}
	return s.settings(ctx, who)
}

func (a *api) getSettings(w http.ResponseWriter, r *http.Request, who app.Identity) {
	d, err := a.store.settings(r.Context(), who)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *api) putSettings(w http.ResponseWriter, r *http.Request, who app.Identity) {
	var in struct {
		Reader    readerSettings `json:"reader"`
		UpdatedAt time.Time      `json:"updated_at"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil || !in.Reader.valid() {
		a.fail(w, bad("ajustes no válidos"))
		return
	}
	if now := time.Now(); in.UpdatedAt.IsZero() || in.UpdatedAt.After(now.Add(5*time.Minute)) {
		in.UpdatedAt = now
	}
	d, err := a.store.saveSettings(r.Context(), who, in.Reader, in.UpdatedAt)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
