package main

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type service struct {
	store store
	tv    *tvmaze
	log   *slog.Logger
	now   func() time.Time

	mu       sync.Mutex
	cache    map[int64]cachedShow // series ya descifradas, por id
	dailyOn  time.Time            // día de la última puesta al día
	noticeAt time.Time            // última revisión de avisos
	kickOnce sync.Once
	kickC    chan struct{}
}

type cachedShow struct {
	ver int64     // versión de la fila
	d   *showData // solo lectura: la comparten todas las peticiones
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

// ---- Series descifradas, con caché ----

// showsFor da los datos de unas series, descifrando solo las que han cambiado
// desde la última vez.
func (s *service) showsFor(ctx context.Context, ids []int64) (map[int64]*showData, error) {
	out := map[int64]*showData{}
	if len(ids) == 0 {
		return out, nil
	}
	stamps, err := s.store.showStamps(ctx, ids)
	if err != nil {
		return nil, err
	}
	var stale []int64
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[int64]cachedShow{}
	}
	for id, st := range stamps {
		if c, ok := s.cache[id]; ok && c.ver == st.Version {
			out[id] = c.d
		} else {
			stale = append(stale, id)
		}
	}
	s.mu.Unlock()
	if len(stale) == 0 {
		return out, nil
	}
	got, at, err := s.store.showsData(ctx, stale)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	for id, d := range got {
		s.cache[id] = cachedShow{ver: at[id], d: d}
		out[id] = d
	}
	s.mu.Unlock()
	return out, nil
}

func (s *service) forget(ids []int64) {
	s.mu.Lock()
	for _, id := range ids {
		delete(s.cache, id)
	}
	s.mu.Unlock()
}

// ---- Lo de una persona ----

// showView es una serie seguida con su progreso.
type showView struct {
	follow
	Show      *showData
	Prog      progress
	HasPoster bool
	watched   idSet
}

type view struct {
	shows []showView // por nombre
	now   time.Time
	today time.Time
}

func (s *service) load(ctx context.Context, owner string) (*view, error) {
	now := s.clock()
	v := &view{now: now, today: dayOf(now)}
	follows, err := s.store.follows(ctx, owner)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(follows))
	for _, f := range follows {
		ids = append(ids, f.ShowID)
	}
	data, err := s.showsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	posters, err := s.store.posterSources(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range follows {
		d := data[f.ShowID]
		if d == nil {
			continue
		}
		w := setOf(f.Watched)
		_, hp := posters[f.ShowID]
		v.shows = append(v.shows, showView{follow: f, Show: d, Prog: progressOf(d, w, now), HasPoster: hp, watched: w})
	}
	sort.SliceStable(v.shows, func(i, j int) bool {
		return strings.ToLower(v.shows[i].Show.Name) < strings.ToLower(v.shows[j].Show.Name)
	})
	return v, nil
}

func (v *view) show(id int64) (showView, bool) {
	for _, sv := range v.shows {
		if sv.ID == id {
			return sv, true
		}
	}
	return showView{}, false
}

func (v *view) byTVmaze(tvmazeID int64) (showView, bool) {
	for _, sv := range v.shows {
		if sv.Show.TVmazeID == tvmazeID {
			return sv, true
		}
	}
	return showView{}, false
}

// airing es un capítulo que sale (o ha salido) en unas fechas.
type airing struct {
	View    showView
	Episode episode
}

// week son los capítulos de las series seguidas (salvo las abandonadas) que
// salen entre from y to, en orden de emisión.
func (v *view) week(from, to time.Time) []airing {
	var out []airing
	for _, sv := range v.shows {
		if sv.State == stateAbandoned {
			continue
		}
		for _, e := range between(sv.Show, from, to) {
			out = append(out, airing{sv, e})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := airKey(out[i].Episode), airKey(out[j].Episode)
		if !a.Equal(b) {
			return a.Before(b)
		}
		return strings.ToLower(out[i].View.Show.Name) < strings.ToLower(out[j].View.Show.Name)
	})
	return out
}

// ---- Buscar y seguir ----

func (s *service) search(ctx context.Context, q string) ([]found, error) {
	q = clip(q, 80)
	if q == "" {
		return nil, bad("Escribe el nombre de la serie.")
	}
	return s.tv.search(ctx, q)
}

// freshFor es lo que vale una serie guardada al seguirla (si no, se pide).
const freshFor = 12 * time.Hour

// fetchShow pide una serie a TVmaze y la guarda (con su cartel).
func (s *service) fetchShow(ctx context.Context, tvmazeID int64) (int64, *showData, error) {
	d, err := s.tv.show(ctx, tvmazeID)
	if errors.Is(err, errNoMatch) {
		return 0, nil, bad("TVmaze no tiene esa serie.")
	}
	if err != nil {
		return 0, nil, err
	}
	id, err := s.store.saveShow(ctx, d, s.clock())
	if err != nil {
		return 0, nil, err
	}
	s.fetchPoster(ctx, id, &d)
	return id, &d, nil
}

// fetchPoster pide el cartel si no se tiene o ha cambiado. Si falla, la
// serie sale sin él.
func (s *service) fetchPoster(ctx context.Context, showID int64, d *showData) {
	if d.Image == "" {
		return
	}
	have, err := s.store.posterSources(ctx, []int64{showID})
	if err != nil || have[showID] == s.store.posterToken(d.Image) {
		return
	}
	img, err := s.tv.poster(ctx, d.Image)
	if err != nil {
		return
	}
	if err := s.store.setPoster(ctx, showID, d.Image, img, s.clock()); err != nil {
		s.log.Warn("series: guardar cartel", "err", err)
	}
}

// followShow sigue una serie de TVmaze (los datos se piden en el servidor, no
// se toman del navegador ni de la IA). Si ya la seguía, la devuelve (y le
// pone el estado, si se da).
func (s *service) followShow(ctx context.Context, owner string, tvmazeID int64, state string) (follow, bool, error) {
	if tvmazeID <= 0 || tvmazeID > 1e9 {
		return follow{}, false, bad("Esa serie no es válida.")
	}
	if state != "" && !validState(state) {
		return follow{}, false, bad("Ese estado no es válido.")
	}
	now := s.clock()
	id, checked, err := s.store.showByTVmaze(ctx, tvmazeID)
	var d *showData
	switch {
	case errors.Is(err, errNotFound) || (err == nil && now.Sub(checked) > freshFor):
		id, d, err = s.fetchShow(ctx, tvmazeID)
		if err != nil {
			return follow{}, false, err
		}
	case err != nil:
		return follow{}, false, err
	default:
		got, err := s.showsFor(ctx, []int64{id})
		if err != nil {
			return follow{}, false, err
		}
		if d = got[id]; d == nil {
			return follow{}, false, errNotFound
		}
	}
	fd := followData{State: state}
	if fd.State == "" {
		fd.State = statePending
	}
	// Lo que ya se sabe al seguirla no se avisa: la vuelta ya anunciada y lo
	// que ha salido hoy antes de seguirla.
	today := dayOf(now)
	for _, e := range d.Episodes {
		if e.premiere() && e.Season > 1 && e.day().After(today) {
			fd.Announced = append(fd.Announced, e.Season)
		}
		if e.day().Equal(today) && !noticeDue(e).After(now) {
			fd.Notified = append(fd.Notified, e.ID)
		}
	}
	f, existed, err := s.store.addFollow(ctx, owner, id, fd)
	if err != nil || !existed || state == "" || f.State == state {
		return f, existed, err
	}
	f, err = s.store.updateFollow(ctx, owner, f.ID, func(f *follow) (bool, error) {
		f.State = state
		return true, nil
	})
	return f, true, err
}

// unfollow deja de seguir una serie.
func (s *service) unfollow(ctx context.Context, owner string, id int64) error {
	f, err := s.store.follow(ctx, owner, id)
	if err != nil {
		return err
	}
	if err := s.store.deleteFollow(ctx, owner, id); err != nil {
		return err
	}
	s.forget([]int64{f.ShowID})
	return nil
}

// ---- Marcar vistos y lo demás de una serie ----

// markWatched marca o desmarca capítulos (uno, hasta uno, o una temporada).
// Devuelve la serie y cuántos han cambiado.
func (s *service) markWatched(ctx context.Context, owner string, id int64, mode string, epID int64, season int, on bool) (follow, int, error) {
	f, err := s.store.follow(ctx, owner, id)
	if err != nil {
		return f, 0, err
	}
	got, err := s.showsFor(ctx, []int64{f.ShowID})
	if err != nil {
		return f, 0, err
	}
	d := got[f.ShowID]
	if d == nil {
		return f, 0, errNotFound
	}
	now := s.clock()
	n := 0
	f, err = s.store.updateFollow(ctx, owner, id, func(f *follow) (bool, error) {
		w := setOf(f.Watched)
		var ok bool
		n, ok = mark(d, w, mode, epID, season, on, now)
		if !ok {
			return false, bad("Ese capítulo no es de esta serie.")
		}
		if n == 0 {
			return false, nil
		}
		f.Watched = w.list()
		if on {
			f.State = autoState(f.State, d, w, now)
		}
		return true, nil
	})
	return f, n, err
}

// setMeta guarda el estado, la valoración y la nota de una serie.
func (s *service) setMeta(ctx context.Context, owner string, id int64, state string, rating int, note string) (follow, error) {
	if !validState(state) {
		return follow{}, bad("Ese estado no es válido.")
	}
	if rating < 0 || rating > 5 {
		return follow{}, bad("La valoración va de 1 a 5.")
	}
	return s.store.updateFollow(ctx, owner, id, func(f *follow) (bool, error) {
		f.State, f.Rating, f.Note = state, rating, clip(note, 300)
		return true, nil
	})
}

// ---- Pelis y juegos ----

// itemInput es una peli o un juego tal como llega de la web o de Talia.
type itemInput struct {
	Title, Platform, State, Day, Note, Origin string
	Year, Rating                              int
}

func validKind(k string) bool { return k == kindFilm || k == kindGame }

// buildItem valida una peli o un juego.
func (s *service) buildItem(in itemInput) (itemData, error) {
	today := dayOf(s.clock())
	d := itemData{Title: clip(in.Title, 120), Year: in.Year, Platform: clip(in.Platform, 40), State: in.State, Rating: in.Rating,
		Note: clip(in.Note, 300), Source: "manual", Origin: in.Origin}
	switch {
	case d.Title == "":
		return d, bad("Falta el título.")
	case d.Year != 0 && (d.Year < 1870 || d.Year > today.Year()+5):
		return d, bad("Ese año no es válido.")
	case d.Rating < 0 || d.Rating > 5:
		return d, bad("La valoración va de 1 a 5.")
	}
	if d.State == "" {
		d.State = statePending
	}
	if !validState(d.State) {
		return d, bad("Ese estado no es válido.")
	}
	if t := strings.TrimSpace(in.Day); t != "" {
		day, err := parseDay(t)
		switch {
		case err != nil:
			return d, bad("La fecha va como 2026-11-09.")
		case day.After(today):
			return d, bad("La fecha no puede ser futura.")
		case day.Year() < 1900:
			return d, bad("Esa fecha es demasiado antigua.")
		}
		d.Day = isoDay(day)
	} else if d.State == stateDone {
		d.Day = isoDay(today)
	}
	return d, nil
}

// addItem apunta una peli o un juego. Si ya estaba (mismo título y, si se
// da, año), lo pone al día con lo que venga.
func (s *service) addItem(ctx context.Context, owner, kind string, in itemInput, merge bool) (item, bool, error) {
	if !validKind(kind) {
		return item{}, false, bad("Tipo no válido.")
	}
	d, err := s.buildItem(in)
	if err != nil {
		return item{}, false, err
	}
	if merge {
		list, err := s.store.items(ctx, owner, kind)
		if err != nil {
			return item{}, false, err
		}
		for _, it := range list {
			if sameTitle(it.Title, d.Title) && (in.Year == 0 || it.Year == 0 || it.Year == in.Year) {
				it.Title = d.Title
				if in.Year != 0 {
					it.Year = in.Year
				}
				if in.Platform != "" {
					it.Platform = d.Platform
				}
				if in.State != "" {
					it.State = d.State
					if d.State == stateDone && it.Day == "" {
						it.Day = d.Day
					}
				}
				if in.Rating != 0 {
					it.Rating = d.Rating
				}
				if strings.TrimSpace(in.Day) != "" {
					it.Day = d.Day
				}
				if in.Note != "" {
					it.Note = d.Note
				}
				return it, true, s.store.updateItem(ctx, owner, it)
			}
		}
	}
	id, err := s.store.addItem(ctx, owner, kind, d)
	return item{ID: id, Kind: kind, Created: s.clock(), itemData: d}, false, err
}

func (s *service) updateItem(ctx context.Context, owner string, id int64, in itemInput) (item, error) {
	old, err := s.store.item(ctx, owner, id)
	if err != nil {
		return old, err
	}
	in.Origin = old.Origin
	d, err := s.buildItem(in)
	if err != nil {
		return old, err
	}
	d.Source, d.SourceID = old.Source, old.SourceID
	old.itemData = d
	return old, s.store.updateItem(ctx, owner, old)
}

// sortItems: primero lo que está en curso, luego lo pendiente, y lo
// terminado por fecha (lo último primero).
func sortItems(list []item) {
	rank := map[string]int{stateWatching: 0, statePending: 1, statePaused: 2, stateDone: 3, stateAbandoned: 4}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if rank[a.State] != rank[b.State] {
			return rank[a.State] < rank[b.State]
		}
		if a.Day != b.Day {
			return a.Day > b.Day
		}
		return a.ID > b.ID
	})
}

// ---- Resumen de la mañana ----

// todayLines son las líneas para el resumen de la mañana de la Agenda: los
// capítulos de las series seguidas que salen hoy. Sin ninguno, ninguna.
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	v, err := s.load(ctx, owner)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, a := range v.week(v.today, v.today) {
		lines = append(lines, airingLine(a))
		if len(lines) == 10 {
			break
		}
	}
	return lines, nil
}

// airingLine es «Serie 3x01 «Título», 21:00 · estreno de la temporada 3».
func airingLine(a airing) string {
	e := a.Episode
	s := a.View.Show.Name + " " + epCode(e)
	if e.Name != "" {
		s += " «" + e.Name + "»"
	}
	if e.hasTime() {
		s += ", " + e.At.In(madrid).Format("15:04")
	}
	if p := premiereNote(e); p != "" {
		s += " · " + p
	}
	return s
}

func premiereNote(e episode) string {
	switch {
	case e.premiere() && e.Season == 1:
		return "estreno de la serie"
	case e.premiere():
		return "estreno de la temporada " + strconv.Itoa(e.Season)
	}
	return ""
}

// ---- Tarea de fondo ----

const (
	refreshHour  = 6 // la puesta al día diaria, desde las 6:00
	noticeHour   = 9 // los avisos de capítulos, desde las 9:00 (o a su hora)
	noticeEvery  = 5 * time.Minute
	recheckAfter = 6 * 24 * time.Hour // aunque TVmaze no diga que ha cambiado
	keepNotified = 200
)

func (s *service) kick() {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	select {
	case s.kickC <- struct{}{}:
	default:
	}
}

// run pone al día las series una vez al día y revisa los avisos cada pocos
// minutos.
func (s *service) run(ctx context.Context) {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		s.round(rctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kickC:
		}
	}
}

// round es una pasada: la puesta al día (si toca) y los avisos.
func (s *service) round(ctx context.Context) {
	now := s.clock()
	s.mu.Lock()
	daily := !dayOf(now).Equal(s.dailyOn) && now.In(madrid).Hour() >= refreshHour
	notices := now.Sub(s.noticeAt) >= noticeEvery
	if notices {
		s.noticeAt = now
	}
	s.mu.Unlock()
	if daily && s.tv.ready() {
		err := s.refresh(ctx)
		if err == nil || !errors.Is(err, errSourceWait) {
			s.mu.Lock()
			s.dailyOn = dayOf(now)
			s.mu.Unlock()
		}
		if err != nil && !errors.Is(err, errSourceWait) {
			s.log.Warn("series: puesta al día", "err", err)
		}
	}
	if notices {
		if n, err := s.checkNotices(ctx); err != nil {
			s.log.Warn("series: avisos", "err", err)
		} else if n > 0 {
			s.log.Info("series: avisos", "avisos", n)
		}
	}
}

// refresh vuelve a pedir a TVmaze las series seguidas que han cambiado (según
// /updates/shows) o que llevan días sin mirarse, completa los carteles que
// falten y borra las que ya no sigue nadie.
func (s *service) refresh(ctx context.Context) error {
	gone, err := s.store.gcShows(ctx)
	if err != nil {
		return err
	}
	s.forget(gone)
	stamps, err := s.store.showStamps(ctx, nil)
	if err != nil || len(stamps) == 0 {
		return err
	}
	ids := make([]int64, 0, len(stamps))
	for id := range stamps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	data, err := s.showsFor(ctx, ids)
	if err != nil {
		return err
	}
	upd, uerr := s.tv.updates(ctx, "week")
	if errors.Is(uerr, errSourceWait) {
		return uerr
	}
	now := s.clock()
	for _, id := range ids {
		d, st := data[id], stamps[id]
		if d == nil {
			continue
		}
		need := now.Sub(st.Checked) >= recheckAfter
		if uerr == nil {
			need = need || upd[d.TVmazeID] > d.Updated
		} else {
			need = need || now.Sub(st.Checked) >= 20*time.Hour
		}
		if !need {
			s.fetchPoster(ctx, id, d) // solo pide si falta o ha cambiado
			continue
		}
		nd, err := s.tv.show(ctx, d.TVmazeID)
		switch {
		case errors.Is(err, errNoMatch):
			s.store.touchShow(ctx, id, now) // TVmaze ya no la tiene: se queda lo que había
			continue
		case errors.Is(err, errSourceWait):
			return err
		case err != nil:
			continue
		}
		if _, err := s.store.saveShow(ctx, nd, now); err != nil {
			return err
		}
		s.fetchPoster(ctx, id, &nd)
	}
	return nil
}

// checkNotices prepara los avisos de todas las personas: el capítulo de hoy
// de cada serie seguida (una vez, a partir de las 9:00 o a su hora si es más
// tarde) y la vuelta de una serie cuando se anuncia la fecha de su nueva
// temporada.
func (s *service) checkNotices(ctx context.Context) (int, error) {
	now := s.clock()
	local := now.In(madrid)
	if local.Hour() < noticeHour {
		return 0, nil
	}
	all, err := s.store.allFollows(ctx)
	if err != nil || len(all) == 0 {
		return 0, err
	}
	ids := make([]int64, 0, len(all))
	for _, f := range all {
		ids = append(ids, f.ShowID)
	}
	data, err := s.showsFor(ctx, ids)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, f := range all {
		d := data[f.ShowID]
		if d == nil {
			continue
		}
		// Casi siempre no hay nada: se mira sin bloquear la fila.
		probe := f
		probe.Notified, probe.Announced = append([]int64(nil), f.Notified...), append([]int(nil), f.Announced...)
		if len(dueNotices(&probe, d, now)) == 0 {
			continue
		}
		var texts []string
		_, err := s.store.updateFollow(ctx, f.Owner, f.ID, func(f *follow) (bool, error) {
			texts = dueNotices(f, d, now)
			return len(texts) > 0, nil
		})
		if errors.Is(err, errNotFound) {
			continue // la ha dejado de seguir mientras tanto
		}
		if err != nil {
			return sent, err
		}
		for _, t := range texts {
			if err := s.store.addNotice(ctx, f.Owner, t, now); err != nil {
				return sent, err
			}
			sent++
		}
	}
	return sent, nil
}

// dueNotices son los avisos que tocan ahora de una serie seguida (y los
// apunta en f para no repetirlos): el capítulo de hoy que no ha visto y la
// vuelta anunciada de una temporada. Las abandonadas, nada.
func dueNotices(f *follow, d *showData, now time.Time) []string {
	if f.State == stateAbandoned {
		return nil
	}
	today := dayOf(now)
	var texts []string
	notified, watched := setOf(f.Notified), setOf(f.Watched)
	announced := map[int]bool{}
	for _, n := range f.Announced {
		announced[n] = true
	}
	for _, e := range d.Episodes {
		day := e.day()
		switch {
		case day.Equal(today) && !notified[e.ID] && !watched[e.ID]:
			if now.Before(noticeDue(e)) {
				continue
			}
			texts = append(texts, airingNotice(d, e))
			f.Notified = append(f.Notified, e.ID)
			notified[e.ID] = true
		case e.premiere() && e.Season > 1 && day.After(today) && !announced[e.Season]:
			texts = append(texts, "📅 «"+d.Name+"» vuelve el "+longDay(day)+" con la temporada "+strconv.Itoa(e.Season)+".")
			f.Announced = append(f.Announced, e.Season)
			announced[e.Season] = true
		}
	}
	if len(f.Notified) > keepNotified {
		f.Notified = f.Notified[len(f.Notified)-keepNotified:]
	}
	return texts
}

// noticeDue es cuándo se avisa de un capítulo: a las 9:00 de su día o a su
// hora, si es más tarde.
func noticeDue(e episode) time.Time {
	d := e.day()
	due := time.Date(d.Year(), d.Month(), d.Day(), noticeHour, 0, 0, 0, madrid)
	if e.hasTime() && e.At.After(due) {
		return e.At
	}
	return due
}

// airingNotice es el texto del aviso de un capítulo que sale hoy.
func airingNotice(d *showData, e episode) string {
	title := ""
	if e.Name != "" {
		title = " «" + e.Name + "»"
	}
	at := ""
	if e.hasTime() {
		at = " a las " + e.At.In(madrid).Format("15:04")
	}
	switch {
	case e.premiere() && e.Season == 1:
		return "🎬 Hoy se estrena «" + d.Name + "»" + at + ": " + epCode(e) + title + "."
	case e.premiere():
		return "🎬 Vuelve «" + d.Name + "»: hoy" + at + " empieza la temporada " + strconv.Itoa(e.Season) + " (" + epCode(e) + title + ")."
	case e.hasTime():
		return "📺 Ya ha salido el " + epCode(e) + " de «" + d.Name + "»" + title + " (hoy" + at + ")."
	}
	return "📺 Hoy sale el " + epCode(e) + " de «" + d.Name + "»" + title + "."
}
