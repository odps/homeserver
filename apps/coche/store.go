package main

import (
	"context"
	"errors"
	"sort"
	"strconv"
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
	maxVehicles = 20
	maxRefuels  = 20000
	maxTasks    = 60 // por vehículo
	maxExpenses = 20000
	keepNotice  = 7 * 24 * time.Hour
)

// store guarda lo de cada persona cifrado (platform/seal). Todas las
// consultas filtran por el dueño, salvo las de la tarea periódica (owners,
// allSettings), que recorren las cuentas para pedir cada municipio una sola
// vez y avisar de lo que vence.
type store struct {
	db  *pgxpool.Pool
	box *seal.Box
}

// Contextos de cifrado: lo de una tabla no se descifra como otra.
const (
	sealVehicle  = "coche/vehicles"
	sealRefuel   = "coche/refuels"
	sealTask     = "coche/maintenance"
	sealExpense  = "coche/expenses"
	sealSettings = "coche/settings"
	sealNotice   = "coche/notices"
	sealPrices   = "coche/prices"
)

// ---- Vehículos ----

// Combustibles de un vehículo; product es el carburante que se busca en las
// gasolineras por defecto.
var fuels = []struct{ Key, Label, Product string }{
	{"gasolina", "Gasolina", "g95"}, {"diesel", "Diésel", "goa"}, {"hibrido", "Híbrido (gasolina)", "g95"},
	{"glp", "GLP (autogás)", "glp"}, {"gnc", "Gas natural (GNC)", "gnc"}, {"electrico", "Eléctrico", ""},
}

func fuelLabel(k string) string {
	for _, f := range fuels {
		if f.Key == k {
			return f.Label
		}
	}
	return "Gasolina"
}

func validFuel(k string) bool {
	for _, f := range fuels {
		if f.Key == k {
			return true
		}
	}
	return false
}

type vehicleData struct {
	Name    string `json:"nombre"`
	Model   string `json:"modelo,omitempty"`    // marca y modelo
	Plate   string `json:"matricula,omitempty"` // dato personal: solo en la web
	Fuel    string `json:"combustible"`
	StartKm int    `json:"km_iniciales,omitempty"`
	Note    string `json:"nota,omitempty"`
}

type vehicle struct {
	ID      int64
	Created time.Time
	vehicleData
}

// unit es la unidad de lo que se echa: litros o kWh.
func (v vehicleData) unit() string {
	if v.Fuel == "electrico" {
		return "kWh"
	}
	return "l"
}

func (s store) scanVehicles(rows pgx.Rows) ([]vehicle, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (vehicle, error) {
		var v vehicle
		var sealed []byte
		if err := r.Scan(&v.ID, &v.Created, &sealed); err != nil {
			return v, err
		}
		return v, s.box.OpenJSON(sealVehicle, sealed, &v.vehicleData)
	})
}

// vehicles son los de una persona, en el orden en que los añadió.
func (s store) vehicles(ctx context.Context, owner string) ([]vehicle, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, created_at, sealed FROM vehicles WHERE owner = $1 ORDER BY id`, owner)
	return s.scanVehicles(rows)
}

func (s store) vehicle(ctx context.Context, owner string, id int64) (vehicle, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, created_at, sealed FROM vehicles WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanVehicles(rows)
	if err != nil {
		return vehicle{}, err
	}
	if len(list) == 0 {
		return vehicle{}, errNotFound
	}
	return list[0], nil
}

func (s store) addVehicle(ctx context.Context, owner string, d vehicleData, at time.Time) (int64, error) {
	sealed, err := s.box.SealJSON(sealVehicle, d)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "coche|"+owner); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicles WHERE owner = $1`, owner).Scan(&n); err != nil {
			return err
		}
		if n >= maxVehicles {
			return errFull
		}
		return tx.QueryRow(ctx, `INSERT INTO vehicles (owner, created_at, sealed) VALUES ($1, $2, $3) RETURNING id`, owner, at, sealed).Scan(&id)
	})
	return id, err
}

func (s store) updateVehicle(ctx context.Context, owner string, v vehicle) error {
	sealed, err := s.box.SealJSON(sealVehicle, v.vehicleData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE vehicles SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, v.ID, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// deleteVehicle borra el vehículo con sus repostajes, tareas y gastos.
func (s store) deleteVehicle(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM vehicles WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// owners son las cuentas con algún vehículo o con preferencias: solo para
// la tarea periódica.
func (s store) owners(ctx context.Context) ([]string, error) {
	rows, _ := s.db.Query(ctx, `SELECT owner FROM vehicles UNION SELECT owner FROM settings ORDER BY 1`)
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// vehicleCheck es la condición de que el vehículo $2 sea de $1.
const vehicleCheck = `EXISTS (SELECT 1 FROM vehicles WHERE owner = $1 AND id = $2)`

// insertChecked inserta una fila de un vehículo de la persona, sin pasar
// del máximo.
func (s store) insertChecked(ctx context.Context, owner string, vehicleID int64, table string, max int, insert string, args ...any) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT `+vehicleCheck, owner, vehicleID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		// Las tareas se cuentan por vehículo; lo demás, por persona.
		var n int
		q, qargs := `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` WHERE owner = $1`, []any{owner}
		if table == "maintenance" {
			q, qargs = q+` AND vehicle_id = $2`, append(qargs, vehicleID)
		}
		if err := tx.QueryRow(ctx, q, qargs...).Scan(&n); err != nil {
			return err
		}
		if n >= max {
			return errFull
		}
		return tx.QueryRow(ctx, insert, args...).Scan(&id)
	})
	return id, err
}

// ---- Repostajes ----

type refuelData struct {
	Km      int     `json:"km"`
	Liters  float64 `json:"litros"`
	Price   float64 `json:"precio"` // por litro (o kWh)
	Total   float64 `json:"total"`
	Full    bool    `json:"lleno"`
	Station string  `json:"gasolinera,omitempty"`
	Note    string  `json:"nota,omitempty"`
	Origin  string  `json:"origen,omitempty"` // web o asistente
}

type refuel struct {
	ID        int64
	VehicleID int64
	Day       time.Time
	refuelData
}

func (s store) scanRefuels(rows pgx.Rows) ([]refuel, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (refuel, error) {
		var x refuel
		var sealed []byte
		if err := r.Scan(&x.ID, &x.VehicleID, &x.Day, &sealed); err != nil {
			return x, err
		}
		return x, s.box.OpenJSON(sealRefuel, sealed, &x.refuelData)
	})
	sortRefuels(list)
	return list, err
}

// sortRefuels los deja por fecha y, el mismo día, por km.
func sortRefuels(list []refuel) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if !a.Day.Equal(b.Day) {
			return a.Day.Before(b.Day)
		}
		if a.Km != b.Km {
			return a.Km < b.Km
		}
		return a.ID < b.ID
	})
}

// refuels son los repostajes de una persona (de un vehículo, o de todos con
// vehicleID 0), del más antiguo al más nuevo.
func (s store) refuels(ctx context.Context, owner string, vehicleID int64) ([]refuel, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, vehicle_id, day, sealed FROM refuels WHERE owner = $1 AND ($2::bigint = 0 OR vehicle_id = $2)`, owner, vehicleID)
	return s.scanRefuels(rows)
}

func (s store) refuel(ctx context.Context, owner string, id int64) (refuel, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, vehicle_id, day, sealed FROM refuels WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanRefuels(rows)
	if err != nil {
		return refuel{}, err
	}
	if len(list) == 0 {
		return refuel{}, errNotFound
	}
	return list[0], nil
}

func (s store) addRefuel(ctx context.Context, owner string, r refuel) (int64, error) {
	sealed, err := s.box.SealJSON(sealRefuel, r.refuelData)
	if err != nil {
		return 0, err
	}
	return s.insertChecked(ctx, owner, r.VehicleID, "refuels", maxRefuels,
		`INSERT INTO refuels (owner, vehicle_id, day, sealed) VALUES ($1, $2, $3, $4) RETURNING id`, owner, r.VehicleID, isoDay(r.Day), sealed)
}

func (s store) updateRefuel(ctx context.Context, owner string, r refuel) error {
	sealed, err := s.box.SealJSON(sealRefuel, r.refuelData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE refuels SET day = $3, sealed = $4 WHERE owner = $1 AND id = $2`, owner, r.ID, isoDay(r.Day), sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteRefuel(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM refuels WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Mantenimiento ----

// Tipos de tarea, con lo habitual de cada una (se puede cambiar).
var taskKinds = []struct {
	Key, Label  string
	EveryKm     int
	EveryMonths int
}{
	{"aceite", "Aceite y filtro", 15000, 12},
	{"itv", "ITV", 0, 24},
	{"neumaticos", "Neumáticos", 40000, 60},
	{"frenos", "Frenos (pastillas)", 30000, 0},
	{"filtros", "Filtros (aire, habitáculo)", 30000, 24},
	{"distribucion", "Correa de distribución", 120000, 96},
	{"revision", "Revisión", 30000, 24},
	{"otro", "Otra", 0, 0},
}

func taskKindLabel(k string) string {
	for _, x := range taskKinds {
		if x.Key == k {
			return x.Label
		}
	}
	return "Otra"
}

func validTaskKind(k string) bool {
	for _, x := range taskKinds {
		if x.Key == k {
			return true
		}
	}
	return false
}

type taskData struct {
	Kind        string    `json:"tipo"`
	Name        string    `json:"nombre"`
	EveryKm     int       `json:"cada_km,omitempty"`
	EveryMonths int       `json:"cada_meses,omitempty"`
	LastKm      int       `json:"ultimo_km,omitempty"`
	LastDay     time.Time `json:"ultima_fecha,omitzero"`
	NextKm      int       `json:"proximo_km,omitempty"` // si se sabe (fija la próxima)
	NextDay     time.Time `json:"proxima_fecha,omitzero"`
	Note        string    `json:"nota,omitempty"`
	Notified    string    `json:"avisado,omitempty"` // vencimiento del que ya se avisó
}

type task struct {
	ID        int64
	VehicleID int64
	Created   time.Time
	taskData
}

// cycle identifica el vencimiento actual: cambia al marcarla como hecha o
// al cambiar sus fechas o km, y así se vuelve a avisar.
func (t taskData) cycle() string {
	return isoDay(t.LastDay) + "|" + strconv.Itoa(t.LastKm) + "|" + isoDay(t.NextDay) + "|" + strconv.Itoa(t.NextKm) + "|" +
		strconv.Itoa(t.EveryKm) + "|" + strconv.Itoa(t.EveryMonths)
}

func (s store) scanTasks(rows pgx.Rows) ([]task, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (task, error) {
		var t task
		var sealed []byte
		if err := r.Scan(&t.ID, &t.VehicleID, &t.Created, &sealed); err != nil {
			return t, err
		}
		return t, s.box.OpenJSON(sealTask, sealed, &t.taskData)
	})
}

// tasks son las tareas de una persona (de un vehículo, o de todos con 0).
func (s store) tasks(ctx context.Context, owner string, vehicleID int64) ([]task, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, vehicle_id, created_at, sealed FROM maintenance WHERE owner = $1 AND ($2::bigint = 0 OR vehicle_id = $2) ORDER BY id`, owner, vehicleID)
	return s.scanTasks(rows)
}

func (s store) task(ctx context.Context, owner string, id int64) (task, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, vehicle_id, created_at, sealed FROM maintenance WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanTasks(rows)
	if err != nil {
		return task{}, err
	}
	if len(list) == 0 {
		return task{}, errNotFound
	}
	return list[0], nil
}

func (s store) addTask(ctx context.Context, owner string, t task) (int64, error) {
	sealed, err := s.box.SealJSON(sealTask, t.taskData)
	if err != nil {
		return 0, err
	}
	return s.insertChecked(ctx, owner, t.VehicleID, "maintenance", maxTasks,
		`INSERT INTO maintenance (owner, vehicle_id, sealed) VALUES ($1, $2, $3) RETURNING id`, owner, t.VehicleID, sealed)
}

func (s store) updateTask(ctx context.Context, owner string, t task) error {
	sealed, err := s.box.SealJSON(sealTask, t.taskData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE maintenance SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, t.ID, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteTask(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM maintenance WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// taskDone guarda la tarea hecha y su gasto, a la vez.
func (s store) taskDone(ctx context.Context, owner string, t task, e expense) (int64, error) {
	tSealed, err := s.box.SealJSON(sealTask, t.taskData)
	if err != nil {
		return 0, err
	}
	eSealed, err := s.box.SealJSON(sealExpense, e.expenseData)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE maintenance SET sealed = $3 WHERE owner = $1 AND id = $2`, owner, t.ID, tSealed)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return tx.QueryRow(ctx, `INSERT INTO expenses (owner, vehicle_id, maintenance_id, day, sealed) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			owner, t.VehicleID, t.ID, isoDay(e.Day), eSealed).Scan(&id)
	})
	return id, err
}

// ---- Gastos ----

// Tipos de gasto.
var expenseKinds = []struct{ Key, Label string }{
	{"mantenimiento", "Mantenimiento o taller"}, {"seguro", "Seguro"}, {"parking", "Parking"}, {"peaje", "Peaje"},
	{"multa", "Multa"}, {"impuesto", "Impuesto (IVTM)"}, {"lavado", "Lavado"}, {"otro", "Otro"},
}

func expenseLabel(k string) string {
	for _, x := range expenseKinds {
		if x.Key == k {
			return x.Label
		}
	}
	return "Otro"
}

func validExpense(k string) bool {
	for _, x := range expenseKinds {
		if x.Key == k {
			return true
		}
	}
	return false
}

type expenseData struct {
	Kind   string  `json:"tipo"`
	Amount float64 `json:"importe"`
	Km     int     `json:"km,omitempty"`
	Note   string  `json:"nota,omitempty"`
	Origin string  `json:"origen,omitempty"`
}

type expense struct {
	ID        int64
	VehicleID int64
	TaskID    int64 // 0: no viene de una tarea
	Day       time.Time
	expenseData
}

func (s store) scanExpenses(rows pgx.Rows) ([]expense, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (expense, error) {
		var e expense
		var tid *int64
		var sealed []byte
		if err := r.Scan(&e.ID, &e.VehicleID, &tid, &e.Day, &sealed); err != nil {
			return e, err
		}
		if tid != nil {
			e.TaskID = *tid
		}
		return e, s.box.OpenJSON(sealExpense, sealed, &e.expenseData)
	})
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].Day.Equal(list[j].Day) {
			return list[i].Day.Before(list[j].Day)
		}
		return list[i].ID < list[j].ID
	})
	return list, err
}

const expenseCols = `id, vehicle_id, maintenance_id, day, sealed`

// expenses son los gastos de una persona (de un vehículo, o de todos con 0),
// del más antiguo al más nuevo.
func (s store) expenses(ctx context.Context, owner string, vehicleID int64) ([]expense, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+expenseCols+` FROM expenses WHERE owner = $1 AND ($2::bigint = 0 OR vehicle_id = $2)`, owner, vehicleID)
	return s.scanExpenses(rows)
}

func (s store) expense(ctx context.Context, owner string, id int64) (expense, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+expenseCols+` FROM expenses WHERE owner = $1 AND id = $2`, owner, id)
	list, err := s.scanExpenses(rows)
	if err != nil {
		return expense{}, err
	}
	if len(list) == 0 {
		return expense{}, errNotFound
	}
	return list[0], nil
}

func (s store) addExpense(ctx context.Context, owner string, e expense) (int64, error) {
	sealed, err := s.box.SealJSON(sealExpense, e.expenseData)
	if err != nil {
		return 0, err
	}
	return s.insertChecked(ctx, owner, e.VehicleID, "expenses", maxExpenses,
		`INSERT INTO expenses (owner, vehicle_id, day, sealed) VALUES ($1, $2, $3, $4) RETURNING id`, owner, e.VehicleID, isoDay(e.Day), sealed)
}

func (s store) updateExpense(ctx context.Context, owner string, e expense) error {
	sealed, err := s.box.SealJSON(sealExpense, e.expenseData)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE expenses SET day = $3, sealed = $4 WHERE owner = $1 AND id = $2`, owner, e.ID, isoDay(e.Day), sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

func (s store) deleteExpense(ctx context.Context, owner string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM expenses WHERE owner = $1 AND id = $2`, owner, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errNotFound
	}
	return err
}

// ---- Preferencias ----

// defaultMunicipio es Valencia, mientras la persona no elija otro.
var defaultMunicipio = municipio{ID: 7214, Name: "Valencia", Province: "Valencia / València"}

type settingsData struct {
	Municipio municipio `json:"municipio"`
	Product   string    `json:"carburante,omitempty"` // vacío: el del primer vehículo
	Summary   bool      `json:"resumen,omitempty"`    // la más barata en el resumen de la mañana
}

// settings son las preferencias de una persona (las de por defecto si no
// las ha cambiado).
func (s store) settings(ctx context.Context, owner string) (settingsData, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT sealed FROM settings WHERE owner = $1`, owner).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return settingsData{Municipio: defaultMunicipio}, nil
	}
	var d settingsData
	if err == nil {
		err = s.box.OpenJSON(sealSettings, sealed, &d)
	}
	if d.Municipio.ID == 0 {
		d.Municipio = defaultMunicipio
	}
	return d, err
}

func (s store) saveSettings(ctx context.Context, owner string, d settingsData) error {
	sealed, err := s.box.SealJSON(sealSettings, d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO settings (owner, sealed) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET sealed = excluded.sealed, updated_at = now()`, owner, sealed)
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

// takeNotices entrega los avisos pendientes de una persona (los de más de
// tres días ya no se mandan) y los marca como entregados. De paso borra los
// de más de 7 días.
func (s store) takeNotices(ctx context.Context, owner string, now time.Time) ([]notice, error) {
	rows, _ := s.db.Query(ctx, `UPDATE notices SET delivered_at = $2
		WHERE owner = $1 AND delivered_at IS NULL AND created_at > $2::timestamptz - interval '3 days'
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
		out = append(out, notice{Kind: "coche", Text: x.text})
	}
	return out, nil
}

// ---- Municipios (públicos) ----

type municipio struct {
	ID       int    `json:"id"`
	Name     string `json:"nombre"`
	Province string `json:"provincia"`
}

// municipiosAt es cuándo se guardó la lista (cero si no la hay).
func (s store) municipiosAt(ctx context.Context) (time.Time, error) {
	var at *time.Time
	err := s.db.QueryRow(ctx, `SELECT max(fetched_at) FROM municipios`).Scan(&at)
	if err != nil || at == nil {
		return time.Time{}, err
	}
	return *at, nil
}

// saveMunicipios cambia la lista entera.
func (s store) saveMunicipios(ctx context.Context, list []municipio, at time.Time) error {
	if len(list) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM municipios`); err != nil {
			return err
		}
		rows := make([][]any, 0, len(list))
		for _, m := range list {
			rows = append(rows, []any{int32(m.ID), m.Name, m.Province, at})
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"municipios"}, []string{"id", "nombre", "provincia", "fetched_at"}, pgx.CopyFromRows(rows))
		return err
	})
}

// allMunicipios es la lista entera, por nombre.
func (s store) allMunicipios(ctx context.Context) ([]municipio, error) {
	rows, _ := s.db.Query(ctx, `SELECT id, nombre, provincia FROM municipios ORDER BY nombre, provincia`)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (municipio, error) {
		var m municipio
		var id int32
		err := r.Scan(&id, &m.Name, &m.Province)
		m.ID = int(id)
		return m, err
	})
}

func (s store) municipio(ctx context.Context, id int) (municipio, error) {
	m := municipio{ID: id}
	err := s.db.QueryRow(ctx, `SELECT nombre, provincia FROM municipios WHERE id = $1`, int32(id)).Scan(&m.Name, &m.Province)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, errNotFound
	}
	return m, err
}

// ---- Precios de las gasolineras (públicos, por municipio) ----

func (s store) priceToken(municipioID int) int64 {
	return s.box.Token("municipio", strconv.Itoa(municipioID))
}

// savePrices guarda las gasolineras de un municipio.
func (s store) savePrices(ctx context.Context, municipioID int, p priceList) error {
	sealed, err := s.box.SealJSON(sealPrices+"/"+strconv.Itoa(municipioID), p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO prices (key_token, fetched_at, sealed) VALUES ($1, $2, $3)
		ON CONFLICT (key_token) DO UPDATE SET fetched_at = excluded.fetched_at, sealed = excluded.sealed`,
		s.priceToken(municipioID), p.Fetched, sealed)
	return err
}

// prices son las gasolineras guardadas de un municipio (vacío si no hay).
func (s store) prices(ctx context.Context, municipioID int) (priceList, error) {
	var sealed []byte
	var p priceList
	err := s.db.QueryRow(ctx, `SELECT sealed FROM prices WHERE key_token = $1`, s.priceToken(municipioID)).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, s.box.OpenJSON(sealPrices+"/"+strconv.Itoa(municipioID), sealed, &p)
}

// pricesFetched dice, de cada municipio pedido, cuándo se pidió.
func (s store) pricesFetched(ctx context.Context, ids []int) (map[int]time.Time, error) {
	byToken := map[int64]int{}
	var tokens []int64
	for _, id := range ids {
		t := s.priceToken(id)
		byToken[t] = id
		tokens = append(tokens, t)
	}
	rows, _ := s.db.Query(ctx, `SELECT key_token, fetched_at FROM prices WHERE key_token = ANY($1)`, tokens)
	out := map[int]time.Time{}
	var tok int64
	var at time.Time
	_, err := pgx.ForEachRow(rows, []any{&tok, &at}, func() error { out[byToken[tok]] = at; return nil })
	return out, err
}

// prunePrices borra los municipios que ya no tiene elegidos nadie (y que no
// se han pedido en el último día).
func (s store) prunePrices(ctx context.Context, keep []int, before time.Time) error {
	tokens := []int64{}
	for _, id := range keep {
		tokens = append(tokens, s.priceToken(id))
	}
	_, err := s.db.Exec(ctx, `DELETE FROM prices WHERE NOT (key_token = ANY($1)) AND fetched_at < $2`, tokens, before)
	return err
}
