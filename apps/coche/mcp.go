package main

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
	"homeserver/platform/seal"
)

// Tools de Talia. Las cuentas las hace el programa (como en la web): la IA
// solo las cuenta. Respuestas en JSON con claves en español. La matrícula no
// sale nunca por aquí.

const originAssistant = "asistente"

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

func r1(v float64) float64 { return math.Round(v*10) / 10 }
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// optDay lee una fecha opcional (vacía: hoy).
func (s *service) optDay(in string) (time.Time, error) {
	if strings.TrimSpace(in) == "" {
		return s.today(), nil
	}
	d, err := parseDay(in)
	if err != nil {
		return d, errors.New("la fecha va como AAAA-MM-DD")
	}
	return d, nil
}

func optDate(in string) (time.Time, error) {
	if strings.TrimSpace(in) == "" {
		return time.Time{}, nil
	}
	d, err := parseDay(in)
	if err != nil {
		return d, errors.New("la fecha va como AAAA-MM-DD")
	}
	return d, nil
}

// pickVehicle es el vehículo del que habla Talia.
func (s *service) pickVehicle(ctx context.Context, owner, q string) (vehicle, error) {
	list, err := s.store.vehicles(ctx, owner)
	if err != nil {
		return vehicle{}, err
	}
	return findVehicle(list, q)
}

// findTask busca una tarea por id, por nombre o por tipo.
func findTask(st *vstats, q string) (taskState, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return taskState{}, bad("Di qué tarea (su nombre o su id).")
	}
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		if t, ok := st.task(id); ok {
			return t, nil
		}
	}
	fq := strings.Join(seal.Words(q), " ")
	var exact, partial []taskState
	for _, t := range st.Tasks {
		name := strings.Join(seal.Words(t.Name), " ")
		switch {
		case name == fq || t.Kind == fq:
			exact = append(exact, t)
		case fq != "" && (strings.Contains(name, fq) || strings.Contains(fq, name) || strings.Contains(strings.Join(seal.Words(taskKindLabel(t.Kind)), " "), fq)):
			partial = append(partial, t)
		}
	}
	if len(exact) == 0 {
		exact = partial
	}
	var names []string
	for _, t := range st.Tasks {
		names = append(names, t.Name+" ("+idStr(t.ID)+")")
	}
	switch len(exact) {
	case 0:
		if len(names) == 0 {
			return taskState{}, bad("Ese vehículo no tiene tareas de mantenimiento: créala con coche_mantenimiento.")
		}
		return taskState{}, bad("No hay ninguna tarea así. Son: " + strings.Join(names, ", ") + ".")
	case 1:
		return exact[0], nil
	}
	return taskState{}, bad("Hay varias que encajan: usa el id. Son: " + strings.Join(names, ", ") + ".")
}

// productFrom entiende el carburante como lo diga la persona.
func productFrom(q string) string {
	if validProduct(q) {
		return q
	}
	w := " " + strings.Join(seal.Words(q), " ") + " "
	switch {
	case w == "  ":
		return ""
	case strings.Contains(w, " glp ") || strings.Contains(w, "autogas") || strings.Contains(w, "licuado"):
		return "glp"
	case strings.Contains(w, " gnc ") || strings.Contains(w, "gas natural"):
		return "gnc"
	case strings.Contains(w, "renovable"):
		return "dren"
	case strings.Contains(w, "diesel") || strings.Contains(w, "gasoleo") || strings.Contains(w, "gasoil"):
		if strings.Contains(w, "premium") || strings.Contains(w, "plus") {
			return "goap"
		}
		return "goa"
	case strings.Contains(w, "98"):
		return "g98"
	case strings.Contains(w, "e10"):
		return "g95e10"
	case strings.Contains(w, "gasolina") || strings.Contains(w, "95") || strings.Contains(w, "sin plomo"):
		return "g95"
	}
	return ""
}

// ---- Salidas ----

type refuelOut struct {
	ID         int64   `json:"id"`
	Fecha      string  `json:"fecha"`
	Km         int     `json:"km"`
	Cantidad   float64 `json:"cantidad"`
	Unidad     string  `json:"unidad"`
	Precio     float64 `json:"precio_por_unidad"`
	Total      float64 `json:"total_eur"`
	Lleno      bool    `json:"lleno"`
	Gasolinera string  `json:"gasolinera,omitempty"`
	Nota       string  `json:"nota,omitempty"`
	Consumo    float64 `json:"consumo_tramo_por_100km,omitempty"`
}

func refuelOutOf(st *vstats, r refuel) refuelOut {
	o := refuelOut{ID: r.ID, Fecha: isoDay(r.Day), Km: r.Km, Cantidad: r3(r.Liters), Unidad: st.Vehicle.unit(), Precio: r3(r.Price), Total: r2(r.Total),
		Lleno: r.Full, Gasolinera: r.Station, Nota: r.Note}
	for _, sg := range st.Segments {
		if sg.ToID == r.ID {
			o.Consumo = r1(sg.L100)
		}
	}
	return o
}

type taskOut struct {
	ID          int64  `json:"id"`
	Vehiculo    string `json:"vehiculo"`
	Nombre      string `json:"nombre"`
	Tipo        string `json:"tipo"`
	Cada        string `json:"cada,omitempty"`
	Estado      string `json:"estado"` // vencida, pronto, ok, sin-datos
	Proxima     string `json:"proxima"`
	ProxFecha   string `json:"proxima_fecha,omitempty"`
	ProxKm      int    `json:"proximos_km,omitempty"`
	KmRestantes *int   `json:"km_restantes,omitempty"`
	DiasRest    *int   `json:"dias_restantes,omitempty"`
	UltFecha    string `json:"ultima_fecha,omitempty"`
	UltKm       int    `json:"ultimos_km,omitempty"`
	Nota        string `json:"nota,omitempty"`
}

func taskOutOf(st *vstats, t taskState, today time.Time) taskOut {
	o := taskOut{ID: t.ID, Vehiculo: st.Vehicle.Name, Nombre: t.Name, Tipo: t.Kind, Cada: everyText(t.taskData), Estado: t.Status, Proxima: dueText(t, today),
		ProxKm: t.DueKm, UltKm: t.LastKm, Nota: t.Note}
	if t.hasDay() {
		o.ProxFecha = isoDay(t.DueDay)
		d := t.DaysLeft
		o.DiasRest = &d
	}
	if t.hasKm() {
		k := t.KmLeft
		o.KmRestantes = &k
	}
	if !t.LastDay.IsZero() {
		o.UltFecha = isoDay(t.LastDay)
	}
	return o
}

type expenseOut struct {
	ID      int64   `json:"id"`
	Fecha   string  `json:"fecha"`
	Tipo    string  `json:"tipo"`
	Importe float64 `json:"importe_eur"`
	Km      int     `json:"km,omitempty"`
	Nota    string  `json:"nota,omitempty"`
}

func expenseOutOf(e expense) expenseOut {
	return expenseOut{ID: e.ID, Fecha: isoDay(e.Day), Tipo: e.Kind, Importe: r2(e.Amount), Km: e.Km, Nota: e.Note}
}

type monthOut struct {
	Mes         string  `json:"mes"`
	Combustible float64 `json:"combustible_eur"`
	Otros       float64 `json:"otros_eur"`
}

type vehicleOut struct {
	ID             int64        `json:"id"`
	Nombre         string       `json:"nombre"`
	Modelo         string       `json:"modelo,omitempty"`
	Combustible    string       `json:"combustible"`
	Unidad         string       `json:"unidad"`
	KmUltimos      int          `json:"km_ultimos_apuntados"`
	KmUltimosFecha string       `json:"km_ultimos_fecha"`
	KmHoy          int          `json:"km_estimados_hoy"`
	KmMes          int          `json:"km_al_mes,omitempty"`
	ConsumoMedio   float64      `json:"consumo_medio_por_100km,omitempty"`
	ConsumoUltimo  float64      `json:"consumo_ultimo_tramo_por_100km,omitempty"`
	CombustibleKm  float64      `json:"combustible_eur_km,omitempty"`
	TotalKm        float64      `json:"todo_eur_km,omitempty"`
	MesActual      float64      `json:"gasto_mes_actual_eur"`
	Doce           float64      `json:"gasto_12_meses_eur"`
	Combustible12  float64      `json:"combustible_total_eur"`
	Otros12        float64      `json:"otros_gastos_total_eur"`
	Meses          []monthOut   `json:"meses"`
	Mantenimiento  []taskOut    `json:"mantenimiento"`
	Repostajes     []refuelOut  `json:"ultimos_repostajes"`
	Gastos         []expenseOut `json:"ultimos_gastos"`
	Nota           string       `json:"nota,omitempty"`
}

func vehicleOutOf(st *vstats, today time.Time) vehicleOut {
	v := st.Vehicle
	o := vehicleOut{ID: v.ID, Nombre: v.Name, Modelo: v.Model, Combustible: fuelLabel(v.Fuel), Unidad: v.unit(), KmUltimos: st.LastKm,
		KmUltimosFecha: isoDay(st.LastKmDay), KmHoy: st.EstKm, KmMes: int(math.Round(st.KmPerDay * 30.44)), ConsumoMedio: r1(st.AvgL100),
		ConsumoUltimo: r1(st.LastL100), CombustibleKm: r3(st.FuelPerKm), TotalKm: r3(st.AllPerKm), MesActual: r2(st.thisMonth()), Doce: r2(st.year()),
		Combustible12: r2(st.FuelCost), Otros12: r2(st.OtherCost), Meses: []monthOut{}, Mantenimiento: []taskOut{}, Repostajes: []refuelOut{},
		Gastos: []expenseOut{}}
	if st.AvgL100 == 0 {
		o.Nota = "el consumo sale con dos llenados apuntados"
	}
	for _, m := range st.Months {
		if m.total() > 0 {
			o.Meses = append(o.Meses, monthOut{Mes: m.Month.Format("2006-01"), Combustible: r2(m.Fuel), Otros: r2(m.Other)})
		}
	}
	for _, t := range st.Tasks {
		o.Mantenimiento = append(o.Mantenimiento, taskOutOf(st, t, today))
	}
	for i := len(st.Refuels) - 1; i >= 0 && len(o.Repostajes) < 5; i-- {
		o.Repostajes = append(o.Repostajes, refuelOutOf(st, st.Refuels[i]))
	}
	for i := len(st.Expenses) - 1; i >= 0 && len(o.Gastos) < 5; i-- {
		o.Gastos = append(o.Gastos, expenseOutOf(st.Expenses[i]))
	}
	return o
}

// ---- Entradas ----

type refuelIn struct {
	Vehiculo   string  `json:"vehiculo,omitempty" jsonschema:"nombre o id del vehículo; vacío si solo tiene uno"`
	Fecha      string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD; vacía: hoy"`
	Km         int     `json:"km" jsonschema:"km del cuentakilómetros. Si no los dice, pregúntaselos"`
	Litros     float64 `json:"litros" jsonschema:"litros echados (kWh en un eléctrico)"`
	Precio     float64 `json:"precio_litro,omitempty" jsonschema:"precio por litro en euros"`
	Total      float64 `json:"total,omitempty" jsonschema:"total pagado en euros, si no da el precio por litro"`
	Lleno      *bool   `json:"lleno,omitempty" jsonschema:"si llenó el depósito (por defecto sí); el consumo se calcula entre llenados"`
	Gasolinera string  `json:"gasolinera,omitempty"`
	Nota       string  `json:"nota,omitempty"`
}

type refuelAdded struct {
	Apuntado     refuelOut `json:"apuntado"`
	ConsumoMedio float64   `json:"consumo_medio_por_100km,omitempty"`
	KmHoy        int       `json:"km_estimados_hoy"`
}

type vehicleIn struct {
	Vehiculo string `json:"vehiculo,omitempty" jsonschema:"nombre o id del vehículo; vacío: todos"`
}

type summaryOut struct {
	Vehiculos []vehicleOut `json:"vehiculos"`
}

type tasksIn struct {
	Vehiculo     string `json:"vehiculo,omitempty" jsonschema:"nombre o id del vehículo; vacío: todos (para añadir, si solo tiene uno)"`
	Anadir       string `json:"anadir,omitempty" jsonschema:"para crear una tarea: su nombre (Cambio de aceite, ITV, Neumáticos…). Vacío: solo las lista"`
	Tipo         string `json:"tipo,omitempty" jsonschema:"aceite, itv, neumaticos, frenos, filtros, distribucion, revision u otro"`
	CadaKm       int    `json:"cada_km,omitempty"`
	CadaMeses    int    `json:"cada_meses,omitempty"`
	UltimaFecha  string `json:"ultima_fecha,omitempty" jsonschema:"AAAA-MM-DD: cuándo se hizo por última vez"`
	UltimosKm    int    `json:"ultimos_km,omitempty" jsonschema:"km de la última vez"`
	ProximaFecha string `json:"proxima_fecha,omitempty" jsonschema:"AAAA-MM-DD, si se sabe (la ITV)"`
	ProximosKm   int    `json:"proximos_km,omitempty"`
	Nota         string `json:"nota,omitempty"`
}

type tasksOut struct {
	Creada *taskOut  `json:"creada,omitempty"`
	Tareas []taskOut `json:"tareas"`
}

type doneIn struct {
	Vehiculo     string  `json:"vehiculo,omitempty" jsonschema:"nombre o id del vehículo; vacío si solo tiene uno"`
	Tarea        string  `json:"tarea" jsonschema:"nombre o id de la tarea (de coche_mantenimiento)"`
	Fecha        string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD; vacía: hoy"`
	Km           int     `json:"km,omitempty" jsonschema:"km del cuentakilómetros; vacío: los estimados"`
	Coste        float64 `json:"coste,omitempty" jsonschema:"lo que costó en euros (se apunta como gasto)"`
	ProximaFecha string  `json:"proxima_fecha,omitempty" jsonschema:"AAAA-MM-DD, si se sabe (la ITV)"`
	ProximosKm   int     `json:"proximos_km,omitempty"`
	Nota         string  `json:"nota,omitempty"`
}

type doneOut struct {
	Tarea taskOut    `json:"tarea"`
	Gasto expenseOut `json:"gasto"`
}

type gasIn struct {
	Combustible string `json:"combustible,omitempty" jsonschema:"gasolina 95, gasolina 98, diésel, diésel premium, GLP… Vacío: el de su vehículo o el elegido en la web"`
	Municipio   string `json:"municipio,omitempty" jsonschema:"nombre de un municipio de España; vacío: el elegido en la web (por defecto Valencia)"`
	Cuantas     int    `json:"cuantas,omitempty" jsonschema:"cuántas (por defecto 5, máximo 20)"`
}

type stationOut struct {
	Rotulo    string  `json:"rotulo"`
	Direccion string  `json:"direccion"`
	Localidad string  `json:"localidad,omitempty"`
	Horario   string  `json:"horario,omitempty"`
	Precio    float64 `json:"precio_eur_litro"`
}

type gasOut struct {
	Municipio   string       `json:"municipio"`
	Provincia   string       `json:"provincia,omitempty"`
	Carburante  string       `json:"carburante"`
	Actualizado string       `json:"precios_de,omitempty"`
	Media       float64      `json:"media_eur_litro,omitempty"`
	Gasolineras []stationOut `json:"gasolineras"`
	Nota        string       `json:"nota,omitempty"`
}

type expenseIn struct {
	Vehiculo string  `json:"vehiculo,omitempty" jsonschema:"nombre o id del vehículo; vacío si solo tiene uno"`
	Tipo     string  `json:"tipo" jsonschema:"seguro, parking, peaje, multa, impuesto, lavado, mantenimiento u otro"`
	Importe  float64 `json:"importe" jsonschema:"en euros"`
	Fecha    string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD; vacía: hoy"`
	Km       int     `json:"km,omitempty"`
	Nota     string  `json:"nota,omitempty" jsonschema:"concepto (Parking del aeropuerto…)"`
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "coche_repostaje_apuntar", Description: "Apunta un repostaje (o una carga, en un eléctrico): km del cuentakilómetros, litros y el precio por litro o el total. Si no dice los km o los litros, pregúntalos antes. Devuelve el consumo del tramo si cierra un llenado"},
		func(ctx context.Context, req *mcp.CallToolRequest, in refuelIn) (*mcp.CallToolResult, refuelAdded, error) {
			who, err := identity(req)
			if err != nil {
				return nil, refuelAdded{}, err
			}
			v, err := svc.pickVehicle(ctx, who.User, in.Vehiculo)
			if err != nil {
				return nil, refuelAdded{}, err
			}
			day, err := svc.optDay(in.Fecha)
			if err != nil {
				return nil, refuelAdded{}, err
			}
			full := in.Lleno == nil || *in.Lleno
			r, err := svc.addRefuel(ctx, who.User, refuelInput{VehicleID: v.ID, Day: day, Km: in.Km, Liters: in.Litros, Price: in.Precio, Total: in.Total,
				Full: full, Station: in.Gasolinera, Note: in.Nota, Origin: originAssistant})
			if err != nil {
				return nil, refuelAdded{}, err
			}
			st, err := svc.load(ctx, who.User, v.ID)
			if err != nil {
				return nil, refuelAdded{}, err
			}
			return nil, refuelAdded{Apuntado: refuelOutOf(st, r), ConsumoMedio: r1(st.AvgL100), KmHoy: st.EstKm}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "coche_resumen", Description: "Cómo va cada vehículo: km apuntados y estimados de hoy, consumo medio y del último tramo, coste por km (combustible y todo), gasto de este mes, de los últimos 12 meses y por meses, el mantenimiento con lo próximo que toca y los últimos repostajes y gastos. Todo calculado por el programa"},
		func(ctx context.Context, req *mcp.CallToolRequest, in vehicleIn) (*mcp.CallToolResult, summaryOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, summaryOut{}, err
			}
			out := summaryOut{Vehiculos: []vehicleOut{}}
			today := svc.today()
			if strings.TrimSpace(in.Vehiculo) != "" {
				v, err := svc.pickVehicle(ctx, who.User, in.Vehiculo)
				if err != nil {
					return nil, out, err
				}
				st, err := svc.load(ctx, who.User, v.ID)
				if err != nil {
					return nil, out, err
				}
				out.Vehiculos = append(out.Vehiculos, vehicleOutOf(st, today))
				return nil, out, nil
			}
			all, err := svc.loadAll(ctx, who.User)
			if err != nil {
				return nil, out, err
			}
			if len(all) == 0 {
				return nil, out, bad("Aún no hay ningún vehículo: añádelo en la web de Coche.")
			}
			for _, st := range all {
				out.Vehiculos = append(out.Vehiculos, vehicleOutOf(st, today))
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "coche_mantenimiento", Description: "Las tareas de mantenimiento (aceite, ITV, neumáticos, frenos…) con cuándo toca cada una, por km o por fecha, y su estado (vencida, pronto, ok). Con «anadir» crea una: hace falta cada cuánto (km o meses; si no, lo habitual de su tipo) y la última vez o la próxima. Avisa por Telegram cuando toca"},
		func(ctx context.Context, req *mcp.CallToolRequest, in tasksIn) (*mcp.CallToolResult, tasksOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, tasksOut{}, err
			}
			out := tasksOut{Tareas: []taskOut{}}
			today := svc.today()
			if strings.TrimSpace(in.Anadir) != "" || strings.TrimSpace(in.Tipo) != "" {
				v, err := svc.pickVehicle(ctx, who.User, in.Vehiculo)
				if err != nil {
					return nil, out, err
				}
				last, err := optDate(in.UltimaFecha)
				if err != nil {
					return nil, out, err
				}
				next, err := optDate(in.ProximaFecha)
				if err != nil {
					return nil, out, err
				}
				kind := strings.ToLower(strings.TrimSpace(in.Tipo))
				if !validTaskKind(kind) {
					kind = kindFrom(in.Anadir)
				}
				name := in.Anadir
				if strings.EqualFold(strings.TrimSpace(name), kind) {
					name = "" // «itv»: el nombre del tipo
				}
				t, err := svc.addTask(ctx, who.User, taskInput{VehicleID: v.ID, Kind: kind, Name: name, EveryKm: in.CadaKm, EveryMonths: in.CadaMeses,
					LastKm: in.UltimosKm, LastDay: last, NextKm: in.ProximosKm, NextDay: next, Note: in.Nota})
				if err != nil {
					return nil, out, err
				}
				st, err := svc.load(ctx, who.User, v.ID)
				if err != nil {
					return nil, out, err
				}
				if ts, ok := st.task(t.ID); ok {
					c := taskOutOf(st, ts, today)
					out.Creada = &c
				}
				for _, ts := range st.Tasks {
					out.Tareas = append(out.Tareas, taskOutOf(st, ts, today))
				}
				return nil, out, nil
			}
			var list []*vstats
			if strings.TrimSpace(in.Vehiculo) != "" {
				v, err := svc.pickVehicle(ctx, who.User, in.Vehiculo)
				if err != nil {
					return nil, out, err
				}
				st, err := svc.load(ctx, who.User, v.ID)
				if err != nil {
					return nil, out, err
				}
				list = append(list, st)
			} else if list, err = svc.loadAll(ctx, who.User); err != nil {
				return nil, out, err
			}
			for _, st := range list {
				for _, ts := range st.Tasks {
					out.Tareas = append(out.Tareas, taskOutOf(st, ts, today))
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "coche_mantenimiento_hecho", Description: "Marca una tarea de mantenimiento como hecha (desde ahí cuenta la siguiente) y apunta su coste como gasto. Sin km, toma los estimados de ese día"},
		func(ctx context.Context, req *mcp.CallToolRequest, in doneIn) (*mcp.CallToolResult, doneOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, doneOut{}, err
			}
			v, err := svc.pickVehicle(ctx, who.User, in.Vehiculo)
			if err != nil {
				return nil, doneOut{}, err
			}
			st, err := svc.load(ctx, who.User, v.ID)
			if err != nil {
				return nil, doneOut{}, err
			}
			ts, err := findTask(st, in.Tarea)
			if err != nil {
				return nil, doneOut{}, err
			}
			day, err := svc.optDay(in.Fecha)
			if err != nil {
				return nil, doneOut{}, err
			}
			next, err := optDate(in.ProximaFecha)
			if err != nil {
				return nil, doneOut{}, err
			}
			_, e, err := svc.taskDone(ctx, who.User, ts.ID, doneInput{Day: day, Km: in.Km, Cost: in.Coste, NextKm: in.ProximosKm, NextDay: next,
				Note: in.Nota, Origin: originAssistant})
			if err != nil {
				return nil, doneOut{}, err
			}
			if st, err = svc.load(ctx, who.User, v.ID); err != nil {
				return nil, doneOut{}, err
			}
			nt, _ := st.task(ts.ID)
			return nil, doneOut{Tarea: taskOutOf(st, nt, svc.today()), Gasto: expenseOutOf(e)}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "coche_gasolineras", Description: "Las gasolineras más baratas de un municipio de España para un carburante, con su precio, dirección y horario (precios públicos del Ministerio, que se actualizan varias veces al día). Por defecto, el municipio y el carburante de la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in gasIn) (*mcp.CallToolResult, gasOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, gasOut{}, err
			}
			n := in.Cuantas
			if n <= 0 {
				n = 5
			}
			n = min(n, 20)
			prod := productFrom(in.Combustible)
			if strings.TrimSpace(in.Combustible) != "" && prod == "" {
				return nil, gasOut{}, bad("No conozco ese carburante: gasolina 95, gasolina 98, diésel, diésel premium, GLP o GNC.")
			}
			var m *municipio
			if q := strings.TrimSpace(in.Municipio); q != "" {
				all, err := svc.municipioList(ctx)
				if err != nil {
					return nil, gasOut{}, errors.New("el Ministerio no responde ahora; prueba más tarde")
				}
				found := searchMunicipios(all, q, 20)
				if len(found) == 0 {
					return nil, gasOut{}, bad("No encuentro ese municipio.")
				}
				m = &found[0]
				fq := strings.Join(seal.Words(q), " ")
				for i := range found {
					if strings.Join(seal.Words(found[i].Name), " ") == fq {
						m = &found[i]
						break
					}
				}
			}
			g, err := svc.gas(ctx, who.User, m, prod, n, true)
			if err != nil {
				return nil, gasOut{}, err
			}
			out := gasOut{Municipio: g.Settings.Municipio.Name, Provincia: g.Settings.Municipio.Province, Carburante: productLabel(g.Product),
				Media: r3(g.Avg), Gasolineras: []stationOut{}}
			if !g.Prices.Updated.IsZero() {
				out.Actualizado = g.Prices.Updated.In(madrid).Format("2006-01-02 15:04")
			}
			for _, s := range g.Best {
				out.Gasolineras = append(out.Gasolineras, stationOut{Rotulo: s.Brand, Direccion: s.Address, Localidad: s.Town, Horario: s.Hours, Precio: s.Price})
			}
			switch {
			case g.Prices.Fetched.IsZero():
				out.Nota = "aún no hay precios de ese municipio: el Ministerio no responde ahora"
			case len(out.Gasolineras) == 0:
				out.Nota = "ninguna gasolinera de ese municipio lo vende"
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "coche_gasto_apuntar", Description: "Apunta un gasto del vehículo que no sea combustible: seguro, parking, peaje, multa, impuesto de circulación, lavado, taller… (lo de una tarea de mantenimiento, mejor con coche_mantenimiento_hecho)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in expenseIn) (*mcp.CallToolResult, expenseOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, expenseOut{}, err
			}
			v, err := svc.pickVehicle(ctx, who.User, in.Vehiculo)
			if err != nil {
				return nil, expenseOut{}, err
			}
			day, err := svc.optDay(in.Fecha)
			if err != nil {
				return nil, expenseOut{}, err
			}
			kind := strings.Join(seal.Words(in.Tipo), " ")
			switch {
			case strings.HasPrefix(kind, "peaje"):
				kind = "peaje"
			case strings.HasPrefix(kind, "multa"):
				kind = "multa"
			case strings.Contains(kind, "ivtm") || strings.Contains(kind, "circulacion"):
				kind = "impuesto"
			case strings.Contains(kind, "taller"):
				kind = "mantenimiento"
			}
			e, err := svc.addExpense(ctx, who.User, expenseInput{VehicleID: v.ID, Kind: kind, Day: day, Amount: in.Importe, Km: in.Km, Note: in.Nota,
				Origin: originAssistant})
			if err != nil {
				return nil, expenseOut{}, err
			}
			return nil, expenseOutOf(e), nil
		})
}

// kindFrom adivina el tipo de una tarea por su nombre.
func kindFrom(name string) string {
	w := strings.Join(seal.Words(name), " ")
	for _, x := range []struct{ word, kind string }{{"aceite", "aceite"}, {"itv", "itv"}, {"neumatic", "neumaticos"}, {"rueda", "neumaticos"},
		{"freno", "frenos"}, {"pastilla", "frenos"}, {"filtro", "filtros"}, {"distribucion", "distribucion"}, {"correa", "distribucion"},
		{"revision", "revision"}} {
		if strings.Contains(w, x.word) {
			return x.kind
		}
	}
	return "otro"
}
