package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Números a la española: punto para los miles y coma para los decimales
// («152.300 km», «1,439 €/l», «6,2 l/100 km»).

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

// daysBetween son los días de a a b (b posterior: positivo).
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

// euros es un importe con céntimos: «1.234,56 €».
func euros(v float64) string { return num(v, 2) + " €" }

// eurosRound es un importe sin céntimos si es grande: «12.346 €».
func eurosRound(v float64) string {
	if math.Abs(v) >= 1000 {
		return num(v, 0) + " €"
	}
	return num(v, 2) + " €"
}

// kmStr es una distancia: «152.300 km».
func kmStr(v int) string { return num(float64(v), 0) + " km" }

// perLiter es un precio por litro con tres decimales, como en las
// gasolineras: «1,439 €/l».
func perLiter(v float64, unit string) string { return num(v, 3) + " €/" + unit }

// eurPerKm es un coste por km: «0,094 €/km».
func eurPerKm(v float64) string { return num(v, 3) + " €/km" }

var errNumber = errors.New("número no válido")

// parseNum lee un número como lo escribiría la persona: «1.234,56»,
// «1234.56», «45,3», «70 000». Con punto y coma a la vez, el último es el
// decimal; con solo uno de los dos, es el decimal.
func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer(" ", "", " ", "", " ", "", "€", "", "%", "", "−", "-").Replace(s)
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

// parseKm lee unos km, donde el punto siempre es de miles («152.300»).
func parseKm(s string) (int, error) {
	t := strings.NewReplacer(" ", "", " ", "", ".", "", "km", "", "KM", "").Replace(strings.TrimSpace(s))
	if t == "" {
		return 0, errNumber
	}
	v, err := strconv.ParseFloat(strings.Replace(t, ",", ".", 1), 64)
	if err != nil || v < 0 || v > 1e8 {
		return 0, errNumber
	}
	return int(math.Round(v)), nil
}

// parseOpt es parseNum para un campo que puede ir vacío (0).
func parseOpt(s string) (float64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return parseNum(s)
}

// parseOptKm es parseKm para un campo que puede ir vacío (0).
func parseOptKm(s string) (int, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return parseKm(s)
}

var months = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}

// shortDate es «3 nov 2026» (o «3 nov» si es de este año).
func shortDate(d, today time.Time) string {
	s := fmt.Sprintf("%d %s", d.Day(), months[d.Month()-1][:3])
	if d.Year() != today.Year() {
		s += fmt.Sprintf(" %d", d.Year())
	}
	return s
}

// monthLabel es «nov 2026».
func monthLabel(d time.Time) string { return months[d.Month()-1][:3] + " " + strconv.Itoa(d.Year()) }

// inDays dice cuánto falta o cuánto hace: «hoy», «mañana», «en 5 días»,
// «hace 3 días».
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

// addMonths suma meses a un día sin pasarse de mes (31 ene + 1 = 28 feb).
func addMonths(d time.Time, n int) time.Time {
	y, m := d.Year(), int(d.Month())-1+n
	y += m / 12
	m %= 12
	if m < 0 {
		m += 12
		y--
	}
	last := time.Date(y, time.Month(m+2), 0, 0, 0, 0, 0, time.UTC).Day()
	return time.Date(y, time.Month(m+1), min(d.Day(), last), 0, 0, 0, 0, time.UTC)
}
