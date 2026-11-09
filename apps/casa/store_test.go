package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas y
// el REE falso: los precios de hoy y de mañana, las tareas, los avisos, las
// lecturas, que nadie vea lo de otro y que nada quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, f, c := newTestService(t)
	ctx := context.Background()
	today := day0("2026-11-09")

	// Mediodía: los precios de hoy, una sola vez; los de mañana aún no se buscan.
	if !svc.refreshPrices(ctx) || f.count() != 1 {
		t.Fatalf("precios de hoy: %d peticiones", f.count())
	}
	if p, _ := svc.store.prices(ctx, today); len(p.Hours) != 24 || !near(p.Hours[14].P, 0.08697) {
		t.Fatalf("guardados: %+v", p)
	}
	c.add(30 * time.Minute)
	if svc.refreshPrices(ctx) || f.count() != 1 {
		t.Fatal("vuelve a pedir lo que ya tiene")
	}

	// Tareas de ana y de luis, y sus ajustes.
	caldera, existed, err := svc.addTemplate(ctx, "ana", "caldera")
	if err != nil || existed || caldera.Name != "Revisión de la caldera" || !caldera.Due.Equal(today) || !caldera.Notified.Equal(today) {
		t.Fatalf("sugerencia: %+v %v", caldera, err)
	}
	if again, existed, _ := svc.addTemplate(ctx, "ana", "caldera"); !existed || again.ID != caldera.ID {
		t.Fatal("la sugerencia se repite")
	}
	if _, _, err := svc.addTemplate(ctx, "ana", "nada"); !isInput(err) {
		t.Fatalf("sugerencia que no existe: %v", err)
	}
	filtros, err := svc.addTask(ctx, "ana", taskInput{Name: "Limpiar los filtros del deshumidificador", Every: 3, Unit: unitMonths,
		Last: day0("2026-08-10"), Note: "los de la marca de ejemplo"})
	if err != nil || isoDay(filtros.Due) != "2026-11-10" || !filtros.Notified.IsZero() {
		t.Fatalf("tarea: %+v %v", filtros, err)
	}
	campana, err := svc.addTask(ctx, "ana", taskInput{Name: "Desengrasar la campana extractora", Every: 2, Unit: unitMonths, Last: day0("2026-09-01")})
	if err != nil || isoDay(campana.Due) != "2026-11-01" {
		t.Fatalf("atrasada al crearla: %+v %v", campana, err)
	}
	if _, err := svc.addTask(ctx, "luis", taskInput{Name: "Regar el huerto urbano", Every: 1, Unit: unitWeeks, Last: day0("2026-11-03")}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.saveSettings(ctx, "ana", settingsData{PriceLow: true, LowBelow: 0.07}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.saveSettings(ctx, "luis", settingsData{MaintOff: true, GasUnit: "kWh"}); err != nil {
		t.Fatal(err)
	}
	// Lo que se crea ya atrasado no se avisa: se acaba de ver.
	if n, err := svc.checkTasks(ctx); err != nil || n != 0 {
		t.Fatalf("avisos al crear: %d %v", n, err)
	}

	// A las 20:20 aún no están los de mañana; a las 20:31, sí. Se insiste
	// cada 10 minutos, no más.
	c.set(at("2026-11-09", 20, 20))
	if svc.refreshPrices(ctx) || f.count() != 2 {
		t.Fatalf("mañana sin publicar: %d", f.count())
	}
	f.set(func(f *fakeREE) { f.published["2026-11-10"] = true })
	c.add(5 * time.Minute)
	if svc.refreshPrices(ctx) || f.count() != 2 {
		t.Fatal("insiste antes de 10 minutos")
	}
	c.set(at("2026-11-09", 20, 31))
	svc.round(ctx)
	if f.count() != 3 || !svc.have(ctx, today.Add(dayUnit)) {
		t.Fatalf("precios de mañana: %d", f.count())
	}
	got, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 1 || got[0].Kind != "casa" || !strings.HasPrefix(got[0].Text, "💡 Mañana la luz estará muy barata (0,050 €/kWh como mínimo) de 12 a 15 h.") {
		t.Fatalf("aviso del precio: %+v %v", got, err)
	}
	svc.mu.Lock()
	svc.priceDone = time.Time{}
	svc.mu.Unlock()
	svc.round(ctx)
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatalf("el aviso del precio se repite: %+v", again)
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis no tiene avisos del precio")
	}

	// El 10 a las 9:05: toca la de los filtros (ana) y la del huerto (luis,
	// que tiene los avisos apagados). Un aviso, una vez.
	c.set(at("2026-11-10", 9, 5))
	svc.round(ctx)
	got, _ = svc.store.takeNotices(ctx, "ana", c.now())
	if len(got) != 1 || got[0].Text != "🧰 Casa: hoy toca «Limpiar los filtros del deshumidificador». Cuando lo hagas, márcalo como hecho." {
		t.Fatalf("aviso del mantenimiento: %+v", got)
	}
	c.add(15 * time.Minute)
	svc.round(ctx)
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatalf("se repite: %+v", again)
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis tiene los avisos apagados")
	}
	if lt, _ := svc.store.tasks(ctx, "luis"); len(lt) != 1 || !lt[0].Notified.Equal(lt[0].Due) {
		t.Fatalf("la de luis queda marcada: %+v", lt)
	}

	// Hecha: la próxima, dentro de 3 meses.
	done, err := svc.markDone(ctx, "ana", filtros.ID, time.Time{})
	if err != nil || isoDay(done.Due) != "2027-02-10" || isoDay(done.Last) != "2026-11-10" || len(done.Done) != 2 {
		t.Fatalf("hecha: %+v %v", done, err)
	}
	if _, err := svc.markDone(ctx, "ana", filtros.ID, day0("2026-12-01")); !isInput(err) {
		t.Fatalf("hecha en el futuro: %v", err)
	}
	// Una fecha anterior solo va al historial.
	if old, _ := svc.markDone(ctx, "ana", filtros.ID, day0("2026-05-01")); isoDay(old.Due) != "2027-02-10" || len(old.Done) != 3 {
		t.Fatalf("hecha antes: %+v", old)
	}

	// Lecturas.
	apunta := func(owner, meter, day string, v float64, note string) (reading, bool) {
		t.Helper()
		x, replaced, err := svc.addReading(ctx, owner, readingInput{Meter: meter, Day: day0(day), Value: v, Note: note})
		if err != nil {
			t.Fatalf("lectura %s %s: %v", meter, day, err)
		}
		return x, replaced
	}
	first, _ := apunta("ana", meterPower, "2026-10-01", 1234.5, "lectura de la factura de ejemplo")
	apunta("ana", meterPower, "2026-11-01", 1480.2, "")
	if _, replaced := apunta("ana", meterPower, "2026-11-01", 1490.2, ""); !replaced {
		t.Fatal("la del mismo día no se sustituye")
	}
	apunta("ana", meterWater, "2026-11-01", 321.5, "")
	apunta("luis", meterGas, "2026-11-01", 98.7, "")
	for _, in := range []readingInput{
		{Meter: "vapor", Value: 1},
		{Meter: meterPower, Value: -1},
		{Meter: meterPower, Value: 1, Day: day0("2026-11-11")},
		{Meter: meterPower, Value: 1, Day: day0("2019-01-01")},
	} {
		if _, _, err := svc.addReading(ctx, "ana", in); !isInput(err) {
			t.Errorf("acepta %+v: %v", in, err)
		}
	}
	views, st, err := svc.meterViews(ctx, "ana")
	if err != nil || len(views) != 3 || len(views[0].Readings) != 2 || len(views[0].Periods) != 1 || !near(views[0].Periods[0].Used, 255.7) || st.gasUnit() != "m³" {
		t.Fatalf("contadores: %+v %v", views, err)
	}
	if lv, lst, _ := svc.meterViews(ctx, "luis"); len(lv[0].Readings) != 0 || len(lv[2].Readings) != 1 || lv[2].Unit != "kWh" || lst.gasUnit() != "kWh" {
		t.Fatalf("contadores de luis: %+v", lv)
	}

	// Lo de ana, ni lo ve ni lo toca luis.
	if l, _ := svc.store.tasks(ctx, "luis"); len(l) != 1 {
		t.Fatal("luis ve las tareas de ana")
	}
	if _, err := svc.store.task(ctx, "luis", filtros.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve una tarea de ana")
	}
	if _, err := svc.markDone(ctx, "luis", filtros.ID, time.Time{}); !errors.Is(err, errNotFound) {
		t.Fatal("luis da por hecha una tarea de ana")
	}
	if _, err := svc.updateTask(ctx, "luis", filtros.ID, taskInput{Name: "x", Every: 1, Unit: unitDays}); !errors.Is(err, errNotFound) {
		t.Fatal("luis cambia una tarea de ana")
	}
	if err := svc.store.deleteTask(ctx, "luis", filtros.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una tarea de ana")
	}
	if err := svc.store.deleteReading(ctx, "luis", first.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una lectura de ana")
	}
	if lines, _ := svc.todayLines(ctx, "luis"); len(lines) != 3 || strings.Contains(strings.Join(lines, " "), "deshumidificador") || !strings.Contains(lines[2], "«Regar el huerto urbano»") {
		t.Fatalf("resumen de luis: %q", lines)
	}

	// Nada de lo de ana ni de luis en claro, en ninguna tabla.
	words := []string{"caldera", "deshumidificador", "Desengrasar", "campana", "huerto", "marca de ejemplo", "factura", "1234.5", "1490.2",
		"321.5", "98.7", "muy barata", "hoy toca", "kWh", "luz", "agua", "nombre", "contador", "aviso_barata", "unidad_gas"}
	for _, tb := range []string{"tasks", "readings", "settings", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}
	noPlain(t, svc.store.db, "prices", "ana", "luis") // públicos: sin nadie

	// Se borran los avisos viejos, las lecturas de más de 5 años y los precios de más de 60 días.
	svc.store.db.Exec(ctx, `INSERT INTO notices (owner, created_at, sealed) VALUES ('ana', $1, '\x00')`, c.now().Add(-8*24*time.Hour))
	svc.store.takeNotices(ctx, "ana", c.now())
	var n int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE created_at < $1`, c.now().Add(-7*24*time.Hour)).Scan(&n)
	if n != 0 {
		t.Fatal("quedan avisos viejos")
	}
	svc.store.db.Exec(ctx, `INSERT INTO readings (owner, day, sealed) VALUES ('ana', '2020-01-01', '\x00')`)
	svc.store.savePrices(ctx, day0("2026-08-01"), c.now(), []hourPrice{{T: at("2026-08-01", 0, 0), P: 0.1}})
	c.set(at("2026-11-11", 0, 30))
	svc.round(ctx)
	svc.store.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM readings WHERE day < '2021-11-11') + (SELECT count(*) FROM prices WHERE day < '2026-09-01')`).Scan(&n)
	if n != 0 {
		t.Fatalf("quedan %d filas viejas", n)
	}
}

func TestTodayLines(t *testing.T) {
	svc, _, c := newTestService(t)
	ctx := context.Background()
	if lines, err := svc.todayLines(ctx, "ana"); err != nil || len(lines) != 0 {
		t.Fatalf("sin nada: %q %v", lines, err)
	}
	svc.addTemplate(ctx, "ana", "caldera")
	svc.addTask(ctx, "ana", taskInput{Name: "Purgar los radiadores", Every: 1, Unit: unitYears, Due: day0("2026-11-01")})
	svc.addTask(ctx, "ana", taskInput{Name: "Probar el detector", Every: 6, Unit: unitMonths, Due: day0("2026-11-12")})
	lines, _ := svc.todayLines(ctx, "ana")
	if len(lines) != 1 || lines[0] != "Mantenimiento: hoy toca «Revisión de la caldera»; atrasado: «Purgar los radiadores» (desde el 1 nov)." {
		t.Fatalf("sin precios: %q", lines)
	}
	svc.refreshPrices(ctx)
	lines, _ = svc.todayLines(ctx, "ana")
	if len(lines) != 3 || lines[0] != "Luz hoy: de 0,087 a 0,395 €/kWh (media 0,222)." || lines[1] != "Bloque más barato de 3 h: de 13 a 16 h (0,135 €/kWh de media)." {
		t.Fatalf("con precios: %q", lines)
	}
	// Por la tarde, lo mejor de lo que queda.
	c.set(at("2026-11-09", 17, 30))
	if lines, _ = svc.todayLines(ctx, "ana"); lines[1] != "Bloque más barato de 3 h: de 21 a 24 h (0,305 €/kWh de media)." {
		t.Fatalf("por la tarde: %q", lines)
	}
}
