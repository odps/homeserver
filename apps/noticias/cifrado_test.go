package main

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/db"
	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// noPlain falla si algún byte de la tabla contiene el texto en claro.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	if err := pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, s := range texts {
		if strings.Contains(dump, s) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}

// Lo guardado antes del cifrado (migraciones 001 y 002) se cifra al
// actualizar y se sigue leyendo.
func TestSealExistingData(t *testing.T) {
	ctx := context.Background()
	schema, pool := dbtest.Schema(t)
	before := fstest.MapFS{}
	all, _ := fs.Glob(migrations, "migrations/*.sql")
	for _, p := range all {
		if b, _ := fs.ReadFile(migrations, p); p < "migrations/003" {
			before[p] = &fstest.MapFile{Data: b}
		}
	}
	if err := db.Migrate(ctx, pool, schema, before, "migrations"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO prefs (username) VALUES ('ana')`,
		`INSERT INTO topics (username, name, query, position) VALUES ('ana', 'Salud mental', 'ansiedad terapia', 1),
			('ana', 'Golang', 'golang', 2), ('ana', 'golang', '', 3)`,
		`INSERT INTO own_feeds (username, topic_id, url) SELECT 'ana', id, 'https://mi.blog/privado.xml' FROM topics WHERE name = 'Salud mental'`,
		`INSERT INTO digests (username, day, content, summary) VALUES ('ana', '2026-11-01',
			'[{"titulo": "Salud mental", "noticias": [{"titulo": "Nueva terapia", "enlace": "https://x.es/1", "fuente": "X", "publicada": "2026-11-01T08:00:00Z"}]}]',
			'Resumen reservado')`,
		`INSERT INTO items (feed, source, link, title, published) VALUES ('` + topicFeedURL("ansiedad terapia") + `', 'X', 'https://x.es/2', 'Titular', now()),
			('https://viejo.es/rss', 'Y', 'https://y.es/1', 'De un feed quitado', now())`,
		`INSERT INTO feeds (url, title, error) VALUES ('https://mi.blog/privado.xml', 'Mi blog privado', ''),
			('` + topicFeedURL("ansiedad terapia") + `', '', 'Get "` + topicFeedURL("ansiedad terapia") + `": timeout')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	box, _ := seal.Load("")
	if err := db.MigrateSteps(ctx, pool, schema, migrations, "migrations", steps(box)); err != nil {
		t.Fatal(err)
	}
	st := store{pool, box}
	topics, err := st.topics(ctx, "ana")
	if err != nil || len(topics) != 3 || topics[0].Name != "Salud mental" || topics[0].Query != "ansiedad terapia" ||
		len(topics[0].Feeds) != 1 || topics[0].Feeds[0].URL != "https://mi.blog/privado.xml" || topics[2].Name != "golang" {
		t.Fatalf("intereses: %+v %v", topics, err)
	}
	urls, err := st.feedURLs(ctx)
	if err != nil || !slices.Contains(urls, topicFeedURL("ansiedad terapia")) || !slices.Contains(urls, "https://mi.blog/privado.xml") {
		t.Fatalf("feeds a leer: %v %v", urls, err)
	}
	d, err := st.digest(ctx, "ana", "2026-11-01")
	if err != nil || d.Summary != "Resumen reservado" || len(d.Sections) != 1 || d.Sections[0].Items[0].Title != "Nueva terapia" {
		t.Fatalf("digest: %+v %v", d, err)
	}
	// El nombre sigue siendo único, ahora sin mayúsculas ni tildes.
	if _, err := st.addTopic(ctx, "ana", "SALUD Mental", ""); !errors.Is(err, errTopicExists) {
		t.Fatalf("interés repetido: %v", err)
	}
	if _, err := st.addTopic(ctx, "luis", "Salud mental", ""); err != nil {
		t.Fatalf("el mismo nombre en otra cuenta: %v", err)
	}
	if err := st.updateTopic(ctx, "ana", topics[1].ID, "salud méntal", ""); !errors.Is(err, errTopicExists) {
		t.Fatalf("renombrar a uno que ya existe: %v", err)
	}
	if err := st.addOwnFeed(ctx, "ana", topics[1].ID, "https://mi.blog/privado.xml"); err != nil {
		t.Fatal(err)
	}
	if topics, _ = st.topics(ctx, "ana"); len(topics[0].Feeds) != 0 || len(topics[1].Feeds) != 1 {
		t.Fatalf("el mismo feed otra vez lo mueve de interés: %+v", topics)
	}
	// Noticias y estado de los feeds, por huella.
	q := topicFeedURL("ansiedad terapia")
	if its, err := st.itemsSince(ctx, []string{q}, time.Now().Add(-time.Hour)); err != nil || len(its) != 1 || its[0].Feed != q {
		t.Fatalf("noticias del interés: %+v %v", its, err)
	}
	states, err := st.feedStates(ctx, []string{q, "https://mi.blog/privado.xml"})
	if err != nil || states["https://mi.blog/privado.xml"].Title != "Mi blog privado" || !strings.Contains(states[q].Error, "timeout") {
		t.Fatalf("estado de los feeds: %+v %v", states, err)
	}
	st.feedChecked(ctx, "https://mi.blog/privado.xml", "", nil)
	if states, _ := st.feedStates(ctx, []string{"https://mi.blog/privado.xml"}); states["https://mi.blog/privado.xml"].Title != "Mi blog privado" {
		t.Fatalf("pierde el título: %+v", states)
	}
	noPlain(t, pool, "items", "ansiedad", "viejo.es")
	noPlain(t, pool, "feeds", "ansiedad", "mi.blog", "Mi blog")
	// Lo de feeds que ya no lee nadie se borra.
	if err := st.dropOtherFeeds(ctx, urls); err != nil {
		t.Fatal(err)
	}
	var left int
	pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&left)
	if left != 1 {
		t.Fatalf("quedan %d noticias, quiero 1", left)
	}
	noPlain(t, pool, "topics", "Salud", "ansiedad", "olang")
	noPlain(t, pool, "own_feeds", "mi.blog")
	noPlain(t, pool, "digests", "Resumen reservado", "terapia", "x.es")
}
