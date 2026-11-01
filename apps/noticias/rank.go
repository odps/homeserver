package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Relevancia sin IA: las noticias que cuentan lo mismo se agrupan por las
// palabras de su titular, y una historia que cubren más medios va antes.

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`el la los las un una unos unas de del al a ante con contra desde en entre hacia hasta para por
		segun sin sobre tras y o u e ni que se su sus es son ha han ser fue era muy mas pero como cuando donde ya no si lo le les
		este esta estos estas ese esa eso esto nos tras dice tiene tras hoy ayer anos ano dia dias the of and to in for on with at by
		from is are was be it its an as that this has have will new says after over`) {
		m[w] = true
	}
	return m
}()

var unaccent = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n", "à", "a", "è", "e", "ò", "o", "ç", "c")

// words son las palabras significativas de un texto, sin acentos.
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(unaccent.Replace(strings.ToLower(s)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !stopwords[w] && (len([]rune(w)) >= 3 || unicode.IsDigit([]rune(w)[0])) {
			out = append(out, w)
		}
	}
	return out
}

type cluster struct {
	items   []item
	words   map[string]bool
	sources map[string]bool
}

func (c *cluster) latest() time.Time { return c.items[0].Published }

// lead es la noticia que representa al grupo: la más reciente con extracto.
func (c *cluster) lead() item {
	for _, it := range c.items {
		if it.Summary != "" {
			return it
		}
	}
	return c.items[0]
}

func (c *cluster) others() []string {
	lead := c.lead().Source
	var out []string
	for s := range c.sources {
		if s != lead {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// group agrupa noticias parecidas: comparten al menos 3 palabras y estas son
// la mayoría de las del titular más corto.
func group(items []item) []*cluster {
	sorted := append([]item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Published.After(sorted[j].Published) })
	var clusters []*cluster
	for _, it := range sorted {
		ws := map[string]bool{}
		for _, w := range words(it.Title) {
			ws[w] = true
		}
		var best *cluster
		bestSim := 0.0
		for _, c := range clusters {
			common := 0
			for w := range ws {
				if c.words[w] {
					common++
				}
			}
			smaller := min(len(ws), len(c.words))
			if common < 3 || smaller == 0 {
				continue
			}
			if sim := float64(common) / float64(smaller); sim >= 0.6 && sim > bestSim {
				best, bestSim = c, sim
			}
		}
		if best == nil {
			best = &cluster{words: ws, sources: map[string]bool{}}
			clusters = append(clusters, best)
		}
		best.items = append(best.items, it)
		best.sources[it.Source] = true
	}
	return clusters
}

// rankClusters ordena por número de medios y, a igualdad, por lo reciente.
func rankClusters(cs []*cluster) {
	sort.SliceStable(cs, func(i, j int) bool {
		if len(cs[i].sources) != len(cs[j].sources) {
			return len(cs[i].sources) > len(cs[j].sources)
		}
		return cs[i].latest().After(cs[j].latest())
	})
}

func toDigestItem(c *cluster) digestItem {
	l := c.lead()
	return digestItem{Title: l.Title, Link: l.Link, Source: l.Source, Summary: l.Summary, Time: l.Published, Others: c.others()}
}

// buildDigest prepara el digest de una cuenta con lo recogido hasta ahora:
// lo más importante (medios de España), en el mundo (internacionales), las
// categorías que tenga activadas, sus intereses, las redes y sus subforos.
func (s store) buildDigest(ctx context.Context, user string, now time.Time) (digest, error) {
	p, err := s.prefs(ctx, user)
	if err != nil {
		return digest{}, err
	}
	topics, err := s.topics(ctx, user)
	if err != nil {
		return digest{}, err
	}
	d := digest{Day: now.In(madrid).Format("2006-01-02"), Created: now}
	used := map[string]bool{} // enlaces ya en el digest

	// section junta las noticias de unos feeds. Por cobertura: agrupa y van
	// antes las que cuentan más medios. Por orden de la red (byRank): tal
	// cual vienen, sin agrupar.
	section := func(title string, urls []string, since time.Duration, limit int, byRank bool) error {
		if len(urls) == 0 || limit <= 0 {
			return nil
		}
		items, err := s.itemsSince(ctx, urls, now.Add(-since))
		if err != nil {
			return err
		}
		sec := digestSection{Title: title}
		if byRank {
			sec.Kind = "red"
			for _, it := range items { // ya vienen por «fecha» = posición, de más a menos
				if len(sec.Items) >= limit {
					break
				}
				if !used[it.Link] {
					sec.Items = append(sec.Items, digestItem{Title: it.Title, Link: it.Link, Source: it.Source, Summary: it.Summary, Time: it.Published})
					used[it.Link] = true
				}
			}
		} else {
			cs := group(items)
			rankClusters(cs)
			for _, c := range cs {
				if len(sec.Items) >= limit {
					break
				}
				if it := toDigestItem(c); !used[it.Link] {
					sec.Items = append(sec.Items, it)
					used[it.Link] = true
				}
			}
		}
		if len(sec.Items) > 0 {
			d.Sections = append(d.Sections, sec)
		}
		return nil
	}

	disabled := map[string]bool{}
	for _, k := range p.Disabled {
		disabled[k] = true
	}
	var spain, world []string
	for _, g := range generalSources {
		switch {
		case disabled[g.Key]:
		case g.Group == "mundo":
			world = append(world, g.URL)
		default:
			spain = append(spain, g.URL)
		}
	}
	enabled := map[string]bool{}
	for _, k := range p.Presets {
		enabled[k] = true
	}
	var errs []error
	errs = append(errs, section("Lo más importante", spain, 30*time.Hour, p.TopCount, false))
	if p.World {
		errs = append(errs, section("En el mundo", world, 30*time.Hour, p.WorldCount, false))
	}
	for _, pr := range presets {
		if pr.Group == "tema" && enabled[pr.Key] {
			errs = append(errs, section(pr.Name, pr.Feeds(), 36*time.Hour, p.TopicCount, false))
		}
	}
	for _, t := range topics {
		var urls []string
		if t.Query != "" {
			urls = append(urls, topicFeedURL(t.Query))
		}
		for _, f := range t.Feeds {
			urls = append(urls, f.URL)
		}
		errs = append(errs, section(t.Name, urls, 48*time.Hour, p.TopicCount, false))
	}
	for _, pr := range presets {
		if pr.Group == "redes" && enabled[pr.Key] {
			errs = append(errs, section(pr.Name, pr.Feeds(), 6*time.Hour, p.TopicCount, true))
		}
	}
	for _, r := range p.Reddit {
		errs = append(errs, section("r/"+r, []string{redditFeedURL(r)}, 6*time.Hour, p.TopicCount, true))
	}
	return d, errors.Join(errs...)
}

// summaryPrompt es lo que se le pasa al modelo para el resumen del día.
func summaryPrompt(d digest) string {
	var b strings.Builder
	b.WriteString("Escribe un resumen de 4 o 5 frases de las noticias más importantes de hoy, de más a menos relevante. Estas son, con el número de medios que las cuentan:\n\n")
	if len(d.Sections) == 0 {
		return ""
	}
	for i, it := range d.Sections[0].Items {
		if i == 8 {
			break
		}
		fmt.Fprintf(&b, "- %s (%d medios). %s\n", it.Title, len(it.Others)+1, it.Summary)
	}
	return b.String()
}

// plainDigest es el digest en texto para Telegram o el asistente.
func plainDigest(d digest, perSection int) string {
	var b strings.Builder
	if d.Summary != "" {
		b.WriteString(d.Summary + "\n\n")
	}
	for _, sec := range d.Sections {
		b.WriteString(sec.Title + "\n")
		for i, it := range sec.Items {
			if i == perSection {
				break
			}
			src := it.Source
			if n := len(it.Others); n > 0 {
				src += fmt.Sprintf(" y %d más", n)
			}
			fmt.Fprintf(&b, "• %s (%s)\n  %s\n", it.Title, src, it.Link)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()
