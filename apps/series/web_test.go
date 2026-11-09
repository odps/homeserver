package main

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"homeserver/platform/app"
)

// sampleView son unas series de ejemplo (inventadas: el repositorio es
// público) para ver las páginas sin base de datos.
func sampleView(t *testing.T) *view {
	now := at(12, 0)
	v := &view{now: now, today: dayOf(now)}
	add := func(id, tvmaze int64, state string, poster bool, watched ...int64) {
		d := sampleShow(t, tvmaze)
		w := setOf(watched)
		v.shows = append(v.shows, showView{follow: follow{ID: id, ShowID: id, followData: followData{State: state, Watched: watched, Rating: int(id % 6), Note: "nota de ejemplo"}},
			Show: d, Prog: progressOf(d, w, now), HasPoster: poster, watched: w})
	}
	add(1, 1001, stateWatching, true, 50001, 50002, 50003)
	add(2, 1002, stateWatching, false, 60001, 60002)
	add(3, 1003, stateDone, false, 70001, 70002)
	// Una larga, con muchas temporadas, en pausa.
	long := sampleShow(t, 1001)
	long.Name = "Una serie con un nombre muy largo para ver cómo se parte en el móvil"
	long.Episodes = nil
	for s := 1; s <= 4; s++ {
		for n := 1; n <= 10; n++ {
			long.Episodes = append(long.Episodes, episode{ID: int64(s*100 + n), Season: s, Number: n, Name: "Capítulo", Date: isoDay(day("2020-01-01").AddDate(s, 0, n*7))})
		}
	}
	lw := idSet{101: true, 102: true}
	v.shows = append(v.shows, showView{follow: follow{ID: 4, followData: followData{State: statePaused}}, Show: long, Prog: progressOf(long, lw, now), watched: lw})
	return v
}

func sampleItems() []item {
	return []item{
		{ID: 1, Kind: kindFilm, itemData: itemData{Title: "Peli de Ejemplo", Year: 2024, Platform: "Cine", State: stateDone, Rating: 4, Day: "2026-10-30", Note: "buena"}},
		{ID: 2, Kind: kindFilm, itemData: itemData{Title: "Otra Peli Inventada", State: statePending, Origin: "asistente"}},
		{ID: 3, Kind: kindFilm, itemData: itemData{Title: "Peli a medias", State: stateWatching, Platform: "Plataforma"}},
	}
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con SERIES_PREVIEW_DIR escribe las páginas y lo
// estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	c := &clock{t: at(12, 0)}
	tv := newTVmaze(defaultURLs, false)
	tv.now = c.now
	w := &web{svc: &service{tv: tv, log: slog.New(slog.DiscardHandler), now: c.now}}
	who := app.Identity{User: "ana", Name: "Ana"}
	v := sampleView(t)
	items := sampleItems()

	now := w.nowData(v, []item{items[2], {ID: 9, Kind: kindGame, itemData: itemData{Title: "Juego de Ejemplo", State: stateWatching, Platform: "Consola"}}})
	now.Name = "Ana"
	empty := w.nowData(&view{now: v.now, today: v.today}, nil)
	empty.Empty = true

	shows := w.showsData(who, v)
	search := w.showsData(who, v)
	search.Query, search.Searched = "serie", true
	search.Results = []resultRow{
		{TVmazeID: 1001, Name: "Serie de Ejemplo", Year: "2025", Status: "En emisión", Network: "Canal de Ejemplo", Genres: "Drama", HaveID: 1},
		{TVmazeID: 2002, Name: "Serie Nueva", Year: "2026", Status: "En desarrollo"},
	}
	noResults := w.showsData(who, &view{})
	noResults.Query, noResults.Searched = "nada", true
	tv.fail("límite de peticiones: se espera antes de volver a pedir", true)
	failing := w.showsData(who, &view{})

	sv, _ := v.show(1)
	ficha := w.showPageData(who, v, sv)
	sv3, _ := v.show(3)
	fin := w.showPageData(who, v, sv3)
	fin.Confirm = true
	sv4, _ := v.show(4)
	long := w.showPageData(who, v, sv4)

	pelis := itemsPageData(who, kindFilm, append([]item(nil), items...), v.today)
	pelisErr := itemsPageData(who, kindFilm, nil, v.today)
	pelisErr.Error, pelisErr.Form = "Ese año no es válido.", itemForm{Title: "X", Year: "1500"}
	juegos := itemsPageData(who, kindGame, []item{{ID: 9, Kind: kindGame, itemData: itemData{Title: "Juego de Ejemplo", State: stateWatching}}}, v.today)
	editItem := itemPageData(who, kindFilm, items[1], nil)
	confirmItem := itemPageData(who, kindFilm, items[0], nil)
	confirmItem.Confirm = true

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"ahora", "ahora.html", now},
		{"ahora-vacio", "ahora.html", empty},
		{"series", "series.html", shows},
		{"series-busqueda", "series.html", search},
		{"series-sin-resultados", "series.html", noResults},
		{"series-tvmaze-falla", "series.html", failing},
		{"serie", "serie.html", ficha},
		{"serie-terminada", "serie.html", fin},
		{"serie-larga", "serie.html", long},
		{"pelis", "items.html", pelis},
		{"pelis-error", "items.html", pelisErr},
		{"juegos", "items.html", juegos},
		{"peli", "item.html", editItem},
		{"peli-borrar", "item.html", confirmItem},
	}
	wants := map[string][]string{
		"ahora": {"Te toca ver", "Serie de Ejemplo", "2x01", "temporada nueva", `name="accion" value="uno:50101"`, `name="desde" value="ahora"`,
			"Esta semana", "Hoy · lunes 9 de noviembre", "03:00", "Jueves 12 de noviembre", "estreno de la temporada 2", "sin hora",
			`src="/series/cartel/1"`, "Pelis y juegos en curso", "Juego de Ejemplo", "Datos de series", "TVmaze"},
		"ahora-vacio":           {"Aún no sigues nada", "Ninguna de tus series"},
		"series":                {"Viendo", "En pausa", "Terminada", "3 de 5 vistos", "Te toca el 2x01", `href="/series/serie/4"`, `action="/series/buscar"`, "★★☆☆☆"},
		"series-busqueda":       {"Resultados para «serie»", "Ya la sigues", `name="tvmaze" value="2002"`, "Seguir"},
		"series-sin-resultados": {"No ha salido nada"},
		"series-tvmaze-falla":   {"TVmaze no responde ahora", "se vuelve a intentar en 5 min", "No sigues ninguna serie"},
		"serie": {"Serie de Ejemplo", "2025–", "En emisión", "Canal de Ejemplo", "Drama, Mystery", "3</strong> de <span class=\"tnum\">5</span>", "(7 anunciados)",
			"Te toca:", "2x01 «La vuelta»", "Temporada 1", "Temporada 2", `id="t2" open`, "✓ Visto", `value="quitar:50001"`, `value="hasta:50102"`,
			"Desmarcar la temporada", "Marcar la temporada como vista", "sin fecha aún", `href="https://www.tvmaze.com/shows/1001/serie-de-ejemplo"`,
			`<option value="viendo" selected>`, "nota de ejemplo", "Una serie inventada", `src="/series/cartel/1"`},
		"serie-terminada": {"Serie Terminada de Prueba", "2023", "Terminada", "Vas al día", "¿Dejar de seguir", `name="confirmar" value="1"`},
		"serie-larga":     {"Temporada 4", "2</strong> de <span class=\"tnum\">40</span>"},
		"pelis":           {"Vista", "Quiero verla", "Viéndola", "Peli de Ejemplo", "(2024)", "Cine · 30 oct · buena", "con Talia", `href="/series/pelis/2"`, `action="/series/pelis"`},
		"pelis-error":     {"Ese año no es válido", `value="1500"`, " open", "Ninguna peli apuntada"},
		"juegos":          {"Jugando", "Juego de Ejemplo", "PS5, Switch"},
		"peli":            {"Otra Peli Inventada", "Apuntado con Talia", `action="/series/pelis/2"`, `<option value="pendiente" selected>`, "Borrar…"},
		"peli-borrar":     {"¿Borrar «Peli de Ejemplo»?", `<option value="4" selected>`, `value="2026-10-30"`},
	}
	nots := map[string][]string{
		"ahora-vacio":     {"Te toca ver", "Al día"},
		"series-busqueda": {`name="tvmaze" value="1001"`},
		"serie-terminada": {"Te toca:", `src="/series/cartel/3"`},
		"peli":            {"¿Borrar"},
		"series":          {"Abandonada"},
	}

	dir := os.Getenv("SERIES_PREVIEW_DIR")
	base := "/series"
	if dir != "" {
		base = "."
		os.MkdirAll(dir, 0o755)
	}
	for _, p := range cases {
		t.Run(p.file, func(t *testing.T) {
			d := p.data
			d.Version, d.Base = "v0.0.0-test", base
			var buf bytes.Buffer
			if err := pages.ExecuteTemplate(&buf, p.tmpl, d); err != nil {
				t.Fatalf("%s: %v", p.tmpl, err)
			}
			out := buf.String()
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script", "NaN", "static.tvmaze.com", "api.tvmaze.com"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s contiene %q", p.file, bad)
				}
			}
			if regexp.MustCompile(`\son[a-z]+=`).MatchString(out) {
				t.Errorf("%s tiene un manejador de eventos", p.file)
			}
			if !strings.Contains(out, `aria-current="page"`) {
				t.Errorf("%s: falta la pestaña activa", p.file)
			}
			if dir == "" {
				for _, want := range wants[p.file] {
					if !strings.Contains(out, want) {
						t.Errorf("%s: falta %q", p.file, want)
					}
				}
				for _, not := range nots[p.file] {
					if strings.Contains(out, not) {
						t.Errorf("%s: sobra %q", p.file, not)
					}
				}
			} else if err := os.WriteFile(filepath.Join(dir, p.file+".html"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
	if dir != "" {
		static, _ := fs.Sub(webFS, "web/static")
		os.MkdirAll(filepath.Join(dir, "static"), 0o755)
		for _, n := range []string{"app.css", "icon.svg"} {
			b, _ := fs.ReadFile(static, n)
			os.WriteFile(filepath.Join(dir, "static", n), b, 0o644)
		}
		// Un cartel de ejemplo para las páginas.
		os.MkdirAll(filepath.Join(dir, "cartel"), 0o755)
		os.WriteFile(filepath.Join(dir, "cartel", "1"), testPNG(), 0o644)
	}
}
