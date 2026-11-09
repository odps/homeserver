package main

import (
	"strings"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{kmStr(152300), "152.300 km"}, {perLiter(1.4391, "l"), "1,439 €/l"}, {euros(-1234.5), "−1.234,50 €"}, {eurosRound(12345.6), "12.346 €"},
		{eurPerKm(0.0941), "0,094 €/km"}, {num(6.82, 1), "6,8"}, {inDays(0), "hoy"}, {inDays(1), "mañana"}, {inDays(5), "en 5 días"},
		{inDays(-3), "hace 3 días"}, {shortDate(day("2026-11-10"), day("2026-11-06")), "10 nov"}, {shortDate(day("2027-01-02"), day("2026-11-06")), "2 ene 2027"},
		{titleCase("AVENIDA DEL PUERTO, S/N"), "Avenida del Puerto, S/N"}, {titleCase("VALENCIA / VALÈNCIA"), "Valencia / València"},
		{everyText(taskData{EveryKm: 15000, EveryMonths: 12}), "cada 15.000 km o 1 año"}, {everyText(taskData{EveryMonths: 24}), "cada 2 años"},
		{everyText(taskData{EveryMonths: 6}), "cada 6 meses"}, {everyText(taskData{}), ""},
	} {
		if c.got != c.want {
			t.Errorf("%q, quería %q", c.got, c.want)
		}
	}
	for in, want := range map[string]int{"152.300": 152300, "152300": 152300, "152 300 km": 152300, "1.234,6": 1235} {
		if v, err := parseKm(in); err != nil || v != want {
			t.Errorf("parseKm(%q) = %v %v", in, v, err)
		}
	}
	for _, in := range []string{"", "abc", "-5"} {
		if _, err := parseKm(in); err == nil {
			t.Errorf("parseKm(%q) lo acepta", in)
		}
	}
	for in, want := range map[string]float64{"40,5": 40.5, "1,459": 1.459, "60.71": 60.71, "1.234,56 €": 1234.56} {
		if v, err := parseNum(in); err != nil || !near(v, want) {
			t.Errorf("parseNum(%q) = %v %v", in, v, err)
		}
	}
	for _, c := range []struct {
		from string
		n    int
		want string
	}{{"2026-01-31", 1, "2026-02-28"}, {"2026-11-06", 12, "2027-11-06"}, {"2026-01-15", -13, "2024-12-15"}, {"2024-02-29", 12, "2025-02-28"}} {
		if got := isoDay(addMonths(day(c.from), c.n)); got != c.want {
			t.Errorf("addMonths(%s, %d) = %s", c.from, c.n, got)
		}
	}
}

// sampleStats es un coche de ejemplo con sus cuentas ya hechas.
func sampleStats(today time.Time) *vstats {
	v := vehicle{ID: 1, Created: time.Date(2026, 1, 1, 10, 0, 0, 0, madrid),
		vehicleData: vehicleData{Name: "Coche de prueba", Model: "Modelo Ejemplo 1.0", Plate: "0000TST", Fuel: "gasolina", StartKm: 10000}}
	rf := func(id int64, d string, km int, l, px float64, full bool, station string) refuel {
		return refuel{ID: id, VehicleID: 1, Day: day(d), refuelData: refuelData{Km: km, Liters: l, Price: px, Total: r2(l * px), Full: full, Station: station}}
	}
	refuels := []refuel{
		rf(1, "2026-09-01", 12000, 40, 1.5, true, "Gasolinera de la esquina"),
		rf(2, "2026-09-10", 12300, 10, 1.5, false, ""),
		rf(3, "2026-09-20", 12600, 30, 1.6, true, "Gasolinera de la esquina"),
		rf(4, "2026-10-15", 13100, 35, 1.4, true, "Bajocoste Ejemplo"),
	}
	expenses := []expense{
		{ID: 1, VehicleID: 1, Day: day("2026-10-01"), expenseData: expenseData{Kind: "seguro", Amount: 300, Note: "Seguro anual"}},
		{ID: 2, VehicleID: 1, Day: day("2026-11-02"), expenseData: expenseData{Kind: "parking", Amount: 5, Km: 13400}},
	}
	tasks := []task{
		{ID: 1, VehicleID: 1, taskData: taskData{Kind: "aceite", Name: "Aceite y filtro", EveryKm: 15000, EveryMonths: 12, LastKm: 12000, LastDay: day("2026-09-01")}},
		{ID: 2, VehicleID: 1, taskData: taskData{Kind: "itv", Name: "ITV", EveryMonths: 24, NextDay: day("2026-11-10")}},
		{ID: 3, VehicleID: 1, taskData: taskData{Kind: "frenos", Name: "Frenos (pastillas)", NextKm: 13300}},
		{ID: 4, VehicleID: 1, taskData: taskData{Kind: "neumaticos", Name: "Neumáticos", EveryKm: 40000}},
	}
	return compute(v, refuels, expenses, tasks, today)
}

func TestCompute(t *testing.T) {
	today := dayOf(testNow())
	st := sampleStats(today)
	// Dos tramos: 40 l (10 a medias + 30) en 600 km y 35 l en 500 km.
	if len(st.Segments) != 2 || st.Segments[0].Km != 600 || !near(st.Segments[0].Liters, 40) || st.Segments[0].ToID != 3 ||
		!near(st.Segments[1].L100, 7) {
		t.Fatalf("tramos: %+v", st.Segments)
	}
	if !near(st.AvgL100, 75.0/1100*100) || !near(st.LastL100, 7) || !near(st.FuelPerKm, 112.0/1100) {
		t.Fatalf("consumo: media %v último %v €/km %v", st.AvgL100, st.LastL100, st.FuelPerKm)
	}
	if !near(st.FuelCost, 172) || !near(st.OtherCost, 305) || !near(st.AllPerKm, 477.0/3400) {
		t.Fatalf("gasto: %v %v %v", st.FuelCost, st.OtherCost, st.AllPerKm)
	}
	if len(st.Months) != 12 || isoDay(st.Months[0].Month) != "2025-12-01" || !near(st.Months[9].Fuel, 123) || !near(st.Months[10].Other, 300) ||
		!near(st.thisMonth(), 5) || !near(st.year(), 477) {
		t.Fatalf("meses: %+v", st.Months)
	}
	// Los últimos km, los del parking; el ritmo, desde que se añadió.
	if st.LastKm != 13400 || isoDay(st.LastKmDay) != "2026-11-02" || !near(st.KmPerDay, 3400.0/305) || st.EstKm != 13445 {
		t.Fatalf("km: %d %v ritmo %v hoy %d", st.LastKm, st.LastKmDay, st.KmPerDay, st.EstKm)
	}

	// Las tareas, de la más urgente a la que no tiene datos.
	var order []string
	for _, x := range st.Tasks {
		order = append(order, x.Kind+":"+x.Status)
	}
	if strings.Join(order, " ") != "frenos:vencida itv:pronto aceite:ok neumaticos:sin-datos" {
		t.Fatalf("orden: %v", order)
	}
	fr, itv, ac, ne := st.Tasks[0], st.Tasks[1], st.Tasks[2], st.Tasks[3]
	if fr.KmLeft != -145 || itv.DaysLeft != 4 || ac.DueKm != 27000 || isoDay(ac.DueDay) != "2027-09-01" || ac.EstDay.IsZero() {
		t.Fatalf("vencimientos: %+v %+v %+v", fr, itv, ac)
	}
	for _, c := range []struct{ got, want string }{
		{dueText(fr, today), "a los 13.300 km: ya van ≈13.445 km"},
		{dueText(itv, today), "el 10 nov (en 4 días)"},
		{dueText(ac, today), "el 1 sep 2027 (en 299 días) o a los 27.000 km (faltan ≈13.555 km, hacia mar 2030)"},
		{dueText(ne, today), "Sin fecha ni km: apunta cuándo se hizo o cuándo toca"},
		{dueNotice("Coche de prueba", itv, st.LastKm, today), "🔧 Coche de prueba: ITV — vence el 10 nov (en 4 días)"},
		{dueNotice("Coche de prueba", fr, st.LastKm, today), "🔧 Coche de prueba: Frenos (pastillas) — llevas 13.400 km y tocaba a los 13.300 km"},
		{dueNotice("Coche de prueba", ac, st.LastKm, today), ""},
		{strings.Join(summaryDue(fr, today), " "), "pasada de km (≈13.445 de 13.300)"},
	} {
		if c.got != c.want {
			t.Errorf("%q, quería %q", c.got, c.want)
		}
	}

	// Sin repostajes ni ritmo: lo que hay.
	empty := compute(vehicle{ID: 2, Created: testNow(), vehicleData: vehicleData{Name: "Nuevo", StartKm: 500}}, nil, nil, nil, today)
	if empty.AvgL100 != 0 || empty.EstKm != 500 || empty.KmPerDay != 0 || empty.thisMonth() != 0 {
		t.Fatalf("vacío: %+v", empty)
	}
	// Un llenado solo no da consumo; uno a medias primero se ignora.
	one := compute(vehicle{ID: 3, Created: testNow()}, []refuel{
		{ID: 1, Day: day("2026-10-01"), refuelData: refuelData{Km: 1000, Liters: 20, Total: 30}},
		{ID: 2, Day: day("2026-10-10"), refuelData: refuelData{Km: 1300, Liters: 40, Total: 60, Full: true}},
	}, nil, nil, today)
	if len(one.Segments) != 0 || one.AvgL100 != 0 || !near(one.FuelCost, 90) {
		t.Fatalf("un llenado: %+v", one)
	}
}

func TestProductAndKind(t *testing.T) {
	for in, want := range map[string]string{"g98": "g98", "Gasolina 95": "g95", "gasolina": "g95", "diésel": "goa", "Gasóleo A": "goa",
		"diesel premium": "goap", "98": "g98", "GLP": "glp", "autogás": "glp", "gas natural": "gnc", "E10": "g95e10", "agua": "", "": ""} {
		if got := productFrom(in); got != want {
			t.Errorf("productFrom(%q) = %q, quería %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Cambio de aceite": "aceite", "ITV": "itv", "ruedas nuevas": "neumaticos", "pastillas de freno": "frenos",
		"correa de distribución": "distribucion", "lavar": "otro"} {
		if got := kindFrom(in); got != want {
			t.Errorf("kindFrom(%q) = %q", in, got)
		}
	}
	vs := []vehicle{{ID: 1, vehicleData: vehicleData{Name: "Eléctrico", Fuel: "electrico"}}, {ID: 2, vehicleData: vehicleData{Name: "Furgo", Fuel: "diesel"}}}
	if productFor(settingsData{}, vs) != "goa" || productFor(settingsData{Product: "g98"}, vs) != "g98" || productFor(settingsData{}, nil) != "g95" {
		t.Fatal("carburante por defecto")
	}
	if v, err := findVehicle(vs, "furgo"); err != nil || v.ID != 2 {
		t.Fatalf("buscar vehículo: %+v %v", v, err)
	}
	if _, err := findVehicle(vs, ""); !isInput(err) {
		t.Fatal("con dos vehículos hay que decir cuál")
	}
	if v, err := findVehicle(vs[1:], ""); err != nil || v.ID != 2 {
		t.Fatal("con uno solo, ese")
	}
	if _, err := findVehicle(nil, ""); !isInput(err) {
		t.Fatal("sin vehículos")
	}
}

func TestSearchMunicipios(t *testing.T) {
	all := []municipio{{1, "Alboraya", "Valencia / València"}, {7214, "Valencia", "Valencia / València"}, {2, "Valencia de Ejemplo", "Ejemplo"},
		{3, "Vall d'Alcalà (la)", "Alicante"}, {4, "Villanueva de Ejemplo", "Ejemplo"}}
	got := searchMunicipios(all, "valencia", 3)
	if len(got) != 3 || got[0].ID != 7214 || got[1].ID != 2 || got[2].ID != 1 {
		t.Fatalf("valencia: %+v", got)
	}
	if got := searchMunicipios(all, "alcala", 5); len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("sin tildes: %+v", got)
	}
	if got := searchMunicipios(all, " ", 5); got != nil {
		t.Fatal("vacío")
	}
}
