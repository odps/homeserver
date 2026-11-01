package main

import (
	"net/url"
	"regexp"
	"strings"
)

// Medios generales: los de España alimentan «Lo más importante» y los
// internacionales, «En el mundo». Cada cuenta puede desactivar los que no
// quiera. Todos son RSS públicos de los propios medios.
type generalSource struct {
	Key   string
	Name  string
	URL   string
	Group string // es o mundo
}

var generalSources = []generalSource{
	{"elpais", "El País", "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/portada", "es"},
	{"elpais-int", "El País · Internacional", "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/internacional/portada", "es"},
	{"eldiario", "elDiario.es", "https://www.eldiario.es/rss/", "es"},
	{"elmundo", "El Mundo", "https://www.elmundo.es/rss/portada.xml", "es"},
	{"abc", "ABC", "https://www.abc.es/rss/2.0/portada/", "es"},
	{"lavanguardia", "La Vanguardia", "https://www.lavanguardia.com/rss/home.xml", "es"},
	{"20minutos", "20minutos", "https://www.20minutos.es/rss/", "es"},
	{"europapress", "Europa Press · Nacional", "https://www.europapress.es/rss/rss.aspx?ch=00066", "es"},
	{"europapress-int", "Europa Press · Internacional", "https://www.europapress.es/rss/rss.aspx?ch=00069", "es"},
	{"bbcmundo", "BBC Mundo", "https://feeds.bbci.co.uk/mundo/rss.xml", "es"},
	{"bbc", "BBC News", "https://feeds.bbci.co.uk/news/world/rss.xml", "mundo"},
	{"guardian", "The Guardian", "https://www.theguardian.com/world/rss", "mundo"},
	{"nyt", "The New York Times", "https://rss.nytimes.com/services/xml/rss/nyt/World.xml", "mundo"},
	{"aljazeera", "Al Jazeera", "https://www.aljazeera.com/xml/rss/all.xml", "mundo"},
	{"france24", "France 24", "https://www.france24.com/en/rss", "mundo"},
	{"dw", "DW", "https://rss.dw.com/xml/rss-en-world", "mundo"},
	{"npr", "NPR", "https://feeds.npr.org/1004/rss.xml", "mundo"},
	{"lemonde", "Le Monde", "https://www.lemonde.fr/en/international/rss_full.xml", "mundo"},
}

func generalByURL(u string) (generalSource, bool) {
	for _, s := range generalSources {
		if s.URL == u {
			return s, true
		}
	}
	return generalSource{}, false
}

// Raíces de Google News y Reddit (sustituibles en los tests).
var (
	googleNewsRoot = "https://news.google.com/rss"
	redditRoot     = "https://www.reddit.com"
	mastodonRoot   = "https://mastodon.social"
	blueskyRoot    = "https://public.api.bsky.app"
	googleTrends   = "https://trends.google.com/trending/rss?geo=ES"
)

const gnLocale = "hl=es&gl=ES&ceid=ES:es"

// topicFeedURL es el feed de búsqueda de un interés: noticias en español de
// los últimos días que hablan de la consulta.
func topicFeedURL(query string) string {
	return googleNewsRoot + "/search?" + url.Values{"q": {query + " when:2d"}, "hl": {"es"}, "gl": {"ES"}, "ceid": {"ES:es"}}.Encode()
}

// gnTopic es una sección oficial de Google News (TECHNOLOGY, SPORTS…).
func gnTopic(topic string) string {
	return googleNewsRoot + "/headlines/section/topic/" + topic + "?" + gnLocale
}

// preset es una categoría lista para activar: varios feeds que se juntan en
// una sección. Las de redes («redes») conservan el orden de la propia red
// (lo más votado o compartido primero) en vez de agruparse por cobertura.
type preset struct {
	Key   string
	Name  string
	Group string // tema o redes
	Feeds func() []string
	Hint  string
}

func feeds(urls ...string) func() []string { return func() []string { return urls } }

var presets = []preset{
	{"tecnologia", "Tecnología", "tema", func() []string {
		return []string{gnTopic("TECHNOLOGY"), "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/tecnologia/portada",
			"https://www.xataka.com/feedburner.xml", "https://www.theverge.com/rss/index.xml"}
	}, "Google News, El País, Xataka, The Verge"},
	{"ciencia", "Ciencia", "tema", func() []string {
		return []string{gnTopic("SCIENCE"), "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/ciencia/portada"}
	}, "Google News, El País"},
	{"economia", "Economía", "tema", func() []string {
		return []string{gnTopic("BUSINESS"), "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/economia/portada",
			"https://e00-expansion.uecdn.es/rss/portada.xml", "https://cincodias.elpais.com/rss/cincodias/portada.xml"}
	}, "Google News, El País, Expansión, Cinco Días"},
	{"deportes", "Deportes", "tema", func() []string {
		return []string{gnTopic("SPORTS"), "https://e00-marca.uecdn.es/rss/portada.xml", "https://www.mundodeportivo.com/rss/home.xml",
			"https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/deportes/portada"}
	}, "Google News, Marca, Mundo Deportivo, El País"},
	{"salud", "Salud", "tema", func() []string {
		return []string{gnTopic("HEALTH"), "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/salud-y-bienestar/portada"}
	}, "Google News, El País"},
	{"cultura", "Cultura", "tema", func() []string {
		return []string{"https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/cultura/portada", topicFeedURL("cultura libros exposición")}
	}, "El País, Google News"},
	{"cine", "Cine y series", "tema", func() []string {
		return []string{gnTopic("ENTERTAINMENT"), "https://www.espinof.com/feedburner.xml"}
	}, "Google News, Espinof"},
	{"videojuegos", "Videojuegos", "tema", func() []string {
		return []string{"https://www.3djuegos.com/feedburner.xml", topicFeedURL("videojuegos")}
	}, "3DJuegos, Google News"},
	{"motor", "Motor", "tema", func() []string {
		return []string{"https://www.motorpasion.com/feedburner.xml", topicFeedURL("coches motor")}
	}, "Motorpasión, Google News"},
	{"musica", "Música", "tema", func() []string {
		return []string{"https://jenesaispop.com/feed/", topicFeedURL("música conciertos")}
	}, "Jenesaispop, Google News"},
	{"tendencias", "Tendencias en Google", "redes", func() []string { return []string{googleTrends} }, "Lo más buscado hoy en España"},
	{"hackernews", "Hacker News", "redes", feeds("https://hnrss.org/frontpage"), "Portada de Hacker News"},
	{"mastodon", "Lo más compartido en Mastodon", "redes", func() []string { return []string{mastodonRoot + "/api/v1/trends/links?limit=20"} }, "Enlaces en tendencia (mastodon.social)"},
	{"bluesky", "Tendencias en Bluesky", "redes", func() []string { return []string{blueskyRoot + "/xrpc/app.bsky.unspecced.getTrendingTopics"} }, "Temas del momento"},
}

func presetByKey(k string) (preset, bool) {
	for _, p := range presets {
		if p.Key == k {
			return p, true
		}
	}
	return preset{}, false
}

var reSubreddit = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)

// cleanSubreddit acepta «golang», «r/golang» o la URL del subforo.
func cleanSubreddit(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "/r/"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "r/")
	s = strings.Trim(s, "/ ")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if !reSubreddit.MatchString(s) {
		return ""
	}
	return s
}

// redditFeedURL es lo más votado del día en un subforo.
func redditFeedURL(sub string) string { return redditRoot + "/r/" + sub + "/top/.rss?t=day" }

// ranked: feeds en los que manda el orden de la propia red (tendencias,
// votos), no la fecha ni la cobertura.
func ranked(u string) bool {
	if strings.HasPrefix(u, redditRoot+"/r/") {
		return true
	}
	for _, p := range presets {
		if p.Group == "redes" {
			for _, f := range p.Feeds() {
				if f == u {
					return true
				}
			}
		}
	}
	return false
}
