package main

import (
	"html"
	"regexp"
	"strings"
)

// toTelegramHTML convierte el Markdown sencillo que escribe el modelo al HTML
// que admite Telegram (negrita, cursiva, código, enlaces). Todo lo demás se
// escapa: el texto del modelo nunca se interpreta como HTML.
var (
	mdCodeBlock = regexp.MustCompile("(?s)```[a-zA-Z0-9]*\n?(.*?)```")
	mdCode      = regexp.MustCompile("`([^`\n]+)`")
	mdLink      = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	mdBold      = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdItalic    = regexp.MustCompile(`(^|[\s(])[*_]([^*_\n]+)[*_]([\s).,;:!?]|$)`)
	mdHeading   = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	mdBullet    = regexp.MustCompile(`(?m)^(\s*)[-*]\s+`)
)

func toTelegramHTML(md string) string {
	// Los bloques de código se apartan antes de tocar nada.
	var blocks []string
	s := mdCodeBlock.ReplaceAllStringFunc(md, func(m string) string {
		blocks = append(blocks, mdCodeBlock.FindStringSubmatch(m)[1])
		return "\x00" + string(rune('A'+len(blocks)-1)) + "\x00"
	})
	s = html.EscapeString(s)
	s = mdCode.ReplaceAllString(s, "<code>$1</code>")
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		return `<a href="` + strings.ReplaceAll(p[2], `"`, "%22") + `">` + p[1] + "</a>"
	})
	s = mdHeading.ReplaceAllString(s, "<b>$1</b>")
	s = mdBold.ReplaceAllString(s, "<b>$1</b>")
	s = mdItalic.ReplaceAllString(s, "$1<i>$2</i>$3")
	s = mdBullet.ReplaceAllString(s, "$1• ")
	for i, b := range blocks {
		s = strings.Replace(s, "\x00"+string(rune('A'+i))+"\x00", "<pre>"+html.EscapeString(strings.TrimRight(b, "\n"))+"</pre>", 1)
	}
	return s
}
