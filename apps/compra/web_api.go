package main

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"homeserver/platform/app"
)

// API JSON de lista.js: tachar y destachar sin recargar. Solo JSON del mismo
// origen y con el cuerpo limitado. «hecho» es el estado final, así que
// reenviar lo mismo (lo que hace la cola sin conexión) no cambia nada.

const maxAPIBody = 1 << 10

type markIn struct {
	ID    int64 `json:"id"`
	Hecho *bool `json:"hecho"`
}

func (w *web) apiMark(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	reply := func(code int, v any) {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		rw.WriteHeader(code)
		json.NewEncoder(rw).Encode(v)
	}
	fail := func(code int, msg string) { reply(code, map[string]any{"ok": false, "error": msg}) }
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		fail(http.StatusForbidden, "Petición de otro sitio.")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		fail(http.StatusUnsupportedMediaType, "Hace falta JSON.")
		return
	}
	var in markIn
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxAPIBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(http.StatusRequestEntityTooLarge, "Demasiado grande.")
			return
		}
		fail(http.StatusBadRequest, "Petición no válida.")
		return
	}
	if in.ID <= 0 || in.Hecho == nil {
		fail(http.StatusBadRequest, "Petición no válida.")
		return
	}
	err := w.svc.store.setDone(r.Context(), who.User, in.ID, *in.Hecho)
	switch {
	case errors.Is(err, errNotFound):
		fail(http.StatusNotFound, "Ya no está en la lista.")
	case err != nil:
		w.svc.log.Error("compra web", "op", "api marcar", "err", err)
		fail(http.StatusInternalServerError, "Algo ha fallado.")
	default:
		reply(http.StatusOK, map[string]any{"ok": true, "id": in.ID, "hecho": *in.Hecho})
	}
}
