package main

import "net/url"

// Medios generales que alimentan «Lo más importante». Cada cuenta puede
// desactivar los que no quiera. Todos son RSS públicos de los propios medios.
type generalSource struct {
	Key  string
	Name string
	URL  string
}

var generalSources = []generalSource{
	{"elpais", "El País", "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/portada"},
	{"elpais-int", "El País · Internacional", "https://feeds.elpais.com/mrss-s/pages/ep/site/elpais.com/section/internacional/portada"},
	{"eldiario", "elDiario.es", "https://www.eldiario.es/rss/"},
	{"elmundo", "El Mundo", "https://www.elmundo.es/rss/portada.xml"},
	{"abc", "ABC", "https://www.abc.es/rss/2.0/portada/"},
	{"lavanguardia", "La Vanguardia", "https://www.lavanguardia.com/rss/home.xml"},
	{"20minutos", "20minutos", "https://www.20minutos.es/rss/"},
	{"europapress", "Europa Press · Nacional", "https://www.europapress.es/rss/rss.aspx?ch=00066"},
	{"europapress-int", "Europa Press · Internacional", "https://www.europapress.es/rss/rss.aspx?ch=00069"},
	{"bbcmundo", "BBC Mundo", "https://feeds.bbci.co.uk/mundo/rss.xml"},
}

func generalByURL(u string) (generalSource, bool) {
	for _, s := range generalSources {
		if s.URL == u {
			return s, true
		}
	}
	return generalSource{}, false
}

// googleNewsBase es la búsqueda de Google News en RSS (sustituible en los tests).
var googleNewsBase = "https://news.google.com/rss/search"

// topicFeedURL es el feed de búsqueda de un interés: noticias en español de
// los últimos días que hablan de la consulta.
func topicFeedURL(query string) string {
	return googleNewsBase + "?" + url.Values{"q": {query + " when:2d"}, "hl": {"es"}, "gl": {"ES"}, "ceid": {"ES:es"}}.Encode()
}
