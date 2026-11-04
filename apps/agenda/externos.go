package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"homeserver/platform/app"
)

// Eventos que mandan otras apps del servidor (las rutinas de Entreno, los
// vencimientos de Papeles). Cada app manda siempre su lista completa; aquí
// se crea, cambia o borra un evento por elemento, con nombre fijo
// («<prefijo><id>.ics»). Un evento cuya huella (la propiedad mark) no ha
// cambiado no se toca, para no perder lo retocado en el móvil.

// extEvent es un evento que manda otra app.
type extEvent struct {
	mark  string                                        // huella de lo mandado
	build func(now time.Time) (*ical.Calendar, error)   // el evento nuevo
	carry func(old *ical.Component, cal *ical.Calendar) // lo que se conserva del viejo (nil: nada)
}

// syncExternal deja los eventos «prefix*» del calendario collID como want.
func (s *service) syncExternal(ctx context.Context, collID int64, prefix, markProp string, want map[string]extEvent) (changed, removed int, err error) {
	objs, err := s.store.listObjects(ctx, collID)
	if err != nil {
		return 0, 0, err
	}
	have := map[string]object{}
	for _, o := range objs {
		if strings.HasPrefix(o.Name, prefix) {
			have[o.Name] = o
		}
	}
	now := s.now()
	for name, ev := range want {
		cal, err := ev.build(now)
		if err != nil {
			return changed, removed, err
		}
		if m, err := master(cal); err == nil {
			m.Props.SetText(markProp, ev.mark)
		}
		ifMatch, create := "", true
		if o, ok := have[name]; ok {
			ifMatch, create = o.ETag, false
			if old, err := decodeCal(o.Data); err == nil {
				if m, err := master(old); err == nil {
					if p := m.Props.Get(markProp); p != nil && p.Value == ev.mark {
						continue // igual que estaba
					}
					if ev.carry != nil {
						ev.carry(m, cal)
					}
				}
			}
		}
		if _, err := s.store.putObject(ctx, collID, name, cal, ifMatch, create, now); err != nil {
			return changed, removed, err
		}
		changed++
	}
	for name := range have {
		if _, ok := want[name]; !ok {
			if err := s.store.deleteObject(ctx, collID, name, ""); err != nil && !errors.Is(err, errNotFound) {
				return changed, removed, err
			}
			removed++
		}
	}
	return changed, removed, nil
}

// internalJSON lee una petición /interno/… de otra app: solo desde dentro,
// con un usuario que ya use la agenda (no se crea cuenta por esto). Si
// devuelve false, la respuesta ya está escrita.
func (w *web) internalJSON(rw http.ResponseWriter, r *http.Request, user *string, in any) bool {
	if !app.Internal(r) {
		http.Error(rw, "solo para las apps del servidor", http.StatusForbidden)
		return false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(in); err != nil || *user == "" {
		http.Error(rw, "petición no válida", http.StatusBadRequest)
		return false
	}
	rw.Header().Set("Content-Type", "application/json")
	users, err := w.svc.store.users(r.Context())
	if err != nil {
		http.Error(rw, "error", http.StatusInternalServerError)
		return false
	}
	if !slices.Contains(users, *user) {
		json.NewEncoder(rw).Encode(map[string]any{"agenda": false})
		return false
	}
	return true
}
