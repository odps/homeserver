package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"homeserver/platform/seal"
)

type service struct {
	store store
	log   *slog.Logger
	now   func() time.Time // nil: time.Now

	known sync.Map // cuentas ya dadas de alta en esta ejecución

	mu      sync.Mutex
	dailyOn time.Time // día de la última pasada diaria
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *service) today() time.Time { return dayOf(s.clock()) }

// inputError es un error de lo que ha escrito la persona: se le puede
// enseñar tal cual. Los demás errores no salen de la app.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func bad(msg string) error { return inputError{msg} }

func isInput(err error) bool {
	var ie inputError
	return errors.As(err, &ie)
}

// ready da de alta la cuenta (con lo de serie) la primera vez que se usa.
func (s *service) ready(ctx context.Context, owner string) error {
	if _, ok := s.known.Load(owner); ok {
		return nil
	}
	if err := s.store.ensure(ctx, owner); err != nil {
		return err
	}
	s.known.Store(owner, true)
	return nil
}

// ---- Categorías y reglas ----

// cats es la lista de categorías con acceso por id y por clave de serie.
type cats struct {
	list  []category
	byID  map[int64]category
	byKey map[string]category
}

func newCats(list []category) cats {
	c := cats{list: list, byID: map[int64]category{}, byKey: map[string]category{}}
	for _, x := range list {
		c.byID[x.ID] = x
		if x.Key != "" {
			c.byKey[x.Key] = x
		}
	}
	return c
}

// name es el nombre de una categoría, o «Sin categoría».
func (c cats) name(id int64) string {
	if x, ok := c.byID[id]; ok {
		return x.Name
	}
	return "Sin categoría"
}

// find busca una categoría por su nombre (sin mayúsculas ni tildes): el
// nombre entero o, si solo hay una, la que empiece así.
func (c cats) find(name string) (category, bool) {
	q := strings.Join(seal.Words(name), " ")
	if q == "" {
		return category{}, false
	}
	var partial []category
	for _, x := range c.list {
		n := strings.Join(seal.Words(x.Name), " ")
		switch {
		case n == q || x.Key == q:
			return x, true
		case strings.HasPrefix(n, q):
			partial = append(partial, x)
		}
	}
	if len(partial) == 1 {
		return partial[0], true
	}
	return category{}, false
}

func (c cats) names() string {
	var out []string
	for _, x := range c.list {
		out = append(out, x.Name)
	}
	return strings.Join(out, ", ")
}

// ruleMatch dice si el concepto contiene el texto de la regla: sus palabras,
// seguidas y sin mayúsculas ni tildes, la última también como comienzo de
// palabra («farmacia» encaja en «Farmacias Gómez»).
func ruleMatch(text, concept string) bool {
	rw, cw := seal.Words(text), seal.Words(concept)
	if len(rw) == 0 {
		return false
	}
	for i := 0; i+len(rw) <= len(cw); i++ {
		ok := true
		for j, w := range rw {
			c := cw[i+j]
			if (j < len(rw)-1 && c != w) || (j == len(rw)-1 && !strings.HasPrefix(c, w)) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// classify elige la categoría de un concepto por las reglas: si encajan
// varias, la del texto más largo (la más concreta). 0 si ninguna.
func classify(rules []rule, concept string) int64 {
	best, bestLen := int64(0), 0
	for _, r := range rules {
		if n := len([]rune(seal.Fold(r.Text))); n > bestLen && ruleMatch(r.Text, concept) {
			best, bestLen = r.CatID, n
		}
	}
	return best
}

// fallback es la categoría de lo que no encaja en ninguna regla: Otros para
// los gastos e Ingresos para los ingresos (si existen).
func (c cats) fallback(kind string) int64 {
	key := keyOther
	if kind == kindIncome {
		key = keyIncome
	}
	return c.byKey[key].ID
}

func (s *service) loadCats(ctx context.Context, owner string) (cats, error) {
	if err := s.ready(ctx, owner); err != nil {
		return cats{}, err
	}
	list, err := s.store.categories(ctx, owner)
	return newCats(list), err
}

func checkBudget(v float64) error {
	if v < 0 || math.IsNaN(v) || v > 1e8 {
		return bad("El presupuesto no es válido.")
	}
	return nil
}

func (s *service) addCategory(ctx context.Context, owner, name string, budget float64) (int64, error) {
	name = clip(name, 40)
	if len(seal.Words(name)) == 0 {
		return 0, bad("Ponle un nombre a la categoría.")
	}
	if err := checkBudget(budget); err != nil {
		return 0, err
	}
	if err := s.ready(ctx, owner); err != nil {
		return 0, err
	}
	id, err := s.store.addCategory(ctx, owner, catData{Name: name, Budget: round2(budget)})
	if errors.Is(err, errDuplicate) {
		return 0, bad("Ya tienes una categoría con ese nombre.")
	}
	if errors.Is(err, errFull) {
		return 0, bad("Has llegado al máximo de categorías.")
	}
	if err == nil && budget > 0 {
		s.checkBudgets(ctx, owner)
	}
	return id, err
}

// updateCategory cambia el nombre y el presupuesto. Si cambia el
// presupuesto, puede volver a avisar este mes.
func (s *service) updateCategory(ctx context.Context, owner string, id int64, name string, budget float64) error {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return err
	}
	x, ok := c.byID[id]
	if !ok {
		return errNotFound
	}
	name = clip(name, 40)
	if len(seal.Words(name)) == 0 {
		return bad("Ponle un nombre a la categoría.")
	}
	if err := checkBudget(budget); err != nil {
		return err
	}
	x.Name = name
	if budget = round2(budget); budget != x.Budget {
		x.Budget, x.Warned = budget, ""
	}
	if err := s.store.updateCategory(ctx, owner, x); errors.Is(err, errDuplicate) {
		return bad("Ya tienes una categoría con ese nombre.")
	} else if err != nil {
		return err
	}
	s.checkBudgets(ctx, owner)
	return nil
}

// setBudgets guarda de una vez los presupuestos de varias categorías.
func (s *service) setBudgets(ctx context.Context, owner string, budgets map[int64]float64) error {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return err
	}
	for id, v := range budgets {
		if err := checkBudget(v); err != nil {
			return err
		}
		x, ok := c.byID[id]
		if !ok {
			return errNotFound
		}
		if v = round2(v); v == x.Budget {
			continue
		}
		x.Budget, x.Warned = v, ""
		if err := s.store.updateCategory(ctx, owner, x); err != nil {
			return err
		}
	}
	s.checkBudgets(ctx, owner)
	return nil
}

// addRule guarda una regla y, si se pide, la aplica a lo ya apuntado que
// esté en Otros, en Ingresos o sin categoría. Devuelve cuántos ha cambiado.
func (s *service) addRule(ctx context.Context, owner, text string, catID int64, apply bool) (int, error) {
	text = clip(text, 60)
	if len(seal.Words(text)) == 0 {
		return 0, bad("Escribe qué tiene que contener el concepto.")
	}
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return 0, err
	}
	if _, ok := c.byID[catID]; !ok {
		return 0, bad("Elige una categoría de la lista.")
	}
	rules, err := s.store.rules(ctx, owner)
	if err != nil {
		return 0, err
	}
	key := strings.Join(seal.Words(text), " ")
	for _, r := range rules {
		if strings.Join(seal.Words(r.Text), " ") == key {
			return 0, bad("Ya hay una regla con ese texto (para " + c.name(r.CatID) + ").")
		}
	}
	if _, err := s.store.addRule(ctx, owner, rule{CatID: catID, ruleData: ruleData{Text: text}}); errors.Is(err, errFull) {
		return 0, bad("Has llegado al máximo de reglas.")
	} else if err != nil {
		return 0, err
	}
	if !apply {
		return 0, nil
	}
	all, err := s.store.movements(ctx, owner, time.Time{}, time.Time{})
	if err != nil {
		return 0, err
	}
	loose := map[int64]bool{0: true, c.byKey[keyOther].ID: true, c.byKey[keyIncome].ID: true}
	var ids []int64
	for _, m := range all {
		if _, known := c.byID[m.CatID]; (loose[m.CatID] || !known) && m.CatID != catID && ruleMatch(text, m.Concept) {
			ids = append(ids, m.ID)
		}
	}
	if err := s.store.setCategory(ctx, owner, ids, catID); err != nil {
		return 0, err
	}
	if len(ids) > 0 {
		s.checkBudgets(ctx, owner)
	}
	return len(ids), nil
}

// ---- Movimientos ----

// movInput es un movimiento tal como llega de la web o de Talia.
type movInput struct {
	Kind    string
	Day     time.Time
	Amount  float64
	Concept string
	CatID   int64 // 0: la decide una regla
	Account string
	Note    string
	Origin  string
}

// buildMov valida un movimiento y le pone categoría si no la trae.
func (s *service) buildMov(c cats, rules []rule, in movInput) (movement, error) {
	m := movement{Day: in.Day, CatID: in.CatID, movData: movData{Kind: strings.ToLower(strings.TrimSpace(in.Kind)), Concept: clip(in.Concept, 120),
		Account: clip(in.Account, 40), Note: clip(in.Note, 300), Origin: in.Origin}}
	if m.Kind == "" {
		m.Kind = kindExpense
	}
	today := s.today()
	switch {
	case m.Kind != kindExpense && m.Kind != kindIncome:
		return m, bad("El tipo es gasto o ingreso.")
	case m.Day.IsZero():
		return m, bad("Falta la fecha.")
	case m.Day.After(today):
		return m, bad("La fecha no puede ser futura.")
	case m.Day.Year() < 2000:
		return m, bad("Esa fecha es demasiado antigua.")
	case math.IsNaN(in.Amount) || in.Amount <= 0:
		return m, bad("Falta el importe.")
	case in.Amount > 1e8:
		return m, bad("Ese importe es demasiado grande.")
	case m.Concept == "":
		return m, bad("Falta el concepto.")
	}
	m.Amount = round2(in.Amount)
	if m.Amount <= 0 {
		return m, bad("Falta el importe.")
	}
	if m.CatID != 0 {
		if _, ok := c.byID[m.CatID]; !ok {
			return m, bad("Elige una categoría de la lista.")
		}
	} else if m.CatID = classify(rules, m.Concept); m.CatID == 0 {
		m.CatID = c.fallback(m.Kind)
	}
	return m, nil
}

func (s *service) addMovement(ctx context.Context, owner string, in movInput) (movement, error) {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return movement{}, err
	}
	rules, err := s.store.rules(ctx, owner)
	if err != nil {
		return movement{}, err
	}
	m, err := s.buildMov(c, rules, in)
	if err != nil {
		return m, err
	}
	if m.ID, err = s.store.addMovement(ctx, owner, m); errors.Is(err, errFull) {
		return m, bad("Has llegado al máximo de movimientos.")
	} else if err != nil {
		return m, err
	}
	if m.Kind == kindExpense {
		s.checkBudgets(ctx, owner)
	}
	return m, nil
}

func (s *service) updateMovement(ctx context.Context, owner string, id int64, in movInput) (movement, error) {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return movement{}, err
	}
	old, err := s.store.movement(ctx, owner, id)
	if err != nil {
		return old, err
	}
	rules, err := s.store.rules(ctx, owner)
	if err != nil {
		return old, err
	}
	in.Origin = old.Origin
	m, err := s.buildMov(c, rules, in)
	if err != nil {
		return m, err
	}
	m.ID, m.SubID = id, old.SubID
	if err := s.store.updateMovement(ctx, owner, m); err != nil {
		return m, err
	}
	s.checkBudgets(ctx, owner)
	return m, nil
}

// ---- Suscripciones ----

type subInput struct {
	Name    string
	Amount  float64
	Period  string
	Next    time.Time
	Notice  int // días de antelación; -1 sin aviso
	CatID   int64
	Account string
	Note    string
}

func (s *service) buildSub(c cats, in subInput) (subscription, error) {
	x := subscription{CatID: in.CatID, subData: subData{Name: clip(in.Name, 60), Period: strings.ToLower(strings.TrimSpace(in.Period)), Next: in.Next,
		Notice: in.Notice, Account: clip(in.Account, 40), Note: clip(in.Note, 300)}}
	today := s.today()
	switch {
	case x.Name == "":
		return x, bad("Ponle un nombre (Netflix, el gimnasio, el seguro del coche…).")
	case math.IsNaN(in.Amount) || in.Amount <= 0:
		return x, bad("Falta el importe de cada cobro.")
	case in.Amount > 1e7:
		return x, bad("Ese importe es demasiado grande.")
	case !validPeriod(x.Period):
		return x, bad("Elige cada cuánto se cobra.")
	case x.Next.IsZero():
		return x, bad("Falta la fecha del próximo cobro.")
	case x.Next.Year() < 2000 || x.Next.After(today.AddDate(3, 0, 0)):
		return x, bad("La fecha del próximo cobro no es válida.")
	case x.Notice < -1 || x.Notice > 60:
		return x, bad("El aviso va de 0 a 60 días antes.")
	}
	x.Amount = round2(in.Amount)
	x.Anchor = x.Next.Day()
	if x.CatID == 0 {
		x.CatID = c.byKey[keySubs].ID
	} else if _, ok := c.byID[x.CatID]; !ok {
		return x, bad("Elige una categoría de la lista.")
	}
	return x, nil
}

func (s *service) addSub(ctx context.Context, owner string, in subInput) (subscription, error) {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return subscription{}, err
	}
	x, err := s.buildSub(c, in)
	if err != nil {
		return x, err
	}
	if x.ID, err = s.store.addSub(ctx, owner, x); errors.Is(err, errFull) {
		return x, bad("Has llegado al máximo de suscripciones.")
	}
	return x, err
}

func (s *service) updateSub(ctx context.Context, owner string, id int64, in subInput) (subscription, error) {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return subscription{}, err
	}
	old, err := s.store.subscription(ctx, owner, id)
	if err != nil {
		return old, err
	}
	x, err := s.buildSub(c, in)
	if err != nil {
		return x, err
	}
	x.ID = id
	if x.Next.Equal(old.Next) {
		x.Notified = old.Notified
		if old.Period == x.Period && old.Anchor > 0 {
			x.Anchor = old.Anchor // el día de cobro de siempre, aunque este mes sea más corto
		}
	}
	return x, s.store.updateSub(ctx, owner, x)
}

// chargeSub registra un cobro: apunta el gasto (con la fecha del cobro, o la
// de hoy si se registra antes) y pasa al siguiente.
func (s *service) chargeSub(ctx context.Context, owner string, id int64) (movement, subscription, error) {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return movement{}, subscription{}, err
	}
	x, err := s.store.subscription(ctx, owner, id)
	if err != nil {
		return movement{}, x, err
	}
	day := x.Next
	if today := s.today(); day.After(today) {
		day = today
	}
	m := movement{Day: day, CatID: x.CatID, movData: movData{Kind: kindExpense, Amount: x.Amount, Concept: x.Name, Account: x.Account,
		Origin: originSub, SubID: x.ID}}
	if _, ok := c.byID[m.CatID]; !ok {
		m.CatID = c.fallback(kindExpense)
	}
	x.Next, x.Notified = x.after(x.Next), time.Time{}
	if m.ID, err = s.store.charge(ctx, owner, x, m); errors.Is(err, errFull) {
		return m, x, bad("Has llegado al máximo de movimientos.")
	} else if err != nil {
		return m, x, err
	}
	s.checkBudgets(ctx, owner)
	return m, x, nil
}

// subsCost es lo que cuestan al mes y al año todas las suscripciones.
func subsCost(list []subscription) (month, year float64) {
	for _, x := range list {
		month += x.monthly()
	}
	return month, month * 12
}

// ---- Resúmenes ----

// catSpend es lo de una categoría en un mes.
type catSpend struct {
	ID     int64
	Name   string
	Spent  float64 // gastos
	Income float64 // ingresos
	Budget float64
	Prev   float64 // gastos del mes anterior
	Count  int
}

// over dice si se ha pasado del presupuesto.
func (c catSpend) over() bool { return c.Budget > 0 && c.Spent > c.Budget+0.005 }

type monthTotal struct {
	Month            time.Time
	Expenses, Income float64
}

type summary struct {
	Month                     time.Time
	Expenses, Income, Balance float64
	PrevExpenses, PrevIncome  float64
	Budget, BudgetSpent       float64 // la suma de los presupuestos y lo gastado en esas categorías
	ByCat                     []catSpend
	History                   []monthTotal // los últimos 6 meses, del más antiguo a este
	Movs                      []movement   // los del mes
}

const historyMonths = 6

// summarize hace las cuentas de un mes con los movimientos de los meses de
// alrededor.
func summarize(c cats, movs []movement, month time.Time) *summary {
	month = monthOf(month)
	prev := month.AddDate(0, -1, 0)
	sum := &summary{Month: month}
	by := map[int64]*catSpend{}
	get := func(id int64) *catSpend {
		if _, ok := c.byID[id]; !ok {
			id = 0
		}
		if by[id] == nil {
			by[id] = &catSpend{ID: id, Name: c.name(id), Budget: c.byID[id].Budget}
		}
		return by[id]
	}
	for _, x := range c.list {
		if x.Budget > 0 {
			get(x.ID)
		}
	}
	hist := map[time.Time]*monthTotal{}
	for i := historyMonths - 1; i >= 0; i-- {
		m := month.AddDate(0, -i, 0)
		hist[m] = &monthTotal{Month: m}
		sum.History = append(sum.History, monthTotal{Month: m})
	}
	for _, m := range movs {
		mm := monthOf(m.Day)
		if h := hist[mm]; h != nil {
			if m.Kind == kindIncome {
				h.Income += m.Amount
			} else {
				h.Expenses += m.Amount
			}
		}
		switch {
		case mm.Equal(month):
			sum.Movs = append(sum.Movs, m)
			cs := get(m.CatID)
			cs.Count++
			if m.Kind == kindIncome {
				cs.Income += m.Amount
				sum.Income += m.Amount
			} else {
				cs.Spent += m.Amount
				sum.Expenses += m.Amount
			}
		case mm.Equal(prev):
			if m.Kind == kindIncome {
				sum.PrevIncome += m.Amount
			} else {
				sum.PrevExpenses += m.Amount
				get(m.CatID).Prev += m.Amount
			}
		}
	}
	for i := range sum.History {
		sum.History[i] = *hist[sum.History[i].Month]
		sum.History[i].Expenses, sum.History[i].Income = round2(sum.History[i].Expenses), round2(sum.History[i].Income)
	}
	sum.Expenses, sum.Income, sum.PrevExpenses, sum.PrevIncome = round2(sum.Expenses), round2(sum.Income), round2(sum.PrevExpenses), round2(sum.PrevIncome)
	sum.Balance = round2(sum.Income - sum.Expenses)
	for _, cs := range by {
		cs.Spent, cs.Income, cs.Prev = round2(cs.Spent), round2(cs.Income), round2(cs.Prev)
		if cs.Budget > 0 {
			sum.Budget += cs.Budget
			sum.BudgetSpent += cs.Spent
		}
		sum.ByCat = append(sum.ByCat, *cs)
	}
	sum.Budget, sum.BudgetSpent = round2(sum.Budget), round2(sum.BudgetSpent)
	// Primero lo que más se ha gastado; luego lo que solo tiene presupuesto
	// o ingresos.
	sort.SliceStable(sum.ByCat, func(i, j int) bool {
		a, b := sum.ByCat[i], sum.ByCat[j]
		if a.Spent != b.Spent {
			return a.Spent > b.Spent
		}
		if a.Budget != b.Budget {
			return a.Budget > b.Budget
		}
		if a.Income != b.Income {
			return a.Income > b.Income
		}
		return seal.Fold(a.Name) < seal.Fold(b.Name)
	})
	return sum
}

// monthSummary son las cuentas de un mes de una persona.
func (s *service) monthSummary(ctx context.Context, owner string, month time.Time) (*summary, cats, error) {
	c, err := s.loadCats(ctx, owner)
	if err != nil {
		return nil, c, err
	}
	month = monthOf(month)
	movs, err := s.store.movements(ctx, owner, month.AddDate(0, -(historyMonths-1), 0), month.AddDate(0, 1, 0))
	if err != nil {
		return nil, c, err
	}
	return summarize(c, movs, month), c, nil
}

// ---- Avisos ----

// checkBudgets avisa de las categorías que han pasado su presupuesto este
// mes (una vez al mes por categoría). Devuelve cuántos avisos ha puesto.
func (s *service) checkBudgets(ctx context.Context, owner string) int {
	n, err := s.budgetNotices(ctx, owner)
	if err != nil {
		s.log.Warn("gastos: presupuestos", "err", err)
	}
	return n
}

func (s *service) budgetNotices(ctx context.Context, owner string) (int, error) {
	list, err := s.store.categories(ctx, owner)
	if err != nil {
		return 0, err
	}
	c := newCats(list)
	has := false
	for _, x := range list {
		has = has || x.Budget > 0
	}
	if !has {
		return 0, nil
	}
	now := s.clock()
	month := monthOf(dayOf(now))
	movs, err := s.store.movements(ctx, owner, month, month.AddDate(0, 1, 0))
	if err != nil {
		return 0, err
	}
	sum := summarize(c, movs, month)
	fired := 0
	for _, cs := range sum.ByCat {
		x, ok := c.byID[cs.ID]
		if !ok || !cs.over() || x.Warned == isoMonth(month) {
			continue
		}
		text := "⚠️ Has pasado el presupuesto de " + x.Name + " de " + months[month.Month()-1] + ": llevas " + euros(cs.Spent) + " de " +
			euros(cs.Budget) + " (" + pct(cs.Spent/cs.Budget*100) + ")."
		x.Warned = isoMonth(month)
		if err := s.store.updateCategory(ctx, owner, x); err != nil {
			return fired, err
		}
		if err := s.store.addNotice(ctx, owner, text, now); err != nil {
			return fired, err
		}
		fired++
	}
	return fired, nil
}

// subNotices avisa de los cobros que llegan en los días de aviso de cada
// suscripción (una vez por cobro).
func (s *service) subNotices(ctx context.Context, owner string) (int, error) {
	list, err := s.store.subscriptions(ctx, owner)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	today := dayOf(now)
	fired := 0
	for _, x := range list {
		if x.Notice < 0 || x.Next.Before(today) || x.Next.After(today.AddDate(0, 0, x.Notice)) || x.Notified.Equal(x.Next) {
			continue
		}
		when := whenDay(x.Next, today)
		if when == "hoy" || when == "mañana" {
			when += " (" + shortDate(x.Next, today) + ")"
		}
		text := "🔔 " + x.Name + ": cobro de " + euros(x.Amount) + " " + when + "."
		x.Notified = x.Next
		if err := s.store.updateSub(ctx, owner, x); err != nil {
			return fired, err
		}
		if err := s.store.addNotice(ctx, owner, text, now); err != nil {
			return fired, err
		}
		fired++
	}
	return fired, nil
}

// ---- Resumen de la mañana ----

// todayLines son las líneas de Gastos para el resumen de la mañana de la
// Agenda: los cobros de hoy y de los próximos días (y los que quedaron sin
// registrar) y, si se ha pasado alguno, el estado de los presupuestos. Sin
// nada que contar, ninguna. No da de alta la cuenta.
func (s *service) todayLines(ctx context.Context, owner string) ([]string, error) {
	today := s.today()
	subs, err := s.store.subscriptions(ctx, owner)
	if err != nil {
		return nil, err
	}
	var lines []string
	byDay := map[time.Time][]string{}
	var days []time.Time
	var late []string
	for _, x := range subs {
		item := x.Name + " " + euros(x.Amount)
		switch {
		case x.Next.Before(today) && !x.Next.Before(today.AddDate(0, 0, -14)):
			late = append(late, item+" (era "+whenDay(x.Next, today)+")")
		case !x.Next.Before(today) && !x.Next.After(today.AddDate(0, 0, 3)):
			if byDay[x.Next] == nil {
				days = append(days, x.Next)
			}
			byDay[x.Next] = append(byDay[x.Next], item)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	for _, d := range days {
		w := whenDay(d, today)
		lines = append(lines, "Cobro "+w+": "+strings.Join(byDay[d], " · ")+".")
	}
	if len(late) > 0 {
		lines = append(lines, "Sin registrar: "+strings.Join(late, " · ")+".")
	}

	list, err := s.store.categories(ctx, owner)
	if err != nil {
		return nil, err
	}
	month := monthOf(today)
	movs, err := s.store.movements(ctx, owner, month, month.AddDate(0, 1, 0))
	if err != nil {
		return nil, err
	}
	sum := summarize(newCats(list), movs, month)
	var over []string
	for _, cs := range sum.ByCat {
		if cs.over() {
			over = append(over, cs.Name+" "+eurosRound(cs.Spent)+" de "+eurosRound(cs.Budget))
		}
	}
	if len(over) > 0 {
		if len(over) > 3 {
			over = append(over[:3], "y "+num(float64(len(over)-3), 0)+" más")
		}
		lines = append(lines, "Presupuesto pasado en "+months[month.Month()-1]+": "+strings.Join(over, " · ")+".")
		if sum.Budget > 0 {
			lines = append(lines, "Este mes llevas "+eurosRound(sum.Expenses)+" en gastos ("+eurosRound(sum.BudgetSpent)+" de "+eurosRound(sum.Budget)+" presupuestados).")
		}
	}
	return lines, nil
}

// ---- Tarea periódica ----

// run comprueba cada 10 minutos los cobros que llegan y, una vez al día,
// los presupuestos y lo que hay que borrar.
func (s *service) run(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		s.round(rctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// noticeHour es desde qué hora se avisa de los cobros (no de madrugada).
const noticeHour = 9

func (s *service) round(ctx context.Context) {
	now := s.clock()
	s.mu.Lock()
	daily := !dayOf(now).Equal(s.dailyOn)
	if daily {
		s.dailyOn = dayOf(now)
	}
	s.mu.Unlock()
	if daily {
		if n, err := s.store.purge(ctx, now); err != nil {
			s.log.Warn("gastos: borrar lo antiguo", "err", err)
		} else if n > 0 {
			s.log.Info("gastos: borrados movimientos antiguos", "n", n)
		}
	}
	owners, err := s.store.owners(ctx)
	if err != nil {
		s.log.Warn("gastos: cuentas", "err", err)
		return
	}
	for _, owner := range owners {
		if daily {
			s.checkBudgets(ctx, owner)
		}
		if now.In(madrid).Hour() < noticeHour {
			continue
		}
		if _, err := s.subNotices(ctx, owner); err != nil {
			s.log.Warn("gastos: avisos de cobros", "err", err)
		}
	}
}
