package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/seal"
)

var (
	errNotFound = errors.New("no existe")
	errFull     = errors.New("has llegado al máximo")
)

const (
	maxAssets  = 300
	maxOps     = 20000
	maxAlerts  = 100
	keepNotice = 7 * 24 * time.Hour
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño, salvo las de la tarea de precios
// (allAssets), que recorre los activos de todos para pedir cada precio una
// sola vez.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealAsset   = "cartera/assets"
	sealOp      = "cartera/operations"
	sealQuote   = "cartera/quotes"
	sealHistory = "cartera/history"
	sealAlert   = "cartera/alerts"
	sealNotice  = "cartera/notices"
)

// Tipos de activo.
var kinds = []struct{ Key, Label string }{
	{"cripto", "Cripto"}, {"accion", "Acción"}, {"etf", "ETF"}, {"fondo", "Fondo"}, {"otro", "Otro"},
}

func kindLabel(k string) string {
	for _, x := range kinds {
		if x.Key == k {
			return x.Label
		}
	}
	return "Otro"
}

func validKind(k string) bool {
	for _, x := range kinds {
		if x.Key == k {
			return true
		}
	}
	return false
}

// ---- Activos ----

// assetData es lo cifrado de cada activo.
type assetData struct {
	Symbol   string    `json:"simbolo"`
	Name     string    `json:"nombre"`
	Kind     string    `json:"tipo"`
	Source   string    `json:"fuente"`    // kraken, coingecko, yahoo, manual
	SourceID string    `json:"id_fuente"` // par de Kraken, id de CoinGecko, símbolo de Yahoo
	Currency string    `json:"moneda"`    // la de su precio
	ISIN     string    `json:"isin,omitempty"`
	Manual   float64   `json:"precio_manual,omitempty"`
	ManualAt time.Time `json:"precio_manual_fecha,omitzero"`
	Note     string    `json:"nota,omitempty"`
}

type asset struct {
	ID      int64
	Owner   string
	Watch   bool
	Created time.Time
	assetData
}

// key identifica el activo en su fuente (para no pedir dos veces lo mismo).
func (a assetData) key() string {
	if a.Source == sourceManual {
		return sourceManual + "|" + strings.ToUpper(a.Symbol) + "|" + strings.ToUpper(a.Name)
	}
	return a.Source + "|" + strings.ToUpper(a.SourceID)
}

func (s store) assetToken(a assetData) int64 { return s.box.Token("activo", a.key()) }

const assetCols = `id, owner, watch, created_at, sealed`

func (s store) scanAssets(rows pgx.Rows) ([]asset, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (asset, error) {
		var a asset
		var sealed []byte
		if err := r.Scan(&a.ID, &a.Owner, &a.Watch, &a.Created, &sealed); err != nil {
			return a, err
		}
		return a, s.box.OpenJSON(sealAsset, sealed, &a.assetData)
	})
	sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Symbol) < strings.ToLower(list[j].Symbol) })
	return list, err
}

// assets son los activos de una persona, por símbolo.
func (s store) assets(ctx context.Context, owner string) ([]asset, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+assetCols+` FROM assets WHERE owner = $1`, owner)
	return s.scanAssets(rows)
}

func (s store) asset(ctx context.Context, owner string, id int64) (asset, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+assetCols+` FROM assets WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanAssets(rows)
	if err != nil {
		return asset{}, err
	}
	if len(list) == 0 {
		return asset{}, errNotFound
	}
	return list[0], nil
}

// allAssets son los de todos: solo para la tarea que pide los precios.
func (s store) allAssets(ctx context.Context) ([]asset, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+assetCols+` FROM assets`)
	return s.scanAssets(rows)
}

// addAsset añade un activo; si ya lo tenía (misma fuente e id), devuelve
// el que había (y lo pone en seguimiento si se pide).
func (s store) addAsset(ctx context.Context, owner string, d assetData, watch bool) (asset, bool, error) {
	tok := s.assetToken(d)
	var out asset
	existed := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "cartera|"+owner); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT `+assetCols+` FROM assets WHERE owner = $1 AND key_token = $2`, owner, tok)
		list, err := s.scanAssets(rows)
		if err != nil {
			return err
		}
		if len(list) > 0 {
			out, existed = list[0], true
			if watch && !out.Watch {
				out.Watch = true
				_, err = tx.Exec(ctx, `UPDATE assets SET watch = true WHERE owner = $1 AND id = $2`, owner, out.ID)
			}
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM assets WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxAssets {
			return errFull
		}
		sealed, err := s.box.SealJSON(sealAsset, d)
		if err != nil {
			return err
		}
		out = asset{Owner: owner, Watch: watch, assetData: d}
		return tx.QueryRow(ctx, `INSERT INTO assets (owner, key_token, watch, sealed) VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
			owner, tok, watch, sealed).Scan(&out.ID, &out.Created)
	})
	return out, existed, err
}

// updateAsset guarda lo editable (nombre, tipo, precio manual, nota…). La
// fuente y su id no cambian: son lo que identifica el activo.
func (s store) updateAsset(ctx context.Context, owner string, a asset) error {
	sealed, err := s.box.SealJSON(sealAsset, a.assetData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE assets SET sealed = $3, watch = $4 WHERE owner = $1 AND id = $2`, owner, a.ID, sealed, a.Watch)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) setWatch(ctx context.Context, owner string, id int64, watch bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE assets SET watch = $3 WHERE owner = $1 AND id = $2`, owner, id, watch)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// deleteAsset borra el activo con sus operaciones, precios y alertas.
func (s store) deleteAsset(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM assets WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Operaciones ----

// Tipos de operación.
const (
	opBuy      = "compra"
	opSell     = "venta"
	opFee      = "comision"
	opDividend = "dividendo"
	opReward   = "recompensa"
	opTransfer = "traspaso"
)

var opKinds = []struct{ Key, Label string }{
	{opBuy, "Compra"}, {opSell, "Venta"}, {opDividend, "Dividendo"}, {opReward, "Recompensa o staking"},
	{opTransfer, "Traspaso entre plataformas"}, {opFee, "Comisión"},
}

func opLabel(k string) string {
	for _, x := range opKinds {
		if x.Key == k {
			return x.Label
		}
	}
	return k
}

// opData es lo cifrado de cada operación. Los importes van en la moneda de
// la operación; el cambio a euros se calcula con el del BCE de ese día.
type opData struct {
	Kind     string  `json:"tipo"`
	Time     string  `json:"hora,omitempty"`     // HH:MM, si se sabe: ordena las del mismo día
	Qty      float64 `json:"cantidad,omitempty"` // unidades del activo
	Price    float64 `json:"precio,omitempty"`   // por unidad (en recompensas, su valor de mercado)
	Amount   float64 `json:"importe,omitempty"`  // dividendo bruto o comisión suelta
	Fee      float64 `json:"comision,omitempty"` // comisión o retención
	Currency string  `json:"moneda"`
	Platform string  `json:"plataforma,omitempty"`
	To       string  `json:"destino,omitempty"` // traspaso: plataforma de destino
	Note     string  `json:"nota,omitempty"`
	Origin   string  `json:"origen,omitempty"` // web o asistente (fase 2: importada)
}

type operation struct {
	ID      int64
	AssetID int64 // 0: comisión general, sin activo
	Day     time.Time
	opData
}

func (s store) scanOps(rows pgx.Rows) ([]operation, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (operation, error) {
		var o operation
		var aid *int64
		var sealed []byte
		if err := r.Scan(&o.ID, &aid, &o.Day, &sealed); err != nil {
			return o, err
		}
		if aid != nil {
			o.AssetID = *aid
		}
		return o, s.box.OpenJSON(sealOp, sealed, &o.opData)
	})
	sortOps(list)
	return list, err
}

// sortOps las deja en orden de fecha (y hora, si la hay): el de FIFO.
func sortOps(list []operation) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if !a.Day.Equal(b.Day) {
			return a.Day.Before(b.Day)
		}
		if a.Time != b.Time && a.Time != "" && b.Time != "" {
			return a.Time < b.Time
		}
		return a.ID < b.ID
	})
}

const opCols = `id, asset_id, day, sealed`

// ops son todas las operaciones de una persona, de la más antigua a la más
// nueva.
func (s store) ops(ctx context.Context, owner string) ([]operation, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+opCols+` FROM operations WHERE owner = $1`, owner)
	return s.scanOps(rows)
}

func (s store) op(ctx context.Context, owner string, id int64) (operation, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+opCols+` FROM operations WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanOps(rows)
	if err != nil {
		return operation{}, err
	}
	if len(list) == 0 {
		return operation{}, errNotFound
	}
	return list[0], nil
}

func nullID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// addOp apunta una operación. El activo tiene que ser de la misma persona.
func (s store) addOp(ctx context.Context, owner string, o operation) (int64, error) {
	sealed, err := s.box.SealJSON(sealOp, o.opData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if o.AssetID != 0 {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM assets WHERE owner = $1 AND id = $2)`, owner, o.AssetID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errNotFound
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM operations WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxOps {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO operations (owner, asset_id, day, sealed) VALUES ($1, $2, $3, $4) RETURNING id`,
			owner, nullID(o.AssetID), isoDay(o.Day), sealed).Scan(&id)
	})
	return id, err
}

func (s store) updateOp(ctx context.Context, owner string, o operation) error {
	sealed, err := s.box.SealJSON(sealOp, o.opData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE operations SET asset_id = $3, day = $4, sealed = $5
		WHERE owner = $1 AND id = $2 AND ($3::bigint IS NULL OR EXISTS (SELECT 1 FROM assets WHERE owner = $1 AND id = $3))`,
		owner, o.ID, nullID(o.AssetID), isoDay(o.Day), sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteOp(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM operations WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// firstOpDay es la fecha de la operación más antigua de todos (para saber
// desde cuándo hacen falta los cambios de divisas). Cero si no hay.
func (s store) firstOpDay(ctx context.Context) (time.Time, error) {
	var d *time.Time
	err := s.db.QueryRow(ctx, `SELECT min(day) FROM operations`).Scan(&d)
	if err != nil || d == nil {
		return time.Time{}, err
	}
	return *d, nil
}

// ---- Cotizaciones ----

// quote es la última cotización de un activo, en su moneda.
type quote struct {
	Price    float64   `json:"precio"`
	Prev     float64   `json:"anterior,omitempty"` // cierre anterior o apertura del día: la base de la variación
	Currency string    `json:"moneda"`
	At       time.Time `json:"hora"`
}

func (q quote) change() float64 {
	if q.Prev <= 0 || q.Price <= 0 {
		return 0
	}
	return (q.Price/q.Prev - 1) * 100
}

func (s store) setQuote(ctx context.Context, owner string, assetID int64, q quote) error {
	sealed, err := s.box.SealJSON(sealQuote, q)
	if err != nil {
		return err
	}
	// Solo si el activo sigue siendo de esa persona (se ha podido borrar).
	_, err = s.db.Exec(ctx, `INSERT INTO quotes (asset_id, owner, updated_at, sealed)
		SELECT id, owner, $3::timestamptz, $4::bytea FROM assets WHERE owner = $1 AND id = $2
		ON CONFLICT (asset_id) DO UPDATE SET updated_at = excluded.updated_at, sealed = excluded.sealed`,
		owner, assetID, q.At, sealed)
	return err
}

// quotes son las cotizaciones de los activos de una persona.
func (s store) quotes(ctx context.Context, owner string) (map[int64]quote, error) {
	rows, _ := s.db.Query(ctx, `SELECT asset_id, sealed FROM quotes WHERE owner = $1`, owner)
	out := map[int64]quote{}
	var id int64
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&id, &sealed}, func() error {
		var q quote
		if err := s.box.OpenJSON(sealQuote, sealed, &q); err != nil {
			return err
		}
		out[id] = q
		return nil
	})
	return out, err
}

// ---- Histórico ----

// series son cierres diarios, en orden de fecha y sin días repetidos.
type series struct {
	Days   []int32   `json:"dias"` // días desde 1970-01-01
	Closes []float64 `json:"cierres"`
}

const dayUnit = 24 * time.Hour

func epochDay(t time.Time) int32  { return int32(t.Unix() / 86400) }
func fromEpoch(d int32) time.Time { return time.Unix(int64(d)*86400, 0).UTC() }

func (h series) len() int { return len(h.Days) }

// merge junta dos series; lo nuevo manda sobre lo viejo el mismo día.
func (h series) merge(n series) series {
	m := map[int32]float64{}
	for i, d := range h.Days {
		m[d] = h.Closes[i]
	}
	for i, d := range n.Days {
		if n.Closes[i] > 0 {
			m[d] = n.Closes[i]
		}
	}
	out := series{}
	for d := range m {
		out.Days = append(out.Days, d)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i] < out.Days[j] })
	for _, d := range out.Days {
		out.Closes = append(out.Closes, m[d])
	}
	return out
}

// at es el cierre del día (o del último anterior con dato).
func (h series) at(day time.Time) (float64, bool) {
	d := epochDay(day)
	i := sort.Search(len(h.Days), func(i int) bool { return h.Days[i] > d })
	if i == 0 {
		return 0, false
	}
	return h.Closes[i-1], true
}

// since son los puntos desde un día (incluido).
func (h series) since(day time.Time) series {
	d := epochDay(day)
	i := sort.Search(len(h.Days), func(i int) bool { return h.Days[i] >= d })
	return series{Days: h.Days[i:], Closes: h.Closes[i:]}
}

func (h series) last() (time.Time, float64, bool) {
	if len(h.Days) == 0 {
		return time.Time{}, 0, false
	}
	return fromEpoch(h.Days[len(h.Days)-1]), h.Closes[len(h.Closes)-1], true
}

func (s store) setHistory(ctx context.Context, owner string, assetID int64, h series, today time.Time) error {
	sealed, err := s.box.SealJSON(sealHistory, h)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO history (asset_id, owner, updated_on, sealed)
		SELECT id, owner, $3::date, $4::bytea FROM assets WHERE owner = $1 AND id = $2
		ON CONFLICT (asset_id) DO UPDATE SET updated_on = excluded.updated_on, sealed = excluded.sealed`,
		owner, assetID, isoDay(today), sealed)
	return err
}

// history es el histórico de un activo (vacío si aún no lo hay).
func (s store) history(ctx context.Context, owner string, assetID int64) (series, time.Time, error) {
	var h series
	var sealed []byte
	var on time.Time
	err := s.db.QueryRow(ctx, `SELECT updated_on, sealed FROM history WHERE owner = $1 AND asset_id = $2`, owner, assetID).Scan(&on, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, time.Time{}, nil
	}
	if err != nil {
		return h, on, err
	}
	return h, on, s.box.OpenJSON(sealHistory, sealed, &h)
}

// histories son los históricos de todos los activos de una persona.
func (s store) histories(ctx context.Context, owner string) (map[int64]series, error) {
	rows, _ := s.db.Query(ctx, `SELECT asset_id, sealed FROM history WHERE owner = $1`, owner)
	out := map[int64]series{}
	var id int64
	var sealed []byte
	_, err := pgx.ForEachRow(rows, []any{&id, &sealed}, func() error {
		var h series
		if err := s.box.OpenJSON(sealHistory, sealed, &h); err != nil {
			return err
		}
		out[id] = h
		return nil
	})
	return out, err
}

// historyDays dice, de cada activo con histórico, qué día se actualizó.
func (s store) historyDays(ctx context.Context) (map[int64]time.Time, error) {
	rows, _ := s.db.Query(ctx, `SELECT asset_id, updated_on FROM history`)
	out := map[int64]time.Time{}
	var id int64
	var on time.Time
	_, err := pgx.ForEachRow(rows, []any{&id, &on}, func() error { out[id] = on; return nil })
	return out, err
}

// ---- Alertas ----

// Tipos de alerta. Con activo: precio por encima o por debajo, variación del
// día y caída desde el máximo. Sin activo (la cartera entera): valor por
// encima o por debajo y variación del día.
const (
	alertAbove = "encima"
	alertBelow = "debajo"
	alertDay   = "dia"
	alertDrop  = "caida"
)

type alertData struct {
	Kind      string  `json:"tipo"`
	Threshold float64 `json:"umbral"`         // precio o valor; en dia y caida, porcentaje
	Days      int     `json:"dias,omitempty"` // caida: máximo de los últimos N días
	Repeat    bool    `json:"repetir"`        // cada vez que vuelva a cruzar; si no, una vez
	Last      string  `json:"ultimo,omitempty"`
}

type alert struct {
	ID      int64
	AssetID int64 // 0: la cartera entera
	Active  bool
	Crossed bool
	Created time.Time
	Fired   time.Time
	alertData
}

func (s store) scanAlerts(rows pgx.Rows) ([]alert, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (alert, error) {
		var a alert
		var aid *int64
		var fired *time.Time
		var sealed []byte
		if err := r.Scan(&a.ID, &aid, &a.Active, &a.Crossed, &a.Created, &fired, &sealed); err != nil {
			return a, err
		}
		if aid != nil {
			a.AssetID = *aid
		}
		if fired != nil {
			a.Fired = *fired
		}
		return a, s.box.OpenJSON(sealAlert, sealed, &a.alertData)
	})
}

const alertCols = `id, asset_id, active, crossed, created_at, fired_at, sealed`

func (s store) alerts(ctx context.Context, owner string) ([]alert, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+alertCols+` FROM alerts WHERE owner = $1 ORDER BY id`, owner)
	return s.scanAlerts(rows)
}

func (s store) addAlert(ctx context.Context, owner string, a alert) (int64, error) {
	sealed, err := s.box.SealJSON(sealAlert, a.alertData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if a.AssetID != 0 {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM assets WHERE owner = $1 AND id = $2)`, owner, a.AssetID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errNotFound
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxAlerts {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO alerts (owner, asset_id, sealed) VALUES ($1, $2, $3) RETURNING id`,
			owner, nullID(a.AssetID), sealed).Scan(&id)
	})
	return id, err
}

// setAlertActive enciende o apaga una alerta. Al encenderla se rearma.
func (s store) setAlertActive(ctx context.Context, owner string, id int64, on bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE alerts SET active = $3, crossed = CASE WHEN $3 THEN false ELSE crossed END
		WHERE owner = $1 AND id = $2`, owner, id, on)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// saveAlertState guarda lo que cambia al evaluar una alerta.
func (s store) saveAlertState(ctx context.Context, owner string, a alert) error {
	sealed, err := s.box.SealJSON(sealAlert, a.alertData)
	if err != nil {
		return err
	}
	var fired *time.Time
	if !a.Fired.IsZero() {
		fired = &a.Fired
	}
	_, err = s.db.Exec(ctx, `UPDATE alerts SET active = $3, crossed = $4, fired_at = $5, sealed = $6 WHERE owner = $1 AND id = $2`,
		owner, a.ID, a.Active, a.Crossed, fired, sealed)
	return err
}

func (s store) deleteAlert(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM alerts WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
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
// día ya no se mandan: el precio ya será otro) y los marca como entregados.
// De paso borra los de más de 7 días.
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
	if _, err := s.db.Exec(ctx, `DELETE FROM notices WHERE created_at < $1`, now.Add(-keepNotice)); err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	out := []notice{}
	for _, x := range list {
		out = append(out, notice{Kind: "cartera", Text: x.text})
	}
	return out, nil
}

// ---- Cambios de divisas ----

// fxTable son los cambios del BCE por día: unidades de cada moneda por 1 EUR.
type fxTable struct {
	days  []time.Time // en orden
	rates []map[string]float64
}

func (s store) fx(ctx context.Context) (fxTable, error) {
	rows, _ := s.db.Query(ctx, `SELECT day, rates FROM fx ORDER BY day`)
	var t fxTable
	var day time.Time
	var raw []byte
	_, err := pgx.ForEachRow(rows, []any{&day, &raw}, func() error {
		m := map[string]float64{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		t.days = append(t.days, day)
		t.rates = append(t.rates, m)
		return nil
	})
	return t, err
}

func (s store) saveFX(ctx context.Context, rates map[string]map[string]float64) error {
	if len(rates) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for day, m := range rates {
		raw, _ := json.Marshal(m)
		b.Queue(`INSERT INTO fx (day, rates) VALUES ($1, $2) ON CONFLICT (day) DO UPDATE SET rates = excluded.rates`, day, raw)
	}
	return s.db.SendBatch(ctx, b).Close()
}

// rate son las unidades de cur por 1 EUR el día day (o el último anterior
// publicado; si es anterior a todo lo guardado, el primero). false si no se
// conoce esa moneda.
func (t fxTable) rate(cur string, day time.Time) (float64, bool) {
	if cur == "EUR" || cur == "" {
		return 1, true
	}
	i := sort.Search(len(t.days), func(i int) bool { return t.days[i].After(day) })
	for j := i - 1; j >= 0; j-- {
		if r := t.rates[j][cur]; r > 0 {
			return r, true
		}
	}
	for j := i; j < len(t.days); j++ {
		if r := t.rates[j][cur]; r > 0 {
			return r, true
		}
	}
	return 0, false
}

func (t fxTable) first() time.Time {
	if len(t.days) == 0 {
		return time.Time{}
	}
	return t.days[0]
}

func (t fxTable) last() time.Time {
	if len(t.days) == 0 {
		return time.Time{}
	}
	return t.days[len(t.days)-1]
}
