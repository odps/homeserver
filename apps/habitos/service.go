package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"homeserver/platform/seal"
)

type service struct {
	store store
	log   *slog.Logger
	now   func() time.Time

	mu        sync.Mutex
	lastPurge time.Time
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *service) today() time.Time { return dayOf(s.clock()) }

// inputError es un error de lo que ha escrito la persona: se le puede
// enseñar tal cual. Los demás errores no salen de la app.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func bad(msg string) error { return inputError{msg} }

func isInput(err error) bool {
	var ie inputError
	return errors.As(err, &ie)
}

// Límites de lo que se escribe.
const (
	maxName   = 60
	maxEmoji  = 16 // bytes: un emoji con modificadores
	maxUnit   = 20
	maxTarget = 100000
	maxNote   = 4000
	maxTags   = 10
	maxTag    = 30
	firstYear = 2000
)

// ---- Lo de una persona ----

type view struct {
	habits []habit
	marks  map[int64]marks
	today  time.Time
}

func (s *service) load(ctx context.Context, owner string) (*view, error) {
	v := &view{today: s.today()}
	var err error
	if v.habits, err = s.store.habits(ctx, owner); err != nil {
		return nil, err
	}
	if v.marks, err = s.store.marks(ctx, owner); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *view) m(h habit) marks {
	if m := v.marks[h.ID]; m != nil {
		return m
	}
	return marks{}
}

func (v *view) stats(h habit) habitStats { return statsOf(h, v.m(h), v.today) }

func (v *view) active() []habit {
	return slices.DeleteFunc(slices.Clone(v.habits), func(h habit) bool { return h.Archived })
}

func (v *view) habit(id int64) (habit, bool) {
	for _, h := range v.habits {
		if h.ID == id {
			return h, true
		}
	}
	return habit{}, false
}

// ---- Crear y editar hábitos ----

// habitInput es lo que llega de la web o de Talia para crear o editar.
type habitInput struct {
	Name, Emoji, Kind string
	Target            float64
	Unit              string
	Freq              freq
	Remind            string
}

// checkHabit valida y deja limpio lo que se va a guardar.
func checkHabit(in habitInput) (habitData, error) {
	d := habitData{Name: clip(in.Name, maxName), Emoji: strings.TrimSpace(in.Emoji), Kind: in.Kind, Unit: clip(in.Unit, maxUnit)}
	if d.Name == "" || len(seal.Words(d.Name)) == 0 {
		return d, bad("Ponle un nombre.")
	}
	if len(d.Emoji) > maxEmoji || strings.IndexFunc(d.Emoji, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) }) >= 0 {
		return d, bad("El emoji, uno solo y sin letras.")
	}
	switch d.Kind {
	case "", kindYesNo:
		d.Kind, d.Unit = kindYesNo, ""
	case kindAmount:
		if in.Target <= 0 || in.Target > maxTarget || math.IsNaN(in.Target) {
			return d, bad("Pon el objetivo de cada día: un número mayor que cero (8 vasos, 20 min…).")
		}
		d.Target = in.Target
	default:
		return d, bad("El tipo es «sí o no» o «cantidad».")
	}
	f := in.Freq
	switch f.Kind {
	case "", freqDaily:
		d.Freq = freq{Kind: freqDaily}
	case freqDays:
		var days []int
		for _, wd := range f.Days {
			if wd >= 0 && wd <= 6 && !slices.Contains(days, wd) {
				days = append(days, wd)
			}
		}
		sort.Ints(days)
		switch len(days) {
		case 0:
			return d, bad("Elige qué días de la semana.")
		case 7:
			d.Freq = freq{Kind: freqDaily}
		default:
			d.Freq = freq{Kind: freqDays, Days: days}
		}
	case freqWeekly:
		if f.Times < 1 || f.Times > 7 {
			return d, bad("Las veces por semana van de 1 a 7.")
		}
		d.Freq = freq{Kind: freqWeekly, Times: f.Times}
		if f.Times == 7 {
			d.Freq = freq{Kind: freqDaily}
		}
	default:
		return d, bad("La frecuencia es diaria, unos días de la semana o unas veces por semana.")
	}
	if r := strings.TrimSpace(in.Remind); r != "" {
		m, err := parseHour(r)
		if err != nil {
			return d, bad("La hora del recordatorio va como 09:30.")
		}
		d.Remind = hourText(m)
	}
	return d, nil
}

func (s *service) createHabit(ctx context.Context, owner string, in habitInput) (habit, error) {
	d, err := checkHabit(in)
	if err != nil {
		return habit{}, err
	}
	h, err := s.store.addHabit(ctx, owner, d, s.clock())
	switch {
	case errors.Is(err, errDuplicate):
		return h, bad("Ya tienes un hábito que se llama así.")
	case errors.Is(err, errFull):
		return h, bad("Has llegado al máximo de hábitos (" + strconv.Itoa(maxHabits) + "). Borra alguno archivado.")
	}
	return h, err
}

func (s *service) editHabit(ctx context.Context, owner string, id int64, in habitInput) (habit, error) {
	h, err := s.store.habit(ctx, owner, id)
	if err != nil {
		return h, err
	}
	d, err := checkHabit(in)
	if err != nil {
		return h, err
	}
	h.habitData = d
	err = s.store.updateHabit(ctx, owner, h)
	if errors.Is(err, errDuplicate) {
		return h, bad("Ya tienes un hábito que se llama así.")
	}
	return h, err
}

// findHabit busca un hábito por su nombre (sin tildes ni mayúsculas): el
// exacto, el que empieza así o el que lo contiene. Los archivados solo si
// se nombran enteros.
func findHabit(list []habit, q string) (habit, error) {
	want := strings.Join(seal.Words(q), " ")
	if want == "" {
		return habit{}, bad("Di qué hábito (su nombre).")
	}
	var exact, prefix, partial []habit
	for _, h := range list {
		name := strings.Join(seal.Words(h.Name), " ")
		switch {
		case name == want:
			exact = append(exact, h)
		case h.Archived:
		case strings.HasPrefix(name, want):
			prefix = append(prefix, h)
		case strings.Contains(name, want):
			partial = append(partial, h)
		}
	}
	for _, l := range [][]habit{exact, prefix, partial} {
		switch len(l) {
		case 0:
			continue
		case 1:
			return l[0], nil
		}
		var names []string
		for _, h := range l {
			names = append(names, "«"+h.Name+"»")
		}
		return habit{}, bad("Hay varios que encajan: " + strings.Join(names, ", ") + ". Di cuál.")
	}
	var names []string
	for _, h := range list {
		if !h.Archived {
			names = append(names, "«"+h.Name+"»")
		}
	}
	if len(names) == 0 {
		return habit{}, bad("No hay ningún hábito con ese nombre (aún no tienes ninguno).")
	}
	return habit{}, bad("No hay ningún hábito con ese nombre. Tienes: " + strings.Join(names, ", ") + ".")
}

// ---- Marcar ----

// checkDay valida el día de una marca o del diario: ni futuro ni de antes
// de 2000.
func (s *service) checkDay(d time.Time) error {
	switch {
	case d.After(s.today()):
		return bad("Ese día aún no ha llegado.")
	case d.Year() < firstYear:
		return bad("Esa fecha es demasiado antigua.")
	}
	return nil
}

// setAmount guarda la cantidad hecha un día (0 la quita).
func (s *service) setAmount(ctx context.Context, owner string, h habit, day time.Time, amount float64, origin string) error {
	if err := s.checkDay(day); err != nil {
		return err
	}
	if math.IsNaN(amount) || amount < 0 || amount > maxTarget*10 {
		return bad("Esa cantidad no tiene sentido.")
	}
	if h.Kind != kindAmount && amount > 0 {
		amount = 1
	}
	return s.store.setMark(ctx, owner, h.ID, day, math.Round(amount*1000)/1000, origin)
}

// ---- Diario ----

// cleanTags parte las etiquetas por comas, sin repetir ni almohadillas.
func cleanTags(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range list {
		for _, t := range strings.Split(raw, ",") {
			t = clip(strings.TrimLeft(strings.TrimSpace(t), "#"), maxTag)
			k := seal.Fold(t)
			if t == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}

// checkEntry valida una entrada del diario.
func checkEntry(d *entryData) error {
	if d.Mood < 0 || d.Mood > 5 {
		return bad("El ánimo va de 1 a 5.")
	}
	if d.Energy < 0 || d.Energy > 5 {
		return bad("La energía va de 1 a 5.")
	}
	if d.Sleep != nil && (*d.Sleep < 0 || *d.Sleep > 24 || math.IsNaN(*d.Sleep)) {
		return bad("Las horas de sueño van de 0 a 24.")
	}
	if d.Sleep != nil {
		v := math.Round(*d.Sleep*100) / 100
		d.Sleep = &v
	}
	d.Note = strings.TrimSpace(strings.ReplaceAll(d.Note, "\r\n", "\n"))
	if len([]rune(d.Note)) > maxNote {
		return bad("La nota es demasiado larga (como mucho " + strconv.Itoa(maxNote) + " letras).")
	}
	d.Tags = cleanTags(d.Tags)
	if len(d.Tags) > maxTags {
		return bad("Como mucho " + strconv.Itoa(maxTags) + " etiquetas.")
	}
	return nil
}

func (s *service) saveEntry(ctx context.Context, owner string, day time.Time, d entryData) error {
	if err := s.checkDay(day); err != nil {
		return err
	}
	if err := checkEntry(&d); err != nil {
		return err
	}
	return s.store.saveEntry(ctx, owner, day, d, s.clock())
}

// ---- Resumen de la mañana ----

// todayLines son las líneas de los hábitos para el resumen de la mañana de
// la Agenda: lo que toca hoy, cómo fue ayer y las rachas. Sin hábitos,
// ninguna.
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	v, err := s.load(ctx, owner)
	if err != nil {
		return nil, err
	}
	type streak struct {
		label string
		n     int
	}
	var due []string
	var runs []streak
	var yDone, yTotal int
	yesterday := v.today.Add(-dayUnit)
	for _, h := range v.active() {
		st := v.stats(h)
		if !h.weekly() && h.scheduled(yesterday) && !yesterday.Before(h.startDay(v.m(h))) {
			yTotal++
			if h.done(v.m(h), yesterday) {
				yDone++
			}
		}
		if st.Current > 0 {
			runs = append(runs, streak{h.label() + " " + h.streakUnit(st.Current), st.Current})
		}
		if !st.ScheduledToday {
			continue
		}
		item := h.label()
		switch {
		case h.weekly():
			item += fmt.Sprintf(" (%d de %d esta semana)", st.WeekDone, st.WeekTotal)
		case h.Kind == kindAmount:
			item += " (" + h.goalText() + ")"
		}
		if st.DoneToday {
			item += " ✓"
		}
		due = append(due, item)
	}
	var lines []string
	if len(due) > 0 {
		if len(due) > 8 {
			due = append(due[:7], fmt.Sprintf("y %d más", len(due)-7))
		}
		lines = append(lines, "Hoy: "+strings.Join(due, " · ")+".")
	}
	if yTotal > 0 {
		lines = append(lines, fmt.Sprintf("Ayer: %d de %d hechos.", yDone, yTotal))
	}
	if len(runs) > 0 {
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].n > runs[j].n })
		var parts []string
		for _, r := range runs[:min(len(runs), 4)] {
			parts = append(parts, r.label)
		}
		lines = append(lines, "Rachas: "+strings.Join(parts, " · ")+".")
	}
	return lines, nil
}

// ---- Tarea periódica: los avisos ----

// remindWindow: un recordatorio se manda desde su hora hasta dos horas
// después (si el servidor estaba parado a esa hora, no llega a medianoche).
const remindWindow = 120

func inWindow(hhmm string, minute int) bool {
	t, err := parseHour(hhmm)
	return err == nil && minute >= t && minute < t+remindWindow
}

// run comprueba cada minuto si hay que avisar.
func (s *service) run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 50*time.Second)
		if n, err := s.remind(rctx); err != nil {
			s.log.Warn("habitos: avisos", "err", err)
		} else if n > 0 {
			s.log.Info("habitos: avisos", "avisos", n)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// remind prepara los avisos que tocan ahora: el recordatorio de cada hábito
// que no está hecho (una vez al día) y, a quien lo tenga encendido, el de
// escribir el diario. Devuelve cuántos ha preparado.
func (s *service) remind(ctx context.Context) (int, error) {
	now := s.clock()
	today, minute := dayOf(now), minuteOf(now)
	sent := 0

	s.mu.Lock()
	purge := now.Sub(s.lastPurge) >= time.Hour
	if purge {
		s.lastPurge = now
	}
	s.mu.Unlock()
	if purge {
		if err := s.store.purgeNotices(ctx, now); err != nil {
			return 0, err
		}
	}

	all, err := s.store.allHabits(ctx)
	if err != nil {
		return 0, err
	}
	loaded := map[string]map[int64]marks{}
	for _, h := range all {
		if h.Remind == "" || !inWindow(h.Remind, minute) || h.RemindedOn.Equal(today) {
			continue
		}
		m, ok := loaded[h.Owner]
		if !ok {
			if m, err = s.store.marks(ctx, h.Owner); err != nil {
				return sent, err
			}
			loaded[h.Owner] = m
		}
		hm := m[h.ID]
		if hm == nil {
			hm = marks{}
		}
		// Se reclama el aviso de hoy antes de prepararlo: así no sale dos veces.
		claimed, err := s.store.claimReminder(ctx, h.Owner, h.ID, today)
		if err != nil {
			return sent, err
		}
		st := statsOf(h.habit, hm, today)
		if !claimed || !st.DueToday {
			continue
		}
		if err := s.store.addNotice(ctx, h.Owner, reminderText(h.habit, st), now); err != nil {
			return sent, err
		}
		sent++
	}

	prefs, err := s.store.allSettings(ctx)
	if err != nil {
		return sent, err
	}
	for _, p := range prefs {
		if !p.Nudge || !inWindow(p.nudgeTime(), minute) || p.NudgedOn.Equal(today) {
			continue
		}
		claimed, err := s.store.claimNudge(ctx, p.Owner, today)
		if err != nil {
			return sent, err
		}
		if !claimed {
			continue
		}
		written, err := s.store.hasEntry(ctx, p.Owner, today)
		if err != nil {
			return sent, err
		}
		if written {
			continue
		}
		if err := s.store.addNotice(ctx, p.Owner, "📓 ¿Qué tal el día? Apunta en el diario tu ánimo, tu energía y cómo has dormido.", now); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

// reminderText es el recordatorio de un hábito sin hacer.
func reminderText(h habit, st habitStats) string {
	var b strings.Builder
	b.WriteString("⏰ " + h.label() + ": ")
	switch {
	case h.weekly():
		fmt.Fprintf(&b, "llevas %d de %d esta semana.", st.WeekDone, st.WeekTotal)
	case h.Kind == kindAmount && st.Today > 0:
		b.WriteString("llevas " + trimNum(st.Today, 2) + " de " + h.goalText() + " hoy.")
	case h.Kind == kindAmount:
		b.WriteString("hoy toca " + h.goalText() + ".")
	default:
		b.WriteString("aún no lo has marcado hoy.")
	}
	if st.Current > 0 {
		b.WriteString(" Racha: " + h.streakUnit(st.Current) + ".")
	}
	return b.String()
}
