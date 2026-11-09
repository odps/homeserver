package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

var (
	errNotFound  = errors.New("no existe")
	errFull      = errors.New("has llegado al máximo")
	errDuplicate = errors.New("ya existe")
)

const (
	maxCategories = 60
	maxRules      = 300
	maxMovements  = 50000
	maxSubs       = 100
	keepNotice    = 7 * 24 * time.Hour
	keepYears     = 5 // los movimientos de hace más se borran solos
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño, salvo owners, que da la lista de cuentas a
// la tarea de los avisos.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealCat    = "gastos/categories"
	sealRule   = "gastos/rules"
	sealMov    = "gastos/movements"
	sealSub    = "gastos/subscriptions"
	sealNotice = "gastos/notices"
)

// nullID es NULL para el id 0 (sin categoría).
func nullID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// ownCat es la condición «la categoría $3 es de $1 (o no hay)».
const ownCat = `($3::bigint IS NULL OR EXISTS (SELECT 1 FROM categories WHERE owner = $1 AND id = $3))`

// ---- Cuentas y lo de serie ----

// Las categorías de serie. «otros» recoge lo que no encaja en ninguna regla;
// «ingresos», los ingresos sin categoría.
const (
	keyOther  = "otros"
	keyIncome = "ingresos"
	keySubs   = "suscripciones"
)

var defaultCats = []struct{ Key, Name string }{
	{"vivienda", "Vivienda"}, {"comida", "Comida"}, {"transporte", "Transporte"}, {"ocio", "Ocio"}, {"salud", "Salud"},
	{keySubs, "Suscripciones"}, {"ropa", "Ropa"}, {"educacion", "Educación"}, {keyIncome, "Ingresos"}, {keyOther, "Otros"},
}

// defaultRules son reglas de serie con palabras y marcas comunes (datos
// públicos, no de nadie). Se pueden borrar o cambiar.
var defaultRules = map[string][]string{
	"vivienda":   {"alquiler", "hipoteca", "comunidad", "electricidad", "gas natural", "iberdrola", "endesa", "naturgy"},
	"comida":     {"supermercado", "mercadona", "carrefour", "lidl", "aldi", "eroski", "alcampo", "panaderia", "fruteria", "carniceria"},
	"transporte": {"gasolina", "gasoil", "repsol", "cepsa", "renfe", "autobus", "taxi", "uber", "cabify", "parking", "peaje", "itv"},
	"ocio":       {"restaurante", "cine", "teatro", "concierto", "cafeteria"},
	"salud":      {"farmacia", "dentista", "medico", "clinica", "optica", "fisio"},
	keySubs:      {"netflix", "spotify", "hbo", "disney", "prime video", "icloud", "youtube premium", "gimnasio"},
	"ropa":       {"zara", "primark", "zapateria"},
	"educacion":  {"libreria", "academia", "curso"},
	keyIncome:    {"nomina", "salario", "transferencia recibida"},
}

// ensure da de alta la cuenta la primera vez, con sus categorías y reglas de
// serie. Si la persona borra luego alguna, no vuelve.
func (s store) ensure(ctx context.Context, owner string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO owners (owner) VALUES ($1) ON CONFLICT DO NOTHING`, owner)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		ids := map[string]int64{}
		for _, c := range defaultCats {
			sealed, err := s.box.SealJSON(sealCat, catData{Name: c.Name, Key: c.Key})
			if err != nil {
				return err
			}
			var id int64
			if err := tx.QueryRow(ctx, `INSERT INTO categories (owner, name_token, sealed) VALUES ($1, $2, $3) RETURNING id`,
				owner, s.catToken(c.Name), sealed).Scan(&id); err != nil {
				return err
			}
			ids[c.Key] = id
		}
		for _, c := range defaultCats {
			for _, text := range defaultRules[c.Key] {
				sealed, err := s.box.SealJSON(sealRule, ruleData{Text: text})
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO rules (owner, category_id, sealed) VALUES ($1, $2, $3)`, owner, ids[c.Key], sealed); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// owners son todas las cuentas: solo para la tarea de los avisos.
func (s store) owners(ctx context.Context) ([]string, error) {
	rows, _ := s.db.Query(ctx, `SELECT owner FROM owners ORDER BY owner`)
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ---- Categorías ----

type catData struct {
	Name   string  `json:"nombre"`
	Key    string  `json:"clave,omitempty"`       // la de serie (comida, otros…); vacía en las suyas
	Budget float64 `json:"presupuesto,omitempty"` // al mes, en euros; 0 sin presupuesto
	Warned string  `json:"avisado,omitempty"`     // AAAA-MM del último aviso de presupuesto superado
}

type category struct {
	ID int64
	catData
}

func (s store) catToken(name string) int64 {
	return s.box.Token("categoria", strings.Join(seal.Words(name), " "))
}

// sortCats las ordena por nombre, con Ingresos y Otros al final.
func sortCats(list []category) {
	rank := func(c category) int {
		switch c.Key {
		case keyIncome:
			return 1
		case keyOther:
			return 2
		}
		return 0
	}
	sort.SliceStable(list, func(i, j int) bool {
		if ri, rj := rank(list[i]), rank(list[j]); ri != rj {
			return ri < rj
		}
		return seal.Fold(list[i].Name) < seal.Fold(list[j].Name)
	})
}

func (s store) categories(ctx context.Context, owner string) ([]category, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, sealed FROM categories WHERE owner = $1`, owner)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (category, error) {
		var c category
		var sealed []byte
		if err := r.Scan(&c.ID, &sealed); err != nil {
			return c, err
		}
		return c, s.box.OpenJSON(sealCat, sealed, &c.catData)
	})
	sortCats(list)
	return list, err
}

func (s store) addCategory(ctx context.Context, owner string, d catData) (int64, error) {
	sealed, err := s.box.SealJSON(sealCat, d)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM categories WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxCategories {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO categories (owner, name_token, sealed) VALUES ($1, $2, $3) RETURNING id`,
			owner, s.catToken(d.Name), sealed).Scan(&id)
	})
	if isUnique(err) {
		return 0, errDuplicate
	}
	return id, err
}

func (s store) updateCategory(ctx context.Context, owner string, c category) error {
	sealed, err := s.box.SealJSON(sealCat, c.catData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE categories SET name_token = $3, sealed = $4 WHERE owner = $1 AND id = $2`,
		owner, c.ID, s.catToken(c.Name), sealed)
	if isUnique(err) {
		return errDuplicate
	}
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// deleteCategory la borra con sus reglas; sus movimientos y suscripciones
// quedan sin categoría.
func (s store) deleteCategory(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM categories WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Reglas ----

type ruleData struct {
	Text string `json:"texto"`
}

type rule struct {
	ID    int64
	CatID int64
	ruleData
}

func (s store) rules(ctx context.Context, owner string) ([]rule, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, category_id, sealed FROM rules WHERE owner = $1 ORDER BY id`, owner)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (rule, error) {
		var x rule
		var sealed []byte
		if err := r.Scan(&x.ID, &x.CatID, &sealed); err != nil {
			return x, err
		}
		return x, s.box.OpenJSON(sealRule, sealed, &x.ruleData)
	})
}

// addRule guarda una regla. La categoría tiene que ser de la misma persona.
func (s store) addRule(ctx context.Context, owner string, r rule) (int64, error) {
	sealed, err := s.box.SealJSON(sealRule, r.ruleData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM rules WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxRules {
			return errFull
		}
		err := tx.QueryRow(ctx, `INSERT INTO rules (owner, category_id, sealed)
			SELECT $1, id, $3 FROM categories WHERE owner = $1 AND id = $2 RETURNING id`, owner, r.CatID, sealed).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		return err
	})
	return id, err
}

func (s store) deleteRule(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM rules WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Movimientos ----

// Tipos de movimiento.
const (
	kindExpense = "gasto"
	kindIncome  = "ingreso"
)

// Origen de un movimiento.
const (
	originWeb       = "web"
	originAssistant = "asistente"
	originSub       = "suscripcion"
)

type movData struct {
	Kind    string  `json:"tipo"`
	Amount  float64 `json:"importe"` // en euros, siempre positivo
	Concept string  `json:"concepto"`
	Account string  `json:"cuenta,omitempty"` // cuenta o medio de pago
	Note    string  `json:"nota,omitempty"`
	Origin  string  `json:"origen,omitempty"`
	SubID   int64   `json:"suscripcion,omitempty"` // si salió de registrar un cobro
}

type movement struct {
	ID    int64
	Day   time.Time
	CatID int64 // 0: sin categoría
	movData
}

// signed es el importe con signo: los gastos en negativo.
func (m movement) signed() float64 {
	if m.Kind == kindIncome {
		return m.Amount
	}
	return -m.Amount
}

const movCols = `id, day, category_id, sealed`

func (s store) scanMovs(rows pgx.Rows) ([]movement, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (movement, error) {
		var m movement
		var cat *int64
		var sealed []byte
		if err := r.Scan(&m.ID, &m.Day, &cat, &sealed); err != nil {
			return m, err
		}
		if cat != nil {
			m.CatID = *cat
		}
		return m, s.box.OpenJSON(sealMov, sealed, &m.movData)
	})
	// Lo más reciente arriba.
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].Day.Equal(list[j].Day) {
			return list[i].Day.After(list[j].Day)
		}
		return list[i].ID > list[j].ID
	})
	return list, err
}

// movements son los de una persona con fecha en [from, to); to cero, sin
// límite.
func (s store) movements(ctx context.Context, owner string, from, to time.Time) ([]movement, error) {
	if to.IsZero() {
		to = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	rows, _ := s.db.Query(ctx, `SELECT `+movCols+` FROM movements WHERE owner = $1 AND day >= $2 AND day < $3`, owner, from, to)
	return s.scanMovs(rows)
}

func (s store) movement(ctx context.Context, owner string, id int64) (movement, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+movCols+` FROM movements WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanMovs(rows)
	if err != nil {
		return movement{}, err
	}
	if len(list) == 0 {
		return movement{}, errNotFound
	}
	return list[0], nil
}

func insertMov(ctx context.Context, tx pgx.Tx, owner string, m movement, sealed []byte) (int64, error) {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM movements WHERE owner = $1`, owner).Scan(&n); err != nil {
		return 0, err
	}
	if n >= maxMovements {
		return 0, errFull
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO movements (owner, day, category_id, sealed)
		SELECT $1::text, $2::date, $3::bigint, $4::bytea WHERE `+ownCat+` RETURNING id`,
		owner, m.Day, nullID(m.CatID), sealed).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errNotFound
	}
	return id, err
}

// addMovement apunta un movimiento. La categoría tiene que ser de la misma
// persona.
func (s store) addMovement(ctx context.Context, owner string, m movement) (int64, error) {
	sealed, err := s.box.SealJSON(sealMov, m.movData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		id, err = insertMov(ctx, tx, owner, m, sealed)
		return err
	})
	return id, err
}

func (s store) updateMovement(ctx context.Context, owner string, m movement) error {
	sealed, err := s.box.SealJSON(sealMov, m.movData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE movements SET category_id = $3, day = $4, sealed = $5
		WHERE owner = $1 AND id = $2 AND `+ownCat, owner, m.ID, nullID(m.CatID), m.Day, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// setCategory cambia la categoría de varios movimientos (al aplicar una
// regla a lo ya apuntado).
func (s store) setCategory(ctx context.Context, owner string, ids []int64, cat int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `UPDATE movements SET category_id = $3 WHERE owner = $1 AND id = ANY($2) AND `+ownCat, owner, ids, nullID(cat))
	return err
}

func (s store) deleteMovement(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM movements WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Suscripciones ----

// Periodicidades: cuántos meses (o días, la semanal) hay entre cobros.
var periods = []struct {
	Key, Label string
	Months     int
	Days       int
}{
	{"semanal", "Semanal", 0, 7}, {"mensual", "Mensual", 1, 0}, {"bimestral", "Cada 2 meses", 2, 0},
	{"trimestral", "Trimestral", 3, 0}, {"semestral", "Semestral", 6, 0}, {"anual", "Anual", 12, 0},
}

func validPeriod(k string) bool {
	for _, p := range periods {
		if p.Key == k {
			return true
		}
	}
	return false
}

func periodLabel(k string) string {
	for _, p := range periods {
		if p.Key == k {
			return p.Label
		}
	}
	return k
}

type subData struct {
	Name     string    `json:"nombre"`
	Amount   float64   `json:"importe"`
	Period   string    `json:"periodicidad"`
	Next     time.Time `json:"proximo"`       // próximo cobro
	Anchor   int       `json:"dia,omitempty"` // día del mes en que cobra (si el mes no lo tiene, el último)
	Notice   int       `json:"aviso"`         // días de antelación del aviso; -1, sin aviso
	Account  string    `json:"cuenta,omitempty"`
	Note     string    `json:"nota,omitempty"`
	Notified time.Time `json:"avisado,omitzero"` // el cobro del que ya se avisó
}

type subscription struct {
	ID    int64
	CatID int64
	subData
}

// monthly es lo que cuesta al mes, de media.
func (x subData) monthly() float64 {
	for _, p := range periods {
		if p.Key == x.Period {
			if p.Days > 0 {
				return x.Amount * 365.25 / 12 / float64(p.Days)
			}
			return x.Amount / float64(p.Months)
		}
	}
	return 0
}

// after es el cobro siguiente a d.
func (x subData) after(d time.Time) time.Time {
	for _, p := range periods {
		if p.Key == x.Period {
			if p.Days > 0 {
				return d.AddDate(0, 0, p.Days)
			}
			return addMonths(d, p.Months, x.Anchor)
		}
	}
	return d.AddDate(0, 1, 0)
}

func (s store) scanSubs(rows pgx.Rows) ([]subscription, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (subscription, error) {
		var x subscription
		var cat *int64
		var sealed []byte
		if err := r.Scan(&x.ID, &cat, &sealed); err != nil {
			return x, err
		}
		if cat != nil {
			x.CatID = *cat
		}
		return x, s.box.OpenJSON(sealSub, sealed, &x.subData)
	})
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].Next.Equal(list[j].Next) {
			return list[i].Next.Before(list[j].Next)
		}
		return seal.Fold(list[i].Name) < seal.Fold(list[j].Name)
	})
	return list, err
}

// subscriptions son las de una persona, por fecha del próximo cobro.
func (s store) subscriptions(ctx context.Context, owner string) ([]subscription, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, category_id, sealed FROM subscriptions WHERE owner = $1`, owner)
	return s.scanSubs(rows)
}

func (s store) subscription(ctx context.Context, owner string, id int64) (subscription, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, category_id, sealed FROM subscriptions WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanSubs(rows)
	if err != nil {
		return subscription{}, err
	}
	if len(list) == 0 {
		return subscription{}, errNotFound
	}
	return list[0], nil
}

func (s store) addSub(ctx context.Context, owner string, x subscription) (int64, error) {
	sealed, err := s.box.SealJSON(sealSub, x.subData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxSubs {
			return errFull
		}
		err := tx.QueryRow(ctx, `INSERT INTO subscriptions (owner, category_id, sealed) SELECT $1::text, $3::bigint, $2::bytea WHERE `+
			ownCat+` RETURNING id`, owner, sealed, nullID(x.CatID)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		return err
	})
	return id, err
}

func (s store) updateSub(ctx context.Context, owner string, x subscription) error {
	sealed, err := s.box.SealJSON(sealSub, x.subData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE subscriptions SET category_id = $3, sealed = $4 WHERE owner = $1 AND id = $2 AND `+ownCat,
		owner, x.ID, nullID(x.CatID), sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteSub(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM subscriptions WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// charge registra un cobro: apunta el movimiento y guarda la suscripción con
// el siguiente cobro, todo a la vez.
func (s store) charge(ctx context.Context, owner string, x subscription, m movement) (int64, error) {
	ms, err := s.box.SealJSON(sealMov, m.movData)
	if err != nil {
		return 0, err
	}
	ss, err := s.box.SealJSON(sealSub, x.subData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE subscriptions SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, x.ID, ss)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		id, err = insertMov(ctx, tx, owner, m, ms)
		return err
	})
	return id, err
}

// ---- Avisos para Telegram ----

func (s store) addNotice(ctx context.Context, owner, text string, at time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO notices (owner, created_at, sealed) VALUES ($1, $2, $3)`,
		owner, at, s.box.SealString(sealNotice, text))
	return err
}

type notice struct {
	Kind string `json:"tipo"`
	Text string `json:"texto"`
}

// takeNotices entrega los avisos pendientes de una persona (los de más de un
// día ya no se mandan) y los marca como entregados.
func (s store) takeNotices(ctx context.Context, owner string, now time.Time) ([]notice, error) {
	rows, _ := s.db.Query(ctx, `UPDATE notices SET delivered_at = $2
		WHERE owner = $1 AND delivered_at IS NULL AND created_at > $2::timestamptz - interval '1 day'
		RETURNING id, sealed`, owner, now)
	type row struct {
		id   int64
		text string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		var sealed []byte
		if err := r.Scan(&x.id, &sealed); err != nil {
			return x, err
		}
		var err error
		x.text, err = s.box.OpenString(sealNotice, sealed)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	out := []notice{}
	for _, x := range list {
		out = append(out, notice{Kind: "gastos", Text: x.text})
	}
	return out, nil
}

// purge borra lo que ya no hay que guardar: los avisos de más de 7 días y
// los movimientos de hace más de 5 años.
func (s store) purge(ctx context.Context, now time.Time) (int64, error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM notices WHERE created_at < $1`, now.Add(-keepNotice)); err != nil {
		return 0, err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM movements WHERE day < $1`, monthOf(dayOf(now)).AddDate(-keepYears, 0, 0))
	return tag.RowsAffected(), err
}
