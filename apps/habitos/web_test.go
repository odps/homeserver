package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// sample son hábitos y un diario de ejemplo (genéricos: el repositorio es
// público) para ver las páginas sin base de datos.
func sample() ([]habit, map[int64]marks, []entry) {
	water := mk(1, "Beber agua", kindAmount, 8, freq{Kind: freqDaily}, 120)
	water.Emoji, water.Unit, water.Remind = "💧", "vasos", "10:00"
	read := mk(2, "Leer", kindAmount, 20, freq{Kind: freqDaily}, 90)
	read.Emoji, read.Unit = "📖", "min"
	med := mk(3, "Meditar", kindYesNo, 0, freq{Kind: freqDaily}, 60)
	med.Emoji, med.Remind = "🧘", "08:30"
	stretch := mk(4, "Estirar la espalda con un nombre bastante largo para ver cómo se parte", kindYesNo, 0, freq{Kind: freqDays, Days: []int{1, 3, 5}}, 100)
	run := mk(5, "Correr", kindYesNo, 0, freq{Kind: freqWeekly, Times: 3}, 110)
	run.Emoji = "🏃"
	old := mk(6, "Sin azúcar", kindYesNo, 0, freq{Kind: freqDaily}, 200)
	old.Archived = true
	habits := []habit{water, read, med, stretch, run, old}

	all := map[int64]marks{}
	for _, h := range habits {
		all[h.ID] = marks{}
	}
	for n := 1; n <= 120; n++ {
		d := isoDay(ago(n))
		switch {
		case n%7 != 3:
			all[1][d] = 8
		default:
			all[1][d] = 5
		}
		if n <= 90 && n%5 != 0 {
			all[2][d] = 25
		}
		if n <= 14 || n > 20 && n <= 60 && n%3 != 0 {
			all[3][d] = 1
		}
		if n%2 == 0 {
			all[4][d] = 1
		}
		if n%3 == 1 {
			all[5][d] = 1
		}
		if n > 150 || n%2 == 1 {
			all[6][d] = 1
		}
	}
	all[1][isoDay(ago(0))] = 3
	all[3][isoDay(ago(0))] = 1

	f := func(v float64) *float64 { return &v }
	var entries []entry
	for n := 40; n >= 0; n-- {
		if n%6 == 4 {
			continue
		}
		e := entry{Day: ago(n), Updated: ago(n).Add(21 * time.Hour), entryData: entryData{Mood: 2 + n%4, Energy: 1 + (n+2)%5, Sleep: f(5.5 + float64(n%5)*0.75)}}
		if n%3 == 0 {
			e.Note = "Una nota de ejemplo del día, con un par de líneas.\nY una segunda línea."
			e.Tags = []string{"trabajo", "paseo"}
		}
		if n == 1 {
			e.Energy = 0
		}
		entries = append(entries, e)
	}
	return habits, all, entries
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con HABITOS_PREVIEW_DIR escribe las páginas y
// lo estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	habits, all, entries := sample()
	today := dayOf(testNow())
	dir := os.Getenv("HABITOS_PREVIEW_DIR")
	base := "/habitos"
	if dir != "" {
		base = "."
		os.MkdirAll(dir, 0o755)
	}

	hoy := func(day time.Time) pageData {
		d := pageData{Name: "Ana", Tab: "hoy", Day: navOf(day, today), Journal: &journalState{Written: day.Before(today), Today: day.Equal(today)}}
		for _, h := range habits {
			if h.Archived {
				continue
			}
			row := rowOf(h, all[h.ID], today, day)
			row.Base, row.DayISO = base, isoDay(day)
			if row.Scheduled || row.Done {
				d.Items = append(d.Items, row)
			} else {
				d.Others = append(d.Others, row)
			}
		}
		return d
	}
	list := pageData{Name: "Ana", Tab: "habitos", Title: "Hábitos"}
	for _, h := range habits {
		if h.Archived {
			list.Archived = append(list.Archived, rowOf(h, all[h.ID], today, today))
		} else {
			list.Items = append(list.Items, rowOf(h, all[h.ID], today, today))
		}
	}
	ficha := func(h habit, day time.Time, confirm bool, msg string) pageData {
		m := all[h.ID]
		row := rowOf(h, m, today, day)
		return pageData{Name: "Ana", Tab: "habitos", Title: h.Name, Habit: &row, Form: formOf(h), Heat: buildHeatmap(h, m, today, day, 26), Confirm: confirm, Error: msg,
			Edit: &dayEdit{ISO: isoDay(day), Label: relDay(day, today), Done: h.done(m, day), Value: trimNum(m[isoDay(day)], 3)}}
	}
	diario := func(day time.Time, list []entry, month time.Time, confirm bool, msg string) pageData {
		var e entry
		exists := false
		for _, x := range list {
			if x.Day.Equal(day) {
				e, exists = x, true
			}
		}
		if !exists {
			e = entry{Day: day}
		}
		var mine []entry
		for _, x := range list {
			if monthStart(x.Day).Equal(month) {
				mine = append(mine, x)
			}
		}
		d := pageData{Name: "Ana", Tab: "diario", Title: "Diario", Entry: entryFormOf(e, exists, today), Chart: buildMoodChart(mine, month),
			MonthAvg: monthAverages(mine), Nudge: settingsData{Nudge: true}, NudgeTime: defaultNudge, Confirm: confirm, Error: msg,
			Month: &monthNav{Key: month.Format("2006-01"), Label: monthName(month), Prev: month.AddDate(0, -1, 0).Format("2006-01")}}
		for i := len(mine) - 1; i >= 0; i-- {
			d.Entries = append(d.Entries, entryRowOf(mine[i], today))
		}
		return d
	}
	datos := pageData{Name: "Ana", Tab: "datos", Title: "Datos", Range: "90"}
	var active []habit
	for _, h := range habits {
		if !h.Archived {
			active = append(active, h)
			datos.Stats = append(datos.Stats, rowOf(h, all[h.ID], today, today))
		}
	}
	for _, c := range correlations(active, all, entries, ago(89), today) {
		datos.Corrs = append(datos.Corrs, corrRowOf(c))
	}
	datos.SleepCorr = sleepCorrText(entries, ago(89))
	errForm := formOf(habits[4])
	errForm.Times = "9"
	newForm := pageData{Name: "Ana", Tab: "habitos", Title: "Nuevo hábito", Form: &habitForm{Kind: kindYesNo, FreqKind: freqDaily, Times: "3", Days: dayOpts(nil)}}
	newErr := pageData{Name: "Ana", Tab: "habitos", Title: "Nuevo hábito", Error: "Pon el objetivo de cada día: un número mayor que cero (8 vasos, 20 min…).",
		Form: &habitForm{Name: "Beber agua", Kind: kindAmount, Unit: "vasos", FreqKind: freqDays, Times: "3", Days: dayOpts([]int{1, 2})}}
	month := monthStart(today)

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"hoy", "hoy.html", hoy(today)},
		{"hoy-otro-dia", "hoy.html", hoy(ago(3))},
		{"hoy-vacio", "hoy.html", pageData{Name: "Ana", Tab: "hoy", Day: navOf(today, today), Journal: &journalState{Today: true}}},
		{"habitos", "habitos.html", list},
		{"habitos-vacio", "habitos.html", pageData{Name: "Ana", Tab: "habitos", Title: "Hábitos"}},
		{"habito", "habito.html", ficha(habits[0], today, false, "")},
		{"habito-dias", "habito.html", ficha(habits[3], ago(2), true, "")},
		{"habito-semanal", "habito.html", ficha(habits[4], ago(1), false, "")},
		{"habito-archivado", "habito.html", ficha(habits[5], today, false, "")},
		{"habito-nuevo", "habito-form.html", newForm},
		{"habito-nuevo-error", "habito-form.html", newErr},
		{"diario", "diario.html", diario(today, entries, month, false, "")},
		{"diario-mes-anterior", "diario.html", diario(ago(5), entries, month.AddDate(0, -1, 0), true, "")},
		{"diario-vacio", "diario.html", diario(today, nil, month, false, "Las horas de sueño, con un número: 7 o 7,5.")},
		{"datos", "datos.html", datos},
		{"datos-vacio", "datos.html", pageData{Name: "Ana", Tab: "datos", Title: "Datos", Range: "90"}},
	}
	cases[7].data.Error, cases[7].data.Form = "Las veces por semana van de 1 a 7.", errForm

	wants := map[string][]string{
		"hoy": {"Hoy, jueves 12 de noviembre", "Para hoy", "Beber agua", "3 de 8 vasos", `action="/habitos/marcar"`, `name="accion" value="sumar"`,
			`name="accion" value="hecho"`, `name="accion" value="deshacer"`, "racha de", "is-done", "Hoy no toca", "¿Qué tal el día?", "Escribir", `id="h1"`},
		"hoy-otro-dia":       {"Lunes 9 de noviembre", "Ese día tocaba", `name="dia" value="2026-11-09"`, "Volver a hoy", "Día siguiente", "escrito"},
		"hoy-vacio":          {"Aún no tienes hábitos", `href="/habitos/habito/nuevo"`},
		"habitos":            {"Nuevo hábito", "💧 Beber agua", "en 30 días", "Archivados (1)", "Sin azúcar", "aviso a las 10:00", "L, X y V", "3 veces por semana"},
		"habitos-vacio":      {"Aún no tienes hábitos"},
		"habito":             {"Beber agua", "8 vasos al día", "Aviso a las 10:00", "Mejor racha", "Calendario", `class="c-done"`, `class="c-part on"`, `class="c-done"`, `href="?dia=`, "A medias", "Cambiar el hábito", `value="Beber agua"`, "Archivar", "Borrar…"},
		"habito-dias":        {"L, X y V", `class="c-miss"`, `class="c-off"`, "Tocaba y no", "¿Borrar «Estirar", `name="confirmar" value="1"`, `name="dias" value="1" checked`, "martes"},
		"habito-semanal":     {"Correr", "semanas cumplidas", "esta semana", "Las veces por semana van de 1 a 7", `value="9"`, " open", `value="semana" checked`},
		"habito-archivado":   {"Archivado", "Recuperar", `name="on" value="0"`},
		"habito-nuevo":       {"Nuevo hábito", "Crear hábito", `value="si_no" checked`, `value="diaria" checked`, `type="time"`},
		"habito-nuevo-error": {"Pon el objetivo", `value="Beber agua"`, `value="cantidad" checked`, `value="dias" checked`, `name="dias" value="2" checked`},
		"diario": {"Hoy", "Ánimo", "Energía", `name="animo"`, "checked", "Horas de sueño", "noviembre de 2026", "Ánimo medio", `class="mood"`, `class="sleep"`,
			"Una nota de ejemplo", "trabajo", "Aviso de la tarde", `name="aviso" value="1" checked`, `value="21:30"`, "Borrar…", "Mes anterior"},
		"diario-mes-anterior": {"octubre de 2026", "¿Borrar lo de este día?", "Día siguiente"},
		"diario-vacio":        {"Nada escrito este mes", "Las horas de sueño, con un número", `name="animo" value="0" checked`},
		"datos":               {"Cumplimiento", "Con tu diario", "cuando lo haces", "Correr", "Este mes", "duermes 7 h o más"},
		"datos-vacio":         {"Aún no hay datos"},
	}
	nots := map[string][]string{
		"hoy-vacio":      {"Para hoy"},
		"habitos":        {"Para hoy"},
		"habito":         {"¿Borrar «"},
		"habito-nuevo":   {"Borrar"},
		"diario-vacio":   {"Borrar…", `class="mood"`},
		"habito-semanal": {"Tocaba y no"},
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
			for _, bad := range []string{"ZgotmplZ", "<no value>", "style=", "<script", "NaN", "Inf"} {
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
	}
}
