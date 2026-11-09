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
	"time"
)

// samplePrices son los precios de ejemplo de hoy (los reales del fixture) y
// de mañana (inventados).
func samplePrices(t *testing.T) (dayPrices, dayPrices) {
	days, err := parsePVPC(fixtureReply(t))
	if err != nil {
		t.Fatal(err)
	}
	today := dayPrices{Day: day0("2026-11-09"), Hours: days[day0("2026-11-09")], Fetched: testNow().Add(-3 * time.Hour)}
	tomorrow := dayPrices{Day: day0("2026-11-10")}
	for i, p := range tomorrowPrices {
		tomorrow.Hours = append(tomorrow.Hours, hourPrice{T: at("2026-11-10", i, 0), P: p})
	}
	return today, tomorrow
}

// sampleTasks son tareas genéricas en todos los estados.
func sampleTasks() []task {
	today := dayOf(testNow())
	mk := func(id int64, name string, every int, unit string, last string, note string) task {
		t := task{ID: id, taskData: taskData{Name: name, Every: every, Unit: unit, Note: note}}
		if last != "" {
			t.Last = day0(last)
			t.Done = []time.Time{t.Last, t.Last.AddDate(-1, 0, 0)}
			t.Due = t.next(t.Last)
		} else {
			t.Due = today
		}
		return t
	}
	list := []task{
		mk(1, "Revisión de la caldera", 1, unitYears, "2025-10-20", "La hace un técnico autorizado; guarda el justificante."),
		mk(2, "Limpiar el filtro de la campana", 2, unitMonths, "2026-09-09", ""),
		mk(3, "Limpiar los filtros del aire acondicionado con un nombre largo para ver cómo se parte en el móvil", 3, unitMonths, "2026-08-14", ""),
		mk(4, "Purgar los radiadores", 1, unitYears, "", ""),
		mk(5, "Limpiar el filtro del lavavajillas", 1, unitMonths, "2026-11-01", ""),
	}
	list[0].Template = "caldera"
	sortTasks(list)
	return list
}

func sampleReadings() []reading {
	var out []reading
	v := 10200.0
	d := day0("2025-11-01")
	for i := 0; i < 13; i++ {
		out = append(out, reading{ID: int64(i + 1), Day: d, readingData: readingData{Meter: meterPower, Value: v}})
		v += 180 + float64(i%4)*45
		d = addMonths(d, 1)
	}
	out[3].Note = "lectura de la factura"
	return out
}

// TestWebRender ejecuta cada plantilla con datos de ejemplo (y los casos
// límite) sin base de datos. Con CASA_PREVIEW_DIR escribe las páginas y lo
// estático en esa carpeta para verlas en un navegador.
func TestWebRender(t *testing.T) {
	c := &clock{t: testNow()}
	r := newREE(defaultREE, false)
	r.now = c.now
	r.st = sourceStatus{OK: testNow().Add(-3 * time.Hour)}
	w := &web{svc: &service{ree: r, log: slog.New(slog.DiscardHandler), now: c.now}}
	today, tomorrow := samplePrices(t)
	now := testNow()

	luz := pageData{Name: "Ana", Tab: "luz", Today: luzViewOf(analyze(today, now), "Hoy", now), Tomorrow: luzViewOf(analyze(tomorrow, now), "Mañana", now), Source: w.sourceView()}
	luzSolo := luz
	luzSolo.Tomorrow = nil
	lateNow := at("2026-11-09", 18, 10)
	luzTarde := luz
	luzTarde.Today = luzViewOf(analyze(today, lateNow), "Hoy", lateNow)
	r2 := newREE(defaultREE, false)
	r2.now = c.now
	r2.st = sourceStatus{Err: "error 502", ErrAt: testNow().Add(-time.Minute), Until: testNow().Add(4 * time.Minute)}
	luzVacia := pageData{Name: "Ana", Tab: "luz", Source: (&web{svc: &service{ree: r2, now: c.now}}).sourceView()}

	tasksData := func(list []task, f *taskForm, msg string) pageData {
		if f == nil {
			f = &taskForm{Every: "3", Unit: unitMonths}
		}
		d := pageData{Name: "Ana", Tab: "mantenimiento", Title: "Mantenimiento", TForm: *f, Units: unitOptions(f.Unit), Error: msg}
		have := map[string]bool{}
		for _, tk := range list {
			row := taskRowOf(tk, dayOf(now))
			d.Tasks = append(d.Tasks, row)
			switch row.State {
			case stLate:
				d.Late++
			case stToday:
				d.Due++
			}
			have[tk.Template] = true
		}
		for _, s := range suggestions[:4] {
			if !have[s.Key] {
				d.Suggest = append(d.Suggest, s)
			}
		}
		return d
	}
	tasks := sampleTasks()
	ficha := func(tk task, confirm bool, msg string) pageData {
		v := &taskView{taskRow: taskRowOf(tk, dayOf(now))}
		for _, d := range tk.Done {
			v.Done = append(v.Done, longDate(d))
		}
		f := taskForm{Name: tk.Name, Every: "1", Unit: tk.Unit, Last: dayValue(tk.Last), Due: dayValue(tk.Due), Note: tk.Note}
		return pageData{Name: "Ana", Tab: "mantenimiento", Title: tk.Name, Task: v, TForm: f, Units: unitOptions(tk.Unit), Confirm: confirm, Error: msg}
	}

	readings := func(meter string, list []reading, f *readingForm, msg string) pageData {
		if f == nil {
			f = &readingForm{Meter: meter, Day: isoDay(dayOf(now))}
		}
		d := pageData{Name: "Ana", Tab: "lecturas", Title: "Lecturas", RForm: *f, MeterOpts: meterOptions(f.Meter), Error: msg}
		for _, m := range meters {
			d.MeterTabs = append(d.MeterTabs, meterTab{Key: m.Key, Label: m.Label, Emoji: m.Emoji, On: m.Key == meter})
		}
		d.Meter = meterSummaryOf(meterOf(list, meter, settingsData{}.unit(meter), dayOf(now)), dayOf(now))
		return d
	}
	agua := []reading{
		{ID: 20, Day: day0("2026-09-01"), readingData: readingData{Meter: meterWater, Value: 512.3}},
		{ID: 21, Day: day0("2026-10-01"), readingData: readingData{Meter: meterWater, Value: 8.1, Note: "contador nuevo"}},
		{ID: 22, Day: day0("2026-11-01"), readingData: readingData{Meter: meterWater, Value: 14.6}},
	}

	type page struct {
		file, tmpl string
		data       pageData
	}
	cases := []page{
		{"luz", "luz.html", luz},
		{"luz-sin-manana", "luz.html", luzSolo},
		{"luz-tarde", "luz.html", luzTarde},
		{"luz-vacia", "luz.html", luzVacia},
		{"mantenimiento", "mantenimiento.html", tasksData(tasks, nil, "")},
		{"mantenimiento-vacio", "mantenimiento.html", tasksData(nil, nil, "")},
		{"mantenimiento-error", "mantenimiento.html", tasksData(tasks, &taskForm{Name: "Algo", Every: "tres", Unit: unitWeeks}, "Cada cuánto va con un número entero: cada 3 meses.")},
		{"tarea", "tarea.html", ficha(tasks[0], false, "")},
		{"tarea-borrar", "tarea.html", ficha(tasks[1], true, "")},
		{"lecturas", "lecturas.html", readings(meterPower, sampleReadings(), nil, "")},
		{"lecturas-agua", "lecturas.html", readings(meterWater, agua, nil, "")},
		{"lecturas-vacio", "lecturas.html", readings(meterGas, nil, &readingForm{Meter: meterGas, Day: "2026-11-09", Value: "doce"}, "La lectura es un número: lo que marca el contador.")},
		{"ajustes", "ajustes.html", pageData{Name: "Ana", Tab: "ajustes", Title: "Ajustes", Settings: settingsFormOf(settingsData{}), Flash: "Guardado."}},
		{"ajustes-error", "ajustes.html", pageData{Name: "Ana", Tab: "ajustes", Title: "Ajustes", Settings: settingsForm{Low: true, LowBelow: "0,3", HighAbove: "0,2", GasUnit: "kWh"}, Error: "El umbral de «muy barata» tiene que ser menor que el de «muy cara»."}},
	}
	wants := map[string][]string{
		"luz": {"0,203 €/kWh", "Ahora, de 12 a 13 h", "Hoy · lunes, 9 de noviembre", "Mañana · martes, 10 de noviembre", "de 13 a 16 h", "0,135 €/kWh de media",
			`class="b barata`, `class="b media now"`, `class="band"`, "0,087 €/kWh", "0,395 €/kWh", "14 h", "Todas las horas", "Consultada hace 3 h", "de 12 a 15 h"},
		"luz-sin-manana": {"se publican hacia las 20:30"},
		"luz-tarde":      {"El más barato ya ha pasado; desde ahora", "de 21 a 24 h", "Ahora, de 18 a 19 h", "lv-cara"},
		"luz-vacia":      {"Aún no están los precios de hoy", "Falla: error 502", "se vuelve a intentar en 5 min"},
		"mantenimiento": {"Revisión de la caldera", "Atrasada", "Toca hoy", "Pronto", "Al día", "tocaba el 20 oct (20 días de retraso)", `action="/casa/tarea/4/hecho"`,
			`name="desde" value="lista"`, "Sugerencias", `name="clave" value="aire"`, "1 atrasada", "2 para hoy"},
		"mantenimiento-vacio": {"Aún no hay tareas", `name="clave" value="caldera"`, "cada año"},
		"mantenimiento-error": {"número entero", `value="tres"`, `<option value="semanas" selected>`, " open"},
		"tarea":               {"Revisión de la caldera", "Hecha hoy", `action="/casa/tarea/1/hecho"`, "Últimas veces", "guarda el justificante", `value="2025-10-20"`, "Borrar…"},
		"tarea-borrar":        {"¿Borrar «Limpiar el filtro de la campana»?", `name="confirmar" value="1"`},
		"lecturas":            {"Última lectura", "Desde la anterior", "Media diaria (último año)", `<rect class="b use"`, "lectura de la factura", "la primera", `aria-current="true"><span aria-hidden="true">💡</span> Luz`},
		"lecturas-agua":       {"m³", "¿contador nuevo?", "contador nuevo", "0,21 m³"},
		"lecturas-vacio":      {"Con dos lecturas de Gas", "La lectura es un número", `value="doce"`, `<option value="gas" selected>`},
		"ajustes":             {`name="mantenimiento" value="1" checked`, `value="0,100"`, `value="0,250"`, `<option value="m³" selected>`},
		"ajustes-error":       {"tiene que ser menor", `name="barata" value="1" checked`, `<option value="kWh" selected>`},
	}
	nots := map[string][]string{
		"luz-sin-manana": {"Mañana · "},
		"luz":            {"El más barato ya ha pasado"},
		"mantenimiento":  {`name="clave" value="caldera"`},
		"ajustes":        {`name="barata" value="1" checked`},
		"lecturas-vacio": {"Última lectura"},
	}

	dir := os.Getenv("CASA_PREVIEW_DIR")
	base := "/casa"
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
