package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"
)

type service struct {
	store store
	log   *slog.Logger
	now   func() time.Time // nil: time.Now
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

var errNothing = errors.New("no he entendido nada que añadir")

// addText interpreta el texto («leche, 2 kg de patatas y pan») y lo añade.
func (s *service) addText(ctx context.Context, owner, text, source string) ([]addResult, error) {
	list := parseAdd(text)
	if len(list) == 0 {
		return nil, errNothing
	}
	return s.store.add(ctx, owner, list, source, s.clock())
}

// addName añade una cosa tal cual (una sugerencia), sin partir el nombre.
func (s *service) addName(ctx context.Context, owner, name, source string) ([]addResult, error) {
	name = clip(name, maxName)
	if name == "" || nameKey(name) == "" {
		return nil, errNothing
	}
	return s.store.add(ctx, owner, []parsed{{Name: name}}, source, s.clock())
}

// ---- Sugerencias, sin IA ----

type suggestion struct {
	Name    string
	Times   int
	AvgDays float64
	Since   int // días desde la última compra
	token   int64
}

const maxSuggest = 8

// suggestions son «se te estará acabando» (lo que, por su intervalo medio,
// toca comprar ya) y «lo de siempre» (lo más comprado). Nada de lo que ya
// está en la lista, y nada repetido entre las dos.
func (s *service) suggestions(ctx context.Context, owner string, inList []item) (running, usual []suggestion, err error) {
	hist, err := s.store.history(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	have := map[int64]bool{}
	for _, it := range inList {
		have[it.token] = true
	}
	today := dayOf(s.clock())
	type due struct {
		suggestion
		ratio float64
	}
	var dues []due
	taken := map[int64]bool{}
	for _, h := range hist {
		if have[h.token] || h.Intervals < 1 || h.AvgDays < 1 {
			continue
		}
		since := int(math.Round(today.Sub(h.Last).Hours() / 24))
		if float64(since) >= math.Round(h.AvgDays) {
			dues = append(dues, due{suggestion{Name: h.Name, Times: h.Times, AvgDays: h.AvgDays, Since: since, token: h.token}, float64(since) / h.AvgDays})
		}
	}
	sort.SliceStable(dues, func(i, j int) bool { return dues[i].ratio > dues[j].ratio })
	for _, d := range dues {
		if len(running) == maxSuggest {
			break
		}
		running = append(running, d.suggestion)
		taken[d.token] = true
	}
	for _, h := range hist {
		if len(usual) == maxSuggest {
			break
		}
		if have[h.token] || taken[h.token] || h.Times < 2 {
			continue
		}
		since := int(math.Round(today.Sub(h.Last).Hours() / 24))
		usual = append(usual, suggestion{Name: h.Name, Times: h.Times, AvgDays: h.AvgDays, Since: since})
	}
	return running, usual, nil
}

// ---- Buscar por nombre (Talia) ----

var errAmbiguous = errors.New("hay varias")

// findByName busca en la lista por el nombre normalizado: primero igual y,
// si no, las que contengan todas sus palabras. wantDone filtra por estado
// (nil: cualquiera).
func (s *service) findByName(ctx context.Context, owner, name string, wantDone *bool) ([]item, error) {
	list, err := s.store.items(ctx, owner)
	if err != nil {
		return nil, err
	}
	key := nameKey(name)
	if key == "" {
		return nil, errNotFound
	}
	ok := func(it item) bool { return wantDone == nil || it.Done == *wantDone }
	var exact, partial []item
	words := strings.Fields(key)
	for _, it := range list {
		if !ok(it) {
			continue
		}
		k := nameKey(it.Name)
		if k == key {
			exact = append(exact, it)
			continue
		}
		have := map[string]bool{}
		for _, w := range strings.Fields(k) {
			have[w] = true
		}
		all := true
		for _, w := range words {
			if !have[w] {
				all = false
				break
			}
		}
		if all {
			partial = append(partial, it)
		}
	}
	switch {
	case len(exact) > 0:
		return exact, nil
	case len(partial) == 1:
		return partial, nil
	case len(partial) > 1:
		return partial, errAmbiguous
	}
	return nil, errNotFound
}

// ---- Tarea periódica ----

// run borra cada día lo que ya no hay que guardar.
func (s *service) run(ctx context.Context) {
	tick := time.NewTicker(12 * time.Hour)
	defer tick.Stop()
	for {
		if h, p, err := s.store.purge(ctx, s.clock()); err != nil {
			s.log.Warn("borrar lo antiguo", "err", err)
		} else if h+p > 0 {
			s.log.Info("borrado lo antiguo", "historial", h, "secciones", p)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
