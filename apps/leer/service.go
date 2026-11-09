package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"homeserver/platform/seal"
)

// service es lo común a la web y a las tools de Talia: guardar, descargar,
// marcar, etiquetar y la tarea de fondo (reintentos y aviso semanal).
type service struct {
	store store
	fetch *fetcher
	log   *slog.Logger
	now   func() time.Time
	sem   chan struct{} // descargas a la vez: el móvil no da para más
}

func newService(st store, f *fetcher, log *slog.Logger, now func() time.Time) *service {
	return &service{store: st, fetch: f, log: log, now: now, sem: make(chan struct{}, 3)}
}

func (s *service) clock() time.Time { return s.now() }

// inputError es un error de lo que ha escrito la persona: se le cuenta tal
// cual.
type inputError string

func (e inputError) Error() string { return string(e) }

// isInput dice si el error se le puede enseñar a la persona.
func isInput(err error) bool {
	var ie inputError
	var fe *fetchError
	return errors.As(err, &ie) || errors.As(err, &fe) || errors.Is(err, errFull)
}

const (
	maxTags   = 10
	maxTagLen = 30
)

// cleanTags limpia las etiquetas: separadas por comas, sin «#» delante, sin
// repetir (sin contar mayúsculas ni tildes) y como mucho 10.
func cleanTags(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		for _, t := range strings.Split(raw, ",") {
			t = strings.Join(strings.Fields(stripControl(strings.TrimLeft(strings.TrimSpace(t), "#"))), " ")
			if t == "" {
				continue
			}
			if utf8.RuneCountInString(t) > maxTagLen {
				return nil, inputError(fmt.Sprintf("Las etiquetas son de como mucho %d letras.", maxTagLen))
			}
			if k := seal.Fold(t); !seen[k] {
				seen[k] = true
				out = append(out, t)
			}
		}
	}
	if len(out) > maxTags {
		return nil, inputError(fmt.Sprintf("Como mucho %d etiquetas por artículo.", maxTags))
	}
	return out, nil
}

// mergeTags añade a las que hay las nuevas que falten.
func mergeTags(have, add []string) ([]string, bool) {
	seen := map[string]bool{}
	for _, t := range have {
		seen[seal.Fold(t)] = true
	}
	changed := false
	for _, t := range add {
		if k := seal.Fold(t); !seen[k] && len(have) < maxTags {
			seen[k] = true
			have = append(have, t)
			changed = true
		}
	}
	return have, changed
}

// save guarda un enlace y lo descarga ya. Si ya estaba, le añade las
// etiquetas nuevas, lo saca del archivo y, si su descarga había fallado, la
// vuelve a intentar. Aunque la descarga falle, el enlace queda guardado.
func (s *service) save(ctx context.Context, owner, raw string, tags []string, origin string) (article, bool, error) {
	u, err := s.fetch.cleanURL(raw)
	if err != nil {
		return article{}, false, err
	}
	if tags, err = cleanTags(tags); err != nil {
		return article{}, false, err
	}
	a, existed, err := s.store.add(ctx, owner, meta{URL: u, Tags: tags, Origin: origin}, s.clock())
	if err != nil {
		return article{}, false, err
	}
	if !existed {
		s.download(ctx, owner, &a, true)
		return a, false, nil
	}
	if merged, changed := mergeTags(a.Tags, tags); changed {
		a.Tags = merged
		if err := s.store.setMeta(ctx, owner, a); err != nil {
			return a, true, err
		}
	}
	if a.Archived {
		no := false
		if err := s.store.setFlags(ctx, owner, a.ID, flags{Archived: &no, Read: &no}, s.clock()); err != nil {
			return a, true, err
		}
	}
	if a.State != stateOK {
		s.download(ctx, owner, &a, true)
	}
	a, err = s.store.get(ctx, owner, a.ID)
	return a, true, err
}

// download descarga el artículo y guarda lo que salga, bien o mal. manual
// es cuando lo pide la persona (al guardar o con «Reintentar»): entonces
// vuelve a contar los intentos automáticos desde cero.
func (s *service) download(ctx context.Context, owner string, a *article, manual bool) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return
	}
	fctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	now := s.clock()
	var ex extracted
	p, err := s.fetch.get(fctx, a.URL)
	if err == nil {
		if final := p.URL.String(); final != a.URL {
			a.Final = final
		}
		ex, err = extract(p, now)
	}
	if ex.Title != "" {
		a.Title = ex.Title
	}
	var blocks []block
	if err == nil {
		a.State, a.Attempts, a.Err = stateOK, 0, ""
		a.Site, a.Author, a.Published, a.Image, a.Excerpt, a.Lang = ex.Site, ex.Author, ex.Published, ex.Image, ex.Excerpt, ex.Lang
		a.Words, a.Minutes, a.Truncated = ex.Words, ex.Minutes, ex.Truncated
		blocks = ex.Blocks
	} else {
		fe := asFetchError(err)
		if manual {
			a.Attempts = 0
		}
		a.State, a.Err = stateError, fe.msg
		a.Attempts++
		if fe.permanent {
			a.Attempts = maxAttempts
		}
		a.Words, a.Minutes, a.Truncated = 0, 0, false
		s.log.Info("descarga fallida", "motivo", fe.msg) // sin la dirección
	}
	a.Fetched = now
	if err := s.store.saveFetch(context.WithoutCancel(ctx), owner, *a, blocks, now); err != nil && !errors.Is(err, errNotFound) {
		s.log.Error("guardar descarga", "err", err)
	}
}

// retry vuelve a descargar un artículo porque lo pide la persona.
func (s *service) retry(ctx context.Context, owner string, id int64) (article, error) {
	a, err := s.store.get(ctx, owner, id)
	if err != nil {
		return a, err
	}
	s.download(ctx, owner, &a, true)
	return s.store.get(ctx, owner, id)
}

// mark cambia leído, favorito o archivado. Archivar es darlo por leído.
func (s *service) mark(ctx context.Context, owner string, id int64, f flags) (article, error) {
	if f.Archived != nil && *f.Archived && f.Read == nil {
		yes := true
		f.Read = &yes
	}
	if err := s.store.setFlags(ctx, owner, id, f, s.clock()); err != nil {
		return article{}, err
	}
	return s.store.get(ctx, owner, id)
}

// setTags cambia todas las etiquetas de un artículo.
func (s *service) setTags(ctx context.Context, owner string, id int64, tags []string) (article, error) {
	tags, err := cleanTags(tags)
	if err != nil {
		return article{}, err
	}
	a, err := s.store.get(ctx, owner, id)
	if err != nil {
		return a, err
	}
	a.Tags = tags
	return a, s.store.setMeta(ctx, owner, a)
}

// tagCount es una etiqueta con cuántos artículos la llevan.
type tagCount struct {
	Name string
	N    int
}

// allTags son las etiquetas de una persona, las más usadas primero.
func (s *service) allTags(ctx context.Context, owner string) ([]tagCount, error) {
	list, _, err := s.store.list(ctx, owner, filter{List: listAll})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	var out []tagCount
	for _, a := range list {
		for _, t := range a.Tags {
			k := seal.Fold(t)
			if i, ok := idx[k]; ok {
				out[i].N++
				continue
			}
			idx[k] = len(out)
			out = append(out, tagCount{Name: t, N: 1})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return seal.Fold(out[i].Name) < seal.Fold(out[j].Name)
	})
	return out, nil
}

// ---- Resumen de la mañana ----

// todayLines es la línea del resumen de la mañana: cuántos quedan por leer y
// uno para hoy (de los cortos, y cada día otro).
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	p, err := s.store.prefs(ctx, owner)
	if err != nil || !p.Summary {
		return nil, err
	}
	list, _, err := s.store.list(ctx, owner, filter{List: listPending})
	if err != nil {
		return nil, err
	}
	var unread, short, ok []article
	for _, a := range list {
		if a.Read {
			continue
		}
		unread = append(unread, a)
		if a.State == stateOK {
			ok = append(ok, a)
			if a.Minutes <= 15 {
				short = append(short, a)
			}
		}
	}
	if len(unread) == 0 {
		return nil, nil
	}
	line := fmt.Sprintf("%d artículos pendientes", len(unread))
	if len(unread) == 1 {
		line = "1 artículo pendiente"
	}
	pick := short
	if len(pick) == 0 {
		pick = ok
	}
	if len(pick) == 0 {
		return []string{line + "."}, nil
	}
	sort.SliceStable(pick, func(i, j int) bool { return pick[i].ID < pick[j].ID })
	day := int(s.clock().Unix() / 86400)
	a := pick[day%len(pick)]
	return []string{fmt.Sprintf("%s · sugerencia: «%s» (%d min)", line, oneLine(a.title(), 90), a.Minutes)}, nil
}

// ---- Tarea de fondo ----

// run reintenta las descargas que fallaron por algo pasajero y manda el
// aviso semanal a quien lo tenga puesto.
func (s *service) run(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 9*time.Minute)
		s.round(rctx)
		cancel()
	}
}

func (s *service) round(ctx context.Context) {
	items, err := s.store.toRetry(ctx, s.clock(), 20)
	if err != nil {
		s.log.Warn("reintentos", "err", err)
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		a := it.article
		s.download(ctx, it.Owner, &a, false)
	}
	s.weekly(ctx)
}

// weekly manda los domingos desde las 10:00 «tienes N pendientes» a quien
// tenga puesto el aviso (por defecto, apagado) y tenga algo sin leer.
func (s *service) weekly(ctx context.Context) {
	now := s.clock()
	if now.Weekday() != time.Sunday || now.Hour() < 10 {
		return
	}
	owners, err := s.store.weeklyOwners(ctx, now)
	if err != nil {
		s.log.Warn("aviso semanal", "err", err)
		return
	}
	for _, o := range owners {
		c, err := s.store.counts(ctx, o)
		if err != nil || c.Unread == 0 {
			continue
		}
		text := fmt.Sprintf("📚 Tienes %d artículos pendientes en Leer después.", c.Unread)
		if c.Unread == 1 {
			text = "📚 Tienes 1 artículo pendiente en Leer después."
		}
		if err := s.store.addNotice(ctx, o, text, now); err != nil {
			s.log.Warn("aviso semanal", "err", err)
		}
	}
}
