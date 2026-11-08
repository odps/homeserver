package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// Alertas: se evalúan con código tras cada actualización de precios y lo que
// salta va a la cola de avisos (notices), que recoge la pasarela para
// Telegram. «Una vez» se apaga al saltar; «cada vez» no se repite mientras
// siga cruzada y se rearma al volver al otro lado (con un pequeño margen,
// para que un precio que baila sobre el umbral no avise cada cinco minutos).

const (
	rearmPrice = 0.005 // 0,5 % del umbral
	rearmPct   = 0.5   // medio punto
	maxDropDay = 3650
)

func validAlertKind(k string, portfolio bool) bool {
	switch k {
	case alertAbove, alertBelow, alertDay:
		return true
	case alertDrop:
		return !portfolio
	}
	return false
}

// checkAlert valida una alerta nueva.
func checkAlert(a *alert) error {
	if a.Kind == alertDrop && a.AssetID == 0 {
		return errors.New("la caída desde el máximo solo vale para un activo")
	}
	if !validAlertKind(a.Kind, a.AssetID == 0) {
		return errors.New("tipo de alerta no válido")
	}
	if a.Threshold <= 0 || math.IsInf(a.Threshold, 0) {
		return errors.New("pon un umbral mayor que cero")
	}
	switch a.Kind {
	case alertDay, alertDrop:
		if a.Threshold >= 100 && a.Kind == alertDrop || a.Threshold > 1000 {
			return errors.New("ese porcentaje no tiene sentido")
		}
	}
	if a.Kind == alertDrop {
		if a.Days == 0 {
			a.Days = 30
		}
		if a.Days < 2 || a.Days > maxDropDay {
			return errors.New("los días van de 2 a 3650")
		}
	} else {
		a.Days = 0
	}
	return nil
}

// alertDesc describe una alerta: «BTC por encima de 70.000 €».
func alertDesc(a alert, symbol, cur string) string {
	who := symbol
	if a.AssetID == 0 {
		who = "La cartera"
		cur = "EUR"
	}
	rep := " (una vez)"
	if a.Repeat {
		rep = " (cada vez)"
	}
	switch a.Kind {
	case alertAbove:
		return who + " por encima de " + price(a.Threshold, cur) + rep
	case alertBelow:
		return who + " por debajo de " + price(a.Threshold, cur) + rep
	case alertDay:
		return who + ": se mueve un " + trimNum(a.Threshold, 2) + " % o más en el día" + rep
	case alertDrop:
		return fmt.Sprintf("%s: cae un %s %% o más desde el máximo de %d días%s", who, trimNum(a.Threshold, 2), a.Days, rep)
	}
	return who
}

// todayNote es « (+3,1 % hoy)» si se sabe la variación del día.
func todayNote(ch float64, known bool) string {
	if !known {
		return ""
	}
	return " (" + pct(ch) + " hoy)"
}

// state es lo que dice una alerta ahora: si se cumple, si se puede rearmar y
// el texto del aviso.
type alertState struct {
	ok, cond, rearm bool
	text            string
}

func evalAsset(a alert, p *position, h series, now time.Time) alertState {
	if p == nil || p.Price <= 0 {
		return alertState{}
	}
	sym, px, cur := p.Asset.Symbol, p.Price, p.Quote.Currency
	if cur == "" {
		cur = p.Asset.Currency
	}
	ch, chOK := p.Quote.change(), p.Quote.Prev > 0
	if chOK && !dayOf(p.Quote.At).Equal(dayOf(now)) && p.Asset.Kind != "cripto" {
		ch, chOK = 0, false // la bolsa no ha abierto hoy
	}
	th := a.Threshold
	switch a.Kind {
	case alertAbove:
		return alertState{ok: true, cond: px >= th, rearm: px < th*(1-rearmPrice),
			text: "📈 " + sym + " ha superado " + price(th, cur) + ": ahora " + price(px, cur) + todayNote(ch, chOK)}
	case alertBelow:
		return alertState{ok: true, cond: px <= th, rearm: px > th*(1+rearmPrice),
			text: "📉 " + sym + " ha bajado de " + price(th, cur) + ": ahora " + price(px, cur) + todayNote(ch, chOK)}
	case alertDay:
		if !chOK {
			return alertState{}
		}
		icon, verb := "📈 ", " sube un "
		if ch < 0 {
			icon, verb = "📉 ", " cae un "
		}
		return alertState{ok: true, cond: math.Abs(ch) >= th, rearm: math.Abs(ch) < math.Max(0, th-rearmPct),
			text: icon + sym + verb + num(math.Abs(ch), 1) + " % hoy: ahora " + price(px, cur)}
	case alertDrop:
		top := px
		for _, c := range h.since(dayOf(now).Add(-time.Duration(a.Days) * dayUnit)).Closes {
			top = math.Max(top, c)
		}
		dd := (top - px) / top * 100
		return alertState{ok: true, cond: dd >= th, rearm: dd < math.Max(0, th-rearmPct*2),
			text: fmt.Sprintf("📉 %s ha caído un %s %% desde su máximo de %d días (%s): ahora %s", sym, num(dd, 1), a.Days, price(top, cur), price(px, cur))}
	}
	return alertState{}
}

func evalPortfolio(a alert, pf *portfolio) alertState {
	if pf.Held == 0 || pf.Value <= 0 {
		return alertState{}
	}
	v, th := pf.Value, a.Threshold
	note := todayNote(pf.DayPct, pf.DayChange != 0)
	switch a.Kind {
	case alertAbove:
		return alertState{ok: true, cond: v >= th, rearm: v < th*(1-rearmPrice),
			text: "💰 Tu cartera ha superado " + eurosRound(th) + ": ahora " + eurosRound(v) + note}
	case alertBelow:
		return alertState{ok: true, cond: v <= th, rearm: v > th*(1+rearmPrice),
			text: "💰 Tu cartera ha bajado de " + eurosRound(th) + ": ahora " + eurosRound(v) + note}
	case alertDay:
		ch := pf.DayPct
		verb := " sube un "
		if ch < 0 {
			verb = " cae un "
		}
		return alertState{ok: true, cond: math.Abs(ch) >= th, rearm: math.Abs(ch) < math.Max(0, th-rearmPct),
			text: "💰 Tu cartera" + verb + num(math.Abs(ch), 1) + " % hoy (" + signed(eurosRound(pf.DayChange), pf.DayChange) + "): ahora " + eurosRound(v)}
	}
	return alertState{}
}

// step aplica a una alerta lo que dice ahora. Devuelve si ha cambiado y el
// texto del aviso si salta.
func step(a *alert, st alertState, now time.Time) (changed bool, fire string) {
	if !a.Active || !st.ok {
		return false, ""
	}
	switch {
	case st.cond && !a.Crossed:
		a.Crossed, a.Fired, a.Last = true, now, st.text
		if !a.Repeat {
			a.Active = false
		}
		return true, st.text
	case a.Crossed && st.rearm:
		a.Crossed = false
		return true, ""
	}
	return false, ""
}

// checkAlerts evalúa las alertas de una persona y encola los avisos.
func (s *service) checkAlerts(ctx context.Context, owner string) (int, error) {
	list, err := s.store.alerts(ctx, owner)
	if err != nil || len(list) == 0 {
		return 0, err
	}
	now := s.clock()
	v, err := s.load(ctx, owner)
	if err != nil {
		return 0, err
	}
	fired := 0
	for i := range list {
		a := &list[i]
		var st alertState
		if a.AssetID == 0 {
			st = evalPortfolio(*a, v.pf)
		} else {
			st = evalAsset(*a, v.pf.pos(a.AssetID), v.hist[a.AssetID], now)
		}
		changed, text := step(a, st, now)
		if !changed {
			continue
		}
		if text != "" {
			if err := s.store.addNotice(ctx, owner, text, now); err != nil {
				return fired, err
			}
			fired++
		}
		if err := s.store.saveAlertState(ctx, owner, *a); err != nil {
			return fired, err
		}
	}
	return fired, nil
}
