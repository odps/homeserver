package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type service struct {
	store store
	ree   *ree
	log   *slog.Logger
	now   func() time.Time

	mu         sync.Mutex
	known      map[time.Time]bool // días con los precios ya guardados
	lastFetch  time.Time
	lastMaint  time.Time
	priceDone  time.Time // día cuyos avisos de precio ya se han repasado
	cleanedOn  time.Time
	kickOnce   sync.Once
	kickC      chan struct{}
	fetchMutex sync.Mutex
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// inputError es un error de lo que ha escrito la persona: se le puede
// enseñar tal cual. Los demás errores no salen de la app.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func bad(msg string) error { return inputError{msg} }

func isInput(err error) bool {
	var ie inputError
	return errors.As(err, &ie)
}

// ---- Precio de la luz ----

// Desde qué hora se busca el precio de mañana (REE lo publica hacia las
// 20:30) y cada cuánto se insiste.
const (
	tomorrowFrom = 20*time.Hour + 15*time.Minute
	gapToday     = 5 * time.Minute
	gapTomorrow  = 10 * time.Minute
)

// sinceMidnight es la hora del día en Madrid.
func sinceMidnight(t time.Time) time.Duration {
	l := t.In(madrid)
	return time.Duration(l.Hour())*time.Hour + time.Duration(l.Minute())*time.Minute
}

// have dice si ya están guardados los precios de un día.
func (s *service) have(ctx context.Context, day time.Time) bool {
	s.mu.Lock()
	ok := s.known[day]
	s.mu.Unlock()
	if ok {
		return true
	}
	p, err := s.store.prices(ctx, day)
	if err != nil || !p.ok() {
		return false
	}
	s.setKnown(day)
	return true
}

func (s *service) setKnown(day time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known == nil {
		s.known = map[time.Time]bool{}
	}
	for d := range s.known {
		if d.Before(day.Add(-3 * dayUnit)) {
			delete(s.known, d)
		}
	}
	s.known[day] = true
}

// refreshPrices pide a REE los precios de hoy, si faltan, y los de mañana a
// partir de las 20:15. Devuelve true si ha guardado algo.
func (s *service) refreshPrices(ctx context.Context) bool {
	s.fetchMutex.Lock()
	defer s.fetchMutex.Unlock()
	now := s.clock()
	today := dayOf(now)
	tomorrow := today.Add(dayUnit)
	needToday := !s.have(ctx, today)
	needTomorrow := sinceMidnight(now) >= tomorrowFrom && !s.have(ctx, tomorrow)
	if !needToday && !needTomorrow {
		return false
	}
	gap := gapTomorrow
	if needToday {
		gap = gapToday
	}
	s.mu.Lock()
	due := now.Sub(s.lastFetch) >= gap
	if due {
		s.lastFetch = now
	}
	s.mu.Unlock()
	if !due || !s.ree.ready() {
		return false
	}
	days, err := s.ree.fetch(ctx, today, tomorrow)
	if err != nil {
		if !errors.Is(err, errSourceWait) && !errors.Is(err, context.Canceled) {
			s.log.Warn("casa: precios de la luz", "err", err)
		}
		return false
	}
	saved := false
	for d, hs := range days {
		if d.Before(today) || d.After(tomorrow) || (d.Equal(today) && !needToday) {
			continue
		}
		if err := s.store.savePrices(ctx, d, now, hs); err != nil {
			s.log.Warn("casa: guardar precios", "err", err)
			continue
		}
		s.setKnown(d)
		saved = true
	}
	return saved
}

// luzDay son los precios de un día ya con sus cuentas, para la web, Talia y
// el resumen.
type luzDay struct {
	dayPrices
	Lo, Hi, Avg       float64
	CheapMax, DearMin float64
	Cheap, Dear       []hourPrice
	Block2, Block3    block
	Has2, Has3        bool
	// Solo hoy: lo que queda desde ahora.
	Now      hourPrice
	HasNow   bool
	Rest3    block
	HasRest3 bool
}

func analyze(p dayPrices, now time.Time) *luzDay {
	if !p.ok() {
		return nil
	}
	l := &luzDay{dayPrices: p}
	l.Lo, l.Hi, l.Avg = p.stats()
	l.CheapMax, l.DearMin = levels(p.Hours)
	l.Cheap, l.Dear = ranked(p.Hours, 3, true), ranked(p.Hours, 3, false)
	l.Block2, l.Has2 = bestBlock(p.Hours, 2)
	l.Block3, l.Has3 = bestBlock(p.Hours, 3)
	if dayOf(now).Equal(p.Day) {
		l.Now, l.HasNow = p.at(now)
		l.Rest3, l.HasRest3 = bestBlock(from(p.Hours, now), 3)
		if !l.HasRest3 {
			l.Rest3, l.HasRest3 = bestBlock(from(p.Hours, now), 1)
		}
	}
	return l
}

// luz son los precios de hoy y de mañana (nil si aún no están).
func (s *service) luz(ctx context.Context) (today, tomorrow *luzDay, err error) {
	now := s.clock()
	d := dayOf(now)
	p, err := s.store.prices(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	q, err := s.store.prices(ctx, d.Add(dayUnit))
	if err != nil {
		return nil, nil, err
	}
	return analyze(p, now), analyze(q, now), nil
}

// spans junta horas en tramos seguidos: «de 18 a 22 h y de 23 a 24 h».
func spans(hs []hourPrice) string {
	list := append([]hourPrice(nil), hs...)
	sort.Slice(list, func(i, j int) bool { return list[i].T.Before(list[j].T) })
	var parts []string
	for i := 0; i < len(list); {
		j := i
		for j+1 < len(list) && list[j+1].T.Sub(list[j].T) == time.Hour {
			j++
		}
		if i == j {
			parts = append(parts, "a las "+hourLabel(list[i].T))
		} else {
			parts = append(parts, "de "+spanLabel(list[i].T, list[j].T.Add(time.Hour)))
		}
		i = j + 1
	}
	return joinY(parts)
}

// priceNotice es el aviso de los precios de mañana según los ajustes ("" si
// no hay nada que avisar).
func priceNotice(l *luzDay, st settingsData) string {
	var low, high []hourPrice
	for _, h := range l.Hours {
		if st.PriceLow && h.P <= st.low() {
			low = append(low, h)
		}
		if st.PriceHigh && h.P >= st.high() {
			high = append(high, h)
		}
	}
	var lines []string
	if len(low) > 0 {
		line := "💡 Mañana la luz estará muy barata (" + kwh(l.Lo) + " como mínimo) " + spans(low) + "."
		if l.Has3 {
			line += " Bloque más barato de 3 h: de " + l.Block3.label() + " (" + kwh(l.Block3.Avg) + " de media)."
		}
		lines = append(lines, line)
	}
	if len(high) > 0 {
		lines = append(lines, "⚡ Mañana la luz estará muy cara (hasta "+kwh(l.Hi)+") "+spans(high)+".")
	}
	return strings.Join(lines, "\n")
}

// checkPriceNotices avisa, a quien lo tenga encendido, si los precios de
// mañana bajan o suben de sus umbrales. Una vez por día.
func (s *service) checkPriceNotices(ctx context.Context, day time.Time) error {
	p, err := s.store.prices(ctx, day)
	if err != nil || !p.ok() {
		return err
	}
	l := analyze(p, s.clock())
	watchers, err := s.store.priceWatchers(ctx, day)
	if err != nil {
		return err
	}
	for owner, st := range watchers {
		if err := s.store.notifyPrice(ctx, owner, day, priceNotice(l, st), s.clock()); err != nil {
			return err
		}
	}
	return nil
}

// ---- Mantenimiento ----

// suggestion es una tarea sugerida que se añade con un clic. Genéricas: el
// repositorio es público.
type suggestion struct {
	Key, Name string
	Every     int
	Unit      string
	Note      string
}

var suggestions = []suggestion{
	{"caldera", "Revisión de la caldera", 1, unitYears, "La hace un técnico autorizado; guarda el justificante."},
	{"gas", "Inspección de la instalación de gas", 5, unitYears, "La periódica la avisa la distribuidora con antelación: desconfía de visitas sin aviso."},
	{"aire", "Limpiar los filtros del aire acondicionado", 3, unitMonths, ""},
	{"campana", "Limpiar el filtro de la campana", 2, unitMonths, ""},
	{"lavavajillas", "Limpiar el filtro del lavavajillas", 1, unitMonths, ""},
	{"lavadora", "Limpiar el filtro y el cajetín de la lavadora", 3, unitMonths, ""},
	{"radiadores", "Purgar los radiadores", 1, unitYears, "Mejor antes de encender la calefacción."},
	{"humo", "Probar el detector de humo", 6, unitMonths, ""},
	{"frigorifico", "Limpiar el frigorífico y su rejilla trasera", 6, unitMonths, ""},
}

// EveryLabel es «cada 3 meses», para la web.
func (t suggestion) EveryLabel() string { return taskData{Every: t.Every, Unit: t.Unit}.every() }

func findSuggestion(key string) (suggestion, bool) {
	for _, t := range suggestions {
		if t.Key == key {
			return t, true
		}
	}
	return suggestion{}, false
}

// Estados de una tarea.
const (
	stLate  = "atrasada"
	stToday = "hoy"
	stSoon  = "pronto"
	stOK    = "al-dia"
)

// state es cómo va una tarea: atrasada, toca hoy, toca pronto (dentro de
// una décima parte de su periodo, de 3 a 14 días) o al día. days es cuánto
// falta (negativo: cuánto hace).
func (t task) state(today time.Time) (string, int) {
	n := daysBetween(today, t.Due)
	soon := min(max(t.periodDays()/10, 3), 14)
	switch {
	case n < 0:
		return stLate, n
	case n == 0:
		return stToday, n
	case n <= soon:
		return stSoon, n
	}
	return stOK, n
}

// taskInput es una tarea tal como llega de la web o de Talia.
type taskInput struct {
	Name     string
	Every    int
	Unit     string
	Last     time.Time // última vez que se hizo (opcional)
	Due      time.Time // próxima (opcional: si no, la última más el periodo, u hoy)
	Note     string
	Template string
}

// buildTask valida una tarea. old es la que había (al editar).
func (s *service) buildTask(in taskInput, old *task) (task, error) {
	today := dayOf(s.clock())
	t := task{taskData: taskData{Name: clip(in.Name, 80), Every: in.Every, Unit: in.Unit, Note: clip(in.Note, 300), Template: in.Template}}
	if old != nil {
		t.ID, t.Done, t.Template, t.Notified = old.ID, old.Done, old.Template, old.Notified
	}
	switch {
	case t.Name == "":
		return t, bad("Ponle un nombre a la tarea.")
	case !validUnit(t.Unit):
		return t, bad("Elige cada cuánto: días, semanas, meses o años.")
	case t.Every < 1 || t.periodDays() > 20*365:
		return t, bad("La periodicidad va de 1 día a 20 años.")
	}
	for _, d := range []time.Time{in.Last, in.Due} {
		if !d.IsZero() && (d.Year() < 1990 || d.Year() > today.Year()+30) {
			return t, bad("Esa fecha no es válida.")
		}
	}
	if in.Last.After(today) {
		return t, bad("La última vez no puede ser futura.")
	}
	t.Last = in.Last
	if !t.Last.IsZero() && !containsDay(t.Done, t.Last) {
		t.Done = addDone(t.Done, t.Last)
	}
	switch {
	case !in.Due.IsZero():
		t.Due = in.Due
	case !t.Last.IsZero():
		t.Due = t.next(t.Last)
	default:
		t.Due = today // sin saber cuándo se hizo: toca ya
	}
	// Lo que la persona deja como ya pendiente no se le avisa: lo acaba de ver.
	if !t.Due.After(today) {
		t.Notified = t.Due
	}
	return t, nil
}

func containsDay(list []time.Time, d time.Time) bool {
	for _, x := range list {
		if x.Equal(d) {
			return true
		}
	}
	return false
}

// addDone añade un día al historial (de más reciente a más antiguo, sin
// repetir, las 12 últimas).
func addDone(list []time.Time, d time.Time) []time.Time {
	if containsDay(list, d) {
		return list
	}
	out := append(append([]time.Time(nil), list...), d)
	sort.Slice(out, func(i, j int) bool { return out[i].After(out[j]) })
	return out[:min(len(out), 12)]
}

func (s *service) addTask(ctx context.Context, owner string, in taskInput) (task, error) {
	t, err := s.buildTask(in, nil)
	if err != nil {
		return t, err
	}
	t.ID, err = s.store.addTask(ctx, owner, t)
	return t, err
}

func (s *service) updateTask(ctx context.Context, owner string, id int64, in taskInput) (task, error) {
	old, err := s.store.task(ctx, owner, id)
	if err != nil {
		return old, err
	}
	t, err := s.buildTask(in, &old)
	if err != nil {
		return t, err
	}
	return t, s.store.updateTask(ctx, owner, t)
}

// addTemplate añade una tarea sugerida. Si ya la tenía, devuelve la suya.
func (s *service) addTemplate(ctx context.Context, owner, key string) (task, bool, error) {
	tp, ok := findSuggestion(key)
	if !ok {
		return task{}, false, bad("Esa sugerencia no existe.")
	}
	list, err := s.store.tasks(ctx, owner)
	if err != nil {
		return task{}, false, err
	}
	for _, t := range list {
		if t.Template == key {
			return t, true, nil
		}
	}
	t, err := s.addTask(ctx, owner, taskInput{Name: tp.Name, Every: tp.Every, Unit: tp.Unit, Note: tp.Note, Template: tp.Key})
	return t, false, err
}

// markDone la da por hecha el día day (hoy si es cero): la próxima es ese
// día más el periodo. Un día anterior a la última vez solo va al historial.
func (s *service) markDone(ctx context.Context, owner string, id int64, day time.Time) (task, error) {
	t, err := s.store.task(ctx, owner, id)
	if err != nil {
		return t, err
	}
	today := dayOf(s.clock())
	if day.IsZero() {
		day = today
	}
	if day.After(today) {
		return t, bad("No se puede dar por hecha en el futuro.")
	}
	if day.Year() < 1990 {
		return t, bad("Esa fecha no es válida.")
	}
	t.Done = addDone(t.Done, day)
	if t.Last.IsZero() || day.After(t.Last) {
		t.Last = day
		t.Due = t.next(day)
		if !t.Due.After(today) {
			t.Notified = t.Due
		}
	}
	return t, s.store.updateTask(ctx, owner, t)
}

// taskNotice es el aviso de las tareas que tocan.
func taskNotice(list []task, today time.Time) string {
	when := func(t task) string {
		if t.Due.Equal(today) {
			return "hoy"
		}
		return "desde " + relDay(t.Due, today)
	}
	if len(list) == 1 {
		t := list[0]
		if t.Due.Equal(today) {
			return "🧰 Casa: hoy toca «" + t.Name + "». Cuando lo hagas, márcalo como hecho."
		}
		return "🧰 Casa: «" + t.Name + "» toca " + when(t) + ". Cuando lo hagas, márcalo como hecho."
	}
	lines := []string{"🧰 Mantenimiento de casa que toca:"}
	for _, t := range list {
		lines = append(lines, "• "+t.Name+" ("+when(t)+")")
	}
	return strings.Join(lines, "\n")
}

// checkTasks avisa (una vez) de las tareas que tocan, a quien no lo tenga
// apagado. Devuelve cuántos avisos ha apuntado.
func (s *service) checkTasks(ctx context.Context) (int, error) {
	now := s.clock()
	today := dayOf(now)
	owners, err := s.store.dueOwners(ctx, today)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, owner := range owners {
		st, err := s.store.settings(ctx, owner)
		if err != nil {
			return n, err
		}
		list, err := s.store.tasks(ctx, owner)
		if err != nil {
			return n, err
		}
		var due []task
		for _, t := range list {
			if !t.Due.After(today) && !t.Notified.Equal(t.Due) {
				due = append(due, t)
			}
		}
		if len(due) == 0 {
			continue
		}
		text := taskNotice(due, today)
		if st.MaintOff {
			text = "" // apagado: se marcan sin avisar, para no avisar de golpe al encenderlo
		}
		if err := s.store.notifyTasks(ctx, owner, due, text, now); err != nil {
			return n, err
		}
		if text != "" {
			n++
		}
	}
	return n, nil
}

// ---- Lecturas ----

// readingInput es una lectura tal como llega de la web o de Talia.
type readingInput struct {
	Meter string
	Day   time.Time
	Value float64
	Note  string
}

func (s *service) addReading(ctx context.Context, owner string, in readingInput) (reading, bool, error) {
	today := dayOf(s.clock())
	x := reading{Day: in.Day, readingData: readingData{Meter: in.Meter, Value: in.Value, Note: clip(in.Note, 200)}}
	if x.Day.IsZero() {
		x.Day = today
	}
	switch {
	case !validMeter(x.Meter):
		return x, false, bad("Elige el contador: luz, agua o gas.")
	case x.Day.After(today):
		return x, false, bad("La fecha no puede ser futura.")
	case x.Day.Before(today.AddDate(-keepReadings, 0, 0)):
		return x, false, bad("Solo se guardan las lecturas de los últimos 5 años.")
	case x.Value < 0 || x.Value > 1e9 || math.IsNaN(x.Value):
		return x, false, bad("La lectura es lo que marca el contador: un número positivo.")
	}
	var replaced bool
	var err error
	x.ID, replaced, err = s.store.addReading(ctx, owner, x)
	return x, replaced, err
}

// usage es lo gastado entre dos lecturas seguidas de un contador.
type usage struct {
	From, To time.Time
	Days     int
	Used     float64
	PerDay   float64
	Reset    bool // la lectura baja: contador cambiado o reiniciado
}

// meterView es un contador con sus lecturas y consumos.
type meterView struct {
	Meter    string
	Unit     string
	Readings []reading // de la más antigua a la más nueva
	Periods  []usage
	AvgDay   float64 // media diaria del último año (o lo que haya)
	AvgDays  int     // días que cubre esa media
	YearUsed float64 // gastado en ese tiempo
}

func (m meterView) last() (reading, bool) {
	if len(m.Readings) == 0 {
		return reading{}, false
	}
	return m.Readings[len(m.Readings)-1], true
}

func (m meterView) lastPeriod() (usage, bool) {
	for i := len(m.Periods) - 1; i >= 0; i-- {
		if !m.Periods[i].Reset {
			return m.Periods[i], true
		}
	}
	return usage{}, false
}

// meterOf calcula los consumos de un contador.
func meterOf(all []reading, meter, unit string, today time.Time) meterView {
	m := meterView{Meter: meter, Unit: unit}
	for _, r := range all {
		if r.Meter == meter {
			m.Readings = append(m.Readings, r)
		}
	}
	yearAgo := today.AddDate(-1, 0, 0)
	for i := 1; i < len(m.Readings); i++ {
		a, b := m.Readings[i-1], m.Readings[i]
		p := usage{From: a.Day, To: b.Day, Days: daysBetween(a.Day, b.Day), Used: b.Value - a.Value}
		if p.Days <= 0 {
			continue
		}
		if p.Used < 0 {
			p.Reset, p.Used = true, 0
		} else {
			p.PerDay = p.Used / float64(p.Days)
		}
		m.Periods = append(m.Periods, p)
		if !p.Reset && p.To.After(yearAgo) {
			days := p.Days
			used := p.Used
			if p.From.Before(yearAgo) { // solo la parte dentro del último año
				days = daysBetween(yearAgo, p.To)
				used = p.PerDay * float64(days)
			}
			m.AvgDays += days
			m.YearUsed += used
		}
	}
	if m.AvgDays > 0 {
		m.AvgDay = m.YearUsed / float64(m.AvgDays)
	}
	return m
}

// meterViews son los tres contadores de una persona.
func (s *service) meterViews(ctx context.Context, owner string) ([]meterView, settingsData, error) {
	st, err := s.store.settings(ctx, owner)
	if err != nil {
		return nil, st, err
	}
	list, err := s.store.readings(ctx, owner)
	if err != nil {
		return nil, st, err
	}
	today := dayOf(s.clock())
	var out []meterView
	for _, m := range meters {
		out = append(out, meterOf(list, m.Key, st.unit(m.Key), today))
	}
	return out, st, nil
}

// ---- Resumen de la mañana ----

// todayLines son las líneas de Casa para el resumen de la mañana de la
// Agenda: el precio de la luz de hoy (de cuánto a cuánto y el bloque más
// barato de lo que queda) y el mantenimiento que toca hoy o va atrasado.
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	now := s.clock()
	today := dayOf(now)
	var lines []string
	p, err := s.store.prices(ctx, today)
	if err != nil {
		return nil, err
	}
	if l := analyze(p, now); l != nil {
		lines = append(lines, "Luz hoy: de "+num(l.Lo, 3)+" a "+kwh(l.Hi)+" (media "+num(l.Avg, 3)+").")
		if l.HasRest3 {
			b := l.Rest3
			name := "Bloque más barato de 3 h"
			if b.N == 1 {
				name = "La hora más barata que queda"
			}
			lines = append(lines, name+": de "+b.label()+" ("+kwh(b.Avg)+" de media).")
		}
	}
	tasks, err := s.store.tasks(ctx, owner)
	if err != nil {
		return nil, err
	}
	var hoy, late []string
	for _, t := range tasks {
		switch st, _ := t.state(today); st {
		case stToday:
			hoy = append(hoy, "«"+t.Name+"»")
		case stLate:
			late = append(late, "«"+t.Name+"» (desde "+relDay(t.Due, today)+")")
		}
	}
	var parts []string
	if len(hoy) > 0 {
		parts = append(parts, "hoy toca "+joinY(hoy))
	}
	if len(late) > 0 {
		parts = append(parts, "atrasado: "+joinY(late))
	}
	if len(parts) > 0 {
		lines = append(lines, "Mantenimiento: "+strings.Join(parts, "; ")+".")
	}
	return lines, nil
}

// ---- Tarea periódica ----

func (s *service) kick() {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	select {
	case s.kickC <- struct{}{}:
	default:
	}
}

// run pide los precios, avisa de lo que toca y borra lo viejo.
func (s *service) run(ctx context.Context) {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		s.round(rctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kickC:
			s.mu.Lock()
			s.priceDone, s.lastMaint = time.Time{}, time.Time{}
			s.mu.Unlock()
		}
	}
}

// Desde qué hora se avisa del mantenimiento y cada cuánto se mira.
const (
	maintFrom = 9 * time.Hour
	maintGap  = 10 * time.Minute
)

// round es una pasada: precios, avisos y, una vez al día, limpieza.
func (s *service) round(ctx context.Context) {
	s.refreshPrices(ctx)
	now := s.clock()
	today := dayOf(now)
	tomorrow := today.Add(dayUnit)

	s.mu.Lock()
	doPrice := !s.priceDone.Equal(tomorrow)
	doMaint := sinceMidnight(now) >= maintFrom && now.Sub(s.lastMaint) >= maintGap
	if doMaint {
		s.lastMaint = now
	}
	clean := !s.cleanedOn.Equal(today)
	if clean {
		s.cleanedOn = today
	}
	s.mu.Unlock()

	if doPrice && s.have(ctx, tomorrow) {
		if err := s.checkPriceNotices(ctx, tomorrow); err != nil {
			s.log.Warn("casa: avisos del precio", "err", err)
		} else {
			s.mu.Lock()
			s.priceDone = tomorrow
			s.mu.Unlock()
		}
	}
	if doMaint {
		if n, err := s.checkTasks(ctx); err != nil {
			s.log.Warn("casa: avisos del mantenimiento", "err", err)
		} else if n > 0 {
			s.log.Info("casa: mantenimiento", "avisos", n)
		}
	}
	if clean {
		if err := s.store.prunePrices(ctx, today.Add(-keepPrices)); err != nil {
			s.log.Warn("casa: borrar precios viejos", "err", err)
		}
		if err := s.store.pruneReadings(ctx, today.AddDate(-keepReadings, 0, 0)); err != nil {
			s.log.Warn("casa: borrar lecturas viejas", "err", err)
		}
	}
}
