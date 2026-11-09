package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas:
// crear hábitos, marcar, el diario, los avisos, que nadie vea lo de otro y
// que nada quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, c := newTestService(t)
	ctx := context.Background()
	today := dayOf(testNow())

	med, err := svc.createHabit(ctx, "ana", habitInput{Name: "Meditar", Emoji: "🧘", Remind: "09:00"})
	if err != nil {
		t.Fatal(err)
	}
	water, err := svc.createHabit(ctx, "ana", habitInput{Name: "Beber agua", Emoji: "💧", Kind: kindAmount, Target: 8, Unit: "vasos", Remind: "10:00"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.createHabit(ctx, "ana", habitInput{Name: "Salir a correr", Freq: freq{Kind: freqWeekly, Times: 3}, Remind: "18:00"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.createHabit(ctx, "ana", habitInput{Name: "  meditar "}); !isInput(err) || !strings.Contains(err.Error(), "Ya tienes") {
		t.Fatalf("repetido: %v", err)
	}
	if _, err := svc.createHabit(ctx, "ana", habitInput{Name: "Agua", Kind: kindAmount}); !isInput(err) {
		t.Fatalf("sin objetivo: %v", err)
	}
	if _, err := svc.createHabit(ctx, "luis", habitInput{Name: "Meditar"}); err != nil {
		t.Fatalf("luis puede tener uno con el mismo nombre: %v", err)
	}

	// Marcar: hoy, días de antes y nunca el futuro.
	for _, n := range []int{1, 2, 3} {
		if err := svc.setAmount(ctx, "ana", med, ago(n), 1, "web"); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.setAmount(ctx, "ana", water, today, 3, "web"); err != nil {
		t.Fatal(err)
	}
	if err := svc.setAmount(ctx, "ana", med, today.Add(dayUnit), 1, "web"); !isInput(err) {
		t.Fatalf("mañana: %v", err)
	}
	if err := svc.setAmount(ctx, "ana", water, today, -2, "web"); !isInput(err) {
		t.Fatalf("negativa: %v", err)
	}
	v, err := svc.load(ctx, "ana")
	if err != nil || len(v.habits) != 3 {
		t.Fatalf("cargar: %v %v", v, err)
	}
	if st := v.stats(med); st.Current != 3 || st.DoneToday {
		t.Fatalf("racha: %+v", st)
	}
	if st := v.stats(water); st.Today != 3 || st.DoneToday {
		t.Fatalf("agua: %+v", st)
	}
	// Quitar una marca.
	if err := svc.setAmount(ctx, "ana", med, ago(2), 0, "web"); err != nil {
		t.Fatal(err)
	}
	if m, _ := svc.store.habitMarks(ctx, "ana", med.ID); len(m) != 2 {
		t.Fatalf("marcas: %v", m)
	}
	svc.setAmount(ctx, "ana", med, ago(2), 1, "web")

	// Editar y archivar.
	edited, err := svc.editHabit(ctx, "ana", run.ID, habitInput{Name: "Correr", Freq: freq{Kind: freqWeekly, Times: 2}, Remind: "18:00"})
	if err != nil || edited.Freq.Times != 2 {
		t.Fatalf("editar: %+v %v", edited, err)
	}
	if _, err := svc.editHabit(ctx, "ana", run.ID, habitInput{Name: "Meditar"}); !isInput(err) {
		t.Fatalf("editar a un nombre que ya existe: %v", err)
	}

	// El diario.
	sleep := 7.5
	if err := svc.saveEntry(ctx, "ana", ago(1), entryData{Mood: 4, Energy: 3, Sleep: &sleep, Note: "Una nota inventada sobre el proyecto secreto",
		Tags: []string{"trabajo, familia"}, Origin: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.saveEntry(ctx, "ana", ago(1), entryData{Mood: 9}); !isInput(err) {
		t.Fatalf("ánimo raro: %v", err)
	}
	if err := svc.saveEntry(ctx, "ana", today.Add(dayUnit), entryData{Mood: 3}); !isInput(err) {
		t.Fatalf("diario del futuro: %v", err)
	}
	e, ok, err := svc.store.entry(ctx, "ana", ago(1))
	if err != nil || !ok || e.Mood != 4 || *e.Sleep != 7.5 || len(e.Tags) != 2 || !strings.Contains(e.Note, "proyecto secreto") {
		t.Fatalf("entrada: %+v %v %v", e, ok, err)
	}
	// Vaciarla la borra.
	svc.saveEntry(ctx, "ana", ago(5), entryData{Mood: 2})
	if err := svc.saveEntry(ctx, "ana", ago(5), entryData{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.store.entry(ctx, "ana", ago(5)); ok {
		t.Fatal("una entrada vacía se borra")
	}
	if err := svc.store.saveSettings(ctx, "ana", settingsData{Nudge: true, NudgeTime: "21:30"}); err != nil {
		t.Fatal(err)
	}

	// Avisos. A las 9:05, Meditar (no está hecho hoy); el agua aún no.
	c.set(at(9, 5))
	if n, err := svc.remind(ctx); err != nil || n != 1 {
		t.Fatalf("avisos de las 9: %d %v", n, err)
	}
	if n, _ := svc.remind(ctx); n != 0 {
		t.Fatal("el mismo aviso dos veces")
	}
	// A las 10:30, el agua (3 de 8). La pasarela los recoge cada minuto.
	c.set(at(10, 30))
	if n, _ := svc.remind(ctx); n != 1 {
		t.Fatalf("aviso del agua: %d", n)
	}
	got, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 2 || got[0].Kind != "habitos" || got[0].Text != "⏰ 🧘 Meditar: aún no lo has marcado hoy. Racha: 3 días." ||
		got[1].Text != "⏰ 💧 Beber agua: llevas 3 de 8 vasos hoy." {
		t.Fatalf("avisos: %+v %v", got, err)
	}
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatal("los avisos se entregan una sola vez")
	}
	// A las 18:10, Correr: se hace antes y ya no avisa.
	svc.setAmount(ctx, "ana", run, today, 1, "web")
	svc.setAmount(ctx, "ana", run, ago(1), 1, "web")
	c.set(at(18, 10))
	if n, _ := svc.remind(ctx); n != 0 {
		t.Fatalf("aviso de algo ya cumplido: %d", n)
	}
	// El del diario a las 21:30, porque hoy no hay nada escrito.
	c.set(at(21, 40))
	if n, _ := svc.remind(ctx); n != 1 {
		t.Fatalf("aviso del diario: %d", n)
	}
	// Uno que no se recoge en 3 horas ya no se manda.
	svc.store.addNotice(ctx, "ana", "atrasado", at(17, 0))
	got, err = svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[0].Text, "📓 ") {
		t.Fatalf("aviso del diario: %+v %v", got, err)
	}
	// Al día siguiente vuelven a tocar, pero fuera de su ventana no salen
	// (el servidor estaba parado a las 9).
	c.set(at(9, 0).Add(24*time.Hour + 3*time.Hour))
	if n, _ := svc.remind(ctx); n != 0 {
		t.Fatalf("fuera de la ventana de dos horas: %d", n)
	}
	c.set(at(9, 30).Add(48 * time.Hour))
	if n, _ := svc.remind(ctx); n != 1 {
		t.Fatalf("otro día, otra vez: %d", n)
	}
	// Archivado, no avisa.
	if err := svc.store.setArchived(ctx, "ana", med.ID, true); err != nil {
		t.Fatal(err)
	}
	c.set(at(9, 30).Add(72 * time.Hour))
	if n, _ := svc.remind(ctx); n != 0 {
		t.Fatalf("archivado: %d", n)
	}
	svc.store.setArchived(ctx, "ana", med.ID, false)
	c.set(testNow())

	// Lo de ana, ni lo ve ni lo toca luis.
	if l, _ := svc.store.habits(ctx, "luis"); len(l) != 1 || l[0].Name != "Meditar" || l[0].ID == med.ID {
		t.Fatalf("luis ve: %+v", l)
	}
	if _, err := svc.store.habit(ctx, "luis", med.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve un hábito de ana")
	}
	if err := svc.store.setMark(ctx, "luis", med.ID, today, 1, "web"); !errors.Is(err, errNotFound) {
		t.Fatalf("luis marca un hábito de ana: %v", err)
	}
	if err := svc.store.setMark(ctx, "luis", med.ID, ago(1), 0, "web"); !errors.Is(err, errNotFound) {
		t.Fatalf("luis desmarca un hábito de ana: %v", err)
	}
	if m, _ := svc.store.habitMarks(ctx, "ana", med.ID); len(m) != 3 {
		t.Fatalf("luis ha cambiado las marcas de ana: %v", m)
	}
	if m, _ := svc.store.marks(ctx, "luis"); len(m) != 0 {
		t.Fatal("luis ve marcas de ana")
	}
	if err := svc.store.updateHabit(ctx, "luis", habit{ID: water.ID, habitData: habitData{Name: "Robado", Kind: kindYesNo}}); !errors.Is(err, errNotFound) {
		t.Fatalf("luis edita un hábito de ana: %v", err)
	}
	if err := svc.store.setArchived(ctx, "luis", water.ID, true); !errors.Is(err, errNotFound) {
		t.Fatal("luis archiva un hábito de ana")
	}
	if err := svc.store.deleteHabit(ctx, "luis", water.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra un hábito de ana")
	}
	if l, _ := svc.store.entries(ctx, "luis", ago(30), today); len(l) != 0 {
		t.Fatal("luis lee el diario de ana")
	}
	svc.store.deleteEntry(ctx, "luis", ago(1))
	if _, ok, _ := svc.store.entry(ctx, "ana", ago(1)); !ok {
		t.Fatal("luis borra el diario de ana")
	}
	if p, _ := svc.store.settings(ctx, "luis"); p.Nudge {
		t.Fatal("luis tiene los ajustes de ana")
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis recibe los avisos de ana")
	}
	if lines, err := svc.todayLines(ctx, "nadie"); err != nil || len(lines) != 0 {
		t.Fatalf("el resumen de quien no tiene nada: %v %v", lines, err)
	}

	// Nada de lo de ana en claro, en ninguna tabla.
	words := []string{"Meditar", "Beber agua", "agua", "vasos", "correr", "proyecto secreto", "nota inventada", "trabajo", "familia",
		"09:00", "10:00", "21:30", "Racha", "diario", "\"animo\"", "cantidad", "si_no"}
	for _, tb := range []string{"habits", "checkins", "journal", "settings", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}

	// Borrar un hábito se lleva sus marcas.
	if err := svc.store.deleteHabit(ctx, "ana", med.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM checkins WHERE habit_id = $1`, med.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("quedan %d marcas del hábito borrado", left)
	}

	// Los avisos viejos se borran.
	svc.store.addNotice(ctx, "ana", "viejo", c.now().Add(-8*24*time.Hour))
	svc.store.takeNotices(ctx, "ana", c.now())
	var n int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE created_at < $1`, c.now().Add(-keepNotice)).Scan(&n)
	if n != 0 {
		t.Fatalf("avisos viejos guardados: %d", n)
	}
}

func TestTodayLines(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	today := dayOf(testNow())
	if lines, _ := svc.todayLines(ctx, "ana"); len(lines) != 0 {
		t.Fatalf("sin hábitos, sin líneas: %v", lines)
	}
	mkh := func(in habitInput) habit {
		t.Helper()
		h, err := svc.createHabit(ctx, "ana", in)
		if err != nil {
			t.Fatal(err)
		}
		// Como si lo hubiera creado hace un mes.
		svc.store.db.Exec(ctx, `UPDATE habits SET created_at = $2 WHERE id = $1`, h.ID, testNow().Add(-30*24*time.Hour))
		h.Created = testNow().Add(-30 * 24 * time.Hour)
		return h
	}
	read := mkh(habitInput{Name: "Leer", Emoji: "📖", Kind: kindAmount, Target: 20, Unit: "min"})
	med := mkh(habitInput{Name: "Meditar"})
	run := mkh(habitInput{Name: "Correr", Freq: freq{Kind: freqWeekly, Times: 3}})
	mkh(habitInput{Name: "Estirar", Freq: freq{Kind: freqDays, Days: []int{1, 3}}}) // hoy (jueves) no toca
	old := mkh(habitInput{Name: "Archivado"})
	svc.store.setArchived(ctx, "ana", old.ID, true)
	for _, n := range []int{1, 2, 3, 4} {
		svc.setAmount(ctx, "ana", read, ago(n), 25, "web")
	}
	svc.setAmount(ctx, "ana", med, ago(1), 1, "web")
	svc.setAmount(ctx, "ana", med, today, 1, "web")
	svc.setAmount(ctx, "ana", run, ago(2), 1, "web")
	lines, err := svc.todayLines(ctx, "ana")
	if err != nil || len(lines) != 3 ||
		lines[0] != "Hoy: 📖 Leer (20 min) · Meditar ✓ · Correr (1 de 3 esta semana)." ||
		lines[1] != "Ayer: 2 de 3 hechos." ||
		lines[2] != "Rachas: 📖 Leer 4 días · Meditar 2 días." {
		t.Fatalf("líneas: %q %v", lines, err)
	}
}
