package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas y
// TVmaze falso: seguir series, marcar vistos, los avisos, la puesta al día,
// las pelis y los juegos, que nadie vea lo de otro y que nada quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, f, c := newTestService(t)
	ctx := context.Background()

	ej, existed, err := svc.followShow(ctx, "ana", 1001, stateWatching)
	if err != nil || existed || ej.State != stateWatching {
		t.Fatalf("seguir: %+v %v %v", ej, existed, err)
	}
	if again, existed, err := svc.followShow(ctx, "ana", 1001, ""); err != nil || !existed || again.ID != ej.ID {
		t.Fatalf("seguirla otra vez: %+v %v", again, err)
	}
	otra, _, err := svc.followShow(ctx, "ana", 1002, "")
	if err != nil || otra.State != statePending {
		t.Fatalf("seguir sin estado: %+v %v", otra, err)
	}
	fin, _, err := svc.followShow(ctx, "ana", 1003, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.followShow(ctx, "ana", 4040, ""); !isInput(err) {
		t.Fatalf("la que no existe: %v", err)
	}
	if _, _, err := svc.followShow(ctx, "ana", 1001, "regular"); !isInput(err) {
		t.Fatalf("estado raro: %v", err)
	}
	if f.count("/shows/1001") != 1 {
		t.Fatalf("pide la serie %d veces", f.count("/shows/1001"))
	}
	// Cartel: el de TVmaze sí; el de otro sitio, no.
	if img, err := svc.store.poster(ctx, "ana", ej.ID); err != nil || len(img) < 100 {
		t.Fatalf("cartel: %v", err)
	}
	if _, err := svc.store.poster(ctx, "ana", fin.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("cartel de fuera: %v", err)
	}

	// Marcar: hasta el 2x01 (con los anteriores).
	e21 := int64(50101)
	if _, n, err := svc.markWatched(ctx, "ana", ej.ID, markUpTo, e21, 0, true); err != nil || n != 4 {
		t.Fatalf("hasta el 2x01: %d %v", n, err)
	}
	if _, _, err := svc.markWatched(ctx, "ana", ej.ID, markOne, 70001, 0, true); !isInput(err) {
		t.Fatalf("capítulo de otra serie: %v", err)
	}
	// La miniserie entera: pasa sola a terminada.
	if got, n, err := svc.markWatched(ctx, "ana", fin.ID, markSeason, 0, 1, true); err != nil || n != 2 || got.State != stateDone {
		t.Fatalf("miniserie: %+v %d %v", got, n, err)
	}
	if _, err := svc.setMeta(ctx, "ana", fin.ID, stateDone, 4, "nota secreta de ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setMeta(ctx, "ana", fin.ID, stateDone, 9, ""); !isInput(err) {
		t.Fatalf("valoración de 9: %v", err)
	}
	v, err := svc.load(ctx, "ana")
	if err != nil || len(v.shows) != 3 {
		t.Fatalf("cargar: %v", err)
	}
	if sv, _ := v.show(ej.ID); sv.Prog.Next == nil || sv.Prog.Next.ID != 50102 || sv.Prog.Watched != 4 || !sv.HasPoster {
		t.Fatalf("progreso: %+v", sv.Prog)
	}

	// Avisos: antes de las 9:00, nada.
	if n, err := svc.checkNotices(ctx); err != nil || n != 0 {
		t.Fatalf("a las 8:30: %d %v", n, err)
	}
	// luis la sigue a las 9:05: lo que ya se ha avisado hoy no le llega.
	c.set(at(9, 5))
	luisEj, _, err := svc.followShow(ctx, "luis", 1001, stateWatching)
	if err != nil || luisEj.ShowID != ej.ShowID || f.count("/shows/1001") != 1 {
		t.Fatalf("luis sigue la misma: %+v %v", luisEj, err)
	}
	if n, err := svc.checkNotices(ctx); err != nil || n != 2 {
		t.Fatalf("a las 9:05: %d %v", n, err)
	}
	if n, _ := svc.checkNotices(ctx); n != 0 {
		t.Fatal("repite avisos")
	}
	got, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(got) != 2 || got[0].Kind != "series" {
		t.Fatalf("avisos: %+v %v", got, err)
	}
	texts := got[0].Text + "\n" + got[1].Text
	for _, want := range []string{"📺 Ya ha salido el 2x02 de «Serie de Ejemplo» «El domingo» (hoy a las 03:00).",
		"🎬 Vuelve «Otra Serie Inventada»: hoy empieza la temporada 2 (2x01 «Tres»)."} {
		if !strings.Contains(texts, want) {
			t.Errorf("falta el aviso %q en %q", want, texts)
		}
	}
	if again, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(again) != 0 {
		t.Fatal("los avisos se entregan una sola vez")
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatalf("luis recibe avisos: %+v", n)
	}

	// El resumen de la mañana: lo que sale hoy.
	lines, err := svc.todayLines(ctx, "ana")
	if err != nil || len(lines) != 2 || lines[0] != "Otra Serie Inventada 2x01 «Tres» · estreno de la temporada 2" || lines[1] != "Serie de Ejemplo 2x02 «El domingo», 03:00" {
		t.Fatalf("líneas: %q %v", lines, err)
	}
	if lines, _ := svc.todayLines(ctx, "nadie"); len(lines) != 0 {
		t.Fatalf("sin series, sin líneas: %q", lines)
	}

	// Puesta al día: solo se pide lo que TVmaze dice que ha cambiado. La
	// otra serie anuncia su temporada 3: se avisa una vez.
	f.edit(1002, func(m map[string]any) {
		m["updated"] = 1795000000
		emb := m["_embedded"].(map[string]any)
		emb["episodes"] = append(emb["episodes"].([]any), map[string]any{"id": 60201, "name": "Cuatro", "season": 3, "number": 1, "type": "regular",
			"airdate": "2027-01-15", "airtime": "", "airstamp": "2027-01-15T12:00:00+00:00", "runtime": 30})
	})
	if err := svc.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("/shows/1002") != 2 || f.count("/shows/1001") != 1 || f.count("/updates/shows") != 1 {
		t.Fatalf("pide de más o de menos: %v", f.calls)
	}
	if n, _ := svc.checkNotices(ctx); n != 1 {
		t.Fatalf("anuncio de temporada: %d", n)
	}
	got, _ = svc.store.takeNotices(ctx, "ana", c.now())
	if len(got) != 1 || got[0].Text != "📅 «Otra Serie Inventada» vuelve el viernes 15 de enero con la temporada 3." {
		t.Fatalf("anuncio: %+v", got)
	}
	if n, _ := svc.checkNotices(ctx); n != 0 {
		t.Fatal("repite el anuncio")
	}
	// Abandonada, no avisa.
	if _, err := svc.setMeta(ctx, "ana", otra.ID, stateAbandoned, 0, ""); err != nil {
		t.Fatal(err)
	}
	f.edit(1002, func(m map[string]any) {
		m["updated"] = 1796000000
		emb := m["_embedded"].(map[string]any)
		emb["episodes"] = append(emb["episodes"].([]any), map[string]any{"id": 60301, "name": "Cinco", "season": 4, "number": 1, "type": "regular",
			"airdate": "2028-01-15", "airtime": "", "airstamp": "2028-01-15T12:00:00+00:00", "runtime": 30})
	})
	if err := svc.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.checkNotices(ctx); n != 0 {
		t.Fatal("avisa de una abandonada")
	}

	// Pelis y juegos.
	peli, existed, err := svc.addItem(ctx, "ana", kindFilm, itemInput{Title: "Peli de Prueba", Year: 2024, Platform: "Filmin", State: stateDone, Rating: 5, Note: "me encantó"}, true)
	if err != nil || existed || peli.Day != "2026-11-09" {
		t.Fatalf("peli: %+v %v", peli, err)
	}
	// Con el mismo título (sin tildes ni mayúsculas) se pone al día.
	again, existed, err := svc.addItem(ctx, "ana", kindFilm, itemInput{Title: "peli de PRUEBA", Rating: 3}, true)
	if err != nil || !existed || again.ID != peli.ID || again.Rating != 3 || again.Platform != "Filmin" || again.State != stateDone {
		t.Fatalf("poner al día: %+v %v", again, err)
	}
	if _, _, err := svc.addItem(ctx, "ana", kindGame, itemInput{Title: "Juego Inventado", Platform: "Consola", State: stateWatching}, false); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []itemInput{{}, {Title: "x", Year: 1500}, {Title: "x", Rating: 6}, {Title: "x", State: "raro"}, {Title: "x", Day: "2026-12-31"}, {Title: "x", Day: "ayer"}} {
		if _, _, err := svc.addItem(ctx, "ana", kindFilm, bad, false); !isInput(err) {
			t.Errorf("acepta %+v: %v", bad, err)
		}
	}
	if _, _, err := svc.addItem(ctx, "ana", "libro", itemInput{Title: "x"}, false); !isInput(err) {
		t.Error("tipo raro")
	}

	// Lo de ana, ni lo ve ni lo toca luis.
	if _, err := svc.store.follow(ctx, "luis", ej.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve una serie de ana")
	}
	if _, _, err := svc.markWatched(ctx, "luis", ej.ID, markOne, 50102, 0, true); !errors.Is(err, errNotFound) {
		t.Fatalf("luis marca en una de ana: %v", err)
	}
	if _, err := svc.setMeta(ctx, "luis", ej.ID, stateAbandoned, 0, ""); !errors.Is(err, errNotFound) {
		t.Fatal("luis cambia una de ana")
	}
	if err := svc.unfollow(ctx, "luis", fin.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis quita una de ana")
	}
	if _, err := svc.store.poster(ctx, "luis", ej.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve un cartel por el id de ana")
	}
	if _, err := svc.store.poster(ctx, "luis", luisEj.ID); err != nil {
		t.Fatalf("el cartel de luis: %v", err)
	}
	if l, _ := svc.store.items(ctx, "luis", kindFilm); len(l) != 0 {
		t.Fatal("luis ve las pelis de ana")
	}
	if _, err := svc.updateItem(ctx, "luis", peli.ID, itemInput{Title: "robada"}); !errors.Is(err, errNotFound) {
		t.Fatal("luis cambia una peli de ana")
	}
	if err := svc.store.deleteItem(ctx, "luis", peli.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra una peli de ana")
	}
	if v, _ := svc.load(ctx, "luis"); len(v.shows) != 1 || v.shows[0].Prog.Watched != 0 {
		t.Fatal("luis ve lo que ha visto ana")
	}

	// Nada de lo de cada persona en claro, en ninguna tabla.
	words := []string{"Serie de Ejemplo", "Otra Serie", "Terminada de Prueba", "Canal de Ejemplo", "Plataforma de Ejemplo", "El domingo",
		"inventada para las pruebas", "tvmaze.com", "nota secreta", "Peli de Prueba", "Filmin", "me encantó", "Juego Inventado", "Consola",
		"Vuelve", "temporada", "viendo", "terminado", "\"estado\"", "50102", "1001"}
	for _, tb := range []string{"shows", "follows", "items", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}
	noPlain(t, svc.store.db, "posters", "1001", "JFIF")

	// Dejar de seguir: si nadie más la sigue, se borra la serie (y su cartel).
	if err := svc.unfollow(ctx, "ana", fin.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.unfollow(ctx, "ana", ej.ID); err != nil {
		t.Fatal(err)
	}
	var shows, posters int
	svc.store.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM shows), (SELECT count(*) FROM posters)`).Scan(&shows, &posters)
	if shows != 2 || posters != 1 {
		t.Fatalf("quedan %d series y %d carteles", shows, posters)
	}
	if n, err := svc.store.gcShows(ctx); err != nil || len(n) != 0 {
		t.Fatalf("limpieza: %v %v", n, err)
	}
}
