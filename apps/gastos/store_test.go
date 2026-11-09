package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas:
// lo de serie, apuntar con reglas, presupuestos y su aviso, suscripciones y
// sus avisos, el resumen de la mañana, que nadie vea lo de otro y que nada
// quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, c := newTestService(t)
	ctx := context.Background()

	// Lo de serie, una sola vez (también con otra instancia del servicio).
	cs, err := svc.loadCats(ctx, "ana")
	if err != nil || len(cs.list) != len(defaultCats) {
		t.Fatalf("categorías de serie: %d %v", len(cs.list), err)
	}
	other := &service{store: svc.store, log: svc.log, now: svc.now}
	if again, _ := other.loadCats(ctx, "ana"); len(again.list) != len(defaultCats) {
		t.Fatalf("se repiten las de serie: %d", len(again.list))
	}
	if cs.list[len(cs.list)-1].Key != keyOther || cs.list[len(cs.list)-2].Key != keyIncome {
		t.Fatal("Ingresos y Otros van al final")
	}
	rules, _ := svc.store.rules(ctx, "ana")
	if len(rules) < 40 {
		t.Fatalf("reglas de serie: %d", len(rules))
	}
	comida, ocio, otros, ingresos, subsCat := cs.byKey["comida"], cs.byKey["ocio"], cs.byKey[keyOther], cs.byKey[keyIncome], cs.byKey[keySubs]

	apunta := func(in movInput) movement {
		t.Helper()
		in.Origin = originWeb
		m, err := svc.addMovement(ctx, "ana", in)
		if err != nil {
			t.Fatalf("apuntar %+v: %v", in, err)
		}
		return m
	}
	m1 := apunta(movInput{Day: day("2026-11-02"), Amount: 54.3, Concept: "Compra Mercadona centro", Account: "Tarjeta de ejemplo", Note: "nota secreta de prueba"})
	if m1.CatID != comida.ID || m1.Kind != kindExpense {
		t.Fatalf("regla de serie: %+v", m1)
	}
	if m := apunta(movInput{Kind: "ingreso", Day: day("2026-11-01"), Amount: 1500, Concept: "Nómina de la empresa"}); m.CatID != ingresos.ID {
		t.Fatalf("ingreso: %+v", m)
	}
	m3 := apunta(movInput{Day: day("2026-11-03"), Amount: 19.999, Concept: "Regalo raro"})
	if m3.CatID != otros.ID || m3.Amount != 20 {
		t.Fatalf("sin regla, a Otros y en céntimos: %+v", m3)
	}
	if m := apunta(movInput{Day: day("2026-11-04"), Amount: 9, Concept: "Mercadona", CatID: ocio.ID}); m.CatID != ocio.ID {
		t.Fatal("la categoría elegida manda sobre las reglas")
	}
	lc, _ := svc.loadCats(ctx, "luis")
	for _, bad := range []movInput{
		{Day: day("2026-11-02"), Concept: "sin importe"},
		{Day: day("2026-11-02"), Amount: -3, Concept: "negativo"},
		{Day: day("2026-11-10"), Amount: 3, Concept: "futuro"},
		{Day: day("2026-11-02"), Amount: 3},
		{Day: day("2026-11-02"), Amount: 3, Concept: "x", Kind: "regalo"},
		{Amount: 3, Concept: "sin fecha"},
		{Day: day("2026-11-02"), Amount: 3, Concept: "de otro", CatID: lc.list[0].ID},
	} {
		if _, err := svc.addMovement(ctx, "ana", bad); !isInput(err) {
			t.Errorf("acepta %+v: %v", bad, err)
		}
	}
	// La base de datos tampoco deja usar una categoría de otro.
	if _, err := svc.store.addMovement(ctx, "ana", movement{Day: day("2026-11-02"), CatID: lc.list[0].ID, movData: movData{Kind: kindExpense, Amount: 1, Concept: "x"}}); !errors.Is(err, errNotFound) {
		t.Fatalf("categoría de luis en la base de datos: %v", err)
	}

	// Presupuesto: avisa una vez al pasarse en el mes.
	if err := svc.setBudgets(ctx, "ana", map[int64]float64{comida.ID: 100}); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(n) != 0 {
		t.Fatalf("avisa sin pasarse: %+v", n)
	}
	apunta(movInput{Day: day("2026-11-05"), Amount: 60, Concept: "Supermercado del barrio"})
	apunta(movInput{Day: day("2026-11-06"), Amount: 5, Concept: "Panadería"})
	got, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 1 || got[0].Kind != "gastos" || got[0].Text != "⚠️ Has pasado el presupuesto de Comida de noviembre: llevas 114,30 € de 100,00 € (114 %)." {
		t.Fatalf("aviso de presupuesto: %+v %v", got, err)
	}
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatal("los avisos se entregan una sola vez")
	}
	// Si cambia el presupuesto, puede volver a avisar.
	svc.setBudgets(ctx, "ana", map[int64]float64{comida.ID: 110})
	if got, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(got) != 1 || !strings.Contains(got[0].Text, "de 110,00 €") {
		t.Fatalf("tras cambiar el presupuesto: %+v", got)
	}

	// Reglas nuevas, aplicadas a lo ya apuntado en Otros.
	n, err := svc.addRule(ctx, "ana", "regalo raro", ocio.ID, true)
	if err != nil || n != 1 {
		t.Fatalf("regla aplicada: %d %v", n, err)
	}
	if m, _ := svc.store.movement(ctx, "ana", m3.ID); m.CatID != ocio.ID {
		t.Fatal("no se ha aplicado")
	}
	if _, err := svc.addRule(ctx, "ana", "Regalo  RARO", comida.ID, false); !isInput(err) {
		t.Fatalf("regla repetida: %v", err)
	}
	if _, err := svc.addRule(ctx, "ana", "algo", lc.list[0].ID, false); !isInput(err) {
		t.Fatalf("regla con una categoría de luis: %v", err)
	}
	if _, err := svc.addCategory(ctx, "ana", "comida", 0); !isInput(err) {
		t.Fatalf("categoría repetida: %v", err)
	}
	mascotas, err := svc.addCategory(ctx, "ana", "Mascotas", 30)
	if err != nil {
		t.Fatal(err)
	}

	// Suscripciones.
	sub, err := svc.addSub(ctx, "ana", subInput{Name: "Plataforma de ejemplo", Amount: 12.99, Period: "mensual", Next: day("2026-11-11"), Notice: 3})
	if err != nil || sub.CatID != subsCat.ID || sub.Anchor != 11 {
		t.Fatalf("suscripción: %+v %v", sub, err)
	}
	if _, err := svc.addSub(ctx, "ana", subInput{Name: "Seguro de ejemplo", Amount: 240, Period: "anual", Next: day("2027-01-31"), Notice: -1, CatID: mascotas}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.addSub(ctx, "ana", subInput{Name: "Pendiente", Amount: 30, Period: "mensual", Next: day("2026-11-02"), Notice: 1}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []subInput{
		{Amount: 1, Period: "mensual", Next: day("2026-12-01")},
		{Name: "x", Period: "mensual", Next: day("2026-12-01")},
		{Name: "x", Amount: 1, Period: "diaria", Next: day("2026-12-01")},
		{Name: "x", Amount: 1, Period: "mensual"},
		{Name: "x", Amount: 1, Period: "mensual", Next: day("2026-12-01"), Notice: 90},
	} {
		if _, err := svc.addSub(ctx, "ana", bad); !isInput(err) {
			t.Errorf("acepta %+v: %v", bad, err)
		}
	}
	subs, _ := svc.store.subscriptions(ctx, "ana")
	if mo, yr := subsCost(subs); len(subs) != 3 || !near(mo, 62.99) || !near(yr, 755.88) {
		t.Fatalf("coste: %d %v %v", len(subs), mo, yr)
	}
	if subs[0].Name != "Pendiente" {
		t.Fatal("por el próximo cobro")
	}

	// Aviso del cobro: a partir de 3 días antes, una vez.
	c.set(time.Date(2026, 11, 7, 12, 0, 0, 0, madrid))
	if n, _ := svc.subNotices(ctx, "ana"); n != 0 {
		t.Fatal("avisa antes de tiempo")
	}
	c.set(testNow())
	if n, err := svc.subNotices(ctx, "ana"); err != nil || n != 1 {
		t.Fatalf("aviso del cobro: %d %v", n, err)
	}
	if n, _ := svc.subNotices(ctx, "ana"); n != 0 {
		t.Fatal("repite el aviso")
	}
	got, _ = svc.store.takeNotices(ctx, "ana", c.now())
	if len(got) != 1 || got[0].Text != "🔔 Plataforma de ejemplo: cobro de 12,99 € el miércoles 11." {
		t.Fatalf("aviso: %+v", got)
	}

	// El resumen de la mañana.
	lines, err := svc.todayLines(ctx, "ana")
	want := []string{
		"Cobro el miércoles 11: Plataforma de ejemplo 12,99 €.",
		"Sin registrar: Pendiente 30,00 € (era el 2 nov).",
		"Presupuesto pasado en noviembre: Comida 119,30 € de 110,00 €.",
		"Este mes llevas 148,30 € en gastos (119,30 € de 140,00 € presupuestados).",
	}
	if err != nil || strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("resumen:\n%s\n%v", strings.Join(lines, "\n"), err)
	}

	// Registrar el cobro: el gasto (hoy, porque aún no tocaba) y el siguiente.
	m, x, err := svc.chargeSub(ctx, "ana", sub.ID)
	if err != nil || !m.Day.Equal(day("2026-11-09")) || m.Amount != 12.99 || m.CatID != subsCat.ID || m.Origin != originSub || m.SubID != sub.ID ||
		!x.Next.Equal(day("2026-12-11")) {
		t.Fatalf("cobro: %+v %+v %v", m, x, err)
	}
	if x, _ := svc.store.subscription(ctx, "ana", sub.ID); !x.Next.Equal(day("2026-12-11")) || !x.Notified.IsZero() {
		t.Fatalf("siguiente cobro: %+v", x)
	}
	// El pendiente se apunta con su fecha.
	if m, _, err := svc.chargeSub(ctx, "ana", subs[0].ID); err != nil || !m.Day.Equal(day("2026-11-02")) {
		t.Fatalf("cobro atrasado: %+v %v", m, err)
	}

	// Lo de ana, ni lo ve ni lo toca luis.
	if l, _ := svc.store.movements(ctx, "luis", time.Time{}, time.Time{}); len(l) != 0 {
		t.Fatal("luis ve los movimientos de ana")
	}
	if _, err := svc.store.movement(ctx, "luis", m1.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve un movimiento de ana")
	}
	if _, err := svc.updateMovement(ctx, "luis", m1.ID, movInput{Day: day("2026-11-02"), Amount: 1, Concept: "robado"}); !errors.Is(err, errNotFound) {
		t.Fatalf("luis cambia un movimiento de ana: %v", err)
	}
	if err := svc.store.deleteMovement(ctx, "luis", m1.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra un movimiento de ana")
	}
	if _, err := svc.store.subscription(ctx, "luis", sub.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve una suscripción de ana")
	}
	if _, _, err := svc.chargeSub(ctx, "luis", sub.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis registra un cobro de ana")
	}
	if err := svc.store.deleteSub(ctx, "luis", sub.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una suscripción de ana")
	}
	if err := svc.store.deleteCategory(ctx, "luis", comida.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una categoría de ana")
	}
	if err := svc.updateCategory(ctx, "luis", comida.ID, "robada", 1); !errors.Is(err, errNotFound) {
		t.Fatal("luis cambia una categoría de ana")
	}
	if err := svc.setBudgets(ctx, "luis", map[int64]float64{comida.ID: 1}); !errors.Is(err, errNotFound) {
		t.Fatal("luis cambia un presupuesto de ana")
	}
	if err := svc.store.deleteRule(ctx, "luis", rules[0].ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una regla de ana")
	}
	if l, _ := svc.store.subscriptions(ctx, "luis"); len(l) != 0 {
		t.Fatal("luis ve las suscripciones de ana")
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis recibe los avisos de ana")
	}
	if lines, err := svc.todayLines(ctx, "luis"); err != nil || len(lines) != 0 {
		t.Fatalf("el resumen de luis: %v %v", lines, err)
	}
	if lines, err := svc.todayLines(ctx, "nadie"); err != nil || len(lines) != 0 {
		t.Fatalf("el resumen de quien no usa la app: %v %v", lines, err)
	}
	if l, _ := svc.store.owners(ctx); strings.Join(l, ",") != "ana,luis" {
		t.Fatalf("el resumen da de alta: %v", l)
	}

	// Nada de lo de ana en claro, en ninguna tabla.
	words := []string{"Mercadona", "Nómina", "Regalo raro", "nota secreta", "Tarjeta de ejemplo", "Plataforma de ejemplo", "Seguro de ejemplo",
		"Pendiente", "Comida", "Vivienda", "Mascotas", "Supermercado", "Panadería", "netflix", "presupuesto", "mensual", "anual", "gasto", "ingreso",
		"importe", "concepto", "suscripcion"}
	for _, tb := range []string{"owners", "categories", "rules", "movements", "subscriptions", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}

	// Borrar una categoría: sus reglas se van y sus movimientos y
	// suscripciones se quedan sin categoría.
	if err := svc.store.deleteCategory(ctx, "ana", ocio.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := svc.store.movement(ctx, "ana", m3.ID); m.CatID != 0 {
		t.Fatalf("movimiento de una categoría borrada: %+v", m)
	}
	if rs, _ := svc.store.rules(ctx, "ana"); len(rs) != len(rules)-len(defaultRules["ocio"]) {
		t.Fatalf("reglas: %d, antes %d (+1 nueva −%d de Ocio)", len(rs), len(rules), len(defaultRules["ocio"]))
	}

	// Lo viejo se borra: avisos de más de 7 días y movimientos de más de 5 años.
	svc.store.addNotice(ctx, "ana", "viejo", c.now().Add(-8*24*time.Hour))
	if _, err := svc.store.addMovement(ctx, "ana", movement{Day: day("2021-10-31"), movData: movData{Kind: kindExpense, Amount: 1, Concept: "muy viejo"}}); err != nil {
		t.Fatal(err)
	}
	keep, _ := svc.store.addMovement(ctx, "ana", movement{Day: day("2021-11-01"), movData: movData{Kind: kindExpense, Amount: 1, Concept: "viejo pero no tanto"}})
	if n, err := svc.store.purge(ctx, c.now()); err != nil || n != 1 {
		t.Fatalf("borrar lo viejo: %d %v", n, err)
	}
	if _, err := svc.store.movement(ctx, "ana", keep); err != nil {
		t.Fatal("borra de más")
	}
	var notices int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE created_at < $1`, c.now().Add(-keepNotice)).Scan(&notices)
	if notices != 0 {
		t.Fatal("quedan avisos viejos")
	}
}

// TestRound comprueba la tarea periódica: de madrugada no avisa de los
// cobros; desde las 9, sí.
func TestRound(t *testing.T) {
	svc, c := newTestService(t)
	ctx := context.Background()
	if _, err := svc.addSub(ctx, "ana", subInput{Name: "Plataforma de ejemplo", Amount: 9.99, Period: "mensual", Next: day("2026-11-10"), Notice: 1}); err != nil {
		t.Fatal(err)
	}
	c.set(time.Date(2026, 11, 9, 7, 0, 0, 0, madrid))
	svc.round(ctx)
	if n, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(n) != 0 {
		t.Fatalf("avisa de madrugada: %+v", n)
	}
	c.set(time.Date(2026, 11, 9, 9, 5, 0, 0, madrid))
	svc.round(ctx)
	if n, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(n) != 1 || n[0].Text != "🔔 Plataforma de ejemplo: cobro de 9,99 € mañana (10 nov)." {
		t.Fatalf("aviso: %+v", n)
	}
}
