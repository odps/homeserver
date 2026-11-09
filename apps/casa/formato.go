package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // las horas de la luz dependen del cambio de hora, aunque el sistema no tenga las zonas
)

// Números a la española: punto para los miles y coma para los decimales
// («1.234,5 kWh», «0,172 €/kWh»).

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

const dayUnit = 24 * time.Hour

// dayOf es el día (en Madrid) de un instante, a las 00:00 UTC: así los días
// se comparan y se restan sin líos de horario.
func dayOf(t time.Time) time.Time {
	t = t.In(madrid)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// midnight es el instante en que empieza un día (de dayOf) en Madrid.
func midnight(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, madrid)
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

func parseDay(s string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(s))
}

// daysBetween son los días de a a b (los dos de dayOf).
func daysBetween(a, b time.Time) int { return int(math.Round(b.Sub(a).Hours() / 24)) }

// num escribe v con dec decimales, miles con punto y decimales con coma.
func num(v float64, dec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', dec, 64)
	ip, fp, _ := strings.Cut(s, ".")
	var b strings.Builder
	if v < 0 && strings.Trim(s, "0.") != "" {
		b.WriteString("−")
	}
	for i, r := range ip {
		if i > 0 && (len(ip)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if fp != "" {
		b.WriteString("," + fp)
	}
	return b.String()
}

// trimNum es num sin los ceros finales de los decimales.
func trimNum(v float64, maxDec int) string {
	s := num(v, maxDec)
	if strings.Contains(s, ",") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ",")
	}
	return s
}

// kwh es un precio de la luz: «0,172 €/kWh».
func kwh(p float64) string { return num(p, 3) + " €/kWh" }

// kwhShort es el precio sin unidad, para la gráfica: «0,17».
func kwhShort(p float64) string { return num(p, 2) }

var errNumber = errors.New("número no válido")

// parseNum lee un número como lo escribiría la persona: «1.234,5»,
// «1234.5», «12345», «0,75». Con punto y coma a la vez, el último es el
// decimal; con solo comas, la coma es el decimal; con un solo punto seguido
// de tres cifras («12.345»), es de miles, como en los contadores.
func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer(" ", "", " ", "", " ", "", "€", "", "−", "-").Replace(s)
	if s == "" {
		return 0, errNumber
	}
	dot, comma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case dot >= 0 && comma >= 0:
		if comma > dot {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case comma >= 0:
		if strings.Count(s, ",") > 1 {
			return 0, errNumber
		}
		s = strings.Replace(s, ",", ".", 1)
	case strings.Count(s, ".") > 1:
		s = strings.ReplaceAll(s, ".", "") // 1.234.567
	case dot >= 0 && len(s)-dot-1 == 3 && strings.Trim(s[:dot], "-0") != "":
		s = strings.Replace(s, ".", "", 1) // 12.345
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errNumber
	}
	return v, nil
}

var (
	weekdays = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
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

// longDate es «lunes, 9 de noviembre».
func longDate(d time.Time) string {
	return fmt.Sprintf("%s, %d de %s", weekdays[d.Weekday()], d.Day(), months[d.Month()-1])
}

// relDay dice un día respecto a hoy: «hoy», «mañana», «ayer», «el 3 nov».
func relDay(d, today time.Time) string {
	switch daysBetween(today, d) {
	case 0:
		return "hoy"
	case 1:
		return "mañana"
	case -1:
		return "ayer"
	}
	return "el " + shortDate(d, today)
}

// inDays dice cuánto falta o cuánto hace: «en 5 días», «hace 3 días».
func inDays(n int) string {
	switch {
	case n == 0:
		return "hoy"
	case n == 1:
		return "mañana"
	case n == -1:
		return "ayer"
	case n > 0:
		return fmt.Sprintf("en %d días", n)
	}
	return fmt.Sprintf("hace %d días", -n)
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

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
