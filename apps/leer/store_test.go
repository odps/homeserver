package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFlowAndPrivacy recorre lo principal con la base de datos de pruebas y
// el servidor de páginas de ejemplo: guardar, repetir, listas, etiquetas,
// búsqueda, marcar, fallos y reintentos, que nadie vea lo de otro y que
// nada quede en claro.
func TestFlowAndPrivacy(t *testing.T) {
	svc, s, c := newTestService(t)
	ctx := context.Background()

	tom, existed, err := svc.save(ctx, "ana", s.url("/articulo/tomates?utm_source=boletin#comentarios"), []string{"huerto, #Verano"}, "web")
	if err != nil || existed || tom.State != stateOK || tom.Title != "Cómo cultivar tomates en el balcón" || tom.Site != "Huerto de Ejemplo" ||
		tom.Minutes < 1 || strings.Join(tom.Tags, "|") != "huerto|Verano" || tom.URL != s.url("/articulo/tomates") {
		t.Fatalf("guardar: %+v %v", tom, err)
	}
	blocks, err := svc.store.body(ctx, "ana", tom.ID)
	if err != nil || len(blocks) < 6 {
		t.Fatalf("texto: %d %v", len(blocks), err)
	}
	// Otra vez el mismo enlace: el mismo artículo, con la etiqueta nueva.
	again, existed, err := svc.save(ctx, "ana", s.url("/articulo/tomates"), []string{"Huerto", "balcón"}, "asistente")
	if err != nil || !existed || again.ID != tom.ID || strings.Join(again.Tags, "|") != "huerto|Verano|balcón" {
		t.Fatalf("repetido: %+v %v %v", again, existed, err)
	}
	pan, _, err := svc.save(ctx, "ana", s.url("/articulo/pan"), nil, "asistente")
	if err != nil || pan.State != stateOK || pan.Origin != "asistente" {
		t.Fatalf("pan: %+v %v", pan, err)
	}
	bicis, _, _ := svc.save(ctx, "ana", s.url("/articulo/bicis"), []string{"deporte"}, "web")
	if bicis.Minutes <= tom.Minutes {
		t.Fatalf("minutos: %d y %d", bicis.Minutes, tom.Minutes)
	}

	// Lo que no se puede leer queda guardado con su error.
	empty, _, err := svc.save(ctx, "ana", s.url("/vacia"), nil, "web")
	if err != nil || empty.State != stateError || empty.Title != "Solo un título" || empty.Err != errNoText.msg || empty.Attempts != maxAttempts {
		t.Fatalf("sin texto: %+v %v", empty, err)
	}
	s.flaky.Store(1)
	flaky, _, err := svc.save(ctx, "ana", s.url("/cambia"), nil, "web")
	if err != nil || flaky.State != stateError || flaky.Attempts != 1 || !strings.Contains(flaky.Err, "503") {
		t.Fatalf("fallo pasajero: %+v %v", flaky, err)
	}
	// Lo que no es una dirección válida no se guarda.
	for _, bad := range []string{"", "javascript:alert(1)", "https://192.168.1.1/", "file:///etc/passwd"} {
		if _, _, err := svc.save(ctx, "ana", bad, nil, "web"); !isInput(err) {
			t.Errorf("acepta %q: %v", bad, err)
		}
	}

	// Listas y cuentas.
	list, total, err := svc.store.list(ctx, "ana", filter{List: listPending})
	if err != nil || total != 5 || len(list) != 5 || list[0].ID != flaky.ID {
		t.Fatalf("pendientes: %d %v", total, err)
	}
	if _, err := svc.mark(ctx, "ana", tom.ID, flags{Archived: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.mark(ctx, "ana", pan.ID, flags{Favorite: ptr(true), Read: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	cts, _ := svc.store.counts(ctx, "ana")
	if cts != (counts{Pending: 4, Unread: 3, Favorite: 1, Archive: 1}) {
		t.Fatalf("cuentas: %+v", cts)
	}
	if a, _ := svc.store.get(ctx, "ana", tom.ID); !a.Read || !a.Archived || a.ReadAt.IsZero() {
		t.Fatalf("archivar lo da por leído: %+v", a)
	}
	if a, _ := svc.mark(ctx, "ana", pan.ID, flags{Read: ptr(false)}); a.Read || !a.ReadAt.IsZero() || !a.Favorite {
		t.Fatalf("no leído: %+v", a)
	}
	for l, want := range map[string]int{listFavorite: 1, listArchive: 1, listAll: 5, listPending: 4} {
		if _, n, _ := svc.store.list(ctx, "ana", filter{List: l}); n != want {
			t.Errorf("%s: %d", l, n)
		}
	}

	// Buscar por palabras (enteras o su comienzo, sin tildes) y por etiqueta.
	search := func(f filter) []int64 {
		t.Helper()
		list, _, err := svc.store.list(ctx, "ana", f)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, a := range list {
			ids = append(ids, a.ID)
		}
		return ids
	}
	same := func(got []int64, want ...int64) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if ids := search(filter{List: listAll, Query: "TOMATES balcon"}); !same(ids, tom.ID) {
		t.Fatalf("buscar tomates: %v", ids)
	}
	if ids := search(filter{List: listAll, Query: "masa madr"}); !same(ids, pan.ID) {
		t.Fatalf("buscar por el comienzo: %v", ids)
	}
	if ids := search(filter{List: listAll, Query: "paciencia"}); len(ids) != 3 {
		t.Fatalf("buscar en el texto: %v", ids)
	}
	if ids := search(filter{List: listPending, Query: "tomates"}); len(ids) != 0 {
		t.Fatalf("el archivado no está en pendientes: %v", ids)
	}
	if ids := search(filter{List: listAll, Tag: "VERANO"}); !same(ids, tom.ID) {
		t.Fatalf("por etiqueta: %v", ids)
	}
	if ids := search(filter{List: listAll, Query: "Rutas de Ejemplo"}); !same(ids, bicis.ID) {
		t.Fatalf("por el sitio: %v", ids)
	}
	tags, _ := svc.allTags(ctx, "ana")
	if len(tags) != 4 || tags[0].Name != "balcón" && tags[0].N != 1 {
		t.Fatalf("etiquetas: %+v", tags)
	}
	// Cambiar las etiquetas rehace las huellas.
	if _, err := svc.setTags(ctx, "ana", tom.ID, []string{"jardín"}); err != nil {
		t.Fatal(err)
	}
	if ids := search(filter{List: listAll, Tag: "verano"}); len(ids) != 0 {
		t.Fatalf("la etiqueta quitada sigue: %v", ids)
	}
	if ids := search(filter{List: listAll, Query: "jardin tomates"}); !same(ids, tom.ID) {
		t.Fatalf("etiqueta nueva y texto: %v", ids)
	}

	// Volver a guardar algo archivado lo devuelve a pendientes, sin leer.
	if a, existed, _ := svc.save(ctx, "ana", s.url("/articulo/tomates"), nil, "web"); !existed || a.Archived || a.Read {
		t.Fatalf("guardar lo archivado: %+v", a)
	}

	// Reintentos: el pasajero, a la hora; el permanente, no.
	svc.round(ctx)
	if a, _ := svc.store.get(ctx, "ana", flaky.ID); a.State != stateError || a.Attempts != 1 {
		t.Fatalf("antes de la hora: %+v", a)
	}
	c.add(61 * time.Minute)
	hits := s.hits.Load()
	svc.round(ctx)
	if a, _ := svc.store.get(ctx, "ana", flaky.ID); a.State != stateOK || a.Attempts != 0 || a.Err != "" || a.Title != "Página que se arregla" {
		t.Fatalf("reintento: %+v", a)
	}
	if s.hits.Load() != hits+1 {
		t.Fatalf("peticiones en el reintento: %d", s.hits.Load()-hits)
	}
	// «Reintentar» a mano, aunque sea permanente.
	if a, err := svc.retry(ctx, "ana", empty.ID); err != nil || a.State != stateError || a.Attempts != maxAttempts {
		t.Fatalf("reintentar: %+v %v", a, err)
	}
	// Uno que se quedó a medias (la app se paró descargando) se recoge.
	stuck, _, err := svc.store.add(ctx, "ana", meta{URL: s.url("/latin1"), Origin: "web"}, c.now())
	if err != nil {
		t.Fatal(err)
	}
	svc.round(ctx)
	if a, _ := svc.store.get(ctx, "ana", stuck.ID); a.State != stateQueued {
		t.Fatalf("recién guardado: %+v", a)
	}
	c.add(11 * time.Minute)
	svc.round(ctx)
	if a, _ := svc.store.get(ctx, "ana", stuck.ID); a.State != stateOK || a.Title != "Canción de otoño" {
		t.Fatalf("a medias: %+v", a)
	}

	// Nadie ve lo de otro.
	if _, total, _ := svc.store.list(ctx, "luis", filter{List: listAll}); total != 0 {
		t.Fatal("luis ve los artículos de ana")
	}
	if _, err := svc.store.get(ctx, "luis", tom.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis ve un artículo de ana")
	}
	if b, err := svc.store.body(ctx, "luis", tom.ID); !errors.Is(err, errNotFound) || b != nil {
		t.Fatal("luis lee el texto de ana")
	}
	if _, err := svc.mark(ctx, "luis", tom.ID, flags{Archived: ptr(true)}); !errors.Is(err, errNotFound) {
		t.Fatal("luis archiva lo de ana")
	}
	if _, err := svc.setTags(ctx, "luis", tom.ID, []string{"x"}); !errors.Is(err, errNotFound) {
		t.Fatal("luis etiqueta lo de ana")
	}
	if _, err := svc.retry(ctx, "luis", tom.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis descarga lo de ana")
	}
	if err := svc.store.delete(ctx, "luis", tom.ID); !errors.Is(err, errNotFound) {
		t.Fatal("luis borra lo de ana")
	}
	// Luis puede guardar el mismo enlace: es suyo, aparte.
	lt, existed, err := svc.save(ctx, "luis", s.url("/articulo/tomates"), nil, "web")
	if err != nil || existed || lt.ID == tom.ID {
		t.Fatalf("el mismo enlace, otra persona: %+v %v", lt, err)
	}
	if ids, _, _ := svc.store.list(ctx, "luis", filter{List: listAll, Query: "masa"}); len(ids) != 0 {
		t.Fatal("luis encuentra lo de ana")
	}
	if tags, _ := svc.allTags(ctx, "luis"); len(tags) != 0 {
		t.Fatal("luis ve las etiquetas de ana")
	}

	// Avisos y ajustes.
	if err := svc.store.setPrefs(ctx, "ana", prefs{Size: "l", Summary: true, Weekly: true}); err != nil {
		t.Fatal(err)
	}
	svc.round(ctx)
	svc.round(ctx) // una vez por día
	n, err := svc.store.takeNotices(ctx, "ana", c.now())
	if err != nil || len(n) != 1 || n[0].Kind != "leer" || !strings.Contains(n[0].Text, "artículos pendientes") {
		t.Fatalf("aviso semanal: %+v %v", n, err)
	}
	if n, _ := svc.store.takeNotices(ctx, "ana", c.now()); len(n) != 0 {
		t.Fatal("el aviso se entrega dos veces")
	}
	if n, _ := svc.store.takeNotices(ctx, "luis", c.now()); len(n) != 0 {
		t.Fatal("luis recibe los avisos de ana")
	}
	if p, _ := svc.store.prefs(ctx, "luis"); p != defaultPrefs() {
		t.Fatalf("ajustes por defecto: %+v", p)
	}

	// Nada de lo de ana en claro, en ninguna tabla.
	svc.store.addNotice(ctx, "ana", "📚 Tienes 3 artículos pendientes en Leer después.", c.now())
	words := []string{"tomates", "balcón", "balcon", "Huerto de Ejemplo", "masa madre", "Ana Ejemplo", "127.0.0.1", "articulo/", "https://",
		"huerto", "Verano", "jardín", "deporte", "paciencia", "Solo un título", "Canción", "pendientes"}
	for _, tb := range []string{"articles", "notices"} {
		noPlain(t, svc.store.db, tb, words...)
	}

	// Borrar se lleva el texto.
	if err := svc.store.delete(ctx, "ana", tom.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.get(ctx, "ana", tom.ID); !errors.Is(err, errNotFound) {
		t.Fatal("sigue tras borrarlo")
	}

	// Los avisos viejos se borran.
	svc.store.addNotice(ctx, "ana", "viejo", c.now().Add(-8*24*time.Hour))
	svc.store.takeNotices(ctx, "ana", c.now())
	var left int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM notices WHERE created_at < $1`, c.now().Add(-7*24*time.Hour)).Scan(&left)
	if left != 0 {
		t.Fatalf("quedan %d avisos viejos", left)
	}
}

func ptr[T any](v T) *T { return &v }

func TestLimit(t *testing.T) {
	svc, s, _ := newTestService(t)
	ctx := context.Background()
	// Llenar hasta el máximo de golpe, sin descargar.
	if _, err := svc.store.db.Exec(ctx, `INSERT INTO articles (owner, url_token, sealed)
		SELECT 'ana', g, '\x00'::bytea FROM generate_series(1, $1) g`, maxArticles); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.save(ctx, "ana", s.url("/articulo/pan"), nil, "web"); !errors.Is(err, errFull) || !isInput(err) {
		t.Fatalf("al máximo: %v", err)
	}
}

func TestTodayLines(t *testing.T) {
	svc, s, c := newTestService(t)
	ctx := context.Background()
	if lines, err := svc.todayLines(ctx, "ana"); err != nil || len(lines) != 0 {
		t.Fatalf("sin nada: %v %v", lines, err)
	}
	empty, _, _ := svc.save(ctx, "ana", s.url("/vacia"), nil, "web")
	if lines, _ := svc.todayLines(ctx, "ana"); len(lines) != 1 || lines[0] != "1 artículo pendiente." {
		t.Fatalf("solo uno sin texto: %q", lines)
	}
	tom, _, _ := svc.save(ctx, "ana", s.url("/articulo/tomates"), nil, "web")
	svc.save(ctx, "ana", s.url("/articulo/pan"), nil, "web")
	svc.save(ctx, "ana", s.url("/articulo/bicis"), nil, "web")
	svc.mark(ctx, "ana", empty.ID, flags{Archived: ptr(true)})
	seen := map[string]bool{}
	for range 4 {
		lines, err := svc.todayLines(ctx, "ana")
		if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], "3 artículos pendientes · sugerencia: «") || !strings.HasSuffix(lines[0], " min)") {
			t.Fatalf("línea: %q %v", lines, err)
		}
		seen[lines[0]] = true
		c.add(24 * time.Hour)
	}
	// Cada día otro de los cortos.
	if len(seen) < 2 {
		t.Fatalf("siempre la misma sugerencia: %v", seen)
	}
	// Leídos no cuentan; apagado no sale.
	svc.mark(ctx, "ana", tom.ID, flags{Read: ptr(true)})
	if lines, _ := svc.todayLines(ctx, "ana"); !strings.HasPrefix(lines[0], "2 artículos pendientes") {
		t.Fatalf("sin los leídos: %q", lines)
	}
	svc.store.setPrefs(ctx, "ana", prefs{Size: "m", Summary: false})
	if lines, _ := svc.todayLines(ctx, "ana"); len(lines) != 0 {
		t.Fatalf("apagado: %q", lines)
	}
}
