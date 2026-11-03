package main

import (
	"bufio"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Importación de las notas de WhatsApp: el chat exportado (sin archivos).
// Entiende el .txt de Android («12/05/26, 19:11 - Tú: …»), el de iPhone
// («[12/05/26, 19:11:03] Tú: …») y el Markdown de los exportadores que lo
// ordenan por días («## 2 December 2025» y «[19:11] **You:** …»). El fichero
// se procesa en memoria: no se guarda.

type chatMsg struct {
	At    time.Time
	Lines []string
}

var (
	reMDDay     = regexp.MustCompile(`^##\s+(\d{1,2})\s+([A-Za-záéíóúñ]+)\s+(\d{4})\s*$`)
	reMDMsg     = regexp.MustCompile(`^\[(\d{1,2}):(\d{2})\]\s+\*\*[^*]+:\*\*\s?(.*)$`)
	reAndroid   = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{2,4}),?\s+(\d{1,2}):(\d{2})(?::\d{2})?\s*(?:[ap]\.?\s?m\.?)?\s+-\s+[^:]+:\s?(.*)$`)
	reIOS       = regexp.MustCompile(`^‎?\[(\d{1,2})/(\d{1,2})/(\d{2,4}),?\s+(\d{1,2}):(\d{2})(?::\d{2})?\]\s+[^:]+:\s?(.*)$`)
	monthsByKey = map[string]time.Month{}
)

func init() {
	for i, n := range []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"} {
		monthsByKey[n] = time.Month(i + 1)
		monthsByKey[n[:3]] = time.Month(i + 1)
	}
	for i, n := range []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"} {
		monthsByKey[n] = time.Month(i + 1)
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func year(s string) int {
	y := atoi(s)
	if y < 100 {
		y += 2000
	}
	return y
}

// skipMessage: lo que no es una nota escrita.
func skipMessage(text string) bool {
	t := strings.ToLower(strings.Trim(strings.TrimSpace(text), "_*"))
	switch {
	case t == "", strings.Trim(t, ".…") == "":
		return true
	case strings.Contains(t, "deleted this message"), strings.Contains(t, "eliminaste este mensaje"),
		strings.Contains(t, "se eliminó este mensaje"), strings.Contains(t, "media omitted"), strings.Contains(t, "multimedia omitido"),
		strings.HasPrefix(t, "[image]"), strings.HasPrefix(t, "[imagen]"), strings.HasSuffix(t, "(file attached)"),
		strings.HasPrefix(t, "http://"), strings.HasPrefix(t, "https://"):
		return true
	}
	return false
}

// parseChat lee la exportación y devuelve los mensajes en orden.
func parseChat(text string, loc *time.Location) []chatMsg {
	var out []chatMsg
	var day time.Time
	sc := bufio.NewScanner(strings.NewReader(strings.ReplaceAll(text, "\r\n", "\n")))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	inQuote := false
	add := func(at time.Time, first string) {
		out = append(out, chatMsg{At: at})
		inQuote = false
		appendLine(&out[len(out)-1], first, &inQuote)
	}
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if m := reMDDay.FindStringSubmatch(trim); m != nil {
			if mon, ok := monthsByKey[strings.ToLower(m[2])]; ok {
				day = time.Date(atoi(m[3]), mon, atoi(m[1]), 0, 0, 0, 0, loc)
			}
			continue
		}
		if m := reMDMsg.FindStringSubmatch(trim); m != nil && !day.IsZero() {
			add(day.Add(time.Duration(atoi(m[1]))*time.Hour+time.Duration(atoi(m[2]))*time.Minute), m[3])
			continue
		}
		if m := reIOS.FindStringSubmatch(trim); m != nil {
			add(time.Date(year(m[3]), time.Month(atoi(m[2])), atoi(m[1]), atoi(m[4]), atoi(m[5]), 0, 0, loc), m[6])
			continue
		}
		if m := reAndroid.FindStringSubmatch(trim); m != nil {
			add(time.Date(year(m[3]), time.Month(atoi(m[2])), atoi(m[1]), atoi(m[4]), atoi(m[5]), 0, 0, loc), m[6])
			continue
		}
		if trim == "---" || strings.HasPrefix(trim, "# ") || len(out) == 0 {
			continue
		}
		appendLine(&out[len(out)-1], line, &inQuote)
	}
	// Fuera lo que no es texto escrito.
	kept := out[:0]
	for _, m := range out {
		var lines []string
		for _, l := range m.Lines {
			if !skipMessage(l) {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			m.Lines = lines
			kept = append(kept, m)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].At.Before(kept[j].At) })
	return kept
}

// appendLine añade una línea al mensaje, saltando las citas de otro mensaje
// («> _You: …_», que repetirían una sesión anterior).
func appendLine(m *chatMsg, line string, inQuote *bool) {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, ">") {
		*inQuote = !strings.HasSuffix(t, "_") || strings.Count(t, "_") < 2
		return
	}
	if *inQuote {
		if strings.HasSuffix(t, "_") {
			*inQuote = false
		}
		return
	}
	t = strings.TrimPrefix(t, "[Forwarded] ")
	t = strings.TrimPrefix(t, "[Reenviado] ")
	if t != "" {
		m.Lines = append(m.Lines, t)
	}
}

// importSession es una sesión sacada del chat.
type importSession struct {
	Day       time.Time
	Start     time.Time
	Exercises []exerciseLog
	Notes     []string
}

// chatSessions agrupa los mensajes por día y saca las sesiones: un día con al
// menos un ejercicio con series. Las notas sueltas cuentan solo si están a
// menos de una hora de algún ejercicio (así no entran tareas ni ideas
// apuntadas por la mañana).
func chatSessions(msgs []chatMsg, nm names) []importSession {
	type dayMsgs struct {
		day  time.Time
		msgs []chatMsg
	}
	var days []dayMsgs
	for _, m := range msgs {
		d := time.Date(m.At.Year(), m.At.Month(), m.At.Day(), 0, 0, 0, 0, m.At.Location())
		if len(days) == 0 || !days[len(days)-1].day.Equal(d) {
			days = append(days, dayMsgs{day: d})
		}
		days[len(days)-1].msgs = append(days[len(days)-1].msgs, m)
	}
	var out []importSession
	for _, d := range days {
		// Qué mensajes tienen ejercicios, para saber qué notas son del entreno.
		var exTimes []time.Time
		for _, m := range d.msgs {
			exs, _ := assemble(m.Lines, nm)
			for _, e := range exs {
				if len(e.Sets) > 0 {
					exTimes = append(exTimes, m.At)
					break
				}
			}
		}
		if len(exTimes) == 0 {
			continue
		}
		near := func(t time.Time) bool {
			for _, x := range exTimes {
				if d := t.Sub(x); d > -time.Hour && d < time.Hour {
					return true
				}
			}
			return false
		}
		var lines []string
		for _, m := range d.msgs {
			if near(m.At) {
				lines = append(lines, m.Lines...)
			}
		}
		exs, notes := assemble(lines, nm)
		var kept []exerciseLog
		for _, e := range exs {
			if len(e.Sets) > 0 || e.Key != "" {
				kept = append(kept, e)
			}
		}
		out = append(out, importSession{Day: d.day, Start: exTimes[0], Exercises: kept, Notes: notes})
	}
	return out
}
