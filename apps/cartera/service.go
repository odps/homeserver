package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type service struct {
	store  store
	market *market
	log    *slog.Logger
	now    func() time.Time

	yahooGap time.Duration // pausa entre peticiones a Yahoo (0 en los tests)

	mu       sync.Mutex
	last     map[string]time.Time // última pasada de cada fuente
	dailyOn  time.Time            // día de la última pasada diaria
	kickOnce sync.Once
	kickC    chan struct{}
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// inputError es un error de lo que ha escrito la persona: se le puede
// enseñar tal cual. Los demás errores no salen de la app.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func bad(msg string) error { return inputError{msg} }

func isInput(err error) bool {
	var ie inputError
	return errors.As(err, &ie)
}

// ---- Lo de una persona, ya calculado ----

type view struct {
	assets []asset
	ops    []operation
	quotes map[int64]quote
	hist   map[int64]series
	fx     fxTable
	pf     *portfolio
	now    time.Time
}

func (s *service) load(ctx context.Context, owner string) (*view, error) {
	v := &view{now: s.clock()}
	var err error
	if v.assets, err = s.store.assets(ctx, owner); err != nil {
		return nil, err
	}
	if v.ops, err = s.store.ops(ctx, owner); err != nil {
		return nil, err
	}
	if v.quotes, err = s.store.quotes(ctx, owner); err != nil {
		return nil, err
	}
	if v.hist, err = s.store.histories(ctx, owner); err != nil {
		return nil, err
	}
	if v.fx, err = s.store.fx(ctx); err != nil {
		return nil, err
	}
	v.pf = compute(v.assets, append([]operation(nil), v.ops...), v.quotes, v.hist, v.fx, v.now)
	return v, nil
}

func (v *view) asset(id int64) (asset, bool) {
	for _, a := range v.assets {
		if a.ID == id {
			return a, true
		}
	}
	return asset{}, false
}

// series es el valor de la cartera desde from.
func (v *view) series(from time.Time) []valuePoint {
	return valueSeries(v.assets, append([]operation(nil), v.ops...), v.quotes, v.hist, v.fx, from, v.now)
}

// ---- Buscar y añadir activos ----

// search busca en las tres fuentes: Kraken primero (cripto en euros), luego
// Yahoo (acciones, ETF y fondos; también por ISIN) y CoinGecko. Lo que falle
// se dice aparte, sin detalles.
func (s *service) search(ctx context.Context, q string) ([]found, []string) {
	q = clip(q, 60)
	if q == "" {
		return nil, nil
	}
	var out []found
	var failed []string
	try := func(name string, f func(context.Context, string) ([]found, error)) {
		list, err := f(ctx, q)
		if err != nil && !errors.Is(err, errNoMatch) {
			failed = append(failed, name)
		}
		out = append(out, list...)
	}
	if looksISIN(q) {
		try(sourceName(sourceYahoo), s.market.searchYahoo)
		return out, failed
	}
	try(sourceName(sourceKraken), s.market.searchKraken)
	try(sourceName(sourceYahoo), s.market.searchYahoo)
	try(sourceName(sourceGecko), s.market.searchGecko)
	return out, failed
}

// resolve pide a la fuente los datos de un activo (lo que se guarda no se
// toma del navegador ni de la IA): nombre, moneda, tipo, y, si la fuente lo
// da a la vez, cotización e histórico.
func (s *service) resolve(ctx context.Context, ref string) (assetData, *quote, series, error) {
	source, id, ok := parseRef(ref)
	if !ok {
		return assetData{}, nil, series{}, bad("Ese activo no es válido.")
	}
	switch source {
	case sourceKraken:
		p, err := s.market.krakenPair(ctx, id)
		if err != nil {
			return assetData{}, nil, series{}, err
		}
		return assetData{Symbol: p.Base, Name: p.Base, Kind: "cripto", Source: sourceKraken, SourceID: p.Altname, Currency: "EUR"}, nil, series{}, nil
	case sourceGecko:
		list, err := s.market.searchGecko(ctx, id)
		if err != nil {
			return assetData{}, nil, series{}, err
		}
		for _, f := range list {
			if f.SourceID == id {
				return assetData{Symbol: f.Symbol, Name: f.Name, Kind: "cripto", Source: sourceGecko, SourceID: f.SourceID, Currency: "EUR"}, nil, series{}, nil
			}
		}
		return assetData{}, nil, series{}, errNoMatch
	case sourceYahoo:
		info, err := s.market.yahooChart(ctx, id, "5y")
		if err != nil {
			return assetData{}, nil, series{}, err
		}
		kind := info.Kind
		if kind == "" {
			kind = "otro"
		}
		name := info.Name
		if name == "" {
			name = id
		}
		q := info.quote
		return assetData{Symbol: strings.ToUpper(id), Name: name, Kind: kind, Source: sourceYahoo, SourceID: id, Currency: info.Currency}, &q, info.Hist, nil
	}
	return assetData{}, nil, series{}, errNoMatch
}

// addFound añade (o encuentra, si ya estaba) el activo de una búsqueda, con
// su histórico. isin y kind, si vienen, completan lo que da la fuente.
func (s *service) addFound(ctx context.Context, owner, ref, kind, isin string, watch bool) (asset, bool, error) {
	d, q, h, err := s.resolve(ctx, ref)
	if errors.Is(err, errNoMatch) {
		return asset{}, false, bad("La fuente no tiene ese activo.")
	}
	if err != nil {
		return asset{}, false, err
	}
	if validKind(kind) {
		d.Kind = kind
	}
	if looksISIN(isin) {
		d.ISIN = strings.ToUpper(isin)
	}
	a, existed, err := s.store.addAsset(ctx, owner, d, watch)
	if err != nil || existed {
		return a, existed, err
	}
	if q == nil {
		// Su cotización ya, para no esperar a la siguiente pasada.
		var got map[string]quote
		switch d.Source {
		case sourceKraken:
			got, _ = s.market.krakenQuotes(ctx, []string{d.SourceID})
		case sourceGecko:
			got, _ = s.market.geckoQuotes(ctx, []string{d.SourceID})
		}
		if x, ok := got[d.SourceID]; ok {
			q = &x
		}
	}
	if q != nil {
		s.store.setQuote(ctx, owner, a.ID, *q)
	}
	if h.len() == 0 {
		h, _ = s.fetchHistory(ctx, a.assetData, true)
	}
	if h.len() > 0 {
		if err := s.store.setHistory(ctx, owner, a.ID, h, dayOf(s.clock())); err != nil {
			s.log.Warn("histórico de un activo nuevo", "err", err)
		}
	}
	s.kick() // su cotización, en la siguiente pasada
	return a, false, nil
}

var reCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

// manualAsset es un activo sin fuente: el precio se pone a mano.
func (s *service) addManual(ctx context.Context, owner, symbol, name, kind, cur string, px float64, watch bool) (asset, bool, error) {
	symbol, name, cur = clip(symbol, 20), clip(name, 80), strings.ToUpper(strings.TrimSpace(cur))
	if cur == "" {
		cur = "EUR"
	}
	switch {
	case symbol == "" && name == "":
		return asset{}, false, bad("Ponle al menos un nombre o un símbolo.")
	case !reCurrency.MatchString(cur):
		return asset{}, false, bad("La moneda va con su código de tres letras: EUR, USD…")
	case !validKind(kind):
		kind = "otro"
	}
	if px < 0 || math.IsInf(px, 0) {
		return asset{}, false, bad("El precio no es válido.")
	}
	if symbol == "" {
		symbol = strings.ToUpper(clip(name, 12))
	}
	if name == "" {
		name = symbol
	}
	d := assetData{Symbol: symbol, Name: name, Kind: kind, Source: sourceManual, Currency: cur, Manual: px}
	if px > 0 {
		d.ManualAt = s.clock()
	}
	return s.store.addAsset(ctx, owner, d, watch)
}

// ---- Cotizar cualquier cosa (Talia) ----

// priced es un activo encontrado con su cotización.
type priced struct {
	found
	quote
}

// lookup cotiza un símbolo, nombre o ISIN aunque no esté en la cartera: en
// Kraken si es una cripto que tenga en euros; si no, en Yahoo y, por
// último, en CoinGecko. Pocas peticiones: un resultado por fuente.
func (s *service) lookup(ctx context.Context, q string) ([]priced, error) {
	q = clip(q, 60)
	if q == "" {
		return nil, bad("Di qué activo quieres cotizar.")
	}
	var out []priced
	var lastErr error
	if !looksISIN(q) {
		if list, err := s.market.searchKraken(ctx, q); err == nil && len(list) > 0 && strings.EqualFold(list[0].Symbol, q) {
			qs, err := s.market.krakenQuotes(ctx, []string{list[0].SourceID})
			if p, ok := qs[list[0].SourceID]; err == nil && ok {
				return []priced{{list[0], p}}, nil
			}
		} else if err != nil {
			lastErr = err
		}
	}
	if list, err := s.market.searchYahoo(ctx, q); err == nil && len(list) > 0 {
		info, err := s.market.yahooChart(ctx, list[0].SourceID, "5d")
		if err == nil {
			f := list[0]
			f.Currency = info.Currency
			out = append(out, priced{f, info.quote})
		} else {
			lastErr = err
		}
	} else if err != nil {
		lastErr = err
	}
	if len(out) > 0 || looksISIN(q) {
		return out, lastErr
	}
	if list, err := s.market.searchGecko(ctx, q); err == nil && len(list) > 0 {
		qs, err := s.market.geckoQuotes(ctx, []string{list[0].SourceID})
		if p, ok := qs[list[0].SourceID]; err == nil && ok {
			out = append(out, priced{list[0], p})
		} else if err != nil {
			lastErr = err
		}
	} else if err != nil {
		lastErr = err
	}
	return out, lastErr
}

// ---- Operaciones ----

// opInput es una operación tal como llega de la web o de Talia.
type opInput struct {
	AssetID  int64
	Kind     string
	Day      time.Time
	Time     string
	Qty      float64
	Price    float64
	Total    float64 // importe total, si no se da el precio por unidad
	Fee      float64
	Currency string
	Platform string
	To       string
	Note     string
	Origin   string
}

var reClock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// defaultPlatforms son las que salen siempre al apuntar; las demás, las que
// ya se hayan usado.
var defaultPlatforms = []string{"DEGIRO", "Binance", "Kraken"}

// platforms son las plataformas conocidas de una persona.
func platforms(ops []operation) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	for _, p := range defaultPlatforms {
		add(p)
	}
	for _, o := range ops {
		add(o.Platform)
		add(o.To)
	}
	return out
}

// canonPlatform escribe una plataforma como ya se conocía («kraken» →
// «Kraken»), para que no salgan dos veces en el reparto.
func canonPlatform(p string, known []string) string {
	p = clip(p, 40)
	for _, k := range known {
		if strings.EqualFold(k, p) {
			return k
		}
	}
	return p
}

// buildOp valida una operación y la deja lista para guardar.
func (s *service) buildOp(ctx context.Context, owner string, v *view, in opInput) (operation, error) {
	o := operation{AssetID: in.AssetID, Day: in.Day, opData: opData{Kind: in.Kind, Note: clip(in.Note, 300), Origin: in.Origin}}
	var a asset
	if in.AssetID != 0 {
		var ok bool
		if a, ok = v.asset(in.AssetID); !ok {
			return o, errNotFound
		}
	}
	today := dayOf(s.clock())
	switch {
	case o.Day.IsZero():
		return o, bad("Falta la fecha.")
	case o.Day.After(today):
		return o, bad("La fecha no puede ser futura.")
	case o.Day.Year() < 1990:
		return o, bad("Esa fecha es demasiado antigua.")
	}
	if t := strings.TrimSpace(in.Time); t != "" {
		if !reClock.MatchString(t) {
			return o, bad("La hora va como 09:30.")
		}
		o.Time = t
	}
	for _, x := range []float64{in.Qty, in.Price, in.Total, in.Fee} {
		if x < 0 || math.IsNaN(x) || math.IsInf(x, 0) || x > 1e12 {
			return o, bad("Los números no pueden ser negativos.")
		}
	}
	o.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if o.Currency == "" {
		o.Currency = a.Currency
		if o.Currency == "" {
			o.Currency = "EUR"
		}
	}
	if !reCurrency.MatchString(o.Currency) {
		return o, bad("La moneda va con su código de tres letras: EUR, USD…")
	}
	known := platforms(v.ops)
	o.Platform, o.To = canonPlatform(in.Platform, known), canonPlatform(in.To, known)
	o.Qty, o.Price, o.Fee = in.Qty, in.Price, in.Fee
	if in.AssetID == 0 && o.Kind != opFee {
		return o, bad("Elige el activo.")
	}
	switch o.Kind {
	case opBuy, opSell:
		if o.Qty <= 0 {
			return o, bad("Falta la cantidad.")
		}
		if o.Price <= 0 && in.Total > 0 {
			o.Price = in.Total / o.Qty
		}
		if o.Price <= 0 {
			return o, bad("Falta el precio (por unidad o el total).")
		}
	case opReward:
		if o.Qty <= 0 {
			return o, bad("Falta la cantidad recibida.")
		}
		if o.Price <= 0 && in.Total > 0 {
			o.Price = in.Total / o.Qty
		}
		o.Fee = 0
		if o.Price <= 0 {
			// Su valor de mercado ese día, del histórico (en la moneda del
			// activo).
			px, ok := v.hist[a.ID].at(o.Day)
			if o.Day.Equal(today) {
				if q, okq := v.quotes[a.ID]; okq && q.Price > 0 {
					px, ok = q.Price, true
				}
			}
			if !ok || px <= 0 {
				return o, bad("No sé lo que valía ese día: pon el precio por unidad.")
			}
			o.Price, o.Currency = px, a.Currency
		}
	case opDividend:
		o.Amount = in.Total
		if o.Amount <= 0 && o.Qty > 0 && o.Price > 0 {
			o.Amount = o.Qty * o.Price
		}
		if o.Amount <= 0 {
			return o, bad("Falta el importe bruto del dividendo.")
		}
	case opFee:
		o.Amount, o.Price = in.Total+in.Fee, 0
		o.Fee = 0
		if in.AssetID == 0 {
			o.Qty = 0
		}
		if o.Amount <= 0 && o.Qty <= 0 {
			return o, bad("Falta el importe de la comisión (o las unidades que se cobró).")
		}
	case opTransfer:
		if o.Qty <= 0 {
			return o, bad("Falta la cantidad traspasada.")
		}
		if o.Platform == "" || o.To == "" || strings.EqualFold(o.Platform, o.To) {
			return o, bad("Un traspaso va de una plataforma a otra distinta.")
		}
		o.Price = 0
	default:
		return o, bad("Tipo de operación no válido.")
	}
	if o.Kind != opTransfer {
		o.To = ""
	}
	return o, nil
}

func (s *service) addOp(ctx context.Context, owner string, in opInput) (operation, error) {
	v, err := s.load(ctx, owner)
	if err != nil {
		return operation{}, err
	}
	o, err := s.buildOp(ctx, owner, v, in)
	if err != nil {
		return o, err
	}
	o.ID, err = s.store.addOp(ctx, owner, o)
	if err == nil && (v.fx.first().IsZero() || o.Day.Before(v.fx.first())) {
		s.kick() // faltan los cambios de esas fechas
	}
	return o, err
}

func (s *service) updateOp(ctx context.Context, owner string, id int64, in opInput) (operation, error) {
	v, err := s.load(ctx, owner)
	if err != nil {
		return operation{}, err
	}
	old, err := s.store.op(ctx, owner, id)
	if err != nil {
		return old, err
	}
	in.Origin = old.Origin
	o, err := s.buildOp(ctx, owner, v, in)
	if err != nil {
		return o, err
	}
	o.ID = id
	if err := s.store.updateOp(ctx, owner, o); err != nil {
		return o, err
	}
	if o.Day.Before(v.fx.first()) {
		s.kick()
	}
	return o, nil
}

// ---- Resumen de la mañana ----

// todayLines son las líneas de la cartera para el resumen de la mañana de la
// Agenda: el valor y cuánto ha cambiado desde ayer y en 7 días. Sin nada en
// cartera, ninguna.
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	v, err := s.load(ctx, owner)
	if err != nil {
		return nil, err
	}
	if v.pf.Held == 0 || v.pf.Value <= 0 {
		return nil, nil
	}
	lines := []string{"Valor: " + eurosRound(v.pf.Value) + " (" + signed(eurosRound(v.pf.Total), v.pf.Total) + " en total)"}
	vs := v.series(dayOf(v.now).Add(-8 * dayUnit))
	var parts []string
	if d, p, ok := change(vs, 1); ok {
		parts = append(parts, "desde ayer "+pct(p)+" ("+signed(eurosRound(d), d)+")")
	}
	if d, p, ok := change(vs, 7); ok {
		parts = append(parts, "en 7 días "+pct(p)+" ("+signed(eurosRound(d), d)+")")
	}
	if len(parts) > 0 {
		s := strings.Join(parts, " · ")
		lines = append(lines, strings.ToUpper(s[:1])+s[1:]+".")
	}
	return lines, nil
}

// ---- Tarea periódica ----

// Cada cuánto se pide a cada fuente.
var every = map[string]time.Duration{sourceKraken: 5 * time.Minute, sourceGecko: 10 * time.Minute, sourceYahoo: 15 * time.Minute}

func (s *service) kick() {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	select {
	case s.kickC <- struct{}{}:
	default:
	}
}

// run actualiza precios, cambios e históricos y evalúa las alertas.
func (s *service) run(ctx context.Context) {
	s.kickOnce.Do(func() { s.kickC = make(chan struct{}, 1) })
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		s.round(rctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kickC:
			s.mu.Lock()
			s.dailyOn = time.Time{} // lo diario (cambios, históricos) otra vez
			s.mu.Unlock()
		}
	}
}

// live son los activos de todos que se tienen o se siguen, por dueño.
func (s *service) live(ctx context.Context) ([]asset, error) {
	all, err := s.store.allAssets(ctx)
	if err != nil {
		return nil, err
	}
	owners := map[string][]asset{}
	for _, a := range all {
		owners[a.Owner] = append(owners[a.Owner], a)
	}
	var out []asset
	for owner, list := range owners {
		ops, err := s.store.ops(ctx, owner)
		if err != nil {
			return nil, err
		}
		e := newEngine(list, fxTable{})
		for _, o := range ops {
			e.apply(o)
		}
		for _, a := range list {
			if a.Watch || e.pos[a.ID].Qty > eps {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// round es una pasada: las fuentes a las que les toca y, una vez al día,
// los cambios y los históricos.
func (s *service) round(ctx context.Context) {
	now := s.clock()
	// Casi siempre no toca nada: entonces ni se lee la base de datos.
	s.mu.Lock()
	idle := dayOf(now).Equal(s.dailyOn)
	for src, d := range every {
		if now.Sub(s.last[src]) >= d {
			idle = false
		}
	}
	s.mu.Unlock()
	if idle {
		return
	}
	assets, err := s.live(ctx)
	if err != nil {
		s.log.Warn("cartera: activos", "err", err)
		return
	}
	touched := map[string]bool{}
	for _, src := range []string{sourceKraken, sourceGecko, sourceYahoo} {
		s.mu.Lock()
		if s.last == nil {
			s.last = map[string]time.Time{}
		}
		due := now.Sub(s.last[src]) >= every[src]
		if due {
			s.last[src] = now
		}
		s.mu.Unlock()
		var mine []asset
		for _, a := range assets {
			if a.Source == src {
				mine = append(mine, a)
			}
		}
		// En espera tras un error, se salta esta vez sin insistir.
		if !due || len(mine) == 0 || !s.market.ready(src) {
			continue
		}
		for _, owner := range s.refreshQuotes(ctx, src, mine) {
			touched[owner] = true
		}
	}

	s.mu.Lock()
	daily := !dayOf(now).Equal(s.dailyOn)
	if daily {
		s.dailyOn = dayOf(now)
	}
	s.mu.Unlock()
	if daily {
		if err := s.syncFX(ctx); err != nil && !errors.Is(err, errSourceWait) {
			s.log.Warn("cartera: cambios de divisas", "err", err)
		}
		for _, owner := range s.refreshHistories(ctx, assets) {
			touched[owner] = true
		}
	}

	for owner := range touched {
		if n, err := s.checkAlerts(ctx, owner); err != nil {
			s.log.Warn("cartera: alertas", "err", err)
		} else if n > 0 {
			s.log.Info("cartera: alertas", "avisos", n)
		}
	}
}

// refreshQuotes pide las cotizaciones de una fuente (una llamada para todas
// en Kraken y CoinGecko; una por símbolo, con pausa, en Yahoo) y las guarda.
// Devuelve los dueños con algo nuevo.
func (s *service) refreshQuotes(ctx context.Context, src string, list []asset) []string {
	ids := map[string]bool{}
	var uniq []string
	for _, a := range list {
		if !ids[a.SourceID] {
			ids[a.SourceID] = true
			uniq = append(uniq, a.SourceID)
		}
	}
	got := map[string]quote{}
	switch src {
	case sourceKraken:
		q, err := s.market.krakenQuotes(ctx, uniq)
		if err != nil {
			return nil
		}
		got = q
	case sourceGecko:
		q, err := s.market.geckoQuotes(ctx, uniq)
		if err != nil {
			return nil
		}
		got = q
	case sourceYahoo:
		for i, id := range uniq {
			if i > 0 && s.yahooGap > 0 {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(s.yahooGap):
				}
			}
			info, err := s.market.yahooChart(ctx, id, "5d")
			if errors.Is(err, errSourceWait) {
				break // Yahoo se ha quejado: se espera, sin insistir
			}
			if err == nil {
				got[id] = info.quote
			}
		}
	}
	owners := map[string]bool{}
	for _, a := range list {
		q, ok := got[a.SourceID]
		if !ok {
			continue
		}
		if err := s.store.setQuote(ctx, a.Owner, a.ID, q); err != nil {
			s.log.Warn("cartera: guardar cotización", "err", err)
			continue
		}
		owners[a.Owner] = true
	}
	var out []string
	for o := range owners {
		out = append(out, o)
	}
	return out
}

// fetchHistory pide el histórico de un activo: entero (lo que dé la fuente
// sin clave) o solo el último mes para ponerlo al día.
func (s *service) fetchHistory(ctx context.Context, a assetData, full bool) (series, error) {
	switch a.Source {
	case sourceKraken:
		return s.market.krakenHistory(ctx, a.SourceID)
	case sourceGecko:
		days := 30
		if full {
			days = 365
		}
		return s.market.geckoHistory(ctx, a.SourceID, days)
	case sourceYahoo:
		rng := "1mo"
		if full {
			rng = "5y"
		}
		info, err := s.market.yahooChart(ctx, a.SourceID, rng)
		return info.Hist, err
	}
	return series{}, nil
}

// refreshHistories pone al día, una vez al día, el histórico de cada activo
// (lo mismo en dos cuentas se pide una sola vez).
func (s *service) refreshHistories(ctx context.Context, list []asset) []string {
	updated, err := s.store.historyDays(ctx)
	if err != nil {
		s.log.Warn("cartera: históricos", "err", err)
		return nil
	}
	today := dayOf(s.clock())
	cache := map[string]series{}
	owners := map[string]bool{}
	yahooAsked := 0
	for _, a := range list {
		if a.Source == sourceManual {
			continue
		}
		on, has := updated[a.ID]
		if has && !on.Before(today) {
			continue
		}
		key := a.key()
		h, ok := cache[key]
		if !ok {
			if a.Source == sourceYahoo {
				if !s.market.ready(sourceYahoo) {
					continue
				}
				if yahooAsked > 0 && s.yahooGap > 0 {
					select {
					case <-ctx.Done():
						return nil
					case <-time.After(s.yahooGap):
					}
				}
				yahooAsked++
			}
			h, err = s.fetchHistory(ctx, a.assetData, !has)
			if err != nil {
				continue
			}
			cache[key] = h
		}
		old, _, err := s.store.history(ctx, a.Owner, a.ID)
		if err != nil {
			continue
		}
		if err := s.store.setHistory(ctx, a.Owner, a.ID, old.merge(h), today); err != nil {
			s.log.Warn("cartera: guardar histórico", "err", err)
			continue
		}
		owners[a.Owner] = true
	}
	var out []string
	for o := range owners {
		out = append(out, o)
	}
	return out
}

// syncFX tiene al día los cambios del BCE desde la operación más antigua (o
// desde hace algo más de un año, para las gráficas).
func (s *service) syncFX(ctx context.Context) error {
	t, err := s.store.fx(ctx)
	if err != nil {
		return err
	}
	today := dayOf(s.clock())
	need := today.AddDate(-1, 0, -10)
	first, err := s.store.firstOpDay(ctx)
	if err != nil {
		return err
	}
	if !first.IsZero() && first.Before(need) {
		need = first
	}
	var rates map[string]map[string]float64
	switch {
	case t.first().IsZero() || t.first().After(need.Add(7*dayUnit)):
		rates, err = s.market.fxSeries(ctx, need)
	case t.last().Before(today.Add(-7 * dayUnit)):
		rates, err = s.market.fxSeries(ctx, t.last().Add(dayUnit))
	case t.last().Before(today):
		// El BCE no publica en fin de semana ni en festivo: el último que haya.
		rates, err = s.market.fxLatest(ctx)
	default:
		return nil
	}
	if errors.Is(err, errNoMatch) {
		return nil // aún no hay cambios nuevos publicados
	}
	if err != nil {
		return err
	}
	return s.store.saveFX(ctx, rates)
}
