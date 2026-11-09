package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Números y fechas a la española: punto para los miles y coma para los
// decimales («1.234,56 €»), meses en español.

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

// monthOf es el primer día del mes de d.
func monthOf(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC) }

func isoMonth(m time.Time) string { return m.Format("2006-01") }

// parseMonth lee «2026-11».
func parseMonth(s string) (time.Time, error) {
	m, err := time.Parse("2006-01", strings.TrimSpace(s))
	if err != nil || m.Year() < 2000 || m.Year() > 2100 {
		return time.Time{}, errors.New("mes no válido")
	}
	return m, nil
}

// daysIn son los días que tiene el mes de m.
func daysIn(m time.Time) int { return monthOf(m).AddDate(0, 1, -1).Day() }

// addMonths suma n meses a d quedándose en el día anchor (o en el último
// del mes si ese no tiene tantos): el 31 de enero más un mes es el 28 de
// febrero, y otro más, el 31 de marzo.
func addMonths(d time.Time, n, anchor int) time.Time {
	if anchor <= 0 {
		anchor = d.Day()
	}
	m := monthOf(d).AddDate(0, n, 0)
	return m.AddDate(0, 0, min(anchor, daysIn(m))-1)
}

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

// euros es un importe con céntimos: «1.234,56 €».
func euros(v float64) string { return num(v, 2) + " €" }

// eurosRound es un importe sin céntimos si es grande: «12.346 €».
func eurosRound(v float64) string {
	if math.Abs(v) >= 1000 {
		return num(v, 0) + " €"
	}
	return euros(v)
}

// signedEuros pone el signo delante también a lo positivo: «+12,30 €».
func signedEuros(v float64) string {
	s := euros(v)
	if v >= 0.005 {
		return "+" + s
	}
	return s
}

// pct es un porcentaje sin decimales: «84 %».
func pct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return num(math.Round(v), 0) + " %"
}

// moneyInput es un importe para un campo de formulario: «12,5».
func moneyInput(v float64) string {
	if v == 0 {
		return ""
	}
	return strings.ReplaceAll(trimNum(v, 2), ".", "")
}

var errNumber = errors.New("número no válido")

// parseNum lee un número como lo escribiría la persona: «1.234,56»,
// «1234.56», «12,5», «1 200». Con punto y coma a la vez, el último es el
// decimal; con solo uno de los dos, es el decimal.
func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer(" ", "", " ", "", " ", "", "€", "", "−", "-").Replace(s)
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
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errNumber
	}
	return v, nil
}

// parseMoney es parseNum para importes, donde «1.200» son mil doscientos: un
// único punto seguido de tres cifras, con algo distinto de cero delante, es
// de miles.
func parseMoney(s string) (float64, error) {
	t := strings.TrimSpace(strings.NewReplacer("€", "", " ", "").Replace(s))
	t = strings.TrimPrefix(strings.TrimPrefix(t, "-"), "−")
	if ip, fp, ok := strings.Cut(t, "."); ok && !strings.ContainsAny(t, ",") && len(fp) == 3 && !strings.Contains(fp, ".") &&
		strings.Trim(ip, "0") != "" && strings.Trim(ip, "0123456789") == "" && strings.Trim(fp, "0123456789") == "" {
		s = strings.Replace(s, ".", "", 1)
	}
	return parseNum(s)
}

// parseOptMoney es parseMoney para un campo que puede ir vacío (0).
func parseOptMoney(s string) (float64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return parseMoney(s)
}

// round2 deja un importe en céntimos.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

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

// monthLabel es «noviembre 2026».
func monthLabel(m time.Time) string { return months[m.Month()-1] + " " + strconv.Itoa(m.Year()) }

// monthShort es «nov» (y el año si no es el de today).
func monthShort(m, today time.Time) string {
	s := months[m.Month()-1][:3]
	if m.Year() != today.Year() {
		s += " " + strconv.Itoa(m.Year()%100)
	}
	return s
}

// whenDay dice un día cercano en palabras: «hoy», «mañana», «el jueves 12»,
// o «el 3 dic».
func whenDay(d, today time.Time) string {
	switch n := int(d.Sub(today) / dayUnit); {
	case n == 0:
		return "hoy"
	case n == 1:
		return "mañana"
	case n > 1 && n < 7:
		return fmt.Sprintf("el %s %d", weekdays[d.Weekday()], d.Day())
	}
	return "el " + shortDate(d, today)
}

// inDays es «en 3 días», «mañana», «hoy» o «hace 2 días».
func inDays(d, today time.Time) string {
	switch n := int(d.Sub(today) / dayUnit); {
	case n == 0:
		return "hoy"
	case n == 1:
		return "mañana"
	case n == -1:
		return "ayer"
	case n < 0:
		return fmt.Sprintf("hace %d días", -n)
	default:
		return fmt.Sprintf("en %d días", n)
	}
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}
