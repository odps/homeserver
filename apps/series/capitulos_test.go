package main

import (
	"context"
	"testing"
	"time"
)

func sampleShow(t *testing.T, id int64) *showData {
	t.Helper()
	c := &clock{t: testNow()}
	tv := newTestTV(newFakeTV(t), c)
	d, err := tv.show(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestAirTimes(t *testing.T) {
	d := sampleShow(t, 1001)
	today := dayOf(testNow())
	// El domingo 8 a las 21:00 en Nueva York es el lunes 9 a las 3:00 en Madrid.
	e2, _ := d.find(2, 2)
	if !e2.day().Equal(day("2026-11-09")) || whenText(e2, today) != "hoy, 03:00" || !e2.aired(testNow()) {
		t.Fatalf("2x02: %v %q", e2.day(), whenText(e2, today))
	}
	e3, _ := d.find(2, 3)
	if whenText(e3, today) != "jue 12 nov, 03:00" || e3.aired(testNow()) {
		t.Fatalf("2x03: %q", whenText(e3, today))
	}
	// En verano (CEST) también: 21:00 en Nueva York (EDT) son las 3:00.
	summer := episode{Season: 1, Number: 1, Date: "2026-07-05", At: time.Date(2026, 7, 6, 1, 0, 0, 0, time.UTC)}
	if summer.At.In(madrid).Format("15:04") != "03:00" || !summer.day().Equal(day("2026-07-06")) {
		t.Fatalf("verano: %v", summer.At.In(madrid))
	}
	// Sin hora vale la fecha, y ya ha salido desde que empieza el día.
	plat := episode{Season: 2, Number: 1, Date: "2026-11-09"}
	if !plat.aired(at(0, 1)) || plat.aired(at(0, 0).Add(-time.Minute)) || whenText(plat, today) != "hoy" {
		t.Fatalf("sin hora: %q", whenText(plat, today))
	}
	if e4, _ := d.find(2, 4); e4.aired(testNow()) || whenText(e4, today) != "sin fecha" {
		t.Fatal("sin fecha no ha salido")
	}
	for d, want := range map[string]string{"2026-11-09": "hoy", "2026-11-10": "mañana", "2026-11-08": "ayer", "2026-11-13": "viernes 13 nov",
		"2026-11-20": "20 nov", "2027-01-15": "15 ene 2027"} {
		if got := dayName(day(d), today); got != want {
			t.Errorf("%s: %q", d, got)
		}
	}
	if longDay(day("2027-01-15")) != "viernes 15 de enero" {
		t.Error(longDay(day("2027-01-15")))
	}
	// A las 9:00 o a su hora, si es más tarde.
	if !noticeDue(e2).Equal(at(9, 0)) || !noticeDue(episode{Date: "2026-11-09", At: at(21, 30)}).Equal(at(21, 30)) || !noticeDue(plat).Equal(at(9, 0)) {
		t.Fatal("hora del aviso")
	}
}

func TestProgressAndMark(t *testing.T) {
	d := sampleShow(t, 1001)
	now := testNow()
	w := idSet{}
	p := progressOf(d, w, now)
	// Emitidos: 1x01–1x03, 2x01 y 2x02 (a las 3:00 de hoy).
	if p.Aired != 5 || p.Total != 7 || p.Left != 5 || p.Next.ID != 50001 || p.Upcoming.ID != 50103 || p.CaughtUp {
		t.Fatalf("de cero: %+v", p)
	}
	if n, ok := mark(d, w, markUpTo, 50003, 0, true, now); !ok || n != 3 {
		t.Fatalf("hasta el 1x03: %d", n)
	}
	p = progressOf(d, w, now)
	if p.Watched != 3 || p.Next.ID != 50101 || !p.NextIsNew || p.Last.ID != 50003 {
		t.Fatalf("tras la temporada 1: %+v", p)
	}
	// La temporada 2: solo lo emitido.
	if n, ok := mark(d, w, markSeason, 0, 2, true, now); !ok || n != 2 || w[50103] {
		t.Fatalf("temporada 2: %d %v", n, w)
	}
	p = progressOf(d, w, now)
	if !p.CaughtUp || p.Next != nil || p.AllDone {
		t.Fatalf("al día: %+v", p)
	}
	// Desmarcar uno y la temporada.
	if n, _ := mark(d, w, markOne, 50002, 0, false, now); n != 1 || progressOf(d, w, now).Next.ID != 50002 {
		t.Fatal("desmarcar uno")
	}
	if n, _ := mark(d, w, markSeason, 0, 1, false, now); n != 2 || len(w) != 2 {
		t.Fatalf("desmarcar la temporada: %v", w)
	}
	// Marcar uno suelto que aún no ha salido se puede; uno de otra serie, no.
	if _, ok := mark(d, w, markOne, 50103, 0, true, now); !ok || !w[50103] {
		t.Fatal("uno futuro")
	}
	if _, ok := mark(d, w, markOne, 70001, 0, true, now); ok {
		t.Fatal("de otra serie")
	}
	if _, ok := mark(d, w, markSeason, 0, 9, true, now); ok {
		t.Fatal("temporada que no existe")
	}

	// Estados automáticos.
	if autoState(statePending, d, idSet{50001: true}, now) != stateWatching {
		t.Fatal("quiero verla → viendo")
	}
	fin := sampleShow(t, 1003)
	all := idSet{70001: true, 70002: true}
	if autoState(stateWatching, fin, all, now) != stateDone || autoState(stateAbandoned, fin, all, now) != stateAbandoned {
		t.Fatal("vista entera → terminada")
	}
	if autoState(stateWatching, d, idSet{50001: true, 50002: true, 50003: true, 50101: true, 50102: true}, now) != stateWatching {
		t.Fatal("en emisión y al día sigue en viendo")
	}

	// La semana: el 2x02 (hoy) y el 2x03 (jueves).
	week := between(d, dayOf(now), dayOf(now).Add(6*dayUnit))
	if len(week) != 2 || week[0].ID != 50102 || week[1].ID != 50103 {
		t.Fatalf("semana: %+v", week)
	}
}

func TestParseState(t *testing.T) {
	for in, want := range map[string]string{
		"quiero ver": statePending, "Pendiente": statePending, "viendo": stateWatching, "Jugando": stateWatching, "en pausa": statePaused,
		"vista": stateDone, "Terminado": stateDone, "abandonada": stateAbandoned, "": "", "regular": "",
	} {
		if got := parseState(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if stateLabel(kindGame, stateWatching) != "Jugando" || stateLabel(kindFilm, stateDone) != "Vista" || stateLabel(kindShow, statePending) != "Quiero verla" {
		t.Error("etiquetas")
	}
	if showStatus("Running") != "En emisión" || showStatus("Ended") != "Terminada" {
		t.Error("situación")
	}
}
