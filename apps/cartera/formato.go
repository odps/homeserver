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
// («70.250,12 €», «+3,1 %»).

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

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

// signed pone el signo delante también a lo positivo.
func signed(s string, v float64) string {
	if v > 0 && s != "0" && strings.Trim(s, "0,.") != "" {
		return "+" + s
	}
	return s
}

// curSymbol es el símbolo de una moneda para mostrarla.
func curSymbol(c string) string {
	switch c {
	case "EUR":
		return "€"
	case "USD":
		return "$"
	case "GBP":
		return "£"
	}
	return c
}

// priceDecimals: los precios grandes sin decimales, los normales con dos y
// los de céntimos (muchas criptos) con cuatro cifras significativas.
func priceDecimals(v float64) int {
	a := math.Abs(v)
	switch {
	case a >= 1000:
		return 0
	case a >= 1 || a == 0:
		return 2
	}
	d := 2 - int(math.Floor(math.Log10(a))) + 1
	return min(d, 10)
}

// price es un precio en su moneda: «70.250 €», «0,1234 $».
func price(v float64, cur string) string {
	return num(v, priceDecimals(v)) + " " + curSymbol(cur)
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

// pct es un porcentaje con signo y un decimal: «+3,1 %».
func pct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return signed(num(v, 1), v) + " %"
}

// qty es una cantidad de unidades, sin ceros sobrantes: «0,0125».
func qty(v float64) string { return trimNum(v, 8) }

var errNumber = errors.New("número no válido")

// parseNum lee un número como lo escribiría la persona: «1.234,56»,
// «1234.56», «0,00012», «70 000». Con punto y coma a la vez, el último es el
// decimal; con solo uno de los dos, es el decimal (las cantidades de cripto
// llevan muchos decimales y es más fácil equivocarse al revés).
func parseNum(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.NewReplacer(" ", "", " ", "", " ", "", "€", "", "$", "", "%", "", "−", "-").Replace(s)
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

// parseMoney es parseNum para importes, precios y umbrales, donde «70.000»
// son setenta mil: un único punto seguido de tres cifras, con algo distinto
// de cero delante, es de miles. En las cantidades (parseNum) sería 70: allí
// «1.500» BTC es uno y medio.
func parseMoney(s string) (float64, error) {
	t := strings.TrimSpace(strings.NewReplacer("€", "", "$", "", " ", "").Replace(s))
	t = strings.TrimPrefix(strings.TrimPrefix(t, "-"), "−")
	if ip, fp, ok := strings.Cut(t, "."); ok && !strings.ContainsAny(t, ",") && len(fp) == 3 && !strings.Contains(fp, ".") &&
		strings.Trim(ip, "0") != "" && strings.Trim(ip, "0123456789") == "" && strings.Trim(fp, "0123456789") == "" {
		s = strings.Replace(s, ".", "", 1)
	}
	return parseNum(s)
}

// parseOpt es parseNum (o parseMoney) para un campo que puede ir vacío (0).
func parseOpt(s string, money bool) (float64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	if money {
		return parseMoney(s)
	}
	return parseNum(s)
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
