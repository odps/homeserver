package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // la hora de Madrid para los recordatorios, aunque el sistema no la tenga
)

// Fechas en la hora de Madrid y números a la española (coma decimal).

var madrid = func() *time.Location {
	if l, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return l
	}
	return time.FixedZone("CET", 3600)
}()

const dayUnit = 24 * time.Hour

// dayOf es el día (en Madrid) de t, a las 00:00 UTC: así los días se suman
// y se comparan sin líos de horario de verano.
func dayOf(t time.Time) time.Time {
	t = t.In(madrid)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// minuteOf son los minutos desde las 00:00 de t en Madrid.
func minuteOf(t time.Time) int {
	t = t.In(madrid)
	return t.Hour()*60 + t.Minute()
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

func parseDay(s string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(s))
}

// weekStart es el lunes de la semana de d.
func weekStart(d time.Time) time.Time {
	return d.Add(-time.Duration((int(d.Weekday())+6)%7) * dayUnit)
}

func monthStart(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
}

var (
	weekdays   = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	shortDays  = []string{"D", "L", "M", "X", "J", "V", "S"}
	months     = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
	weekOrder  = []int{1, 2, 3, 4, 5, 6, 0} // de lunes a domingo
	errNumber  = errors.New("número no válido")
	errHourFmt = errors.New("la hora va como HH:MM")
)

// longDate es «lunes 9 de noviembre» (con el año si no es el de today).
func longDate(d, today time.Time) string {
	s := fmt.Sprintf("%s %d de %s", weekdays[d.Weekday()], d.Day(), months[d.Month()-1])
	if d.Year() != today.Year() {
		s += fmt.Sprintf(" de %d", d.Year())
	}
	return s
}

// shortDate es «9 nov» (o «9 nov 2025» si no es de este año).
func shortDate(d, today time.Time) string {
	s := fmt.Sprintf("%d %s", d.Day(), months[d.Month()-1][:3])
	if d.Year() != today.Year() {
		s += fmt.Sprintf(" %d", d.Year())
	}
	return s
}

// relDay es «hoy», «ayer» o la fecha larga.
func relDay(d, today time.Time) string {
	switch today.Sub(d) / dayUnit {
	case 0:
		return "hoy"
	case 1:
		return "ayer"
	}
	return longDate(d, today)
}

// monthName es «noviembre de 2026».
func monthName(d time.Time) string { return months[d.Month()-1] + " de " + strconv.Itoa(d.Year()) }

// num escribe v con dec decimales y coma decimal.
func num(v float64, dec int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	s := strconv.FormatFloat(v, 'f', dec, 64)
	if strings.Trim(s, "-0.") == "" {
		s = strings.TrimPrefix(s, "-")
	}
	return strings.Replace(s, ".", ",", 1)
}

// trimNum es num sin los ceros finales de los decimales: «2,5», «8».
func trimNum(v float64, maxDec int) string {
	s := num(v, maxDec)
	if strings.Contains(s, ",") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ",")
	}
	return s
}

// pctText es «85 %».
func pctText(p float64) string { return num(p, 0) + " %" }

// parseNum lee un número escrito con coma o con punto decimal.
func parseNum(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if s == "" || strings.Count(s, ",")+strings.Count(s, ".") > 1 {
		return 0, errNumber
	}
	v, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errNumber
	}
	return v, nil
}

// parseHour lee «HH:MM» (o «H:MM», «HH») y da los minutos desde las 00:00.
func parseHour(s string) (int, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ".", ":"))
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		m = "0"
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(m) > 2 {
		return 0, errHourFmt
	}
	return hh*60 + mm, nil
}

// hourText es «09:30».
func hourText(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

// plural es «1 día», «3 días».
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
