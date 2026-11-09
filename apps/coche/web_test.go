package main

import (
	"bytes"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// richStats es un coche de ejemplo con más de un año de historia (genérico:
// el repositorio es público), para ver las páginas sin base de datos.
func richStats(today time.Time) *vstats {
	v := vehicle{ID: 1, Created: today.AddDate(-1, -3, 0),
		vehicleData: vehicleData{Name: "Coche de prueba", Model: "Modelo Ejemplo 1.0 TSI", Plate: "0000TST", Fuel: "gasolina", StartKm: 84000}}
	var refuels []refuel
	km := 84000
	for i := 0; i < 32; i++ {
		d := today.AddDate(0, 0, -14*(32-i)+3)
		km += 520 + int(80*math.Sin(float64(i)))
		l := 34 + 4*math.Cos(float64(i)/3)
		px := 1.55 + 0.08*math.Sin(float64(i)/5)
		r := refuel{ID: int64(i + 1), VehicleID: 1, Day: d, refuelData: refuelData{Km: km, Liters: math.Round(l*100) / 100, Price: math.Round(px*1000) / 1000,
			Full: i%6 != 3}}
		r.Total = r2(r.Liters * r.Price)
		switch i % 4 {
		case 0:
			r.Station = "Gasolinera de la esquina"
		case 2:
			r.Station = "Bajocoste Ejemplo"
		}
		if i == 30 {
			r.Origin, r.Note = originAssistant, "viaje de prueba"
		}
		refuels = append(refuels, r)
	}
	var expenses []expense
	add := func(daysAgo int, kind string, amount float64, km int, note string) {
		expenses = append(expenses, expense{ID: int64(len(expenses) + 1), VehicleID: 1, Day: today.AddDate(0, 0, -daysAgo),
			expenseData: expenseData{Kind: kind, Amount: amount, Km: km, Note: note}})
	}
	add(300, "seguro", 385.2, 0, "Seguro a terceros ampliado")
	add(200, "impuesto", 68.5, 0, "")
	add(120, "multa", 100, 0, "Velocidad (con descuento)")
	add(60, "peaje", 12.4, 0, "")
	add(20, "parking", 6.3, 0, "Parking del centro")
	add(3, "lavado", 8, 0, "")
	expenses = append(expenses, expense{ID: 99, VehicleID: 1, TaskID: 1, Day: today.AddDate(0, 0, -150),
		expenseData: expenseData{Kind: "mantenimiento", Amount: 89.9, Km: 95500, Note: "Aceite y filtro: taller de prueba", Origin: originAssistant}})
	tasks := []task{
		{ID: 1, VehicleID: 1, taskData: taskData{Kind: "aceite", Name: "Aceite y filtro", EveryKm: 15000, EveryMonths: 12, LastKm: 95500, LastDay: today.AddDate(0, 0, -150)}},
		{ID: 2, VehicleID: 1, taskData: taskData{Kind: "itv", Name: "ITV", EveryMonths: 24, NextDay: today.AddDate(0, 0, 9), Note: "Estación de ITV de ejemplo"}},
		{ID: 3, VehicleID: 1, taskData: taskData{Kind: "frenos", Name: "Frenos (pastillas)", EveryKm: 30000, LastKm: 70000, LastDay: today.AddDate(-2, 0, 0)}},
		{ID: 4, VehicleID: 1, taskData: taskData{Kind: "neumaticos", Name: "Neumáticos", EveryKm: 40000, EveryMonths: 60}},
		{ID: 5, VehicleID: 1, taskData: taskData{Kind: "distribucion", Name: "Correa de distribución", EveryKm: 120000, LastKm: 60000, LastDay: today.AddDate(-4, 0, 0)}},
	}
	return compute(v, refuels, expenses, tasks, today)
}

func samplePrices(now time.Time) priceList {
	st := func(id, brand, addr, town, hours string, g95, goa float64) station {
		p := map[string]float64{}
		if g95 > 0 {
			p["g95"] = g95
		}
		if goa > 0 {
			p["goa"] = goa
		}
		return station{ID: id, Brand: brand, Address: addr, Town: town, Hours: hours, Prices: p}
	}
	return priceList{Fetched: now.Add(-40 * time.Minute), Updated: now.Add(-55 * time.Minute), Stations: []station{
		st("1", "GASOLINERA EJEMPLO", "Avenida de Ejemplo, 1", "Valencia", "L-D: 24H", 1.459, 1.389),
		st("2", "BAJOCOSTE EJEMPLO", "Calle Falsa, 123", "Valencia", "L-D: 06:00-22:00", 1.399, 1.349),
		st("3", "ESTACION SOLO GASOLINA", "Plaza Mayor de Ejemplo, 4", "Valencia", "L-S: 07:00-21:00", 1.519, 0),
		st("4", "LA DEL PUEBLO CON UN NOMBRE MUY LARGO PARA VER CÓMO SE PARTE", "Carretera de Ejemplo Km 2", "Pueblo de Ejemplo", "L-D: 24H", 1.489, 1.299),
	}}
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con COCHE_PREVIEW_DIR escribe las páginas y lo
// estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	c := &clock{t: testNow()}
	f := newFuelSource(defaultFuelURL, false)
	f.now = c.now
	w := &web{svc: &service{fuel: f, log: slog.New(slog.DiscardHandler), now: c.now}}
	today := dayOf(c.now())
	st := richStats(today)
	if len(st.Segments) < 20 || st.AvgL100 < 5 || st.AvgL100 > 8 {
		t.Fatalf("coche de ejemplo: %d tramos, %v l/100", len(st.Segments), st.AvgL100)
	}
	vehicles := []vehicle{st.Vehicle, {ID: 2, vehicleData: vehicleData{Name: "Moto", Model: "Scooter de ejemplo", Fuel: "gasolina"}}}

	page := func(tab, title string) pageData {
		return pageData{Name: "Ana", Tab: tab, Title: title, VID: 1, Veh: vehViewOf(st, today), Vehicles: w.vehRows(vehicles, 1)}
	}
	summary := page("resumen", "")
	summary.Sum, summary.Tasks, summary.ConsChart = sumOf(st), taskRows(st, today, 4), consChart(st, today)
	summary.CostChart = buildBars(st.Months, "Gasto de cada mes")
	summary.Flash = "Repostaje apuntado."

	fresh := compute(vehicle{ID: 2, Created: testNow(), vehicleData: vehicleData{Name: "Moto", Fuel: "gasolina", StartKm: 1200}}, nil, nil, nil, today)
	freshPage := pageData{Name: "Ana", Tab: "resumen", VID: 2, Veh: vehViewOf(fresh, today), Vehicles: w.vehRows(vehicles[:1], 0)}
	freshPage.Vehicles = nil
	freshPage.Sum, freshPage.CostChart = sumOf(fresh), buildBars(fresh.Months, "x")

	refuels := page("repostajes", "Repostajes")
	refuels.RForm, refuels.Stations, refuels.Refuels, refuels.Sum = w.newRefuelForm(st), w.stations(st), refuelRows(st, today), sumOf(st)
	refuelErr := refuels
	refuelErr.RForm = &refuelForm{Day: isoDay(today), Km: "101200", Liters: "40,5", Unit: "l", KmHint: "101.950"}
	refuelErr.Error = "Falta el precio por litro o el total pagado."
	refuelEdit := page("repostajes", "Editar repostaje")
	refuelEdit.RForm, refuelEdit.Stations, refuelEdit.Confirm = &refuelForm{ID: 31, Day: isoDay(today), Km: "101000", Liters: "38,2", Price: "1,579", Total: "60,32", Full: true, Unit: "l"}, w.stations(st), true

	tasks := page("mantenimiento", "Mantenimiento")
	tasks.Tasks, tasks.TForm, tasks.TaskKinds = taskRows(st, today, 0), &taskForm{Kind: "aceite"}, taskKindOptions("aceite")
	tasksEmpty := page("mantenimiento", "Mantenimiento")
	tasksEmpty.TForm, tasksEmpty.TaskKinds = &taskForm{Kind: "otro", Name: "Algo"}, taskKindOptions("otro")
	tasksEmpty.Error = "Di cada cuánto toca (km o meses) o cuándo es la próxima vez."
	taskPage := func(id int64, confirm bool) pageData {
		ts, _ := st.task(id)
		d := page("mantenimiento", ts.Name)
		row := taskRowOf(ts, today)
		var mine []expense
		for _, e := range st.Expenses {
			if e.TaskID == id {
				mine = append(mine, e)
			}
		}
		d.Task, d.TForm, d.DForm, d.TaskKinds, d.Expenses, d.Confirm = &row, taskFormOf(ts.task), &doneForm{Day: isoDay(today), KmHint: num(float64(st.EstKm), 0)},
			taskKindOptions(ts.Kind), expenseRows(mine, today), confirm
		return d
	}

	expenses := page("gastos", "Gastos")
	expenses.EForm, expenses.ExpenseKinds, expenses.Expenses, expenses.ByKind, expenses.Sum = &expenseForm{Kind: "parking", Day: isoDay(today)},
		expenseKindOptions("parking"), expenseRows(st.Expenses, today), byKind(st, today), sumOf(st)
	expenseEdit := page("gastos", "Editar gasto")
	expenseEdit.EForm, expenseEdit.ExpenseKinds = &expenseForm{ID: 99, Kind: "mantenimiento", Day: isoDay(today), Amount: "89,9", Km: "95500", Note: "Aceite y filtro", FromTask: true},
		expenseKindOptions("mantenimiento")

	vehiclesPage := pageData{Name: "Ana", Tab: "resumen", Title: "Vehículos", VID: 1, Vehicles: w.vehRows(vehicles, 0), VForm: &vehicleForm{Fuel: "gasolina"}, Fuels: fuelOptions("gasolina")}
	vehicleEdit := page("resumen", "Editar Coche de prueba")
	vehicleEdit.VForm, vehicleEdit.Fuels, vehicleEdit.Confirm = &vehicleForm{ID: 1, Name: "Coche de prueba", Model: "Modelo Ejemplo", Plate: "0000TST", Fuel: "gasolina", StartKm: "84000"},
		fuelOptions("gasolina"), true

	set := settingsData{Municipio: defaultMunicipio, Summary: true}
	gv := &gasView{Settings: set, Product: "g95", Prices: samplePrices(c.now())}
	gv.Best, gv.Avg = gv.Prices.cheapest("g95", 12)
	gv.Count = 4
	gas := pageData{Name: "Ana", Tab: "gasolineras", Title: "Gasolineras", VID: 1, Gas: w.gasPageOf(gv), Products: productOptions("g95"), Flash: "Municipio cambiado."}
	gasSearch := gas
	gasSearch.Flash, gasSearch.MQuery, gasSearch.MSearched = "", "valencia", true
	gasSearch.MResults = []municipio{defaultMunicipio, {ID: 900, Name: "Valencia de Ejemplo", Province: "Provincia de Ejemplo"}}
	gasNone := gas
	gasNone.Flash, gasNone.MQuery, gasNone.MSearched = "", "zzz", true
	f.fail("no responde")
	pending := &gasView{Settings: settingsData{Municipio: municipio{ID: 4354, Name: "Madrid", Province: "Madrid"}}, Product: "glp"}
	gasPending := pageData{Name: "Ana", Tab: "gasolineras", Title: "Gasolineras", Gas: w.gasPageOf(pending), Products: productOptions("glp")}

	type pg struct {
		file, tmpl string
		data       pageData
	}
	cases := []pg{
		{"resumen", "resumen.html", summary},
		{"resumen-nuevo", "resumen.html", freshPage},
		{"resumen-vacio", "resumen.html", pageData{Name: "Ana", Tab: "resumen", VForm: &vehicleForm{Fuel: "gasolina"}, Fuels: fuelOptions("gasolina")}},
		{"vehiculos", "vehiculos.html", vehiclesPage},
		{"vehiculo", "vehiculo.html", vehicleEdit},
		{"repostajes", "repostajes.html", refuels},
		{"repostajes-error", "repostajes.html", refuelErr},
		{"repostaje", "repostaje.html", refuelEdit},
		{"mantenimiento", "mantenimiento.html", tasks},
		{"mantenimiento-vacio", "mantenimiento.html", tasksEmpty},
		{"tarea", "tarea.html", taskPage(1, false)},
		{"tarea-itv", "tarea.html", taskPage(2, true)},
		{"tarea-sin-datos", "tarea.html", taskPage(4, false)},
		{"gastos", "gastos.html", expenses},
		{"gasto", "gasto.html", expenseEdit},
		{"gasolineras", "gasolineras.html", gas},
		{"gasolineras-busqueda", "gasolineras.html", gasSearch},
		{"gasolineras-sin-resultados", "gasolineras.html", gasNone},
		{"gasolineras-pendiente", "gasolineras.html", gasPending},
	}
	wants := map[string][]string{
		"resumen": {"Coche de prueba", "0000TST", "Modelo Ejemplo", "≈", "Consumo medio", "l/100 km", "€/km", `class="chart"`, `<polyline class="line"`, `class="avg"`,
			`class="fuel"`, `class="other"`, "Vencida", "Pronto", "ITV", `href="/coche/tarea/2"`, `href="/coche/v/2"`, `aria-current="true">Coche de prueba`,
			`href="/coche/v/1/repostajes#nuevo"`, `href="/coche/v/1/editar"`, "Repostaje apuntado."},
		"resumen-nuevo":       {"hacen falta dos llenados", "Sin tareas", "Aún no hay gastos", "1.200 km"},
		"resumen-vacio":       {"Añade tu vehículo", `action="/coche/vehiculos"`, `<option value="gasolina" selected>`, "Eléctrico", `href="/coche/repostajes"`},
		"vehiculos":           {"Moto", `href="/coche/v/2"`, "Añadir otro"},
		"vehiculo":            {"Editar vehículo", `value="0000TST"`, "¿Borrar Coche de prueba?", `name="confirmar" value="1"`, `action="/coche/v/1/borrar"`},
		"repostajes":          {"Apuntar repostaje", `name="lleno" value="1" checked`, `<option value="Gasolinera de la esquina">`, "a medias", "Talia", "viaje de prueba", "/100 km", `href="/coche/repostaje/32"`},
		"repostajes-error":    {"Falta el precio por litro", `value="40,5"`, `value="101200"`},
		"repostaje":           {"Editar repostaje", "¿Borrar este repostaje?", `action="/coche/repostaje/31/borrar"`, `value="1,579"`},
		"mantenimiento":       {"Correa de distribución", "Sin datos", "Al día", "Añadir una tarea", "Aceite y filtro (cada 15.000 km o 1 año)"},
		"mantenimiento-vacio": {"Sin tareas todavía", " open", "Di cada cuánto", `<option value="otro" selected>`},
		"tarea":               {"Aceite y filtro", "Marcar como hecha", `action="/coche/tarea/1/hecho"`, "Veces que se ha hecho", "taller de prueba", "Próxima vez:", "Última vez:"},
		"tarea-itv":           {"ITV", "Pronto", "¿Borrar la tarea ITV?", "Estación de ITV de ejemplo"},
		"tarea-sin-datos":     {"Sin fecha ni km", "Sin datos"},
		"gastos":              {"Apuntar gasto", "Últimos 12 meses", "Combustible", "Seguro", "Multa", "Parking del centro", "Total", `href="/coche/gasto/99"`},
		"gasto":               {"Editar gasto", "Viene de una tarea", `<option value="mantenimiento" selected>`},
		"gasolineras": {"Gasolina 95 E5 en Valencia", "BAJOCOSTE EJEMPLO", "1,399 €/l", "cts menos que la media", "Media: 1,46", "hace 55 min",
			"Pueblo de Ejemplo", `name="resumen" value="1" checked`, `<option value="g95" selected>`, `action="/coche/gasolineras/buscar#municipio"`, "Municipio cambiado."},
		"gasolineras-busqueda":       {`name="municipio" value="7214"`, "Valencia de Ejemplo", "Elegir", `value="valencia"`},
		"gasolineras-sin-resultados": {"No ha salido ningún municipio con «zzz»"},
		"gasolineras-pendiente":      {"Pidiendo los precios", "El Ministerio falla: no responde", "se vuelve a intentar en 5 min", "GLP (autogás) en Madrid"},
	}
	nots := map[string][]string{
		"resumen":          {"Sin tareas"},
		"repostajes":       {"Falta el precio"},
		"resumen-vacio":    {"Consumo medio"},
		"gasolineras":      {"COOPERATIVA", "Valencia, Valencia"},
		"tarea":            {"¿Borrar la tarea"},
		"mantenimiento":    {" open"},
		"repostajes-error": {"Repostaje apuntado."},
	}

	dir := os.Getenv("COCHE_PREVIEW_DIR")
	base := "/coche"
	if dir != "" {
		base = "."
		os.MkdirAll(dir, 0o755)
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base = "v0.0.0-test", base
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script", "NaN", "Inf", "%!"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s contiene %q", p.file, bad)
				}
			}
			if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
				t.Errorf("%s tiene un manejador de eventos", p.file)
			}
			if !strings.Contains(out, `aria-current="page"`) {
				t.Errorf("%s: falta la pestaña activa", p.file)
			}
			if dir == "" {
				for _, want := range wants[p.file] {
					if !strings.Contains(out, want) {
						t.Errorf("%s: falta %q", p.file, want)
					}
				}
				for _, not := range nots[p.file] {
					if strings.Contains(out, not) {
						t.Errorf("%s: sobra %q", p.file, not)
					}
				}
			} else if err := os.WriteFile(filepath.Join(dir, p.file+".html"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
	if dir != "" {
		static, _ := fs.Sub(webFS, "web/static")
		os.MkdirAll(filepath.Join(dir, "static"), 0o755)
		for _, n := range []string{"app.css", "icon.svg"} {
			b, _ := fs.ReadFile(static, n)
			os.WriteFile(filepath.Join(dir, "static", n), b, 0o644)
		}
	}
}
