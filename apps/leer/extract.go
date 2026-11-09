package main

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	readability "codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
)

// Extracción del texto legible. go-readability (MIT, la versión que mantiene
// Readeck) encuentra el artículo dentro de la página; de su resultado solo se
// guarda texto, en bloques (párrafos, títulos, listas, citas, código): nada
// de HTML, enlaces, imágenes ni atributos. Así no hay nada que sanear al
// enseñarlo, porque las plantillas lo escapan todo.

const (
	maxElems     = 60000  // elementos de la página como mucho
	maxTextRunes = 400000 // texto del artículo como mucho
	maxBlocks    = 4000
	wordsPerMin  = 220
	minWords     = 40 // menos que esto no es un artículo
)

// block es un trozo del texto limpio.
type block struct {
	Kind  string   `json:"k"`           // p, h2, h3, ul, ol, cita, pre, pie
	Text  string   `json:"t,omitempty"` // con saltos de línea (\n) donde había <br>
	Items []string `json:"l,omitempty"` // elementos de una lista
}

// extracted es lo que sale de una página.
type extracted struct {
	Title, Site, Author, Published, Image, Excerpt, Lang string
	Blocks                                               []block
	Words, Minutes                                       int
	Truncated                                            bool
}

// extract saca el artículo de una página descargada. Si no hay texto
// suficiente devuelve errNoText, pero con el título si lo hay.
func extract(p *page, now time.Time) (ex extracted, err error) {
	defer func() {
		if r := recover(); r != nil {
			ex, err = extracted{}, errNoText // una página rara no tumba la app
		}
	}()
	r, err := charset.NewReader(bytes.NewReader(p.Body), p.ContentType)
	if err != nil {
		r = bytes.NewReader(p.Body)
	}
	doc, err := html.Parse(r)
	if err != nil {
		return ex, errNoText
	}
	pageTitle := docTitle(doc)
	parser := readability.NewParser()
	parser.MaxElemsToParse = maxElems
	art, err := parser.ParseAndMutate(doc, p.URL)
	ex.Title = oneLine(art.Title(), 300)
	if ex.Title == "" {
		ex.Title = oneLine(pageTitle, 300)
	}
	if err != nil || art.Node == nil {
		return ex, errNoText
	}
	ex.Site = oneLine(art.SiteName(), 120)
	ex.Title = trimSite(ex.Title, ex.Site)
	ex.Author = oneLine(art.Byline(), 160)
	ex.Lang = oneLine(art.Language(), 20)
	ex.Excerpt = oneLine(art.Excerpt(), 400)
	if t, err := art.PublishedTime(); err == nil && t.Year() >= 1990 && t.Before(now.Add(48*time.Hour)) {
		ex.Published = t.Format("2006-01-02")
	}
	if img := art.ImageURL(); strings.HasPrefix(img, "https://") && len(img) <= maxURL {
		if u, err := url.Parse(img); err == nil && u.Host != "" {
			ex.Image = u.String()
		}
	}

	c := &converter{}
	c.walk(art.Node)
	c.flush()
	ex.Blocks, ex.Truncated = c.out, c.truncated
	// El primer título suele repetir el del artículo.
	if len(ex.Blocks) > 0 && strings.HasPrefix(ex.Blocks[0].Kind, "h") && sameText(ex.Blocks[0].Text, ex.Title) {
		ex.Blocks = ex.Blocks[1:]
	}
	ex.Words = countWords(ex.Blocks)
	if ex.Words < minWords {
		return ex, errNoText
	}
	ex.Minutes = int(math.Ceil(float64(ex.Words) / wordsPerMin))
	if ex.Excerpt == "" {
		for _, b := range ex.Blocks {
			if b.Kind == "p" {
				ex.Excerpt = oneLine(b.Text, 400)
				break
			}
		}
	}
	return ex, nil
}

// trimSite quita del título el nombre del sitio que a veces lleva al final
// («Título | Sitio»).
func trimSite(title, site string) string {
	if site == "" {
		return title
	}
	for _, sep := range []string{" | ", " - ", " – ", " — ", " · ", " :: "} {
		if t, ok := strings.CutSuffix(title, sep+site); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return title
}

func docTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.DataAtom == atom.Title {
		return textOf(n)
	}
	if n.Type == html.ElementNode && (n.DataAtom == atom.Body || n.DataAtom == atom.Svg) {
		return ""
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := docTitle(c); t != "" {
			return t
		}
	}
	return ""
}

// converter pasa el árbol del artículo a bloques de texto.
type converter struct {
	out       []block
	buf       strings.Builder
	runes     int
	truncated bool
}

// Etiquetas que se saltan enteras (con todo lo que tengan dentro).
var skipTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Iframe: true, atom.Frame: true,
	atom.Object: true, atom.Embed: true, atom.Svg: true, atom.Math: true, atom.Canvas: true, atom.Video: true,
	atom.Audio: true, atom.Picture: true, atom.Img: true, atom.Form: true, atom.Button: true, atom.Input: true,
	atom.Select: true, atom.Textarea: true, atom.Head: true, atom.Title: true, atom.Link: true, atom.Meta: true,
	atom.Nav: true, atom.Dialog: true,
}

// Etiquetas de bloque que solo agrupan: cortan el párrafo y se recorren.
var groupTags = map[atom.Atom]bool{
	atom.Div: true, atom.Section: true, atom.Article: true, atom.Main: true, atom.Header: true, atom.Footer: true,
	atom.Aside: true, atom.Figure: true, atom.Dl: true, atom.Dt: true, atom.Dd: true, atom.Li: true, atom.Details: true,
	atom.Summary: true, atom.Address: true, atom.Center: true, atom.Body: true, atom.Html: true, atom.Hr: true,
	atom.Caption: true, atom.Fieldset: true, atom.Hgroup: true,
}

func (c *converter) add(b block) {
	if c.truncated {
		return
	}
	n := utf8.RuneCountInString(b.Text)
	for _, it := range b.Items {
		n += utf8.RuneCountInString(it)
	}
	if n == 0 {
		return
	}
	if c.runes+n > maxTextRunes || len(c.out) >= maxBlocks {
		c.truncated = true
		return
	}
	c.runes += n
	c.out = append(c.out, b)
}

// flush cierra el párrafo con el texto suelto que se haya juntado.
func (c *converter) flush() {
	if t := cleanText(c.buf.String()); t != "" {
		c.add(block{Kind: "p", Text: t})
	}
	c.buf.Reset()
}

func (c *converter) walk(n *html.Node) {
	if c.truncated {
		return
	}
	switch n.Type {
	case html.TextNode:
		c.buf.WriteString(n.Data)
		return
	case html.ElementNode:
	default:
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch)
		}
		return
	}
	a := n.DataAtom
	switch {
	case skipTags[a]:
		return
	case a == atom.Br:
		c.buf.WriteString("\n")
		return
	case a == atom.P:
		c.flush()
		c.add(block{Kind: "p", Text: inlineText(n)})
	case a == atom.H1 || a == atom.H2:
		c.flush()
		c.add(block{Kind: "h2", Text: oneLine(inlineText(n), 300)})
	case a == atom.H3 || a == atom.H4 || a == atom.H5 || a == atom.H6:
		c.flush()
		c.add(block{Kind: "h3", Text: oneLine(inlineText(n), 300)})
	case a == atom.Ul || a == atom.Ol:
		c.flush()
		kind := "ul"
		if a == atom.Ol {
			kind = "ol"
		}
		var items []string
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if t := inlineText(ch); t != "" {
				items = append(items, t)
			}
		}
		if len(items) > 0 {
			c.add(block{Kind: kind, Items: items})
		}
	case a == atom.Blockquote:
		c.flush()
		c.add(block{Kind: "cita", Text: inlineText(n)})
	case a == atom.Pre:
		c.flush()
		c.add(block{Kind: "pre", Text: strings.Trim(preText(n), "\n")})
	case a == atom.Figcaption:
		c.flush()
		c.add(block{Kind: "pie", Text: inlineText(n)})
	case a == atom.Table:
		c.flush()
		c.table(n)
	case groupTags[a]:
		c.flush()
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch)
		}
		c.flush()
	default: // en línea: a, span, em, strong…
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.walk(ch)
		}
	}
}

// table deja cada fila como un párrafo con sus celdas separadas por «·».
func (c *converter) table(n *html.Node) {
	var rows func(*html.Node)
	rows = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type != html.ElementNode || skipTags[ch.DataAtom] {
				continue
			}
			if ch.DataAtom == atom.Tr {
				var cells []string
				for td := ch.FirstChild; td != nil; td = td.NextSibling {
					if td.Type == html.ElementNode && (td.DataAtom == atom.Td || td.DataAtom == atom.Th) {
						if t := oneLine(inlineText(td), 2000); t != "" {
							cells = append(cells, t)
						}
					}
				}
				if len(cells) > 0 {
					c.add(block{Kind: "p", Text: strings.Join(cells, " · ")})
				}
				continue
			}
			rows(ch)
		}
	}
	rows(n)
}

// inlineText es el texto de un nodo, con un salto de línea por cada <br> y
// por cada bloque que tenga dentro.
func inlineText(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			if skipTags[n.DataAtom] {
				return
			}
			if n.DataAtom == atom.Br {
				b.WriteString("\n")
				return
			}
		}
		block := n.Type == html.ElementNode && (groupTags[n.DataAtom] || n.DataAtom == atom.P || n.DataAtom == atom.Ul ||
			n.DataAtom == atom.Ol || n.DataAtom == atom.Blockquote || n.DataAtom == atom.Pre)
		if block {
			b.WriteString("\n")
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
		if block {
			b.WriteString("\n")
		}
	}
	rec(n)
	return cleanText(b.String())
}

// preText es el texto de un <pre> tal cual, con sus espacios.
func preText(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Br {
			b.WriteString("\n")
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r >= ' ' && r != 0x7f {
			return r
		}
		return -1
	}, b.String())
}

func textOf(n *html.Node) string {
	var b strings.Builder
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.TextNode {
			b.WriteString(ch.Data)
		}
	}
	return b.String()
}

// cleanText junta los espacios de cada línea y quita las líneas vacías y los
// caracteres de control.
func cleanText(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.Join(strings.Fields(stripControl(l)), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0xa0: // espacio duro
			return ' '
		case r < ' ' && r != '\t' || r == 0x7f || r == 0x200b || r == 0xfeff:
			return -1
		}
		return r
	}, s)
}

// oneLine deja un texto en una línea de como mucho max letras.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(stripControl(s)), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

func sameText(a, b string) bool {
	return strings.Join(strings.Fields(strings.ToLower(a)), " ") == strings.Join(strings.Fields(strings.ToLower(b)), " ")
}

func countWords(blocks []block) int {
	n := 0
	for _, b := range blocks {
		n += len(strings.Fields(b.Text))
		for _, it := range b.Items {
			n += len(strings.Fields(it))
		}
	}
	return n
}

// plainText es el artículo como texto plano (para Talia), recortado a max
// letras.
func plainText(blocks []block, max int) (string, bool) {
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Kind {
		case "h2", "h3":
			b.WriteString("## " + bl.Text)
		case "ul", "ol":
			for i, it := range bl.Items {
				if i > 0 {
					b.WriteString("\n")
				}
				if bl.Kind == "ol" {
					b.WriteString(fmt.Sprintf("%d. ", i+1))
				} else {
					b.WriteString("- ")
				}
				b.WriteString(strings.ReplaceAll(it, "\n", " "))
			}
		case "cita":
			b.WriteString("> " + strings.ReplaceAll(bl.Text, "\n", "\n> "))
		default:
			b.WriteString(bl.Text)
		}
		b.WriteString("\n\n")
	}
	s := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	return strings.TrimSpace(string([]rune(s)[:max])) + "…", true
}
