package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseFixture(t *testing.T) {
	days, err := parsePVPC(fixtureReply(t))
	if err != nil {
		t.Fatal(err)
	}
	hs := days[day0("2026-11-09")]
	if len(days) != 1 || len(hs) != 24 {
		t.Fatalf("días: %d, horas: %d", len(days), len(hs))
	}
	if !near(hs[0].P, 0.17219) || !hs[0].T.Equal(at("2026-11-09", 0, 0)) || !near(hs[23].P, 0.24259) || hourOf(hs[23].T) != 23 {
		t.Fatalf("horas: %+v … %+v", hs[0], hs[23])
	}

	l := analyze(dayPrices{Day: day0("2026-11-09"), Hours: hs}, testNow())
	if !near(l.Lo, 0.08697) || !near(l.Hi, 0.39488) || !near(l.Avg, 0.22168) {
		t.Fatalf("mín/máx/media: %v %v %v", l.Lo, l.Hi, l.Avg)
	}
	if hoursList(l.Cheap) != "4 h, 14 h y 15 h" || hoursList(l.Dear) != "19 h, 20 h y 21 h" || hourOf(l.Cheap[0].T) != 14 || hourOf(l.Dear[0].T) != 21 {
		t.Fatalf("horas baratas %q, caras %q", hoursList(l.Cheap), hoursList(l.Dear))
	}
	if !l.Has3 || l.Block3.label() != "13 a 16 h" || !near(l.Block3.Avg, 0.13494) || !l.Has2 || l.Block2.label() != "14 a 16 h" {
		t.Fatalf("bloques: %+v %+v", l.Block3, l.Block2)
	}
	if !l.HasNow || hourOf(l.Now.T) != 12 || !near(l.Now.P, 0.20335) || !l.HasRest3 || !l.Rest3.Start.Equal(l.Block3.Start) {
		t.Fatalf("ahora: %+v, desde ahora %+v", l.Now, l.Rest3)
	}
	if level(l.Now.P, l.CheapMax, l.DearMin) != "media" || level(0.08697, l.CheapMax, l.DearMin) != "barata" || level(0.39, l.CheapMax, l.DearMin) != "cara" {
		t.Fatal("tramos")
	}
	// A las 17:00 el mejor bloque ya pasó: lo mejor que queda es al final.
	late := analyze(l.dayPrices, at("2026-11-09", 17, 30))
	if late.Rest3.label() != "21 a 24 h" || !near(late.Rest3.Avg, 0.30546) {
		t.Fatalf("desde las 17:30: %s (%v)", late.Rest3.label(), late.Rest3.Avg)
	}
	// De noche, en la última hora, queda un bloque de una hora.
	last := analyze(l.dayPrices, at("2026-11-09", 23, 10))
	if !last.HasRest3 || last.Rest3.N != 1 || last.Rest3.label() != "23 a 24 h" {
		t.Fatalf("última hora: %+v", last.Rest3)
	}
	// Mañana no tiene «ahora».
	if tm := analyze(dayPrices{Day: day0("2026-11-10"), Hours: hs}, testNow()); tm.HasNow || tm.HasRest3 {
		t.Fatal("mañana con hora en curso")
	}
}

// reply hace una respuesta de REE con estos valores (€/MWh) cada paso.
func reply(start time.Time, step time.Duration, mwh ...float64) reeReply {
	var r reeReply
	r.Included = make([]struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Values []struct {
				Value    *float64 `json:"value"`
				Datetime string   `json:"datetime"`
			} `json:"values"`
		} `json:"attributes"`
	}, 1)
	r.Included[0].ID, r.Included[0].Type = reePVPC, "PVPC"
	for i := range mwh {
		v := mwh[i]
		r.Included[0].Attributes.Values = append(r.Included[0].Attributes.Values, struct {
			Value    *float64 `json:"value"`
			Datetime string   `json:"datetime"`
		}{&v, start.Add(time.Duration(i) * step).In(madrid).Format("2006-01-02T15:04:05.000-07:00")})
	}
	return r
}

func TestParseEdgeCases(t *testing.T) {
	// El día que se atrasa la hora tiene 25 horas (dos veces las 2).
	dst := day0("2026-10-25")
	if hoursIn(dst) != 25 || hoursIn(day0("2026-03-29")) != 23 || hoursIn(day0("2026-11-09")) != 24 {
		t.Fatal("horas del día")
	}
	vals := make([]float64, 25)
	for i := range vals {
		vals[i] = 100 + float64(i)
	}
	days, err := parsePVPC(reply(midnight(dst), time.Hour, vals...))
	hs := days[dst]
	if err != nil || len(hs) != 25 || hourOf(hs[2].T) != 2 || hourOf(hs[3].T) != 2 {
		t.Fatalf("cambio de hora: %d %v", len(hs), err)
	}
	if b, ok := bestBlock(hs, 3); !ok || b.label() != "0 a 2 h" || b.End.Sub(b.Start) != 3*time.Hour { // acaba a las 2 de la segunda vez
		t.Fatalf("bloque con cambio de hora: %+v", b)
	}

	// Por cuartos de hora: la media de cada hora.
	q := make([]float64, 96)
	for i := range q {
		q[i] = float64(100 + 4*(i/4) + i%4) // 100,101,102,103 → 101,5
	}
	days, _ = parsePVPC(reply(midnight(day0("2026-11-09")), 15*time.Minute, q...))
	if hs := days[day0("2026-11-09")]; len(hs) != 24 || !near(hs[0].P, 0.1015) || !near(hs[1].P, 0.1055) {
		t.Fatalf("cuartos de hora: %+v", hs[:2])
	}

	// Un día a medias no vale.
	days, _ = parsePVPC(reply(midnight(day0("2026-11-09")), time.Hour, 1, 2, 3))
	if len(days) != 0 {
		t.Fatalf("día incompleto: %v", days)
	}
	// Un valor absurdo: respuesta mala.
	if _, err := parsePVPC(reply(midnight(day0("2026-11-09")), time.Hour, 99999)); !errors.Is(err, errSourceBad) {
		t.Fatalf("valor absurdo: %v", err)
	}
	// Solo cuenta la serie del PVPC.
	r := reply(midnight(day0("2026-11-09")), time.Hour, make([]float64, 24)...)
	r.Included[0].ID, r.Included[0].Type = "600", "Precio mercado spot"
	if days, _ := parsePVPC(r); len(days) != 0 {
		t.Fatal("ha cogido otra serie")
	}

	// Etiquetas.
	if spanLabel(at("2026-11-09", 22, 0), at("2026-11-10", 0, 0)) != "22 a 24 h" {
		t.Fatal("hasta medianoche")
	}
	ex := []hourPrice{{T: at("2026-11-09", 18, 0)}, {T: at("2026-11-09", 19, 0)}, {T: at("2026-11-09", 20, 0)}, {T: at("2026-11-09", 23, 0)}}
	if s := spans(ex); s != "de 18 a 21 h y a las 23 h" {
		t.Fatalf("tramos: %q", s)
	}
}

func TestPriceNotice(t *testing.T) {
	var hs []hourPrice
	for i, p := range tomorrowPrices {
		hs = append(hs, hourPrice{T: at("2026-11-10", i, 0), P: p})
	}
	l := analyze(dayPrices{Day: day0("2026-11-10"), Hours: hs}, testNow())
	if l.Block3.label() != "12 a 15 h" || l.Block2.label() != "12 a 14 h" {
		t.Fatalf("bloques: %s %s", l.Block3.label(), l.Block2.label())
	}
	if s := priceNotice(l, settingsData{}); s != "" {
		t.Fatalf("todo apagado: %q", s)
	}
	got := priceNotice(l, settingsData{PriceLow: true, PriceHigh: true, LowBelow: 0.07, HighAbove: 0.30})
	want := "💡 Mañana la luz estará muy barata (0,050 €/kWh como mínimo) de 12 a 15 h. Bloque más barato de 3 h: de 12 a 15 h (0,060 €/kWh de media).\n" +
		"⚡ Mañana la luz estará muy cara (hasta 0,330 €/kWh) de 18 a 21 h."
	if got != want {
		t.Fatalf("aviso:\n%s\nquería:\n%s", got, want)
	}
	if s := priceNotice(l, settingsData{PriceHigh: true}); s != "⚡ Mañana la luz estará muy cara (hasta 0,330 €/kWh) de 18 a 22 h." {
		t.Fatalf("con el umbral por defecto: %q", s)
	}
}

func TestREEClient(t *testing.T) {
	c := &clock{t: testNow()}
	f := newFakeREE(t)
	r := newREE(f.URL, true)
	r.now = c.now
	ctx := context.Background()

	days, err := r.fetch(ctx, day0("2026-11-09"), day0("2026-11-10"))
	if err != nil || len(days) != 1 || len(days[day0("2026-11-09")]) != 24 {
		t.Fatalf("hoy sí, mañana aún no: %d días %v", len(days), err)
	}
	q, _ := url.ParseQuery(f.last)
	if q.Get("start_date") != "2026-11-09T00:00" || q.Get("end_date") != "2026-11-10T23:59" {
		t.Fatalf("consulta: %s", f.last)
	}
	// Sin nada publicado, REE da 502: se espera antes de volver a pedir.
	n := f.count()
	if _, err := r.fetch(ctx, day0("2026-11-11"), day0("2026-11-11")); !errors.Is(err, errSourceWait) {
		t.Fatalf("502: %v", err)
	}
	if _, err := r.fetch(ctx, day0("2026-11-09"), day0("2026-11-09")); !errors.Is(err, errSourceWait) || f.count() != n+1 {
		t.Fatal("tras un error se vuelve a pedir enseguida")
	}
	if st := r.status(); st.Err != "error 502" || st.Until.Sub(c.now()) != 2*time.Minute {
		t.Fatalf("estado: %+v", st)
	}
	c.add(3 * time.Minute)
	if _, err := r.fetch(ctx, day0("2026-11-09"), day0("2026-11-09")); err != nil || r.status().Err != "" {
		t.Fatalf("pasada la espera: %v", err)
	}

	// En producción: solo HTTPS a REE y nunca a la red local.
	p := newREE(defaultREE, false)
	for _, raw := range []string{"https://apidatos.ree.es/x", "https://sub.apidatos.ree.es/x"} {
		u, _ := url.Parse(raw)
		if !p.allowed(u) {
			t.Errorf("no deja %s", raw)
		}
	}
	for _, raw := range []string{"http://apidatos.ree.es/x", "https://apidatos.ree.es.malo.com/x", "https://example.com/", "file:///etc/passwd"} {
		u, _ := url.Parse(raw)
		if p.allowed(u) {
			t.Errorf("deja %s", raw)
		}
	}
	for _, a := range []string{"127.0.0.1:443", "10.0.0.2:443", "192.168.1.1:443", "100.100.1.1:443", "[::1]:443", "169.254.0.1:443"} {
		if dialCheck(a) == nil {
			t.Errorf("se conectaría a %s", a)
		}
	}
	if dialCheck("195.0.0.1:443") != nil {
		t.Error("no deja una dirección pública")
	}
	bad := newREE("http://127.0.0.1:1", false)
	if _, err := bad.fetch(ctx, day0("2026-11-09"), day0("2026-11-09")); err == nil || !strings.Contains(err.Error(), "no permitido") {
		t.Fatalf("por HTTP: %v", err)
	}
}

func TestMaintenanceRules(t *testing.T) {
	if d := addMonths(day0("2026-01-31"), 1); isoDay(d) != "2026-02-28" {
		t.Fatalf("31 de enero más un mes: %s", isoDay(d))
	}
	if d := addMonths(day0("2027-12-15"), 3); isoDay(d) != "2028-03-15" {
		t.Fatalf("cambio de año: %s", isoDay(d))
	}
	if d := (taskData{Every: 1, Unit: unitYears}).next(day0("2028-02-29")); isoDay(d) != "2029-02-28" {
		t.Fatalf("29 de febrero más un año: %s", isoDay(d))
	}
	if d := (taskData{Every: 2, Unit: unitWeeks}).next(day0("2026-11-09")); isoDay(d) != "2026-11-23" {
		t.Fatalf("dos semanas: %s", isoDay(d))
	}
	for _, c := range []struct {
		t    taskData
		want string
	}{{taskData{Every: 1, Unit: unitYears}, "cada año"}, {taskData{Every: 3, Unit: unitMonths}, "cada 3 meses"},
		{taskData{Every: 1, Unit: unitWeeks}, "cada semana"}, {taskData{Every: 10, Unit: unitDays}, "cada 10 días"}} {
		if got := c.t.every(); got != c.want {
			t.Errorf("%+v: %q", c.t, got)
		}
	}
	today := day0("2026-11-09")
	mk := func(due string, every int, unit string) task {
		return task{Due: day0(due), taskData: taskData{Every: every, Unit: unit}}
	}
	for _, c := range []struct {
		t     task
		state string
		days  int
	}{
		{mk("2026-11-01", 1, unitMonths), stLate, -8},
		{mk("2026-11-09", 1, unitMonths), stToday, 0},
		{mk("2026-11-12", 1, unitMonths), stSoon, 3},
		{mk("2026-11-13", 1, unitMonths), stOK, 4},   // un mes: pronto son 3 días
		{mk("2026-11-20", 1, unitYears), stSoon, 11}, // un año: pronto son 14 días
		{mk("2026-11-30", 1, unitYears), stOK, 21},
	} {
		if st, n := c.t.state(today); st != c.state || n != c.days {
			t.Errorf("%s cada %d %s: %s %d", isoDay(c.t.Due), c.t.Every, c.t.Unit, st, n)
		}
	}

	s := &service{now: testNow}
	for _, in := range []taskInput{
		{Every: 3, Unit: unitMonths},            // sin nombre
		{Name: "x", Every: 0, Unit: unitMonths}, // sin periodo
		{Name: "x", Every: 3, Unit: "siglos"},   // unidad rara
		{Name: "x", Every: 21, Unit: unitYears}, // demasiado
		{Name: "x", Every: 3, Unit: unitMonths, Last: day0("2026-12-01")},
		{Name: "x", Every: 3, Unit: unitMonths, Due: day0("1900-01-01")},
	} {
		if _, err := s.buildTask(in, nil); !isInput(err) {
			t.Errorf("acepta %+v: %v", in, err)
		}
	}
	tk, err := s.buildTask(taskInput{Name: "Filtros", Every: 3, Unit: unitMonths, Last: day0("2026-09-01")}, nil)
	if err != nil || isoDay(tk.Due) != "2026-12-01" || len(tk.Done) != 1 || !tk.Notified.IsZero() {
		t.Fatalf("con la última vez: %+v %v", tk, err)
	}
	tk, _ = s.buildTask(taskInput{Name: "Caldera", Every: 1, Unit: unitYears}, nil)
	if !tk.Due.Equal(today) || !tk.Notified.Equal(today) {
		t.Fatalf("sin fecha toca hoy, sin aviso: %+v", tk)
	}
	if got := addDone([]time.Time{day0("2026-01-01")}, day0("2026-06-01")); len(got) != 2 || !got[0].Equal(day0("2026-06-01")) {
		t.Fatalf("historial: %v", got)
	}

	if n := taskNotice([]task{{Due: today, taskData: taskData{Name: "Purgar los radiadores"}}}, today); n != "🧰 Casa: hoy toca «Purgar los radiadores». Cuando lo hagas, márcalo como hecho." {
		t.Fatalf("aviso de una: %q", n)
	}
	n := taskNotice([]task{{Due: today.Add(-2 * dayUnit), taskData: taskData{Name: "A"}}, {Due: today, taskData: taskData{Name: "B"}}}, today)
	if n != "🧰 Mantenimiento de casa que toca:\n• A (desde el 7 nov)\n• B (hoy)" {
		t.Fatalf("aviso de varias: %q", n)
	}
}

func TestConsumption(t *testing.T) {
	today := day0("2026-11-09")
	r := func(id int64, day, meter string, v float64) reading {
		return reading{ID: id, Day: day0(day), readingData: readingData{Meter: meter, Value: v}}
	}
	list := []reading{
		r(1, "2025-09-01", meterPower, 1000),
		r(2, "2025-11-09", meterPower, 1300), // 300 en 69 días
		r(3, "2026-05-09", meterPower, 2000), // 700 en 181 días
		r(4, "2026-11-09", meterPower, 2920), // 920 en 184 días
		r(5, "2026-10-01", meterWater, 50),
		r(6, "2026-11-01", meterWater, 10), // contador nuevo
		r(7, "2026-11-09", meterWater, 12.4),
	}
	m := meterOf(list, meterPower, "kWh", today)
	if len(m.Readings) != 4 || len(m.Periods) != 3 || m.Periods[2].Days != 184 || !near(m.Periods[2].PerDay, 5) {
		t.Fatalf("luz: %+v", m.Periods)
	}
	// El último año, justo desde la lectura del 9 nov 2025: 1.620 kWh en 365 días.
	if m.AvgDays != 365 || !near(m.YearUsed, 1620) || !near(m.AvgDay, 1620.0/365) {
		t.Fatalf("media del año: %d días, %v, %v", m.AvgDays, m.YearUsed, m.AvgDay)
	}
	w := meterOf(list, meterWater, "m³", today)
	if len(w.Periods) != 2 || !w.Periods[0].Reset || w.Periods[1].Reset || !near(w.Periods[1].Used, 2.4) || !near(w.AvgDay, 0.3) {
		t.Fatalf("agua: %+v (media %v)", w.Periods, w.AvgDay)
	}
	if p, ok := w.lastPeriod(); !ok || !near(p.PerDay, 0.3) {
		t.Fatal("último periodo")
	}
	if c := usageChart(m, today); c == nil || len(c.Bars) != 2 || len(c.XTicks) != 6 { // solo el último año
		t.Fatalf("gráfica: %+v", c)
	}
	if g := meterOf(list, meterGas, "m³", today); len(g.Readings) != 0 || usageChart(g, today) != nil {
		t.Fatal("gas sin lecturas")
	}
}

func TestParseNum(t *testing.T) {
	for in, want := range map[string]float64{"12345": 12345, "12.345": 12345, "1.234,5": 1234.5, "1234.5": 1234.5, "0,75": 0.75, "12 345,6": 12345.6, "0.125": 0.125, "1.234.567": 1234567} {
		if got, err := parseNum(in); err != nil || !near(got, want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "abc", "1,2,3", "NaN"} {
		if _, err := parseNum(in); err == nil {
			t.Errorf("acepta %q", in)
		}
	}
}
