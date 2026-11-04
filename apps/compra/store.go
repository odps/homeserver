package main

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

var (
	errNotFound = errors.New("no existe")
	errFull     = errors.New("la lista está llena")
)

const (
	maxItems  = 400 // cosas en la lista
	keepMonth = 18  // meses que se guarda lo que no se vuelve a comprar
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealItem    = "compra/items"
	sealHistory = "compra/history"
	sealPref    = "compra/section_prefs"
	sealSetting = "compra/settings"
)

// Orígenes de una cosa. En la fase 2, «receta».
const (
	sourceWeb       = "web"
	sourceAssistant = "asistente"
	sourceRecipe    = "receta"
)

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

func dayOf(t time.Time) time.Time {
	t = t.In(madrid)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// nameToken es la huella del nombre normalizado.
func (s store) nameToken(name string) int64 { return s.box.Token("producto", nameKey(name)) }

// lock serializa los cambios de la lista de una persona.
func lock(ctx context.Context, tx pgx.Tx, owner string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "compra|"+owner)
	return err
}

// ---- La lista ----

// itemData es lo cifrado de cada cosa.
type itemData struct {
	Name    string    `json:"nombre"`
	Qty     string    `json:"cantidad,omitempty"`
	Note    string    `json:"nota,omitempty"`
	Section string    `json:"seccion"`
	Added   time.Time `json:"anadido"`
	Recipe  string    `json:"receta,omitempty"` // fase 2: de qué receta viene
}

// item es una cosa de la lista.
type item struct {
	ID     int64
	Done   bool
	Source string
	itemData
	token int64
}

func (s store) scanItems(rows pgx.Rows) ([]item, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) {
		var it item
		var sealed []byte
		if err := r.Scan(&it.ID, &it.Done, &it.Source, &it.token, &sealed); err != nil {
			return it, err
		}
		err := s.box.OpenJSON(sealItem, sealed, &it.itemData)
		if !validSection(it.Section) {
			it.Section = otherSection
		}
		return it, err
	})
}

const itemCols = `id, done, source, name_token, sealed`

// items es la lista, en el orden en que se añadió.
func (s store) items(ctx context.Context, owner string) ([]item, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+itemCols+` FROM items WHERE owner = $1 ORDER BY id`, owner)
	return s.scanItems(rows)
}

func (s store) item(ctx context.Context, owner string, id int64) (item, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+itemCols+` FROM items WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanItems(rows)
	if err != nil {
		return item{}, err
	}
	if len(list) == 0 {
		return item{}, errNotFound
	}
	return list[0], nil
}

// Lo que pasó con cada cosa al añadirla.
const (
	addNew    = "anadida"
	addMerged = "junta"     // ya estaba y se ha juntado la cantidad
	addDup    = "ya_estaba" // ya estaba y no se ha cambiado
)

type addResult struct {
	Item   item
	Result string
}

// add añade cosas a la lista. Si una ya está sin tachar no se repite: se
// junta la cantidad si se puede.
func (s store) add(ctx context.Context, owner string, list []parsed, source string, now time.Time) ([]addResult, error) {
	var out []addResult
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		out = out[:0]
		if err := lock(ctx, tx, owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+itemCols+` FROM items WHERE owner = $1 AND NOT done ORDER BY id`, owner)
		open, err := s.scanItems(rows)
		if err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM items WHERE owner = $1`, owner).Scan(&count); err != nil {
			return err
		}
		byToken := map[int64]int{}
		for i, it := range open {
			byToken[it.token] = i
		}
		prefs, err := s.prefsTx(ctx, tx, owner)
		if err != nil {
			return err
		}
		for _, p := range list {
			tok := s.nameToken(p.Name)
			if i, ok := byToken[tok]; ok {
				it := &open[i]
				qty, merged := mergeQty(it.Qty, p.Qty)
				if !merged && (p.Note == "" || it.Note != "") {
					out = append(out, addResult{Item: *it, Result: addDup})
					continue
				}
				it.Qty = qty
				if it.Note == "" {
					it.Note = p.Note
				}
				if err := s.saveItemTx(ctx, tx, owner, *it); err != nil {
					return err
				}
				res := addMerged
				if !merged {
					res = addDup
				}
				out = append(out, addResult{Item: *it, Result: res})
				continue
			}
			if count >= maxItems {
				return errFull
			}
			sec := prefs[tok]
			if sec == "" {
				sec = guessSection(p.Name)
			}
			it := item{Source: source, token: tok, itemData: itemData{Name: p.Name, Qty: p.Qty, Note: p.Note, Section: sec, Added: now}}
			sealed, err := s.box.SealJSON(sealItem, it.itemData)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO items (owner, name_token, source, sealed) VALUES ($1, $2, $3, $4) RETURNING id`,
				owner, tok, source, sealed).Scan(&it.ID); err != nil {
				return err
			}
			count++
			open = append(open, it)
			byToken[tok] = len(open) - 1
			out = append(out, addResult{Item: it, Result: addNew})
		}
		return nil
	})
	return out, err
}

func (s store) saveItemTx(ctx context.Context, tx pgx.Tx, owner string, it item) error {
	sealed, err := s.box.SealJSON(sealItem, it.itemData)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE items SET sealed = $3, name_token = $4 WHERE owner = $1 AND id = $2`, owner, it.ID, sealed, s.nameToken(it.Name))
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// setDone tacha o destacha. Es idempotente: tachar dos veces es tachar.
func (s store) setDone(ctx context.Context, owner string, id int64, done bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE items SET done = $3 WHERE owner = $1 AND id = $2`, owner, id, done)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// update cambia nombre, cantidad, nota y sección. Si la sección no es la que
// diría el diccionario, se recuerda para la próxima vez.
func (s store) update(ctx context.Context, owner string, id int64, name, qty, note, sec string, now time.Time) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+itemCols+` FROM items WHERE owner = $1 AND id = $2`, owner, id)
		list, err := s.scanItems(rows)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return errNotFound
		}
		it := list[0]
		changed := it.Section != sec || nameKey(it.Name) != nameKey(name)
		it.Name, it.Qty, it.Note, it.Section = name, qty, note, sec
		if err := s.saveItemTx(ctx, tx, owner, it); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return s.setPrefTx(ctx, tx, owner, name, sec, now)
	})
}

// remove quita una cosa de la lista (sin pasarla al historial).
func (s store) remove(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM items WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// clear vacía la lista entera, sin pasar nada al historial.
func (s store) clear(ctx context.Context, owner string) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM items WHERE owner = $1`, owner)
	return tag.RowsAffected(), err
}

// ---- Historial ----

// histData es lo cifrado del historial de una cosa.
type histData struct {
	Name      string  `json:"nombre"`
	Times     int     `json:"veces"`
	AvgDays   float64 `json:"media_dias,omitempty"` // intervalo medio entre compras
	Intervals int     `json:"intervalos,omitempty"` // cuántos intervalos hay en la media
}

type histEntry struct {
	histData
	Last  time.Time // día de la última compra
	token int64
}

// finish pasa lo tachado al historial y lo quita de la lista. Devuelve
// cuántas cosas eran. Hacerlo dos veces no cambia nada.
func (s store) finish(ctx context.Context, owner string, now time.Time) (int, error) {
	var n int
	day := dayOf(now)
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+itemCols+` FROM items WHERE owner = $1 AND done ORDER BY id`, owner)
		done, err := s.scanItems(rows)
		if err != nil {
			return err
		}
		n = len(done)
		seen := map[int64]bool{}
		for _, it := range done {
			if seen[it.token] { // dos veces lo mismo en la misma compra: una
				continue
			}
			seen[it.token] = true
			if err := s.bought(ctx, tx, owner, it.token, it.Name, day); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM items WHERE owner = $1 AND done`, owner)
		return err
	})
	return n, err
}

// bought apunta una compra en el historial. Comprarlo otra vez el mismo día
// no cuenta como otra vez.
func (s store) bought(ctx context.Context, tx pgx.Tx, owner string, tok int64, name string, day time.Time) error {
	var h histData
	var last time.Time
	var sealed []byte
	err := tx.QueryRow(ctx, `SELECT last_bought, sealed FROM history WHERE owner = $1 AND name_token = $2 FOR UPDATE`, owner, tok).Scan(&last, &sealed)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		h = histData{Times: 1}
		last = day
	case err != nil:
		return err
	default:
		if err := s.box.OpenJSON(sealHistory, sealed, &h); err != nil {
			return err
		}
		if gap := math.Round(day.Sub(last).Hours() / 24); gap >= 1 {
			h.AvgDays = (h.AvgDays*float64(h.Intervals) + gap) / float64(h.Intervals+1)
			h.Intervals++
			h.Times++
			last = day
		}
	}
	h.Name = name
	b, err := s.box.SealJSON(sealHistory, h)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO history (owner, name_token, last_bought, sealed) VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner, name_token) DO UPDATE SET last_bought = excluded.last_bought, sealed = excluded.sealed`,
		owner, tok, last.Format("2006-01-02"), b)
	return err
}

// history es todo lo comprado, de lo más a lo menos comprado.
func (s store) history(ctx context.Context, owner string) ([]histEntry, error) {
	rows, _ := s.db.Query(ctx, `SELECT name_token, last_bought, sealed FROM history WHERE owner = $1`, owner)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (histEntry, error) {
		var h histEntry
		var sealed []byte
		if err := r.Scan(&h.token, &h.Last, &sealed); err != nil {
			return h, err
		}
		return h, s.box.OpenJSON(sealHistory, sealed, &h.histData)
	})
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Times != list[j].Times {
			return list[i].Times > list[j].Times
		}
		if !list[i].Last.Equal(list[j].Last) {
			return list[i].Last.After(list[j].Last)
		}
		return list[i].Name < list[j].Name
	})
	return list, err
}

// ---- Secciones elegidas a mano y orden ----

type prefData struct {
	Name    string `json:"nombre"`
	Section string `json:"seccion"`
}

// prefsTx son las secciones elegidas a mano: huella → sección.
func (s store) prefsTx(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, owner string) (map[int64]string, error) {
	rows, _ := q.Query(ctx, `SELECT name_token, sealed FROM section_prefs WHERE owner = $1`, owner)
	out := map[int64]string{}
	var tok int64
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&tok, &sealed}, func() error {
		var p prefData
		if err := s.box.OpenJSON(sealPref, sealed, &p); err != nil {
			return err
		}
		if validSection(p.Section) {
			out[tok] = p.Section
		}
		return nil
	})
	return out, err
}

// sectionFor es la sección de una cosa para esta persona.
func (s store) sectionFor(ctx context.Context, owner, name string) (string, error) {
	prefs, err := s.prefsTx(ctx, s.db, owner)
	if err != nil {
		return "", err
	}
	if sec := prefs[s.nameToken(name)]; sec != "" {
		return sec, nil
	}
	return guessSection(name), nil
}

// setPrefTx recuerda la sección elegida; si es la del diccionario, olvida
// la preferencia (no hace falta guardarla).
func (s store) setPrefTx(ctx context.Context, tx pgx.Tx, owner, name, sec string, now time.Time) error {
	tok := s.nameToken(name)
	if sec == guessSection(name) {
		_, err := tx.Exec(ctx, `DELETE FROM section_prefs WHERE owner = $1 AND name_token = $2`, owner, tok)
		return err
	}
	sealed, err := s.box.SealJSON(sealPref, prefData{Name: name, Section: sec})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO section_prefs (owner, name_token, updated_on, sealed) VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner, name_token) DO UPDATE SET updated_on = excluded.updated_on, sealed = excluded.sealed`,
		owner, tok, dayOf(now).Format("2006-01-02"), sealed)
	return err
}

type settingsData struct {
	Order []string `json:"orden,omitempty"`
}

// order es el orden de las secciones de la persona.
func (s store) order(ctx context.Context, owner string) ([]string, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT sealed FROM settings WHERE owner = $1`, owner).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultOrder(), nil
	}
	if err != nil {
		return nil, err
	}
	var d settingsData
	if err := s.box.OpenJSON(sealSetting, sealed, &d); err != nil {
		return nil, err
	}
	return normalizeOrder(d.Order), nil
}

func (s store) setOrder(ctx context.Context, owner string, order []string) error {
	sealed, err := s.box.SealJSON(sealSetting, settingsData{Order: normalizeOrder(order)})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO settings (owner, sealed) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET sealed = excluded.sealed`, owner, sealed)
	return err
}

// ---- Conservación ----

// purge borra del historial lo que no se compra desde hace 18 meses, y las
// secciones elegidas a mano de cosas que ya no están ni en la lista ni en el
// historial y no se tocan desde entonces.
func (s store) purge(ctx context.Context, now time.Time) (int64, int64, error) {
	cut := dayOf(now).AddDate(0, -keepMonth, 0).Format("2006-01-02")
	h, err := s.db.Exec(ctx, `DELETE FROM history WHERE last_bought < $1`, cut)
	if err != nil {
		return 0, 0, err
	}
	p, err := s.db.Exec(ctx, `DELETE FROM section_prefs p WHERE p.updated_on < $1
		AND NOT EXISTS (SELECT 1 FROM history h WHERE h.owner = p.owner AND h.name_token = p.name_token)
		AND NOT EXISTS (SELECT 1 FROM items i WHERE i.owner = p.owner AND i.name_token = p.name_token)`, cut)
	if err != nil {
		return h.RowsAffected(), 0, err
	}
	return h.RowsAffected(), p.RowsAffected(), nil
}
