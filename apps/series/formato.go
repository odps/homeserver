package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Fechas y horas siempre en Madrid: «hoy, 21:00», «vie 14 nov», «3x04».

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

const dayUnit = 24 * time.Hour

// dayOf es el día (en Madrid) de un instante, a las 00:00 UTC: así los días se
// comparan y se restan sin líos de horario.
func dayOf(t time.Time) time.Time {
	t = t.In(madrid)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

func parseDay(s string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(s))
}

var (
	weekdays = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	wdShort  = []string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}
	months   = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
)

// shortDate es «3 nov 2026» (o «3 nov» si es de este año).
func shortDate(d, today time.Time) string {
	s := fmt.Sprintf("%d %s", d.Day(), months[d.Month()-1][:3])
	if d.Year() != today.Year() {
		s += fmt.Sprintf(" %d", d.Year())
	}
	return s
}

// dayName es «hoy», «mañana», «ayer», «jueves 12 nov» (esta semana) o
// «12 nov 2027».
func dayName(d, today time.Time) string {
	switch n := int(d.Sub(today) / dayUnit); {
	case n == 0:
		return "hoy"
	case n == 1:
		return "mañana"
	case n == -1:
		return "ayer"
	case n > 1 && n < 7:
		return weekdays[d.Weekday()] + " " + shortDate(d, today)
	}
	return shortDate(d, today)
}

// longDay es «jueves 12 de noviembre».
func longDay(d time.Time) string {
	return fmt.Sprintf("%s %d de %s", weekdays[d.Weekday()], d.Day(), months[d.Month()-1])
}

// epCode es «3x04».
func epCode(e episode) string { return fmt.Sprintf("%dx%02d", e.Season, e.Number) }

// whenText dice cuándo sale un capítulo, en Madrid: «hoy, 21:00»,
// «vie 14 nov», «sin fecha».
func whenText(e episode, today time.Time) string {
	d := e.day()
	if d.IsZero() {
		return "sin fecha"
	}
	s := dayName(d, today)
	if n := int(d.Sub(today) / dayUnit); n > 1 && n < 7 {
		s = wdShort[d.Weekday()] + " " + shortDate(d, today)
	}
	if e.hasTime() {
		s += ", " + e.At.In(madrid).Format("15:04")
	}
	return s
}

// ago es cuánto hace, en pocas palabras: «hace 4 min».
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return "nunca"
	case d < time.Minute:
		return "ahora mismo"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	}
	return "el " + shortDate(dayOf(t), dayOf(now)) + " a las " + t.In(madrid).Format("15:04")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// plural es «1 capítulo», «3 capítulos».
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
