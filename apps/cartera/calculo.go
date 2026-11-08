package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Las cuentas de la cartera, todas en euros: cada operación se pasa con el
// cambio del BCE de su día, y el valor actual con el último. Sin IA: lo que
// se ve en la web, en el resumen de la mañana y lo que recibe Talia sale de
// aquí.
//
// Ganancia realizada por FIFO, como pide Hacienda: cada venta se casa con las
// compras más antiguas de ese activo, sea cual sea la plataforma. Por eso las
// unidades se siguen por plataforma (para saber dónde está cada cosa) pero el
// coste es uno por activo; el coste de cada plataforma es su parte de las
// unidades.

const eps = 1e-9

// lot es una compra (o recompensa) que aún no se ha vendido entera.
type lot struct {
	Day    time.Time
	Qty    float64
	Cost   float64 // EUR, con comisiones, de las unidades que quedan
	Native float64 // lo mismo en la moneda del activo
}

// position es lo que hay de un activo.
type position struct {
	Asset      asset
	Qty        float64
	Cost       float64 // EUR de lo que queda (FIFO)
	Native     float64 // coste en la moneda del activo
	Realized   float64 // ganancia o pérdida de las ventas
	Income     float64 // dividendos netos y recompensas
	Fees       float64 // comisiones sueltas de este activo
	Platforms  map[string]float64
	lots       []lot
	lastPrice  float64 // precio de la última operación, en la moneda del activo (de reserva)
	oversold   bool
	noFX       bool
	Ops        int
	Price      float64 // precio actual en su moneda; 0 si no se sabe
	PriceFrom  string  // de dónde sale: fuente, manual, histórico
	Quote      quote
	Value      float64 // EUR
	DayChange  float64 // EUR
	DayPct     float64
	Unrealized float64
	UnrealPct  float64
	Priced     bool
}

func (p *position) Avg() float64 {
	if p.Qty <= eps {
		return 0
	}
	return p.Cost / p.Qty
}

func (p *position) AvgNative() float64 {
	if p.Qty <= eps {
		return 0
	}
	return p.Native / p.Qty
}

// engine aplica las operaciones en orden.
type engine struct {
	fx        fxTable
	pos       map[int64]*position
	looseFees float64
	flows     float64 // dinero metido menos el sacado (compras y ventas), en euros
	noFX      map[string]bool
}

func newEngine(assets []asset, fx fxTable) *engine {
	e := &engine{fx: fx, pos: map[int64]*position{}, noFX: map[string]bool{}}
	for _, a := range assets {
		e.pos[a.ID] = &position{Asset: a, Platforms: map[string]float64{}}
	}
	return e
}

// eur pasa un importe de una moneda a euros con el cambio de ese día.
func (e *engine) eur(v float64, cur string, day time.Time) float64 {
	r, ok := e.fx.rate(cur, day)
	if !ok {
		e.noFX[cur] = true
		return v // sin cambio conocido: mejor algo que nada, y se avisa
	}
	return v / r
}

// native pasa euros a la moneda del activo con el cambio de ese día.
func (e *engine) native(v float64, cur string, day time.Time) float64 {
	r, ok := e.fx.rate(cur, day)
	if !ok {
		return v
	}
	return v * r
}

// opCurrency es la moneda de la operación (la del activo si no se dijo).
func opCurrency(o operation, p *position) string {
	if o.Currency != "" {
		return o.Currency
	}
	if p != nil {
		return p.Asset.Currency
	}
	return "EUR"
}

// take saca n unidades por FIFO y devuelve su coste en euros y en la moneda
// del activo.
func (p *position) take(n float64) (cost, native float64) {
	for n > eps && len(p.lots) > 0 {
		l := &p.lots[0]
		if l.Qty <= n+eps {
			cost += l.Cost
			native += l.Native
			n -= l.Qty
			p.lots = p.lots[1:]
			continue
		}
		part := n / l.Qty
		cost += l.Cost * part
		native += l.Native * part
		l.Cost -= l.Cost * part
		l.Native -= l.Native * part
		l.Qty -= n
		n = 0
	}
	if n > eps {
		p.oversold = true
	}
	return cost, native
}

func (p *position) move(platform string, n float64) {
	p.Platforms[platform] += n
	if math.Abs(p.Platforms[platform]) <= eps {
		delete(p.Platforms, platform)
	}
}

func (e *engine) apply(o operation) {
	p := e.pos[o.AssetID]
	cur := opCurrency(o, p)
	if p == nil {
		// Comisión general (sin activo) o de un activo ya borrado.
		if o.Kind == opFee {
			e.looseFees += e.eur(o.Amount+o.Fee, cur, o.Day)
		}
		return
	}
	p.Ops++
	acur := p.Asset.Currency
	unitNative := func(unit float64) float64 {
		if cur == acur {
			return unit
		}
		return e.native(e.eur(unit, cur, o.Day), acur, o.Day)
	}
	switch o.Kind {
	case opBuy, opReward:
		gross := o.Qty * o.Price
		fee := o.Fee
		if o.Kind == opReward {
			fee = 0
		}
		cost := e.eur(gross+fee, cur, o.Day)
		nat := unitNative(gross + fee)
		p.lots = append(p.lots, lot{Day: o.Day, Qty: o.Qty, Cost: cost, Native: nat})
		p.Qty += o.Qty
		p.Cost += cost
		p.Native += nat
		p.move(o.Platform, o.Qty)
		if o.Price > 0 {
			p.lastPrice = unitNative(o.Price)
		}
		if o.Kind == opReward {
			p.Income += cost // valor de mercado al recibirla
		} else {
			e.flows += cost
		}
	case opSell:
		cost, nat := p.take(o.Qty)
		got := e.eur(o.Qty*o.Price-o.Fee, cur, o.Day)
		p.Realized += got - cost
		e.flows -= got
		p.Qty = math.Max(0, p.Qty-o.Qty)
		p.Cost = math.Max(0, p.Cost-cost)
		p.Native = math.Max(0, p.Native-nat)
		p.move(o.Platform, -o.Qty)
		if o.Price > 0 {
			p.lastPrice = unitNative(o.Price)
		}
	case opDividend:
		p.Income += e.eur(o.Amount-o.Fee, cur, o.Day)
	case opFee:
		// En unidades del activo (la red se queda parte de una cripto al
		// sacarla) y/o en dinero.
		if o.Qty > 0 {
			cost, nat := p.take(o.Qty)
			p.Qty = math.Max(0, p.Qty-o.Qty)
			p.Cost = math.Max(0, p.Cost-cost)
			p.Native = math.Max(0, p.Native-nat)
			p.Fees += cost
			p.move(o.Platform, -o.Qty)
		}
		if m := o.Amount + o.Fee; m > 0 {
			p.Fees += e.eur(m, cur, o.Day)
		}
	case opTransfer:
		p.move(o.Platform, -o.Qty)
		p.move(o.To, o.Qty)
		if o.Fee > 0 {
			p.Fees += e.eur(o.Fee, cur, o.Day)
		}
	}
	if p.Qty <= eps {
		p.Qty, p.Cost, p.Native, p.lots = 0, 0, 0, nil
	}
}

// share es una parte del reparto.
type share struct {
	Key   string
	Value float64
	Pct   float64
}

// portfolio es la cartera entera de una persona en este momento.
type portfolio struct {
	Positions  []*position // todos los activos, también los que ya no se tienen
	Value      float64
	Cost       float64
	Unrealized float64
	UnrealPct  float64
	Realized   float64
	Income     float64
	Fees       float64 // comisiones sueltas (de activos y generales)
	DayChange  float64
	DayPct     float64
	Total      float64 // ganancia total: no realizada + realizada + rendimientos − comisiones
	ByKind     []share
	ByPlatform []share
	ByCurrency []share
	Warnings   []string
	Held       int // activos con unidades
}

// pos busca la posición de un activo.
func (pf *portfolio) pos(id int64) *position {
	for _, p := range pf.Positions {
		if p.Asset.ID == id {
			return p
		}
	}
	return nil
}

// priceOf es el precio actual de un activo en su moneda: el de la fuente, el
// manual o, si no hay otro, el último cierre guardado.
func priceOf(a asset, q quote, hasQuote bool, h series) (float64, quote, string) {
	switch {
	case a.Source == sourceManual && a.Manual > 0:
		return a.Manual, quote{Price: a.Manual, Currency: a.Currency, At: a.ManualAt}, "manual"
	case hasQuote && q.Price > 0:
		if q.Currency == "" {
			q.Currency = a.Currency
		}
		return q.Price, q, a.Source
	case a.Manual > 0:
		return a.Manual, quote{Price: a.Manual, Currency: a.Currency, At: a.ManualAt}, "manual"
	}
	if d, c, ok := h.last(); ok {
		return c, quote{Price: c, Currency: a.Currency, At: d}, "historico"
	}
	return 0, quote{}, ""
}

// compute calcula la cartera con las operaciones, las cotizaciones y los
// cambios de divisas.
func compute(assets []asset, ops []operation, quotes map[int64]quote, hist map[int64]series, fx fxTable, now time.Time) *portfolio {
	e := newEngine(assets, fx)
	sortOps(ops)
	for _, o := range ops {
		e.apply(o)
	}
	return e.value(quotes, hist, fx, now)
}

func (e *engine) value(quotes map[int64]quote, hist map[int64]series, fx fxTable, now time.Time) *portfolio {
	pf := &portfolio{Fees: e.looseFees}
	today := dayOf(now)
	kind, plat, curr := map[string]float64{}, map[string]float64{}, map[string]float64{}
	var prevValue float64
	for _, p := range e.pos {
		pf.Positions = append(pf.Positions, p)
		pf.Realized += p.Realized
		pf.Income += p.Income
		pf.Fees += p.Fees
		if p.oversold {
			pf.Warnings = append(pf.Warnings, "En "+p.Asset.Symbol+" se vende más de lo que se había comprado: revisa sus operaciones.")
		}
		q, ok := quotes[p.Asset.ID]
		p.Price, p.Quote, p.PriceFrom = priceOf(p.Asset, q, ok, hist[p.Asset.ID])
		if p.Price <= 0 && p.lastPrice > 0 {
			p.Price, p.PriceFrom = p.lastPrice, "operacion"
			p.Quote = quote{Price: p.lastPrice, Currency: p.Asset.Currency}
		}
		if p.Qty <= eps {
			continue
		}
		pf.Held++
		pf.Cost += p.Cost
		if p.Price <= 0 {
			pf.Warnings = append(pf.Warnings, p.Asset.Symbol+" no tiene precio: ponle uno a mano en su ficha.")
			continue
		}
		cur := p.Quote.Currency
		r, okFX := fx.rate(cur, today)
		if !okFX {
			e.noFX[cur] = true
			r = 1
		}
		p.Priced = true
		p.Value = p.Qty * p.Price / r
		p.Unrealized = p.Value - p.Cost
		if p.Cost > eps {
			p.UnrealPct = p.Unrealized / p.Cost * 100
		}
		if p.Quote.Prev > 0 && dayOf(p.Quote.At).Equal(today) || p.Quote.Prev > 0 && p.Asset.Kind == "cripto" {
			p.DayChange = p.Qty * (p.Price - p.Quote.Prev) / r
			p.DayPct = p.Quote.change()
		}
		pf.Value += p.Value
		pf.DayChange += p.DayChange
		prevValue += p.Value - p.DayChange
		kind[kindLabel(p.Asset.Kind)] += p.Value
		curr[p.Asset.Currency] += p.Value
		for name, n := range p.Platforms {
			if n <= eps {
				continue
			}
			if name == "" {
				name = "Sin plataforma"
			}
			plat[name] += p.Value * n / p.Qty
		}
	}
	for c := range e.noFX {
		pf.Warnings = append(pf.Warnings, "Falta el cambio de "+c+": esos importes van sin convertir.")
	}
	sort.Strings(pf.Warnings)
	sort.Slice(pf.Positions, func(i, j int) bool {
		a, b := pf.Positions[i], pf.Positions[j]
		if (a.Qty > eps) != (b.Qty > eps) {
			return a.Qty > eps
		}
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		return strings.ToLower(a.Asset.Symbol) < strings.ToLower(b.Asset.Symbol)
	})
	pf.Unrealized = 0
	for _, p := range pf.Positions {
		if p.Priced {
			pf.Unrealized += p.Unrealized
		}
	}
	if pf.Cost > eps {
		pf.UnrealPct = pf.Unrealized / pf.Cost * 100
	}
	if prevValue > eps {
		pf.DayPct = pf.DayChange / prevValue * 100
	}
	pf.Total = pf.Unrealized + pf.Realized + pf.Income - pf.Fees
	pf.ByKind, pf.ByPlatform, pf.ByCurrency = shares(kind, pf.Value), shares(plat, pf.Value), shares(curr, pf.Value)
	return pf
}

func shares(m map[string]float64, total float64) []share {
	var out []share
	for k, v := range m {
		if v <= 0 {
			continue
		}
		s := share{Key: k, Value: v}
		if total > 0 {
			s.Pct = v / total * 100
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ---- La cartera en el tiempo ----

// valuePoint es el valor de la cartera al cierre de un día, y lo que costó
// lo que había entonces.
type valuePoint struct {
	Day   time.Time
	Value float64
	Cost  float64
	Flow  float64 // lo metido menos lo sacado hasta ese día
}

// valueSeries reconstruye el valor de la cartera día a día desde from: las
// unidades de cada día (por las operaciones) por el cierre de ese día, con el
// cambio de ese día. Hoy, con la cotización actual. Sin cierre guardado de
// un día, el último anterior; si no hay ninguno, el precio de la última
// operación.
func valueSeries(assets []asset, ops []operation, quotes map[int64]quote, hist map[int64]series, fx fxTable, from, now time.Time) []valuePoint {
	if len(ops) == 0 {
		return nil
	}
	sortOps(ops)
	today := dayOf(now)
	if first := ops[0].Day; from.Before(first) {
		from = first
	}
	if from.After(today) {
		return nil
	}
	e := newEngine(assets, fx)
	var out []valuePoint
	i := 0
	for d := from; !d.After(today); d = d.Add(dayUnit) {
		// Las operaciones de antes de from también cuentan (las unidades).
		for i < len(ops) && !ops[i].Day.After(d) {
			e.apply(ops[i])
			i++
		}
		var v, c float64
		for id, p := range e.pos {
			if p.Qty <= eps {
				continue
			}
			c += p.Cost
			px, cur := 0.0, p.Asset.Currency
			if d.Equal(today) {
				q, ok := quotes[id]
				px, q, _ = priceOf(p.Asset, q, ok, hist[id])
				if q.Currency != "" {
					cur = q.Currency
				}
			} else if h, ok := hist[id].at(d); ok {
				px = h
			} else if p.Asset.Source == sourceManual && p.Asset.Manual > 0 {
				px = p.Asset.Manual
			}
			if px <= 0 {
				px = p.lastPrice
			}
			r, ok := fx.rate(cur, d)
			if !ok {
				r = 1
			}
			v += p.Qty * px / r
		}
		out = append(out, valuePoint{Day: d, Value: v, Cost: c, Flow: e.flows})
	}
	return out
}

// change es cuánto ha cambiado el valor desde hace days días (en euros y en
// porcentaje), según la serie. false si no hay dato de entonces.
func change(vs []valuePoint, days int) (float64, float64, bool) {
	if len(vs) < 2 {
		return 0, 0, false
	}
	last := vs[len(vs)-1]
	want := last.Day.Add(-time.Duration(days) * dayUnit)
	for i := len(vs) - 1; i >= 0; i-- {
		if !vs[i].Day.After(want) {
			base := vs[i].Value
			if base <= eps {
				return 0, 0, false
			}
			// Lo que se ha comprado o vendido entre medias no es ganancia ni
			// pérdida: se descuenta el dinero metido o sacado.
			diff := last.Value - base - (last.Flow - vs[i].Flow)
			return diff, diff / base * 100, true
		}
	}
	return 0, 0, false
}
