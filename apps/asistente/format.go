package main

import (
	"regexp"
	"strings"
)

// plainText deja la respuesta en texto plano, como en cualquier chat: el
// modelo tiene pedido no usar Markdown, pero si se le escapa (negritas,
// títulos, tablas, enlaces con corchetes) se quita aquí. Se envía sin
// parse_mode, así que nada se interpreta como formato.
var (
	mdCodeFence = regexp.MustCompile("(?m)^\\s*```[a-zA-Z0-9]*\\s*$\n?")
	mdCode      = regexp.MustCompile("`([^`\n]+)`")
	mdLink      = regexp.MustCompile(`\[+([^\]\n]+?)\]+\((https?://[^\s)]+)\)`)
	mdBold      = regexp.MustCompile(`(\*\*|__)([^*_\n]+)(\*\*|__)`)
	mdItalic    = regexp.MustCompile(`(^|[\s(])[*_]([^*_\n]+)[*_]([\s).,;:!?]|$)`)
	mdHeading   = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	mdQuote     = regexp.MustCompile(`(?m)^>\s?`)
	mdBullet    = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
	mdRule      = regexp.MustCompile(`(?m)^\s*([-*_]\s*){3,}$\n?`)
	mdTableSep  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	blankLines  = regexp.MustCompile(`\n{3,}`)
)

func plainText(md string) string {
	s := mdCodeFence.ReplaceAllString(md, "")
	s = tablesToLines(s)
	s = mdCode.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		if p[1] == p[2] {
			return p[2]
		}
		return p[1] + " (" + p[2] + ")"
	})
	s = mdHeading.ReplaceAllString(s, "")
	s = mdQuote.ReplaceAllString(s, "")
	s = mdRule.ReplaceAllString(s, "")
	s = mdBold.ReplaceAllString(s, "$2")
	s = mdItalic.ReplaceAllString(s, "$1$2$3")
	s = mdBullet.ReplaceAllString(s, "$1• ")
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

// tablesToLines convierte cada fila de una tabla Markdown en una línea
// «• celda — celda»; la cabecera y el separador se quitan.
func tablesToLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		isRow := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "|") }
		if isRow(lines[i]) && i+1 < len(lines) && mdTableSep.MatchString(lines[i+1]) {
			i += 2 // cabecera y separador
			for ; i < len(lines) && isRow(lines[i]); i++ {
				var cells []string
				for _, c := range strings.Split(strings.Trim(strings.TrimSpace(lines[i]), "|"), "|") {
					if c = strings.TrimSpace(c); c != "" {
						cells = append(cells, c)
					}
				}
				out = append(out, "• "+strings.Join(cells, " — "))
			}
			i--
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}
