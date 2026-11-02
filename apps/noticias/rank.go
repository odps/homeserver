package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	section := func(title, kind string, urls []string, since time.Duration, limit int) error {
		if len(urls) == 0 || limit <= 0 {
			return nil
		}
		items, err := s.itemsSince(ctx, urls, now.Add(-since))
		if err != nil {
			return err
		}
		sec := digestSection{Title: title, Kind: kind}
		if kind == "red" {
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
	errs = append(errs, section("Lo más importante", "espana", spain, 30*time.Hour, p.TopCount))
	if p.World {
		errs = append(errs, section("En el mundo", "mundo", world, 30*time.Hour, p.WorldCount))
	}
	for _, pr := range presets {
		if pr.Group == "tema" && enabled[pr.Key] {
			errs = append(errs, section(pr.Name, "tema", pr.Feeds(), 36*time.Hour, p.TopicCount))
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
		errs = append(errs, section(t.Name, "interes", urls, 48*time.Hour, p.TopicCount))
	}
	for _, pr := range presets {
		if pr.Group == "redes" && enabled[pr.Key] {
			errs = append(errs, section(pr.Name, "red", pr.Feeds(), 6*time.Hour, p.TopicCount))
		}
	}
	for _, r := range p.Reddit {
		errs = append(errs, section("r/"+r, "red", []string{redditFeedURL(r)}, 6*time.Hour, p.TopicCount))
	}
	return d, errors.Join(errs...)
}

// Bloques del resumen del día: la marca que se le pide al modelo, el título
// que se enseña y su icono en Telegram.
var summaryParts = []struct{ mark, title, icon string }{
	{"ESPAÑA", "España", "🗞"},
	{"MUNDO", "En el mundo", "🌍"},
	{"INTERESES", "Tus intereses", "⭐"},
}

// summaryPrompt es lo que se le pasa al modelo para el resumen del día: las
// noticias de España, del mundo y de cada interés (y categoría elegida), para
// que lo devuelva en bloques separados. Vacío si no hay nada.
func summaryPrompt(d digest) string {
	var spain, world, mine strings.Builder
	for _, sec := range d.Sections {
		switch sec.Kind {
		case "espana":
			for i, it := range sec.Items {
				if i == 8 {
					break
				}
				fmt.Fprintf(&spain, "- %s (%d medios). %s\n", it.Title, len(it.Others)+1, it.Summary)
			}
		case "mundo":
			for i, it := range sec.Items {
				if i == 6 {
					break
				}
				fmt.Fprintf(&world, "- %s (%d medios). %s\n", it.Title, len(it.Others)+1, it.Summary)
			}
		case "interes", "tema":
			if mine.Len() > 6000 || len(sec.Items) == 0 {
				continue
			}
			fmt.Fprintf(&mine, "[%s]\n", sec.Title)
			for i, it := range sec.Items {
				if i == 3 {
					break
				}
				fmt.Fprintf(&mine, "- %s. %s\n", it.Title, truncate(it.Summary, 200))
			}
		}
	}
	if spain.Len()+world.Len()+mine.Len() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Resume las noticias de hoy en bloques separados, con este formato exacto: la marca del bloque sola en una línea " +
		"(ESPAÑA, MUNDO, INTERESES) y debajo una línea por noticia que empiece por «• », de la más a la menos relevante. " +
		"ESPAÑA: de 3 a 5 líneas. MUNDO: de 2 a 4. INTERESES: una línea por interés con noticias, que empiece por su nombre " +
		"y dos puntos (por ejemplo «• Salud mental: …»). Cada línea, una o dos frases claras. Omite el bloque que no tenga " +
		"noticias. Usa solo lo que hay aquí debajo.\n")
	for _, part := range []struct {
		mark string
		text *strings.Builder
	}{{"ESPAÑA", &spain}, {"MUNDO", &world}, {"INTERESES", &mine}} {
		if part.text.Len() > 0 {
			fmt.Fprintf(&b, "\n== %s ==\n%s", part.mark, part.text.String())
		}
	}
	return b.String()
}

// parseBlocks lee la respuesta del modelo en bloques. Si no trae las marcas,
// todo va en un bloque sin título.
func parseBlocks(text string) []summaryBlock {
	var out []summaryBlock
	cur := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		head := strings.ToUpper(strings.Trim(line, " =#*:-_[]()"))
		head = strings.TrimSpace(strings.TrimLeftFunc(head, func(r rune) bool { return !unicode.IsLetter(r) }))
		found := false
		for _, part := range summaryParts {
			if head == part.mark || head == strings.ToUpper(part.title) || (part.mark == "ESPAÑA" && head == "ESPANA") {
				out = append(out, summaryBlock{Title: part.title})
				cur, found = len(out)-1, true
			}
		}
		if found {
			continue
		}
		if cur < 0 {
			out = append(out, summaryBlock{})
			cur = 0
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "•-*·"))
		if line != "" {
			out[cur].Lines = append(out[cur].Lines, line)
		}
	}
	return slices.DeleteFunc(out, func(b summaryBlock) bool { return len(b.Lines) == 0 })
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// sectionIcon distingue en Telegram de qué es cada sección.
func sectionIcon(kind string) string {
	switch kind {
	case "espana":
		return "🗞"
	case "mundo":
		return "🌍"
	case "interes", "tema":
		return "⭐"
	case "red":
		return "💬"
	}
	return "▪"
}

// plainDigest es el digest en texto para Telegram o el asistente: primero
// el resumen por bloques y, tras una raya, los titulares de cada sección,
// cada cosa con su título y separada por una línea en blanco.
func plainDigest(d digest, perSection int) string {
	var b strings.Builder
	if d.Summary != "" {
		b.WriteString(d.Summary + "\n\n")
	}
	for _, blk := range d.Blocks {
		if blk.Title != "" {
			icon := ""
			for _, part := range summaryParts {
				if part.title == blk.Title {
					icon = part.icon + " "
				}
			}
			b.WriteString(icon + strings.ToUpper(blk.Title) + "\n")
		}
		for _, l := range blk.Lines {
			b.WriteString("• " + l + "\n")
		}
		b.WriteString("\n")
	}
	if (d.Summary != "" || len(d.Blocks) > 0) && len(d.Sections) > 0 {
		b.WriteString("━━━━━━━━━━\nTITULARES\n\n")
	}
	for _, sec := range d.Sections {
		b.WriteString(sectionIcon(sec.Kind) + " " + sec.Title + "\n")
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
