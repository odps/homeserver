package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Tareas programadas: leer los feeds cada 30 minutos y preparar el digest de
// cada cuenta a su hora (con el resumen del asistente, si lo quiere).

type service struct {
	store      store
	fetch      *fetcher
	log        *slog.Logger
	summarizer func(ctx context.Context, prompt string) (string, error) // nil: sin resumen
	mu         sync.Mutex                                               // una recogida a la vez
}

// collect lee todos los feeds (de 4 en 4) y guarda lo nuevo.
func (s *service) collect(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	urls, err := s.store.feedURLs(ctx)
	if err != nil {
		s.log.Warn("feeds", "err", err)
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			f, err := s.fetch.fetch(fctx, u)
			s.store.feedChecked(ctx, u, f.Title, err)
			if err != nil {
				s.log.Warn("leer feed", "url", u, "err", err)
				return
			}
			source := f.Title
			if g, ok := generalByURL(u); ok {
				source, _, _ = strings.Cut(g.Name, " · ") // «El País · Internacional» cuenta como El País
			}
			for i := range f.Items {
				if f.Items[i].Source == "" {
					f.Items[i].Source = source
				}
			}
			if _, err := s.store.saveItems(ctx, u, f.Items); err != nil {
				s.log.Warn("guardar noticias", "url", u, "err", err)
			}
		}()
	}
	wg.Wait()
	if err := s.store.prune(ctx); err != nil {
		s.log.Warn("limpiar noticias", "err", err)
	}
}

// prepare hace (o rehace) el digest de hoy de una cuenta.
func (s *service) prepare(ctx context.Context, user string, now time.Time) (digest, error) {
	d, err := s.store.buildDigest(ctx, user, now)
	if err != nil {
		return d, err
	}
	p, _ := s.store.prefs(ctx, user)
	if p.AISummary && s.summarizer != nil {
		if prompt := summaryPrompt(d); prompt != "" {
			sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			text, err := s.summarizer(sctx, prompt)
			cancel()
			if err != nil {
				s.log.Warn("resumen del día", "usuario", user, "err", err)
			} else {
				d.Summary = text
			}
		}
	}
	return d, s.store.saveDigest(ctx, user, d)
}

// today devuelve el digest de hoy y, si aún no está (o no hay nada), lo prepara.
func (s *service) today(ctx context.Context, user string, now time.Time) (digest, error) {
	day := now.In(madrid).Format("2006-01-02")
	if d, err := s.store.digest(ctx, user, day); err == nil && len(d.Sections) > 0 {
		return d, nil
	} else if err != nil && !errors.Is(err, errNotFound) {
		return d, err
	}
	return s.prepare(ctx, user, now)
}

// run recoge al arrancar y cada 30 minutos; cada 5 minutos mira a quién le
// toca su digest.
func (s *service) run(ctx context.Context) {
	s.collect(ctx)
	collectTick := time.NewTicker(30 * time.Minute)
	digestTick := time.NewTicker(5 * time.Minute)
	defer collectTick.Stop()
	defer digestTick.Stop()
	for {
		s.dueDigests(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-collectTick.C:
			s.collect(ctx)
		case <-digestTick.C:
		}
	}
}

func (s *service) dueDigests(ctx context.Context, now time.Time) {
	users, err := s.store.users(ctx)
	if err != nil {
		return
	}
	local := now.In(madrid)
	day := local.Format("2006-01-02")
	for _, u := range users {
		p, err := s.store.prefs(ctx, u)
		if err != nil || local.Format("15:04") < p.DigestAt {
			continue
		}
		if d, err := s.store.digest(ctx, u, day); err == nil && d.Created.In(madrid).Format("15:04") >= p.DigestAt {
			continue // ya está el de hoy (preparado a su hora o después)
		}
		if _, err := s.prepare(ctx, u, now); err != nil {
			s.log.Warn("preparar digest", "usuario", u, "err", err)
		} else {
			s.log.Info("digest preparado", "usuario", u, "dia", day)
		}
	}
}

// assistantSummarizer pide el resumen al asistente (que tiene el modelo y sus
// claves); responde solo a peticiones del propio servidor.
func assistantSummarizer(base string) func(context.Context, string) (string, error) {
	client := &http.Client{Timeout: 100 * time.Second}
	return func(ctx context.Context, prompt string) (string, error) {
		body, _ := json.Marshal(map[string]string{"prompt": prompt})
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/interno/resumir", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("el asistente no responde: %w", err)
		}
		defer resp.Body.Close()
		var out struct{ Texto, Error string }
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("asistente: %s %s", resp.Status, out.Error)
		}
		return strings.TrimSpace(out.Texto), nil
	}
}
