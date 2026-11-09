package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas y
// el Ministerio falso: vehículos, repostajes, tareas, gastos, avisos, la
// pasada de precios, que nadie vea lo de otro y que nada quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, fm, c := newTestService(t)
	ctx := context.Background()

	if _, err := svc.addVehicle(ctx, "ana", vehicleInput{Fuel: "gasolina"}); !isInput(err) {
		t.Fatalf("sin nombre: %v", err)
	}
	vid, err := svc.addVehicle(ctx, "ana", vehicleInput{Name: "Coche de prueba", Model: "Modelo Ejemplo 1.0", Plate: "0000 tst", Fuel: "gasolina", StartKm: 10000,
		Note: "comprado de segunda mano"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.store.vehicle(ctx, "ana", vid); v.Plate != "0000 TST" || v.Fuel != "gasolina" || v.StartKm != 10000 {
		t.Fatalf("vehículo: %+v", v)
	}

	apunta := func(d string, km int, l, px float64, full bool, station string) refuel {
		t.Helper()
		r, err := svc.addRefuel(ctx, "ana", refuelInput{VehicleID: vid, Day: day(d), Km: km, Liters: l, Price: px, Full: full, Station: station, Origin: "web"})
		if err != nil {
			t.Fatalf("repostaje %s: %v", d, err)
		}
		return r
	}
	apunta("2026-09-01", 12000, 40, 1.5, true, "Gasolinera de la esquina")
	apunta("2026-09-10", 12300, 10, 1.5, false, "")
	r3 := apunta("2026-09-20", 12600, 30, 1.6, true, "Gasolinera de la esquina")
	if r3.Total != 48 {
		t.Fatalf("total del precio: %v", r3.Total)
	}
	// Con el total y sin precio, el precio sale del total.
	r4, err := svc.addRefuel(ctx, "ana", refuelInput{VehicleID: vid, Day: day("2026-10-15"), Km: 13100, Liters: 35, Total: 49, Full: true, Origin: "web"})
	if err != nil || !near(r4.Price, 1.4) {
		t.Fatalf("precio del total: %+v %v", r4, err)
	}
	for _, bad := range []refuelInput{
		{VehicleID: vid, Day: day("2026-10-20"), Km: 13000, Liters: 10, Price: 1.5},             // menos km que antes
		{VehicleID: vid, Day: day("2026-09-05"), Km: 12900, Liters: 10, Price: 1.5},             // más km que después
		{VehicleID: vid, Day: day("2026-10-20"), Liters: 10, Price: 1.5},                        // sin km
		{VehicleID: vid, Day: day("2026-10-20"), Km: 13200, Price: 1.5},                         // sin litros
		{VehicleID: vid, Day: day("2026-10-20"), Km: 13200, Liters: 10},                         // sin precio
		{VehicleID: vid, Day: day("2026-10-20"), Km: 13200, Liters: 10, Price: 150},             // precio raro
		{VehicleID: vid, Day: dayOf(testNow()).Add(dayUnit), Km: 13200, Liters: 10, Price: 1.5}, // futura
		{VehicleID: vid, Day: day("2026-10-20"), Km: 9000, Liters: 10, Price: 1.5},              // menos que los iniciales
		{VehicleID: vid, Day: day("2026-10-20"), Km: 13200, Liters: -1, Price: 1.5},             // negativo
	} {
		if _, err := svc.addRefuel(ctx, "ana", bad); !isInput(err) {
			t.Errorf("acepta %+v: %v", bad, err)
		}
	}

	// Gastos y tareas.
	if _, err := svc.addExpense(ctx, "ana", expenseInput{VehicleID: vid, Kind: "seguro", Day: day("2026-10-01"), Amount: 300, Note: "Seguro anual de prueba"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.addExpense(ctx, "ana", expenseInput{VehicleID: vid, Kind: "parking", Day: day("2026-11-02"), Amount: 5, Km: 13400}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.addExpense(ctx, "ana", expenseInput{VehicleID: vid, Kind: "parking", Day: day("2026-11-02")}); !isInput(err) {
		t.Fatal("gasto sin importe")
	}
	oil, err := svc.addTask(ctx, "ana", taskInput{VehicleID: vid, Kind: "aceite", LastKm: 12000, LastDay: day("2026-09-01")})
	if err != nil || oil.Name != "Aceite y filtro" || oil.EveryKm != 15000 || oil.EveryMonths != 12 {
		t.Fatalf("aceite con lo habitual: %+v %v", oil, err)
	}
	if _, err := svc.addTask(ctx, "ana", taskInput{VehicleID: vid, Kind: "otro", Name: "Algo"}); !isInput(err) {
		t.Fatal("tarea sin cada cuánto")
	}
	// La ITV en 4 días y los frenos ya pasados de km: avisan al crearlas.
	itv, err := svc.addTask(ctx, "ana", taskInput{VehicleID: vid, Kind: "itv", NextDay: day("2026-11-10")})
	if err != nil {
		t.Fatal(err)
	}
	brakes, err := svc.addTask(ctx, "ana", taskInput{VehicleID: vid, Kind: "frenos", NextKm: 13300, Note: "Taller de prueba"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 2 || got[0].Kind != "coche" || got[0].Text != "🔧 Coche de prueba: ITV — vence el 10 nov (en 4 días)" ||
		got[1].Text != "🔧 Coche de prueba: Frenos (pastillas) — llevas 13.400 km y tocaba a los 13.300 km" {
		t.Fatalf("avisos: %+v %v", got, err)
	}
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatal("los avisos se entregan una sola vez")
	}
	// Una pasada después no repite (una vez por vencimiento).
	c.add(2 * time.Hour)
	svc.round(ctx)
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatalf("repite: %+v", again)
	}

	// El resumen de la mañana: solo lo que vence.
	lines, err := svc.todayLines(ctx, "ana")
	if err != nil || len(lines) != 1 ||
		lines[0] != "Coche de prueba: Frenos (pastillas) pasada de km (≈13.490 de 13.300); ITV vence el 10 nov (en 4 días)." {
		t.Fatalf("resumen: %q %v", lines, err)
	}

	// Hecho: cuenta desde ahí, apunta el gasto y ya no sale en el resumen.
	done, e, err := svc.taskDone(ctx, "ana", brakes.ID, doneInput{Cost: 120, Note: "pastillas delanteras", Origin: "web"})
	if err != nil || done.LastKm != 13490 || done.NextKm != 0 || e.Kind != "mantenimiento" || e.TaskID != brakes.ID ||
		e.Note != "Frenos (pastillas): pastillas delanteras" || e.Amount != 120 {
		t.Fatalf("hecho: %+v %+v %v", done, e, err)
	}
	if _, _, err := svc.taskDone(ctx, "ana", itv.ID, doneInput{Day: day("2026-11-05"), NextDay: day("2026-11-01")}); !isInput(err) {
		t.Fatal("la próxima antes que esta")
	}
	if _, _, err := svc.taskDone(ctx, "ana", itv.ID, doneInput{Day: day("2026-11-05"), Cost: 45.5, NextDay: day("2028-11-05")}); err != nil {
		t.Fatal(err)
	}
	if lines, _ := svc.todayLines(ctx, "ana"); len(lines) != 0 {
		t.Fatalf("sin nada que venza, ninguna línea: %q", lines)
	}
	st, err := svc.load(ctx, "ana", vid)
	if err != nil || len(st.Expenses) != 4 || !near(st.OtherCost, 470.5) || len(st.Segments) != 2 || st.LastKm != 13490 {
		t.Fatalf("cuentas: %d gastos %v otros, %d tramos, %d km, %v", len(st.Expenses), st.OtherCost, len(st.Segments), st.LastKm, err)
	}

	// Gasolineras: la pasada pide el municipio de cada uno una sola vez.
	if err := svc.setPrefs(ctx, "ana", "goa", true); err != nil {
		t.Fatal(err)
	}
	svc.round(ctx)
	if fm.count("/EstacionesTerrestres/FiltroMunicipio/7214") != 1 || fm.count("/Listados/Municipios/") != 1 {
		t.Fatalf("pasada de precios: %v", fm.calls)
	}
	svc.round(ctx)
	c.add(15 * time.Minute)
	svc.round(ctx)
	if fm.count("/EstacionesTerrestres/FiltroMunicipio/7214") != 1 {
		t.Fatal("pide antes de 6 horas")
	}
	lines, _ = svc.todayLines(ctx, "ana")
	if len(lines) != 1 || lines[0] != "Gasóleo A (diésel) más barata en Valencia: LA DEL PUEBLO (Carretera de Ejemplo Km 2), 1,299 €/l." {
		t.Fatalf("la más barata en el resumen: %q", lines)
	}
	fm.set(func(f *fakeMinisterio) { f.shift = 0.1 })
	c.add(6 * time.Hour)
	svc.round(ctx)
	g, err := svc.gas(ctx, "ana", nil, "", 3, false)
	if err != nil || fm.count("/EstacionesTerrestres/FiltroMunicipio/7214") != 2 || len(g.Best) != 3 || !near(g.Best[0].Price, 1.399) || g.Product != "goa" {
		t.Fatalf("a las 6 horas: %+v %v", g, err)
	}
	// Otro municipio para luis; el de ana sigue.
	if _, err := svc.setMunicipio(ctx, "luis", 4354); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setMunicipio(ctx, "luis", 123456); !isInput(err) {
		t.Fatal("municipio que no existe")
	}
	c.add(11 * time.Minute) // el cambio pide una pasada; aquí, la siguiente
	svc.round(ctx)
	if fm.count("/EstacionesTerrestres/FiltroMunicipio/4354") != 1 {
		t.Fatal("no pide el municipio de luis")
	}
	gl, _ := svc.gas(ctx, "luis", nil, "", 5, false)
	if gl.Settings.Municipio.Name != "Madrid" || len(gl.Best) != 1 || gl.Best[0].Brand != "OTRA EJEMPLO" {
		t.Fatalf("gasolineras de luis: %+v", gl)
	}
	// Una lista vacía no borra la buena.
	if saved, err := svc.savePrices(ctx, 7214, priceList{Fetched: c.now()}); saved || err != nil {
		t.Fatalf("lista vacía: %v %v", saved, err)
	}
	if p, _ := svc.store.prices(ctx, 7214); len(p.Stations) != 4 {
		t.Fatal("se ha perdido la lista buena")
	}

	// Lo de ana, ni lo ve ni lo toca luis.
	if l, _ := svc.store.vehicles(ctx, "luis"); len(l) != 0 {
		t.Fatal("luis ve los vehículos de ana")
	}
	if _, err := svc.load(ctx, "luis", vid); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve un vehículo de ana")
	}
	if _, err := svc.addRefuel(ctx, "luis", refuelInput{VehicleID: vid, Day: day("2026-11-01"), Km: 20000, Liters: 1, Price: 1}); !errors.Is(err, errNotFound) {
		t.Fatalf("luis apunta en un vehículo de ana: %v", err)
	}
	if _, err := svc.store.addExpense(ctx, "luis", expense{VehicleID: vid, Day: day("2026-11-01"), expenseData: expenseData{Kind: "otro", Amount: 1}}); !errors.Is(err, errNotFound) {
		t.Fatal("luis apunta un gasto en un vehículo de ana")
	}
	if _, err := svc.store.addTask(ctx, "luis", task{VehicleID: vid, taskData: taskData{Kind: "itv", Name: "x", EveryMonths: 1}}); !errors.Is(err, errNotFound) {
		t.Fatal("luis crea una tarea en un vehículo de ana")
	}
	if _, err := svc.updateRefuel(ctx, "luis", r3.ID, refuelInput{Day: day("2026-09-20"), Km: 12600, Liters: 1, Price: 1}); !errors.Is(err, errNotFound) {
		t.Fatal("luis edita un repostaje de ana")
	}
	if _, _, err := svc.taskDone(ctx, "luis", oil.ID, doneInput{}); !errors.Is(err, errNotFound) {
		t.Fatal("luis marca una tarea de ana")
	}
	for name, del := range map[string]func() error{
		"repostaje": func() error { return svc.store.deleteRefuel(ctx, "luis", r3.ID) },
		"tarea":     func() error { return svc.store.deleteTask(ctx, "luis", oil.ID) },
		"gasto":     func() error { return svc.store.deleteExpense(ctx, "luis", st.Expenses[0].ID) },
		"vehículo":  func() error { return svc.store.deleteVehicle(ctx, "luis", vid) },
	} {
		if err := del(); !errors.Is(err, errNotFound) {
			t.Errorf("luis borra un %s de ana: %v", name, err)
		}
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis recibe los avisos de ana")
	}
	if lines, err := svc.todayLines(ctx, "luis"); err != nil || len(lines) != 0 {
		t.Fatalf("el resumen de luis: %v %v", lines, err)
	}

	// Nada de lo de ana en claro, en ninguna tabla; tampoco el municipio de
	// nadie.
	svc.store.addNotice(ctx, "ana", "🔧 Coche de prueba: ITV — prueba", c.now())
	words := []string{"Coche de prueba", "Modelo Ejemplo", "0000 TST", "segunda mano", "Gasolinera de la esquina", "Seguro anual", "Aceite", "ITV",
		"Frenos", "Taller de prueba", "pastillas", "seguro", "parking", "mantenimiento", "gasolina", "13300", "12600", "Valencia", "Madrid", "goa"}
	for _, tb := range []string{"vehicles", "refuels", "maintenance", "expenses", "settings", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}
	noPlain(t, svc.store.db, "prices", "EJEMPLO", "Calle Falsa", "Valencia", "Madrid", "1,399", "1.399")

	// Borrar el vehículo se lleva todo lo suyo.
	if err := svc.store.deleteVehicle(ctx, "ana", vid); err != nil {
		t.Fatal(err)
	}
	var left int
	svc.store.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM refuels) + (SELECT count(*) FROM maintenance) + (SELECT count(*) FROM expenses)`).Scan(&left)
	if left != 0 {
		t.Fatalf("quedan %d filas del vehículo borrado", left)
	}

	// Los avisos viejos se borran; los precios que ya no usa nadie, también.
	svc.store.addNotice(ctx, "ana", "viejo", c.now().Add(-8*24*time.Hour))
	svc.store.takeNotices(ctx, "ana", c.now())
	var n int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE created_at < $1`, c.now().Add(-7*24*time.Hour)).Scan(&n)
	if n != 0 {
		t.Fatal("quedan avisos viejos")
	}
	if err := svc.store.prunePrices(ctx, []int{7214}, c.now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM prices`).Scan(&n)
	if n != 1 {
		t.Fatalf("precios guardados: %d", n)
	}
}

func TestDueByKmAfterRefuel(t *testing.T) {
	svc, _, c := newTestService(t)
	ctx := context.Background()
	vid, _ := svc.addVehicle(ctx, "ana", vehicleInput{Name: "Furgo", Fuel: "diesel", StartKm: 50000})
	if _, err := svc.addTask(ctx, "ana", taskInput{VehicleID: vid, Kind: "aceite", EveryKm: 1000, LastKm: 50000, LastDay: day("2026-10-01")}); err != nil {
		t.Fatal(err)
	}
	// Al estimar no avisa: solo con unos km apuntados que pasen.
	if _, err := svc.addRefuel(ctx, "ana", refuelInput{VehicleID: vid, Day: day("2026-10-20"), Km: 50900, Liters: 40, Price: 1.4, Full: true}); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(n) != 0 {
		t.Fatalf("avisa antes de tiempo: %+v", n)
	}
	if lines, _ := svc.todayLines(ctx, "ana"); len(lines) != 1 || !strings.Contains(lines[0], "Aceite y filtro pasada de km") {
		t.Fatalf("en el resumen, con los km estimados: %q", lines)
	}
	if _, err := svc.addRefuel(ctx, "ana", refuelInput{VehicleID: vid, Day: day("2026-11-05"), Km: 51050, Liters: 30, Price: 1.4, Full: true}); err != nil {
		t.Fatal(err)
	}
	n, _ := svc.store.takeNotices(ctx, "ana", c.now())
	if len(n) != 1 || n[0].Text != "🔧 Furgo: Aceite y filtro — llevas 51.050 km y tocaba a los 51.000 km" {
		t.Fatalf("aviso por km: %+v", n)
	}
}
