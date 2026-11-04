package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools del asistente para la nutrición. Los valores salen de la tabla básica
// o de los habituales; el modelo solo los pone cuando no están (una comida
// de restaurante, una foto), y entonces quedan marcados como estimados.

type foodItemIn struct {
	Nombre   string  `json:"nombre" jsonschema:"qué es (pollo, arroz, yogur griego…)"`
	Cantidad float64 `json:"cantidad,omitempty" jsonschema:"cuánto: gramos, ml o unidades"`
	Unidad   string  `json:"unidad,omitempty" jsonschema:"g, ml, ud o una medida (rebanada, lata, cucharada…)"`
	Kcal     float64 `json:"kcal,omitempty" jsonschema:"solo si no está en la tabla: kcal estimadas de esa cantidad"`
	Proteina float64 `json:"proteina,omitempty" jsonschema:"solo con kcal: gramos de proteína estimados"`
	Hidratos float64 `json:"hidratos,omitempty"`
	Grasa    float64 `json:"grasa,omitempty"`
}

type foodLogIn struct {
	Texto  string       `json:"texto,omitempty" jsonschema:"lo comido tal cual, separado por comas: «200 g de arroz, 2 huevos, 1 yogur griego»"`
	Items  []foodItemIn `json:"items,omitempty" jsonschema:"en vez de texto, cosa a cosa (con kcal y proteína estimadas si no están en la tabla)"`
	Comida string       `json:"comida,omitempty" jsonschema:"desayuno, almuerzo, comida, merienda, cena u otro (por defecto, la de la hora)"`
	Fecha  string       `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD si no es hoy"`
}

type entryView struct {
	ID       int64   `json:"id"`
	Comida   string  `json:"comida"`
	Nombre   string  `json:"nombre"`
	Cantidad string  `json:"cantidad,omitempty"`
	Kcal     float64 `json:"kcal"`
	Proteina float64 `json:"proteina_g"`
	Hidratos float64 `json:"hidratos_g,omitempty"`
	Grasa    float64 `json:"grasa_g,omitempty"`
	Estimado bool    `json:"estimado,omitempty"`
}

func entryViewOf(e mealEntry) entryView {
	m := e.macros.rounded()
	return entryView{ID: e.ID, Comida: e.Meal, Nombre: e.Name, Cantidad: amountText(e.Amount, e.Unit),
		Kcal: m.Kcal, Proteina: m.Prot, Hidratos: m.Carb, Grasa: m.Fat, Estimado: e.Guess}
}

type totalsView struct {
	Kcal     float64 `json:"kcal"`
	Proteina float64 `json:"proteina_g"`
	Hidratos float64 `json:"hidratos_g,omitempty"`
	Grasa    float64 `json:"grasa_g,omitempty"`
}

func totalsOf(m macros) totalsView {
	m = m.rounded()
	return totalsView{m.Kcal, m.Prot, m.Carb, m.Fat}
}

type foodLogOut struct {
	Apuntado []entryView `json:"apuntado"`
	TotalDia totalsView  `json:"total_del_dia"`
	Quedan   *totalsView `json:"quedan_para_el_objetivo,omitempty"`
}

type nutritionIn struct {
	Fecha string `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD si no es hoy"`
}

type nutritionOut struct {
	Dia        string      `json:"dia"`
	Apuntado   []entryView `json:"apuntado"`
	Total      totalsView  `json:"total"`
	Objetivo   *totalsView `json:"objetivo,omitempty"`
	Quedan     *totalsView `json:"quedan,omitempty"`
	Media7     *totalsView `json:"media_7_dias,omitempty"`
	DiasConDat int         `json:"dias_apuntados_de_7"`
	DiasProt   int         `json:"dias_que_llego_a_la_proteina,omitempty"`
	Aviso      string      `json:"aviso,omitempty"`
}

type goalsIn struct {
	Kcal     float64 `json:"kcal,omitempty" jsonschema:"kcal al día"`
	Proteina float64 `json:"proteina,omitempty" jsonschema:"gramos de proteína al día"`
	Hidratos float64 `json:"hidratos,omitempty"`
	Grasa    float64 `json:"grasa,omitempty"`
	Quitar   bool    `json:"quitar,omitempty" jsonschema:"true para quitar todos los objetivos"`
}

type searchIn struct {
	Texto string `json:"texto"`
}

type foodView struct {
	Nombre   string  `json:"nombre"`
	Habitual bool    `json:"habitual,omitempty"`
	Por      string  `json:"valores_por"`
	Kcal     float64 `json:"kcal"`
	Proteina float64 `json:"proteina_g"`
	Hidratos float64 `json:"hidratos_g"`
	Grasa    float64 `json:"grasa_g"`
	Unidad   string  `json:"unidad,omitempty"`
}

type copyIn struct {
	Desde  string `json:"desde" jsonschema:"AAAA-MM-DD del día que se copia"`
	Comida string `json:"comida,omitempty" jsonschema:"solo esa comida (desayuno, comida…); vacío: el día entero"`
	A      string `json:"a,omitempty" jsonschema:"comida de destino si es otra (por defecto, la misma)"`
}

// remaining es lo que queda para los objetivos (nil sin objetivos).
func remaining(g goals, total macros) *totalsView {
	if !g.set() {
		return nil
	}
	v := totalsOf(macros{g.Kcal, g.Prot, g.Carb, g.Fat}.add(total.scale(-1)))
	if g.Kcal == 0 {
		v.Kcal = 0
	}
	if g.Prot == 0 {
		v.Proteina = 0
	}
	if g.Carb == 0 {
		v.Hidratos = 0
	}
	if g.Fat == 0 {
		v.Grasa = 0
	}
	return &v
}

func (s *service) atFor(fecha string) (time.Time, error) {
	if fecha == "" {
		return s.clock(), nil
	}
	return s.dayArg(fecha)
}

// itemsEntries pasa los items de la tool a lo que se apunta: con kcal, tal
// cual (estimado); sin ellas, buscándolos.
func itemsEntries(items []foodItemIn, own []food) ([]mealEntry, error) {
	var out []mealEntry
	var problems []string
	for _, it := range items {
		if it.Kcal > 0 {
			out = append(out, mealEntry{Name: it.Nombre, Amount: it.Cantidad, Unit: it.Unidad, Guess: true,
				macros: macros{it.Kcal, it.Proteina, it.Hidratos, it.Grasa}})
			continue
		}
		text := it.Nombre
		if it.Cantidad > 0 {
			unit := it.Unidad
			if unit == baseUd {
				unit = ""
			}
			text = strings.TrimSpace(fmt.Sprintf("%s %s de %s", trimNum(it.Cantidad), unit, it.Nombre))
			if unit == "" {
				text = trimNum(it.Cantidad) + " " + it.Nombre
			}
		}
		list, _, err := resolveFoods(text, own)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, list...)
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; ") + ". Si no está en la tabla, pasa kcal y proteína estimadas")
	}
	return out, nil
}

func (s *service) dayTotal(ctx context.Context, owner string, day time.Time) (macros, error) {
	list, err := s.store.meals(ctx, owner, dayOf(day), dayOf(day))
	return dayOfFood(day, list).Total, err
}

func (s *service) nutritionSummary(ctx context.Context, owner string, day time.Time) (nutritionOut, error) {
	day = dayOf(day)
	list, err := s.store.meals(ctx, owner, day.AddDate(0, 0, -6), day)
	if err != nil {
		return nutritionOut{}, err
	}
	g, err := s.store.goals(ctx, owner)
	if err != nil {
		return nutritionOut{}, err
	}
	var today []mealEntry
	for _, e := range list {
		if e.Day.Format("2006-01-02") == day.Format("2006-01-02") {
			today = append(today, e)
		}
	}
	d := dayOfFood(day, today)
	out := nutritionOut{Dia: day.Format("2006-01-02"), Apuntado: []entryView{}, Total: totalsOf(d.Total)}
	for _, grp := range d.Groups {
		for _, e := range grp.Entries {
			out.Apuntado = append(out.Apuntado, entryViewOf(e))
		}
	}
	if g.set() {
		t := totalsOf(macros{g.Kcal, g.Prot, g.Carb, g.Fat})
		out.Objetivo = &t
		out.Quedan = remaining(g, d.Total)
	}
	w := weekOfFood(list, day, g)
	out.DiasConDat, out.DiasProt = w.Days, w.ProtDays
	if w.Days > 0 {
		t := totalsOf(w.Avg)
		out.Media7 = &t
	}
	if d.Guess {
		out.Aviso = "parte de lo apuntado son valores estimados"
	}
	return out, nil
}

func addNutritionTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_comida_registrar", Description: "Apunta lo que ha comido la persona. Pasa «texto» tal cual lo dice («200 g de arroz, 2 huevos, 1 yogur griego»): el programa busca los valores en sus alimentos habituales y en una tabla básica. Si algo no está (un plato de restaurante, una foto de un plato), pásalo en «items» con kcal y proteína estimadas: queda marcado como estimado. Devuelve el total del día y lo que queda para el objetivo"},
		func(ctx context.Context, req *mcp.CallToolRequest, in foodLogIn) (*mcp.CallToolResult, foodLogOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			if in.Comida != "" && slotLabel(in.Comida) == "" {
				return nil, foodLogOut{}, errors.New("comida tiene que ser desayuno, almuerzo, comida, merienda, cena u otro")
			}
			at, err := svc.atFor(in.Fecha)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			own, err := svc.store.foods(ctx, who.User)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			var list []mealEntry
			if strings.TrimSpace(in.Texto) != "" {
				l, _, err := resolveFoods(in.Texto, own)
				if err != nil {
					return nil, foodLogOut{}, fmt.Errorf("%v. Si no está en la tabla, pásalo en items con kcal y proteína estimadas", err)
				}
				list = append(list, l...)
			}
			if len(in.Items) > 0 {
				l, err := itemsEntries(in.Items, own)
				if err != nil {
					return nil, foodLogOut{}, err
				}
				list = append(list, l...)
			}
			if len(list) == 0 {
				return nil, foodLogOut{}, errors.New("no hay nada que apuntar: pasa texto o items")
			}
			saved, _, err := svc.saveEntries(ctx, who.User, list, in.Comida, at, "asistente")
			if err != nil {
				return nil, foodLogOut{}, err
			}
			out := foodLogOut{}
			for _, e := range saved {
				out.Apuntado = append(out.Apuntado, entryViewOf(e))
			}
			total, err := svc.dayTotal(ctx, who.User, at)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			g, err := svc.store.goals(ctx, who.User)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			out.TotalDia, out.Quedan = totalsOf(total), remaining(g, total)
			return nil, out, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_nutricion", Description: "Lo comido un día (con id de cada cosa), el total, los objetivos, lo que queda y la media de los últimos 7 días apuntados, calculado por el programa"},
		func(ctx context.Context, req *mcp.CallToolRequest, in nutritionIn) (*mcp.CallToolResult, nutritionOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nutritionOut{}, err
			}
			day, err := svc.atFor(in.Fecha)
			if err != nil {
				return nil, nutritionOut{}, err
			}
			out, err := svc.nutritionSummary(ctx, who.User, day)
			return nil, out, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_comida_borrar", Description: "Borra una cosa apuntada por su id (de entreno_nutricion). Confirma antes con la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, map[string]bool, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nil, err
			}
			if err := svc.store.deleteMeal(ctx, who.User, in.ID); err != nil {
				if errors.Is(err, errNotFound) {
					return nil, nil, fmt.Errorf("no hay nada apuntado con id %d", in.ID)
				}
				return nil, nil, err
			}
			return nil, map[string]bool{"borrado": true}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_comida_copiar", Description: "Copia a hoy lo comido otro día («lo mismo que ayer en el desayuno»): el día entero o una comida"},
		func(ctx context.Context, req *mcp.CallToolRequest, in copyIn) (*mcp.CallToolResult, foodLogOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			from, err := svc.dayArg(in.Desde)
			if err != nil || in.Desde == "" {
				return nil, foodLogOut{}, errors.New("desde va como AAAA-MM-DD")
			}
			for _, k := range []string{in.Comida, in.A} {
				if k != "" && slotLabel(k) == "" {
					return nil, foodLogOut{}, errors.New("la comida tiene que ser desayuno, almuerzo, comida, merienda, cena u otro")
				}
			}
			at := svc.clock()
			saved, err := svc.copyMeals(ctx, who.User, from, in.Comida, in.A, at, "asistente")
			if err != nil {
				return nil, foodLogOut{}, err
			}
			out := foodLogOut{}
			for _, e := range saved {
				out.Apuntado = append(out.Apuntado, entryViewOf(e))
			}
			total, err := svc.dayTotal(ctx, who.User, at)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			g, err := svc.store.goals(ctx, who.User)
			if err != nil {
				return nil, foodLogOut{}, err
			}
			out.TotalDia, out.Quedan = totalsOf(total), remaining(g, total)
			return nil, out, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_alimentos_buscar", Description: "Busca un alimento en los habituales de la persona y en la tabla básica, con sus kcal y macros"},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, map[string][]foodView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, nil, err
			}
			own, err := svc.store.foods(ctx, who.User)
			if err != nil {
				return nil, nil, err
			}
			out := []foodView{}
			for _, m := range matchFoods(in.Texto, own, 30, 10) {
				out = append(out, foodViewOf(m))
			}
			return nil, map[string][]foodView{"alimentos": out}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "entreno_nutricion_objetivos", Description: "Pone los objetivos diarios de kcal y proteína (e hidratos y grasa si los quiere). Lo que no se pase se queda como estaba. No los inventes: pregúntalos"},
		func(ctx context.Context, req *mcp.CallToolRequest, in goalsIn) (*mcp.CallToolResult, totalsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, totalsView{}, err
			}
			g, err := svc.store.goals(ctx, who.User)
			if err != nil {
				return nil, totalsView{}, err
			}
			if in.Quitar {
				g = goals{}
			}
			for _, p := range []struct {
				v   float64
				dst *float64
			}{{in.Kcal, &g.Kcal}, {in.Proteina, &g.Prot}, {in.Hidratos, &g.Carb}, {in.Grasa, &g.Fat}} {
				if p.v != 0 {
					*p.dst = p.v
				}
			}
			if err := svc.store.saveGoals(ctx, who.User, g); err != nil {
				return nil, totalsView{}, err
			}
			return nil, totalsView{g.Kcal, g.Prot, g.Carb, g.Fat}, nil
		})
}

func foodViewOf(m foodMatch) foodView {
	if m.Own != nil {
		f := m.Own
		per := "100 " + f.Base
		if f.Base == baseUd {
			per = "1 " + f.unitName()
		}
		p := f.Per.rounded()
		return foodView{Nombre: f.Name, Habitual: true, Por: per, Kcal: p.Kcal, Proteina: p.Prot, Hidratos: p.Carb, Grasa: p.Fat}
	}
	g := m.Gen
	p := g.per100().rounded()
	v := foodView{Nombre: g.Name, Por: "100 " + g.base(), Kcal: p.Kcal, Proteina: p.Prot, Hidratos: p.Carb, Grasa: p.Fat}
	if g.UnitG > 0 {
		v.Unidad = fmt.Sprintf("1 %s = %s %s", g.Unit, trimNum(g.UnitG), g.base())
	}
	return v
}
