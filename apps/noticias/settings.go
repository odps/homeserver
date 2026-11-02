package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Ajustes de cada persona: las mismas comprobaciones y cambios para la web y
// para el asistente (tools MCP).

const maxSubreddits = 10

var errPrefs = errors.New("Revisa los ajustes: la hora como 07:00, de 3 a 30 noticias generales y del mundo, y de 1 a 15 por sección.")

// check comprueba los ajustes y deja la hora como 07:00.
func (p *prefs) check() error {
	t, err := time.Parse("15:04", strings.TrimSpace(p.DigestAt))
	if err != nil || p.TopCount < 3 || p.TopCount > 30 || p.WorldCount < 3 || p.WorldCount > 30 || p.TopicCount < 1 || p.TopicCount > 15 {
		return errPrefs
	}
	p.DigestAt = t.Format("15:04")
	if len(p.Reddit) > maxSubreddits {
		return fmt.Errorf("como mucho %d subforos", maxSubreddits)
	}
	return nil
}

// subreddits limpia una lista de subforos (golang, r/golang o su enlace) sin
// repetir; aparte devuelve los que no tienen un nombre válido.
func subreddits(list []string) (subs, bad []string) {
	subs = []string{}
	for _, f := range list {
		if sub := cleanSubreddit(f); sub == "" {
			bad = append(bad, f)
		} else if !slices.Contains(subs, sub) {
			subs = append(subs, sub)
		}
	}
	return subs, bad
}

// cleanTopic limpia el nombre y la búsqueda de un interés.
func cleanTopic(name, query string) (string, string, error) {
	name, query = cleanText(name, 60), cleanText(query, 120)
	if name == "" {
		return "", "", errors.New("ponle un nombre al interés")
	}
	return name, query, nil
}

// addTopic añade un interés y recoge ya sus noticias (en segundo plano).
func (s *service) addTopic(ctx context.Context, user, name, query string) (int64, error) {
	name, query, err := cleanTopic(name, query)
	if err != nil {
		return 0, err
	}
	id, err := s.store.addTopic(ctx, user, name, query)
	if err == nil {
		s.collectSoon(ctx)
	}
	return id, err
}

func (s *service) updateTopic(ctx context.Context, user string, id int64, name, query string) error {
	name, query, err := cleanTopic(name, query)
	if err != nil {
		return err
	}
	if err := s.store.updateTopic(ctx, user, id, name, query); err != nil {
		return err
	}
	s.collectSoon(ctx)
	return nil
}

// addFeed pone un feed propio en un interés. Se lee antes de guardarlo (mejor
// saber ya que no es un feed) y lo que trae se guarda; devuelve su título.
func (s *service) addFeed(ctx context.Context, user string, topicID int64, raw string) (string, error) {
	u, err := validFeedURL(raw, s.fetch.allowHTTP)
	if err != nil {
		return "", err
	}
	fctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	f, err := s.fetch.fetch(fctx, u)
	cancel()
	if err != nil {
		return "", fmt.Errorf("no se ha podido leer el feed: %v", err)
	}
	s.store.feedChecked(ctx, u, f.Title, nil)
	for i := range f.Items {
		if f.Items[i].Source == "" {
			f.Items[i].Source = f.Title
		}
	}
	s.store.saveItems(ctx, u, f.Items)
	return f.Title, s.store.addOwnFeed(ctx, user, topicID, u)
}

// knownFeeds son las URL cuyo estado se enseña: los medios generales y los
// feeds propios de la persona.
func knownFeeds(topics []topic) []string {
	var out []string
	for _, g := range generalSources {
		out = append(out, g.URL)
	}
	for _, t := range topics {
		for _, f := range t.Feeds {
			out = append(out, f.URL)
		}
	}
	return out
}
