package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// clock es un reloj de pruebas que se puede adelantar.
type clock struct{ t time.Time }

func (c *clock) now() time.Time   { return c.t }
func (c *clock) days(n int)       { c.t = c.t.AddDate(0, 0, n) }
func newClock(t time.Time) *clock { return &clock{t} }
func day(y, m, d int) time.Time   { return time.Date(y, time.Month(m), d, 19, 0, 0, 0, madrid) }

func newTestService(t *testing.T) (*service, *clock) {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	c := newClock(day(2026, 11, 3))
	return &service{store: store{pool, box}, log: slog.New(slog.DiscardHandler), now: c.now}, c
}

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	if dump == "" {
		t.Errorf("%s está vacía: la prueba no comprueba nada", table)
	}
	for _, s := range texts {
		if strings.Contains(strings.ToLower(dump), strings.ToLower(s)) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}

func names(list []item) string {
	var out []string
	for _, it := range list {
		s := it.Name
		if it.Qty != "" {
			s += " [" + it.Qty + "]"
		}
		if it.Done {
			s += " ✓"
		}
		out = append(out, s)
	}
	return strings.Join(out, " | ")
}

func TestAddMergeAndPrivacy(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.addText(ctx, "ana", "Yogur griego, 2 kg de patatas, lejía y pan (de pueblo)", sourceWeb)
	if err != nil || len(res) != 4 {
		t.Fatalf("añadir: %+v %v", res, err)
	}
	for _, r := range res {
		if r.Result != addNew || r.Item.ID == 0 {
			t.Fatalf("añadir: %+v", r)
		}
	}
	if res[0].Item.Section != "lacteos" || res[1].Item.Section != "fruta" || res[2].Item.Section != "limpieza" || res[3].Item.Note != "de pueblo" {
		t.Fatalf("secciones: %+v", res)
	}
	// Lo que ya está no se repite: se junta la cantidad o se avisa.
	res, err = svc.addText(ctx, "ana", "patatas 500 g, yogures griegos, Lejía x2", sourceAssistant)
	if err != nil || len(res) != 3 || res[0].Result != addMerged || res[0].Item.Qty != "2,5 kg" || res[1].Result != addDup || res[2].Result != addMerged || res[2].Item.Qty != "x2" {
		t.Fatalf("juntar: %+v %v", res, err)
	}
	if res, _ := svc.addText(ctx, "ana", "leche, leche", sourceAssistant); len(res) != 2 || res[0].Result != addNew || res[1].Result != addDup {
		t.Fatalf("dos veces en el mismo texto: %+v", res)
	}
	list, err := svc.store.items(ctx, "ana")
	if err != nil || names(list) != "Yogur griego | patatas [2,5 kg] | lejía [x2] | pan | leche" || list[4].Source != sourceAssistant || list[0].Source != sourceWeb {
		t.Fatalf("lista: %q %v", names(list), err)
	}
	if list[0].Added.IsZero() {
		t.Fatal("sin fecha de añadido")
	}
	// Tachado, otra vez sí se añade (hace falta más).
	if err := svc.store.setDone(ctx, "ana", list[3].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.setDone(ctx, "ana", list[3].ID, true); err != nil {
		t.Fatal("tachar dos veces:", err)
	}
	if res, _ := svc.addText(ctx, "ana", "pan", sourceWeb); len(res) != 1 || res[0].Result != addNew {
		t.Fatalf("pan tachado y otra vez: %+v", res)
	}
	if _, err := svc.addText(ctx, "ana", " , y ", sourceWeb); err != errNothing {
		t.Fatalf("nada que añadir: %v", err)
	}

	// Cambiar la sección se recuerda para la próxima vez.
	if err := svc.store.update(ctx, "ana", list[2].ID, "Lejía", "1 botella", "la de siempre", "higiene", svc.clock()); err != nil {
		t.Fatal(err)
	}
	it, _ := svc.store.item(ctx, "ana", list[2].ID)
	if it.Name != "Lejía" || it.Qty != "1 botella" || it.Note != "la de siempre" || it.Section != "higiene" {
		t.Fatalf("editar: %+v", it)
	}
	svc.store.remove(ctx, "ana", it.ID)
	if res, _ := svc.addText(ctx, "ana", "lejías", sourceWeb); res[0].Item.Section != "higiene" {
		t.Fatalf("no recuerda la sección: %+v", res)
	}
	if sec, _ := svc.store.sectionFor(ctx, "luis", "lejía"); sec != "limpieza" {
		t.Fatalf("la sección de ana vale para luis: %s", sec)
	}
	// Volver a la del diccionario olvida la preferencia.
	list, _ = svc.store.items(ctx, "ana")
	last := list[len(list)-1]
	svc.store.update(ctx, "ana", last.ID, last.Name, "", "", "limpieza", svc.clock())
	var prefs int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM section_prefs`).Scan(&prefs)
	if prefs != 0 {
		t.Fatalf("preferencias: %d", prefs)
	}
	svc.store.update(ctx, "ana", last.ID, last.Name, "", "", "higiene", svc.clock())

	// Orden de las secciones.
	if err := svc.store.setOrder(ctx, "ana", []string{"limpieza", "mascotas"}); err != nil {
		t.Fatal(err)
	}
	if o, _ := svc.store.order(ctx, "ana"); o[0] != "limpieza" || o[1] != "mascotas" || len(o) != len(sections) {
		t.Fatalf("orden: %v", o)
	}
	if o, _ := svc.store.order(ctx, "luis"); o[0] != "fruta" {
		t.Fatalf("orden de luis: %v", o)
	}

	// Al historial y nada en claro.
	svc.store.setDone(ctx, "ana", list[0].ID, true)
	if n, err := svc.store.finish(ctx, "ana", svc.clock()); err != nil || n != 2 {
		t.Fatalf("terminar: %d %v", n, err)
	}
	words := []string{"yogur", "griego", "patata", "lejía", "lejia", "pueblo", "siempre", "botella", "higiene", "limpieza", "lacteos", "mascotas", "Talia"}
	noPlain(t, svc.store.db, "items", words...)
	noPlain(t, svc.store.db, "history", words...)
	noPlain(t, svc.store.db, "section_prefs", words...)
	noPlain(t, svc.store.db, "settings", words...)

	// Nadie ve ni toca lo de otro.
	if l, _ := svc.store.items(ctx, "luis"); len(l) != 0 {
		t.Fatal("luis ve la lista de ana")
	}
	if _, err := svc.store.item(ctx, "luis", list[1].ID); err != errNotFound {
		t.Fatal("luis ve una cosa de ana")
	}
	if err := svc.store.setDone(ctx, "luis", list[1].ID, true); err != errNotFound {
		t.Fatal("luis tacha lo de ana")
	}
	if err := svc.store.update(ctx, "luis", list[1].ID, "x", "", "", "otros", svc.clock()); err != errNotFound {
		t.Fatal("luis edita lo de ana")
	}
	if err := svc.store.remove(ctx, "luis", list[1].ID); err != errNotFound {
		t.Fatal("luis borra lo de ana")
	}
	if n, _ := svc.store.finish(ctx, "luis", svc.clock()); n != 0 {
		t.Fatal("luis termina la compra de ana")
	}
	if n, _ := svc.store.clear(ctx, "luis"); n != 0 {
		t.Fatal("luis vacía la lista de ana")
	}
	if h, _ := svc.store.history(ctx, "luis"); len(h) != 0 {
		t.Fatal("luis ve el historial de ana")
	}
	if l, _ := svc.store.items(ctx, "ana"); len(l) == 0 {
		t.Fatal("la lista de ana ha desaparecido")
	}
	if n, err := svc.store.clear(ctx, "ana"); err != nil || n == 0 {
		t.Fatalf("vaciar: %d %v", n, err)
	}
}

func TestListFull(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	var list []parsed
	for i := 0; i < maxItems; i++ {
		list = append(list, parsed{Name: fmt.Sprintf("cosa %d", i)})
	}
	if _, err := svc.store.add(ctx, "ana", list, sourceWeb, svc.clock()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.addText(ctx, "ana", "una más distinta", sourceWeb); err != errFull {
		t.Fatalf("lista llena: %v", err)
	}
	if res, err := svc.addText(ctx, "ana", list[0].Name, sourceWeb); err != nil || res[0].Result != addDup {
		t.Fatalf("lo que ya está sí: %+v %v", res, err)
	}
}

// buy añade, tacha todo y termina.
func buy(t *testing.T, svc *service, owner, text string) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.addText(ctx, owner, text, sourceWeb); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.store.items(ctx, owner)
	for _, it := range list {
		svc.store.setDone(ctx, owner, it.ID, true)
	}
	if _, err := svc.store.finish(ctx, owner, svc.clock()); err != nil {
		t.Fatal(err)
	}
}

func suggNames(list []suggestion) string {
	var out []string
	for _, s := range list {
		out = append(out, s.Name)
	}
	return strings.Join(out, ", ")
}

func TestHistoryAndSuggestions(t *testing.T) {
	svc, c := newTestService(t)
	ctx := context.Background()
	buy(t, svc, "ana", "leche, huevos, yogures, lejía")
	c.days(7)
	buy(t, svc, "ana", "leche, yogures")
	c.days(7)
	buy(t, svc, "ana", "Leches, yogur, 2 kg de patatas")
	// El mismo día otra vez no cuenta como otra compra.
	buy(t, svc, "ana", "leche")
	c.days(1)
	buy(t, svc, "ana", "patatas")

	h, err := svc.store.history(ctx, "ana")
	if err != nil || len(h) != 5 {
		t.Fatalf("historial: %+v %v", h, err)
	}
	if h[0].Name != "leche" || h[0].Times != 3 || h[0].AvgDays != 7 || h[0].Intervals != 2 || !h[0].Last.Equal(time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("leche: %+v", h[0])
	}
	if h[1].Name != "yogur" || h[1].Times != 3 {
		t.Fatalf("yogur (el último nombre escrito): %+v", h[1])
	}
	if p := h[2]; p.Name != "patatas" || p.Times != 2 || p.AvgDays != 1 {
		t.Fatalf("patatas: %+v", p)
	}

	// Al día siguiente: las patatas (cada día) tocan ya; leche y yogur, aún no.
	c.days(1)
	run, usual, err := svc.suggestions(ctx, "ana", nil)
	if err != nil || suggNames(run) != "patatas" || suggNames(usual) != "leche, yogur" {
		t.Fatalf("sugerencias: %q %q %v", suggNames(run), suggNames(usual), err)
	}
	// Una semana después de la última leche: se estará acabando.
	c.days(5)
	run, usual, _ = svc.suggestions(ctx, "ana", nil)
	if suggNames(run) != "patatas, leche, yogur" || suggNames(usual) != "" {
		t.Fatalf("a la semana: %q %q", suggNames(run), suggNames(usual))
	}
	if run[1].Since != 7 || run[1].AvgDays != 7 {
		t.Fatalf("leche: %+v", run[1])
	}
	// Lo que ya está en la lista no se sugiere.
	svc.addText(ctx, "ana", "leches", sourceWeb)
	list, _ := svc.store.items(ctx, "ana")
	run, _, _ = svc.suggestions(ctx, "ana", list)
	if suggNames(run) != "patatas, yogur" {
		t.Fatalf("con leche en la lista: %q", suggNames(run))
	}
	// Nadie ve las sugerencias de otro.
	if run, usual, _ := svc.suggestions(ctx, "luis", nil); len(run)+len(usual) != 0 {
		t.Fatal("luis ve las sugerencias de ana")
	}

	// Conservación: a los 18 meses sin comprarlo, fuera; las secciones
	// elegidas para lo que ya no está, también.
	svc.store.update(ctx, "ana", list[0].ID, "leche", "", "", "despensa", svc.clock())
	tx, _ := svc.store.db.Begin(ctx)
	svc.store.setPrefTx(ctx, tx, "ana", "tinte", "higiene", svc.clock())
	tx.Commit(ctx)
	if hn, pn, err := svc.store.purge(ctx, svc.clock()); err != nil || hn != 0 || pn != 0 {
		t.Fatalf("aún nada que borrar: %d %d %v", hn, pn, err)
	}
	// Lo comprado el 3 de noviembre (huevos y lejía) cumple 18 meses antes.
	if hn, pn, err := svc.store.purge(ctx, day(2028, 5, 16)); err != nil || hn != 2 || pn != 0 {
		t.Fatalf("a los 18 meses de lo primero: %d %d %v", hn, pn, err)
	}
	h, _ = svc.store.history(ctx, "ana")
	if len(h) != 3 || h[2].Name != "patatas" {
		t.Fatalf("tras borrar: %+v", h)
	}
	if hn, pn, err := svc.store.purge(ctx, day(2028, 5, 25)); err != nil || hn != 3 || pn != 1 {
		t.Fatalf("a los 18 meses de todo: %d %d %v", hn, pn, err)
	}
	var prefs int
	svc.store.db.QueryRow(ctx, `SELECT count(*) FROM section_prefs`).Scan(&prefs)
	if prefs != 1 { // la de la leche sigue: está en la lista
		t.Fatalf("preferencias: %d", prefs)
	}
}

func TestFindByName(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.addText(ctx, "ana", "leche, leche de avena, pan de molde, pan rallado, tomates", sourceWeb)
	no := false
	if l, err := svc.findByName(ctx, "ana", "Leches", &no); err != nil || len(l) != 1 || l[0].Name != "leche" {
		t.Fatalf("exacto: %+v %v", l, err)
	}
	if l, err := svc.findByName(ctx, "ana", "avena", nil); err != nil || len(l) != 1 || l[0].Name != "leche de avena" {
		t.Fatalf("por una palabra: %+v %v", l, err)
	}
	if _, err := svc.findByName(ctx, "ana", "pan", nil); err != errAmbiguous {
		t.Fatalf("varias: %v", err)
	}
	if _, err := svc.findByName(ctx, "ana", "tomate", nil); err != nil {
		t.Fatalf("singular: %v", err)
	}
	if _, err := svc.findByName(ctx, "luis", "leche", nil); err != errNotFound {
		t.Fatal("luis encuentra lo de ana")
	}
}
