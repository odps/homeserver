package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"homeserver/platform/app"
)

// Cambiar de ejercicio: lo apuntado como un ejercicio era en realidad otro
// («press banca» era con mancuernas, no con barra). Se pasan las veces (todas
// o solo las escritas de ciertas maneras) al ejercicio bueno, esas maneras de
// escribirlo quedan como alias para lo que se apunte después y, si se pasa
// todo, las rutinas también cambian.

// writtenName es una forma de escribir un ejercicio y cuántas veces se usó.
type writtenName struct {
	Name  string
	Times int
}

// writtenNames son las formas en que se ha escrito un ejercicio.
func writtenNames(list []done) []writtenName {
	by := map[string]*writtenName{}
	var out []*writtenName
	for _, d := range list {
		n := strings.TrimSpace(d.Entry.Name)
		if n == "" {
			continue
		}
		k := fold(n)
		if w, ok := by[k]; ok {
			w.Times++
			continue
		}
		w := &writtenName{Name: n, Times: 1}
		by[k] = w
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Times > out[j].Times })
	res := make([]writtenName, len(out))
	for i, w := range out {
		res[i] = *w
	}
	return res
}

// moveResult es lo que se ha cambiado.
type moveResult struct {
	Moved    int      `json:"veces"`
	Aliases  []string `json:"alias"`
	Routines int      `json:"rutinas"`
}

// moveExercise pasa a «to» lo apuntado como «from»: todo si names está vacío
// o solo lo escrito de esas maneras. Las maneras pasadas quedan como alias.
// Con todo, también las rutinas.
func (s store) moveExercise(ctx context.Context, owner, from, to string, names []string) (moveResult, error) {
	var res moveResult
	only := map[string]bool{}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			only[fold(n)] = true
		}
	}
	all := len(only) == 0
	official := ""
	if e, ok := lookup(from); ok {
		official = e.Name
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, `SELECT id, sealed FROM entries WHERE owner = $1 AND ex_token = $2 ORDER BY id FOR UPDATE`, owner, s.exToken(from))
		type row struct {
			id int64
			e  exerciseLog
		}
		var list []row
		var id int64
		var sealed []byte
		if _, err := pgx.ForEachRow(rows, []any{&id, &sealed}, func() error {
			var e exerciseLog
			if err := s.box.OpenJSON(sealEntry, sealed, &e); err != nil {
				return err
			}
			list = append(list, row{id, e})
			return nil
		}); err != nil {
			return err
		}
		aliases := map[string]string{}
		for _, x := range list {
			if !all && !only[fold(x.e.Name)] {
				continue
			}
			x.e.Key = to
			sealed, err := s.box.SealJSON(sealEntry, x.e)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE entries SET ex_token = $3, sealed = $4 WHERE owner = $1 AND id = $2`,
				owner, x.id, s.exToken(to), sealed); err != nil {
				return err
			}
			res.Moved++
			// El nombre oficial del ejercicio viejo (el que pone el modo
			// gimnasio) no se vuelve alias: seguiría siendo ese ejercicio.
			if n := strings.TrimSpace(x.e.Name); n != "" && fold(n) != fold(official) {
				aliases[fold(n)] = n
			}
		}
		for _, n := range aliases {
			sealed, err := s.box.SealJSON(sealAlias, aliasData{Alias: n, Key: to})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO aliases (owner, alias_token, sealed) VALUES ($1, $2, $3)
				ON CONFLICT (owner, alias_token) DO UPDATE SET sealed = excluded.sealed`, owner, s.box.Token("alias", fold(n)), sealed); err != nil {
				return err
			}
			res.Aliases = append(res.Aliases, n)
		}
		sort.Strings(res.Aliases)
		if !all {
			return nil
		}
		// Las rutinas: el ejercicio cambia de clave (si la rutina ya tiene el
		// bueno, se deja como está).
		rows, _ = tx.Query(ctx, `SELECT id, sealed FROM routines WHERE owner = $1 FOR UPDATE`, owner)
		type rrow struct {
			id int64
			r  routine
		}
		var rs []rrow
		if _, err := pgx.ForEachRow(rows, []any{&id, &sealed}, func() error {
			var r routine
			if err := s.box.OpenJSON(sealRoutine, sealed, &r); err != nil {
				return err
			}
			rs = append(rs, rrow{id, r})
			return nil
		}); err != nil {
			return err
		}
		for _, x := range rs {
			i := slices.IndexFunc(x.r.Items, func(it routineItem) bool { return it.Key == from })
			if i < 0 || slices.ContainsFunc(x.r.Items, func(it routineItem) bool { return it.Key == to }) {
				continue
			}
			x.r.Items[i].Key = to
			sealed, err := s.box.SealJSON(sealRoutine, x.r)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE routines SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, x.id, sealed); err != nil {
				return err
			}
			res.Routines++
		}
		return nil
	})
	return res, err
}

// validExercise dice si la clave es del catálogo o un ejercicio propio.
func (s *service) validExercise(ctx context.Context, owner, key string) (bool, error) {
	if _, ok := lookup(key); ok {
		return true, nil
	}
	custom, err := s.store.customNames(ctx, owner)
	if err != nil {
		return false, err
	}
	_, ok := custom[key]
	return ok, nil
}

// moveExercise comprueba las claves y pasa lo apuntado de un ejercicio a otro.
func (s *service) moveExercise(ctx context.Context, owner, from, to string, names []string) (moveResult, error) {
	if from == to {
		return moveResult{}, errors.New("elige otro ejercicio: es el mismo")
	}
	for _, k := range []string{from, to} {
		ok, err := s.validExercise(ctx, owner, k)
		if err != nil {
			return moveResult{}, err
		}
		if !ok {
			return moveResult{}, fmt.Errorf("el ejercicio «%s» no existe", k)
		}
	}
	res, err := s.store.moveExercise(ctx, owner, from, to, names)
	if err == nil && res.Routines > 0 {
		s.syncAgendaQuiet(ctx, owner)
	}
	return res, err
}

// ---- Web ----

// moveView es la página «Pasar a otro ejercicio».
type moveView struct {
	Key, Name string
	Names     []writtenName
	Groups    []optGroup
}

func (w *web) movePage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderMove(rw, r, who, "")
}

func (w *web) renderMove(rw http.ResponseWriter, r *http.Request, who app.Identity, msg string) {
	ctx := r.Context()
	key := r.PathValue("key")
	if !reKey.MatchString(key) {
		notFound(rw)
		return
	}
	custom, err := w.svc.store.customNames(ctx, who.User)
	if err != nil {
		w.fail(rw, "cambiar ejercicio", err)
		return
	}
	list, err := w.svc.store.history(ctx, who.User, key, 0)
	if err != nil {
		w.fail(rw, "cambiar ejercicio", err)
		return
	}
	if ok, _ := w.svc.validExercise(ctx, who.User, key); !ok || len(list) == 0 {
		notFound(rw)
		return
	}
	v := &moveView{Key: key, Name: exerciseName(key, "", custom), Names: writtenNames(list)}
	for _, g := range append(customGroup(custom), catalogGroups()...) {
		g.Options = slices.DeleteFunc(slices.Clone(g.Options), func(o option) bool { return o.Key == key })
		v.Groups = append(v.Groups, g)
	}
	w.render(rw, r, "ejercicio_cambiar.html", pageData{Name: who.Name, Tab: "ejercicios", Move: v, Error: msg})
}

func (w *web) movePost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	key := r.PathValue("key")
	if !reKey.MatchString(key) {
		notFound(rw)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "El formulario es demasiado grande.", http.StatusRequestEntityTooLarge)
		return
	}
	to := r.PostForm.Get("a")
	if to == "" {
		w.renderMove(rw, r, who, "Elige a qué ejercicio pasarlo.")
		return
	}
	names := r.PostForm["nombre"]
	if len(names) == 0 {
		w.renderMove(rw, r, who, "Marca al menos una forma de escribirlo.")
		return
	}
	// Todas marcadas: se pasa todo (y las rutinas).
	list, err := w.svc.store.history(r.Context(), who.User, key, 0)
	if err != nil {
		w.fail(rw, "cambiar ejercicio", err)
		return
	}
	if len(names) >= len(writtenNames(list)) {
		names = nil
	}
	res, err := w.svc.moveExercise(r.Context(), who.User, key, to, names)
	if err != nil {
		w.renderMove(rw, r, who, capFirst(err.Error())+".")
		return
	}
	if res.Moved == 0 {
		w.renderMove(rw, r, who, "No había nada que pasar.")
		return
	}
	back(rw, r, "/ejercicio/"+to, "cambiado")
}
