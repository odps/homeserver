package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"homeserver/platform/seal"
)

type service struct {
	store store
	fuel  *fuelSource
	log   *slog.Logger
	now   func() time.Time

	mu       sync.Mutex
	pricesAt time.Time // última pasada de precios
	dueAt    time.Time // última revisión de vencimientos
	kickOnce sync.Once
	kickC    chan struct{}
}

// Cada cuánto se piden los precios de un municipio, cada cuánto se mira si
// hay que pedirlos y cada cuánto se revisan los vencimientos.
const (
	priceEvery      = 6 * time.Hour
	priceCheck      = 10 * time.Minute
	dueEvery        = time.Hour
	municipiosEvery = 30 * 24 * time.Hour
)

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *service) today() time.Time { return dayOf(s.clock()) }

// inputError es un error de lo que ha escrito la persona: se le puede
// enseñar tal cual. Los demás errores no salen de la app.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func bad(msg string) error { return inputError{msg} }

func isInput(err error) bool {
	var ie inputError
	return errors.As(err, &ie)
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }

func badNum(x float64) bool { return x < 0 || math.IsNaN(x) || math.IsInf(x, 0) }

// ---- Cargar y calcular ----

// load hace las cuentas de un vehículo de la persona.
func (s *service) load(ctx context.Context, owner string, vehicleID int64) (*vstats, error) {
	v, err := s.store.vehicle(ctx, owner, vehicleID)
	if err != nil {
		return nil, err
	}
	rs, err := s.store.refuels(ctx, owner, v.ID)
	if err != nil {
		return nil, err
	}
	es, err := s.store.expenses(ctx, owner, v.ID)
	if err != nil {
		return nil, err
	}
	ts, err := s.store.tasks(ctx, owner, v.ID)
	if err != nil {
		return nil, err
	}
	return compute(v, rs, es, ts, s.today()), nil
}

// loadAll hace las cuentas de todos los vehículos de la persona.
func (s *service) loadAll(ctx context.Context, owner string) ([]*vstats, error) {
	vs, err := s.store.vehicles(ctx, owner)
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	rs, err := s.store.refuels(ctx, owner, 0)
	if err != nil {
		return nil, err
	}
	es, err := s.store.expenses(ctx, owner, 0)
	if err != nil {
		return nil, err
	}
	ts, err := s.store.tasks(ctx, owner, 0)
	if err != nil {
		return nil, err
	}
	today := s.today()
	var out []*vstats
	for _, v := range vs {
		var r []refuel
		var e []expense
		var t []task
		for _, x := range rs {
			if x.VehicleID == v.ID {
				r = append(r, x)
			}
		}
		for _, x := range es {
			if x.VehicleID == v.ID {
				e = append(e, x)
			}
		}
		for _, x := range ts {
			if x.VehicleID == v.ID {
				t = append(t, x)
			}
		}
		out = append(out, compute(v, r, e, t, today))
	}
	return out, nil
}

// findVehicle busca un vehículo por id o por nombre, modelo o matrícula. Sin
// nada, el único que haya.
func findVehicle(list []vehicle, q string) (vehicle, error) {
	q = strings.TrimSpace(q)
	if len(list) == 0 {
		return vehicle{}, bad("Aún no hay ningún vehículo: añádelo en la web de Coche.")
	}
	if q == "" {
		if len(list) == 1 {
			return list[0], nil
		}
		return vehicle{}, bad("Hay varios vehículos: di cuál (" + vehicleNames(list) + ").")
	}
	for _, v := range list {
		if idStr(v.ID) == q {
			return v, nil
		}
	}
	fq := strings.Join(seal.Words(q), " ")
	var exact, partial []vehicle
	for _, v := range list {
		name := strings.Join(seal.Words(v.Name), " ")
		all := strings.Join(seal.Words(v.Name+" "+v.Model+" "+v.Plate), " ")
		switch {
		case name == fq || strings.EqualFold(strings.ReplaceAll(v.Plate, " ", ""), strings.ReplaceAll(q, " ", "")):
			exact = append(exact, v)
		case fq != "" && strings.Contains(all, fq):
			partial = append(partial, v)
		}
	}
	if len(exact) == 0 {
		exact = partial
	}
	switch len(exact) {
	case 0:
		return vehicle{}, bad("No hay ningún vehículo así. Son: " + vehicleNames(list) + ".")
	case 1:
		return exact[0], nil
	}
	return vehicle{}, bad("Hay varios que encajan: " + vehicleNames(exact) + ".")
}

func vehicleNames(list []vehicle) string {
	var names []string
	for _, v := range list {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

// ---- Vehículos ----

type vehicleInput struct {
	Name, Model, Plate, Fuel, Note string
	StartKm                        int
}

func buildVehicle(in vehicleInput) (vehicleData, error) {
	d := vehicleData{Name: clip(in.Name, 40), Model: clip(in.Model, 60), Plate: strings.ToUpper(clip(in.Plate, 15)),
		Fuel: in.Fuel, StartKm: in.StartKm, Note: clip(in.Note, 300)}
	switch {
	case d.Name == "" && d.Model != "":
		d.Name = clip(d.Model, 40)
	case d.Name == "":
		return d, bad("Ponle un nombre (el de siempre: «el Ibiza», «la furgo»…).")
	}
	if !validFuel(d.Fuel) {
		d.Fuel = "gasolina"
	}
	if d.StartKm < 0 || d.StartKm > 2000000 {
		return d, bad("Los km iniciales no son válidos.")
	}
	return d, nil
}

func (s *service) addVehicle(ctx context.Context, owner string, in vehicleInput) (int64, error) {
	d, err := buildVehicle(in)
	if err != nil {
		return 0, err
	}
	id, err := s.store.addVehicle(ctx, owner, d, s.clock())
	if err == nil {
		s.kick() // sus gasolineras, si es la primera vez
	}
	return id, err
}

func (s *service) updateVehicle(ctx context.Context, owner string, id int64, in vehicleInput) error {
	v, err := s.store.vehicle(ctx, owner, id)
	if err != nil {
		return err
	}
	if v.vehicleData, err = buildVehicle(in); err != nil {
		return err
	}
	return s.store.updateVehicle(ctx, owner, v)
}

// ---- Repostajes ----

type refuelInput struct {
	VehicleID int64
	Day       time.Time
	Km        int
	Liters    float64
	Price     float64
	Total     float64
	Full      bool
	Station   string
	Note      string
	Origin    string
}

// checkDay valida la fecha de algo apuntado (no futura).
func (s *service) checkDay(d time.Time) error {
	switch {
	case d.IsZero():
		return bad("Falta la fecha.")
	case d.After(s.today()):
		return bad("La fecha no puede ser futura.")
	case d.Year() < 1980:
		return bad("Esa fecha es demasiado antigua.")
	}
	return nil
}

// buildRefuel valida un repostaje. skip es el que se está editando.
func (s *service) buildRefuel(st *vstats, in refuelInput, skip int64) (refuel, error) {
	r := refuel{VehicleID: st.Vehicle.ID, Day: in.Day, refuelData: refuelData{Km: in.Km, Liters: in.Liters, Price: in.Price, Total: in.Total,
		Full: in.Full, Station: clip(in.Station, 80), Note: clip(in.Note, 300), Origin: in.Origin}}
	if err := s.checkDay(r.Day); err != nil {
		return r, err
	}
	for _, x := range []float64{in.Liters, in.Price, in.Total} {
		if badNum(x) {
			return r, bad("Los números no pueden ser negativos.")
		}
	}
	unit := st.Vehicle.unit()
	switch {
	case r.Km <= 0:
		return r, bad("Faltan los km del cuentakilómetros.")
	case r.Km < st.Vehicle.StartKm:
		return r, bad("Son menos km que los iniciales del vehículo (" + kmStr(st.Vehicle.StartKm) + ").")
	case r.Liters <= 0:
		if unit == "kWh" {
			return r, bad("Faltan los kWh cargados.")
		}
		return r, bad("Faltan los litros.")
	case r.Liters > 300:
		return r, bad("Son demasiados " + unitWord(unit) + " para un repostaje.")
	}
	if r.Price <= 0 && r.Total > 0 {
		r.Price = math.Round(r.Total/r.Liters*1000) / 1000
	}
	if r.Total <= 0 && r.Price > 0 {
		r.Total = r2(r.Price * r.Liters)
	}
	if r.Price <= 0 {
		return r, bad("Falta el precio por " + unitWord1(unit) + " o el total pagado.")
	}
	if r.Price < 0.05 || r.Price > 10 {
		return r, bad("Ese precio por " + unitWord1(unit) + " (" + perLiter(r.Price, unit) + ") no parece bien: revisa los " + unitWord(unit) + " y el importe.")
	}
	// Los km tienen que cuadrar con los demás repostajes.
	for _, o := range st.Refuels {
		if o.ID == skip {
			continue
		}
		if (o.Day.Before(r.Day) && o.Km > r.Km) || (o.Day.After(r.Day) && o.Km < r.Km) {
			return r, bad("Los km no cuadran con el repostaje del " + shortDate(o.Day, s.today()) + " (" + kmStr(o.Km) + ").")
		}
	}
	return r, nil
}

func unitWord(unit string) string {
	if unit == "kWh" {
		return "kWh"
	}
	return "litros"
}

func unitWord1(unit string) string {
	if unit == "kWh" {
		return "kWh"
	}
	return "litro"
}

func (s *service) addRefuel(ctx context.Context, owner string, in refuelInput) (refuel, error) {
	st, err := s.load(ctx, owner, in.VehicleID)
	if err != nil {
		return refuel{}, err
	}
	r, err := s.buildRefuel(st, in, 0)
	if err != nil {
		return r, err
	}
	if r.ID, err = s.store.addRefuel(ctx, owner, r); err != nil {
		return r, err
	}
	s.afterChange(ctx, owner)
	return r, nil
}

func (s *service) updateRefuel(ctx context.Context, owner string, id int64, in refuelInput) (refuel, error) {
	old, err := s.store.refuel(ctx, owner, id)
	if err != nil {
		return old, err
	}
	st, err := s.load(ctx, owner, old.VehicleID)
	if err != nil {
		return old, err
	}
	in.VehicleID, in.Origin = old.VehicleID, old.Origin
	r, err := s.buildRefuel(st, in, id)
	if err != nil {
		return r, err
	}
	r.ID = id
	if err := s.store.updateRefuel(ctx, owner, r); err != nil {
		return r, err
	}
	s.afterChange(ctx, owner)
	return r, nil
}

// afterChange revisa los vencimientos tras apuntar km nuevos (sin esperar a
// la siguiente pasada).
func (s *service) afterChange(ctx context.Context, owner string) {
	if _, err := s.checkDue(ctx, owner); err != nil {
		s.log.Warn("coche: vencimientos", "err", err)
	}
}

// ---- Gastos ----

type expenseInput struct {
	VehicleID int64
	Kind      string
	Day       time.Time
	Amount    float64
	Km        int
	Note      string
	Origin    string
}

func (s *service) buildExpense(st *vstats, in expenseInput) (expense, error) {
	e := expense{VehicleID: st.Vehicle.ID, Day: in.Day, expenseData: expenseData{Kind: strings.ToLower(strings.TrimSpace(in.Kind)), Amount: r2(in.Amount),
		Km: in.Km, Note: clip(in.Note, 300), Origin: in.Origin}}
	if err := s.checkDay(e.Day); err != nil {
		return e, err
	}
	if !validExpense(e.Kind) {
		e.Kind = "otro"
	}
	switch {
	case badNum(in.Amount) || in.Amount == 0:
		return e, bad("Falta el importe.")
	case in.Amount > 1e6:
		return e, bad("Ese importe es demasiado grande.")
	case e.Km < 0 || e.Km > 5000000:
		return e, bad("Los km no son válidos.")
	case e.Km > 0 && e.Km < st.Vehicle.StartKm:
		return e, bad("Son menos km que los iniciales del vehículo (" + kmStr(st.Vehicle.StartKm) + ").")
	}
	return e, nil
}

func (s *service) addExpense(ctx context.Context, owner string, in expenseInput) (expense, error) {
	st, err := s.load(ctx, owner, in.VehicleID)
	if err != nil {
		return expense{}, err
	}
	e, err := s.buildExpense(st, in)
	if err != nil {
		return e, err
	}
	if e.ID, err = s.store.addExpense(ctx, owner, e); err != nil {
		return e, err
	}
	if e.Km > 0 {
		s.afterChange(ctx, owner)
	}
	return e, nil
}

func (s *service) updateExpense(ctx context.Context, owner string, id int64, in expenseInput) (expense, error) {
	old, err := s.store.expense(ctx, owner, id)
	if err != nil {
		return old, err
	}
	st, err := s.load(ctx, owner, old.VehicleID)
	if err != nil {
		return old, err
	}
	in.VehicleID, in.Origin = old.VehicleID, old.Origin
	e, err := s.buildExpense(st, in)
	if err != nil {
		return e, err
	}
	e.ID, e.TaskID = id, old.TaskID
	if err := s.store.updateExpense(ctx, owner, e); err != nil {
		return e, err
	}
	s.afterChange(ctx, owner)
	return e, nil
}

// ---- Mantenimiento ----

type taskInput struct {
	VehicleID   int64
	Kind, Name  string
	EveryKm     int
	EveryMonths int
	LastKm      int
	LastDay     time.Time
	NextKm      int
	NextDay     time.Time
	Note        string
}

func (s *service) buildTask(st *vstats, in taskInput) (task, error) {
	t := task{VehicleID: st.Vehicle.ID, taskData: taskData{Kind: strings.ToLower(strings.TrimSpace(in.Kind)), Name: clip(in.Name, 60),
		EveryKm: in.EveryKm, EveryMonths: in.EveryMonths, LastKm: in.LastKm, LastDay: in.LastDay, NextKm: in.NextKm, NextDay: in.NextDay,
		Note: clip(in.Note, 300)}}
	if !validTaskKind(t.Kind) {
		t.Kind = "otro"
	}
	// Sin cada cuánto, lo habitual de ese tipo (la próxima vez, si se da, manda
	// hasta que se haga).
	if t.EveryKm == 0 && t.EveryMonths == 0 {
		for _, k := range taskKinds {
			if k.Key == t.Kind {
				t.EveryKm, t.EveryMonths = k.EveryKm, k.EveryMonths
			}
		}
	}
	if t.Name == "" {
		if t.Kind == "otro" {
			return t, bad("Ponle un nombre a la tarea.")
		}
		t.Name = taskKindLabel(t.Kind)
	}
	switch {
	case t.EveryKm < 0 || t.EveryKm > 1000000 || t.LastKm < 0 || t.LastKm > 5000000 || t.NextKm < 0 || t.NextKm > 5000000:
		return t, bad("Los km no son válidos.")
	case t.EveryMonths < 0 || t.EveryMonths > 240:
		return t, bad("Los meses van de 1 a 240.")
	case !t.LastDay.IsZero() && t.LastDay.After(s.today()):
		return t, bad("La última vez no puede ser futura.")
	case !t.LastDay.IsZero() && t.LastDay.Year() < 1980, !t.NextDay.IsZero() && (t.NextDay.Year() < 1980 || t.NextDay.Year() > 2200):
		return t, bad("Esa fecha no es válida.")
	case t.EveryKm == 0 && t.EveryMonths == 0 && t.NextKm == 0 && t.NextDay.IsZero():
		return t, bad("Di cada cuánto toca (km o meses) o cuándo es la próxima vez.")
	case t.NextKm > 0 && t.LastKm > 0 && t.NextKm <= t.LastKm:
		return t, bad("La próxima vez tiene que ser con más km que la última.")
	}
	return t, nil
}

func (s *service) addTask(ctx context.Context, owner string, in taskInput) (task, error) {
	st, err := s.load(ctx, owner, in.VehicleID)
	if err != nil {
		return task{}, err
	}
	t, err := s.buildTask(st, in)
	if err != nil {
		return t, err
	}
	if t.ID, err = s.store.addTask(ctx, owner, t); err != nil {
		return t, err
	}
	s.afterChange(ctx, owner)
	return t, nil
}

func (s *service) updateTask(ctx context.Context, owner string, id int64, in taskInput) (task, error) {
	old, err := s.store.task(ctx, owner, id)
	if err != nil {
		return old, err
	}
	st, err := s.load(ctx, owner, old.VehicleID)
	if err != nil {
		return old, err
	}
	in.VehicleID = old.VehicleID
	t, err := s.buildTask(st, in)
	if err != nil {
		return t, err
	}
	t.ID, t.Notified = id, old.Notified
	if err := s.store.updateTask(ctx, owner, t); err != nil {
		return t, err
	}
	s.afterChange(ctx, owner)
	return t, nil
}

type doneInput struct {
	Day     time.Time
	Km      int // 0: los estimados de hoy
	Cost    float64
	NextKm  int
	NextDay time.Time
	Note    string
	Origin  string
}

// taskDone marca una tarea como hecha: cuenta desde ahí y apunta su gasto.
func (s *service) taskDone(ctx context.Context, owner string, id int64, in doneInput) (task, expense, error) {
	t, err := s.store.task(ctx, owner, id)
	if err != nil {
		return t, expense{}, err
	}
	st, err := s.load(ctx, owner, t.VehicleID)
	if err != nil {
		return t, expense{}, err
	}
	if in.Day.IsZero() {
		in.Day = s.today()
	}
	if err := s.checkDay(in.Day); err != nil {
		return t, expense{}, err
	}
	switch {
	case badNum(in.Cost) || in.Cost > 1e6:
		return t, expense{}, bad("El coste no es válido.")
	case in.Km < 0 || in.Km > 5000000 || in.NextKm < 0 || in.NextKm > 5000000:
		return t, expense{}, bad("Los km no son válidos.")
	case in.Km > 0 && in.Km < st.Vehicle.StartKm:
		return t, expense{}, bad("Son menos km que los iniciales del vehículo (" + kmStr(st.Vehicle.StartKm) + ").")
	case !in.NextDay.IsZero() && !in.NextDay.After(in.Day):
		return t, expense{}, bad("La próxima vez tiene que ser después de esta.")
	}
	if in.Km == 0 {
		in.Km = st.EstKm
		if in.Day.Before(s.today()) && st.KmPerDay > 0 {
			in.Km -= int(math.Round(st.KmPerDay * float64(daysBetween(in.Day, s.today()))))
		}
		in.Km = max(in.Km, st.Vehicle.StartKm)
	}
	if in.NextKm > 0 && in.NextKm <= in.Km {
		return t, expense{}, bad("La próxima vez tiene que ser con más km que esta.")
	}
	t.LastDay, t.LastKm, t.NextDay, t.NextKm, t.Notified = in.Day, in.Km, in.NextDay, in.NextKm, ""
	note := t.Name
	if n := clip(in.Note, 240); n != "" {
		note += ": " + n
	}
	e := expense{VehicleID: t.VehicleID, TaskID: t.ID, Day: in.Day, expenseData: expenseData{Kind: "mantenimiento", Amount: r2(in.Cost), Km: in.Km,
		Note: note, Origin: in.Origin}}
	if e.ID, err = s.store.taskDone(ctx, owner, t, e); err != nil {
		return t, e, err
	}
	s.afterChange(ctx, owner)
	return t, e, nil
}

// ---- Avisos ----

// dueNotice es el aviso de una tarea, si toca: por fecha, una semana antes;
// por km, cuando los últimos apuntados pasan de los que tocaban.
func dueNotice(vname string, t taskState, lastKm int, today time.Time) string {
	var parts []string
	if t.hasDay() && t.DaysLeft <= 7 {
		switch {
		case t.DaysLeft < 0:
			parts = append(parts, "venció el "+shortDate(t.DueDay, today))
		case t.DaysLeft <= 1:
			parts = append(parts, "vence "+inDays(t.DaysLeft))
		default:
			parts = append(parts, "vence el "+shortDate(t.DueDay, today)+" ("+inDays(t.DaysLeft)+")")
		}
	}
	if t.hasKm() && lastKm >= t.DueKm {
		parts = append(parts, "llevas "+kmStr(lastKm)+" y tocaba a los "+kmStr(t.DueKm))
	}
	if len(parts) == 0 {
		return ""
	}
	return "🔧 " + vname + ": " + t.Name + " — " + strings.Join(parts, "; ")
}

// checkDue avisa (una vez por vencimiento) de las tareas de una persona que
// tocan.
func (s *service) checkDue(ctx context.Context, owner string) (int, error) {
	all, err := s.loadAll(ctx, owner)
	if err != nil {
		return 0, err
	}
	today, now := s.today(), s.clock()
	n := 0
	for _, st := range all {
		for _, t := range st.Tasks {
			cyc := t.cycle()
			if t.Notified == cyc {
				continue
			}
			text := dueNotice(st.Vehicle.Name, t, st.LastKm, today)
			if text == "" {
				continue
			}
			if err := s.store.addNotice(ctx, owner, text, now); err != nil {
				return n, err
			}
			t.task.Notified = cyc
			if err := s.store.updateTask(ctx, owner, t.task); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ---- Resumen de la mañana ----

// summaryDue es lo que dice de una tarea el resumen: «vence el 12 nov (en 5
// días)», «pasada de km (≈150.300 de 150.000)».
func summaryDue(t taskState, today time.Time) []string {
	var parts []string
	if t.hasDay() && t.DaysLeft <= 7 {
		switch {
		case t.DaysLeft < 0:
			parts = append(parts, "venció el "+shortDate(t.DueDay, today))
		case t.DaysLeft <= 1:
			parts = append(parts, "vence "+inDays(t.DaysLeft))
		default:
			parts = append(parts, "vence el "+shortDate(t.DueDay, today)+" ("+inDays(t.DaysLeft)+")")
		}
	}
	if t.hasKm() && t.KmLeft <= 300 {
		if t.KmLeft <= 0 {
			parts = append(parts, "pasada de km (≈"+num(float64(t.DueKm-t.KmLeft), 0)+" de "+num(float64(t.DueKm), 0)+")")
		} else {
			parts = append(parts, "en ≈"+kmStr(t.KmLeft))
		}
	}
	return parts
}

// todayLines son las líneas del coche para el resumen de la mañana de la
// Agenda: solo lo que vence (o ya venció) y, si la persona lo ha pedido, la
// gasolinera más barata. Si no hay nada, ninguna.
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	all, err := s.loadAll(ctx, owner)
	if err != nil {
		return nil, err
	}
	today := s.today()
	var lines []string
	for _, st := range all {
		var items []string
		for _, t := range st.Tasks {
			if parts := summaryDue(t, today); len(parts) > 0 {
				items = append(items, t.Name+" "+strings.Join(parts, " y "))
			}
		}
		if len(items) > 0 && len(lines) < 3 {
			lines = append(lines, st.Vehicle.Name+": "+strings.Join(items, "; ")+".")
		}
	}
	set, err := s.store.settings(ctx, owner)
	if err != nil {
		return nil, err
	}
	if set.Summary {
		vs := make([]vehicle, 0, len(all))
		for _, st := range all {
			vs = append(vs, st.Vehicle)
		}
		prod := productFor(set, vs)
		p, err := s.store.prices(ctx, set.Municipio.ID)
		if err != nil {
			return nil, err
		}
		if s.clock().Sub(p.Fetched) < 30*time.Hour {
			if best, _ := p.cheapest(prod, 1); len(best) > 0 {
				lines = append(lines, productLabel(prod)+" más barata en "+set.Municipio.Name+": "+best[0].Brand+" ("+best[0].Address+"), "+
					perLiter(best[0].Price, "l")+".")
			}
		}
	}
	return lines, nil
}

// ---- Gasolineras ----

// productFor es el carburante que busca la persona: el elegido o, si no, el
// de su primer vehículo que no sea eléctrico.
func productFor(set settingsData, vs []vehicle) string {
	if validProduct(set.Product) {
		return set.Product
	}
	for _, v := range vs {
		for _, f := range fuels {
			if f.Key == v.Fuel && f.Product != "" {
				return f.Product
			}
		}
	}
	return "g95"
}

// municipioList es la lista de municipios; si aún no se tiene, se pide (la
// persona está esperando).
func (s *service) municipioList(ctx context.Context) ([]municipio, error) {
	list, err := s.store.allMunicipios(ctx)
	if err != nil || len(list) > 0 {
		return list, err
	}
	if err := s.syncMunicipios(ctx); err != nil {
		return nil, err
	}
	return s.store.allMunicipios(ctx)
}

func (s *service) syncMunicipios(ctx context.Context) error {
	list, err := s.fuel.municipios(ctx)
	if err != nil {
		return err
	}
	return s.store.saveMunicipios(ctx, list, s.clock())
}

// setMunicipio cambia el municipio de las gasolineras de la persona.
func (s *service) setMunicipio(ctx context.Context, owner string, id int) (municipio, error) {
	m, err := s.store.municipio(ctx, id)
	if errors.Is(err, errNotFound) {
		return m, bad("Ese municipio no está en la lista.")
	}
	if err != nil {
		return m, err
	}
	set, err := s.store.settings(ctx, owner)
	if err != nil {
		return m, err
	}
	set.Municipio = m
	if err := s.store.saveSettings(ctx, owner, set); err != nil {
		return m, err
	}
	s.kick()
	return m, nil
}

// setPrefs cambia el carburante y si va en el resumen de la mañana.
func (s *service) setPrefs(ctx context.Context, owner, product string, summary bool) error {
	set, err := s.store.settings(ctx, owner)
	if err != nil {
		return err
	}
	if product != "" && !validProduct(product) {
		return bad("Ese carburante no está en la lista.")
	}
	set.Product, set.Summary = product, summary
	return s.store.saveSettings(ctx, owner, set)
}

// gasView es lo que se enseña de las gasolineras.
type gasView struct {
	Settings settingsData
	Product  string
	Prices   priceList
	Best     []pricedStation
	Avg      float64
	Count    int // cuántas tienen ese carburante
}

// gas da las gasolineras más baratas de la persona. Con wait (Talia), si los
// precios son viejos se piden en ese momento; si no, se piden en segundo
// plano. m y product, si vienen, cambian los de la persona solo esta vez.
func (s *service) gas(ctx context.Context, owner string, m *municipio, product string, n int, wait bool) (*gasView, error) {
	set, err := s.store.settings(ctx, owner)
	if err != nil {
		return nil, err
	}
	vs, err := s.store.vehicles(ctx, owner)
	if err != nil {
		return nil, err
	}
	g := &gasView{Settings: set, Product: productFor(set, vs)}
	if validProduct(product) {
		g.Product = product
	}
	if m != nil {
		g.Settings.Municipio = *m
	}
	if g.Prices, err = s.store.prices(ctx, g.Settings.Municipio.ID); err != nil {
		return nil, err
	}
	if s.clock().Sub(g.Prices.Fetched) >= priceEvery {
		if wait && s.fuel.ready() {
			p, ferr := s.fuel.stations(ctx, g.Settings.Municipio.ID)
			if ferr == nil {
				saved, err := s.savePrices(ctx, g.Settings.Municipio.ID, p)
				if err != nil {
					return nil, err
				}
				if saved {
					g.Prices = p
				}
			}
		} else {
			s.kick()
		}
	}
	g.Best, g.Avg = g.Prices.cheapest(g.Product, n)
	for _, st := range g.Prices.Stations {
		if st.Prices[g.Product] > 0 {
			g.Count++
		}
	}
	return g, nil
}

// savePrices guarda lo que ha dado el Ministerio, salvo una lista vacía
// cuando había gasolineras hace menos de dos días: a veces responde sin
// ninguna mientras actualiza. Entonces se vuelve a pedir en la siguiente
// pasada.
func (s *service) savePrices(ctx context.Context, id int, p priceList) (bool, error) {
	if len(p.Stations) == 0 {
		old, err := s.store.prices(ctx, id)
		if err != nil {
			return false, err
		}
		if len(old.Stations) > 0 && p.Fetched.Sub(old.Fetched) < 48*time.Hour {
			return false, nil
		}
	}
	return true, s.store.savePrices(ctx, id, p)
}

// ---- Tarea periódica ----

func (s *service) kick() {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	select {
	case s.kickC <- struct{}{}:
	default:
	}
}

// run pide los precios de los municipios elegidos y revisa los
// vencimientos.
func (s *service) run(ctx context.Context) {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		s.round(rctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kickC:
			s.mu.Lock()
			s.pricesAt = time.Time{} // mirar los precios ya
			s.mu.Unlock()
		}
	}
}

// round es una pasada. Casi siempre no toca nada y ni se lee la base de
// datos.
func (s *service) round(ctx context.Context) {
	now := s.clock()
	s.mu.Lock()
	prices := now.Sub(s.pricesAt) >= priceCheck
	due := now.Sub(s.dueAt) >= dueEvery || !dayOf(now).Equal(dayOf(s.dueAt))
	if prices {
		s.pricesAt = now
	}
	if due {
		s.dueAt = now
	}
	s.mu.Unlock()
	if !prices && !due {
		return
	}
	owners, err := s.store.owners(ctx)
	if err != nil {
		s.log.Warn("coche: cuentas", "err", err)
		return
	}
	if prices && len(owners) > 0 {
		s.refreshPrices(ctx, owners)
	}
	if due {
		for _, o := range owners {
			if n, err := s.checkDue(ctx, o); err != nil {
				s.log.Warn("coche: vencimientos", "err", err)
			} else if n > 0 {
				s.log.Info("coche: vencimientos", "avisos", n)
			}
		}
	}
}

// refreshPrices pide, de cada municipio elegido (una sola vez aunque lo
// tengan varios), los precios que tengan más de priceEvery; de paso, la
// lista de municipios una vez al mes, y borra los que ya no usa nadie.
func (s *service) refreshPrices(ctx context.Context, owners []string) {
	now := s.clock()
	seen := map[int]bool{}
	var ids []int
	for _, o := range owners {
		set, err := s.store.settings(ctx, o)
		if err != nil {
			s.log.Warn("coche: preferencias", "err", err)
			continue
		}
		if id := set.Municipio.ID; !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	fetched, err := s.store.pricesFetched(ctx, ids)
	if err != nil {
		s.log.Warn("coche: precios", "err", err)
		return
	}
	for _, id := range ids {
		if now.Sub(fetched[id]) < priceEvery {
			continue
		}
		if !s.fuel.ready() {
			break // en espera tras un error: sin insistir
		}
		p, err := s.fuel.stations(ctx, id)
		if err != nil {
			continue // el estado de la fuente ya lo dice, sin el municipio
		}
		if _, err := s.savePrices(ctx, id, p); err != nil {
			s.log.Warn("coche: guardar precios", "err", err)
		}
	}
	if err := s.store.prunePrices(ctx, ids, now.Add(-24*time.Hour)); err != nil {
		s.log.Warn("coche: borrar precios viejos", "err", err)
	}
	at, err := s.store.municipiosAt(ctx)
	if err == nil && now.Sub(at) >= municipiosEvery && s.fuel.ready() {
		if err := s.syncMunicipios(ctx); err != nil && !errors.Is(err, errSourceWait) {
			s.log.Warn("coche: municipios", "err", err)
		}
	}
}
