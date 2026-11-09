package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
	"homeserver/platform/seal"
)

// Tools de Talia. Las cuentas las hace el programa (como en la web): la IA
// solo las cuenta. Respuestas en JSON con claves en español e importes en
// euros. Solo datos: nada de consejos sobre qué hacer con el dinero.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

// toolError pasa a la IA los errores de lo escrito tal cual y esconde los
// demás.
func toolError(err error) error {
	if err == nil || isInput(err) {
		return err
	}
	if errors.Is(err, errNotFound) {
		return errors.New("no existe")
	}
	return errors.New("algo ha fallado en Gastos; prueba otra vez")
}

// toolMonth lee «AAAA-MM» (vacío: el mes de hoy).
func toolMonth(s string, today time.Time) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return monthOf(today), nil
	}
	m, err := parseMonth(s)
	if err != nil {
		return m, bad("el mes va como AAAA-MM")
	}
	return m, nil
}

// toolCat busca una categoría por su nombre; error con las que hay si no.
func toolCat(c cats, name string) (category, error) {
	x, ok := c.find(name)
	if !ok {
		return x, bad("no hay ninguna categoría «" + clip(name, 40) + "»; las que hay: " + c.names())
	}
	return x, nil
}

// ---- Salidas ----

type movOut struct {
	ID        int64   `json:"id"`
	Fecha     string  `json:"fecha"`
	Tipo      string  `json:"tipo"`
	Importe   float64 `json:"importe_eur"`
	Concepto  string  `json:"concepto"`
	Categoria string  `json:"categoria"`
	Cuenta    string  `json:"cuenta,omitempty"`
	Nota      string  `json:"nota,omitempty"`
	Origen    string  `json:"origen,omitempty"`
}

func movOutOf(c cats, m movement) movOut {
	return movOut{ID: m.ID, Fecha: isoDay(m.Day), Tipo: m.Kind, Importe: m.Amount, Concepto: m.Concept, Categoria: c.name(m.CatID),
		Cuenta: m.Account, Nota: m.Note, Origen: m.Origin}
}

type budgetLine struct {
	Categoria   string   `json:"categoria"`
	Gastado     float64  `json:"gastado_eur"`
	Presupuesto float64  `json:"presupuesto_eur,omitempty"`
	Quedan      *float64 `json:"quedan_eur,omitempty"` // negativo: lo que se ha pasado
	Pct         *float64 `json:"pct_usado,omitempty"`
	Pasado      bool     `json:"pasado,omitempty"`
}

func budgetLineOf(cs catSpend) budgetLine {
	b := budgetLine{Categoria: cs.Name, Gastado: cs.Spent, Presupuesto: cs.Budget, Pasado: cs.over()}
	if cs.Budget > 0 {
		left, p := round2(cs.Budget-cs.Spent), round2(cs.Spent/cs.Budget*100)
		b.Quedan, b.Pct = &left, &p
	}
	return b
}

type subOut struct {
	ID          int64   `json:"id"`
	Nombre      string  `json:"nombre"`
	Importe     float64 `json:"importe_eur"`
	Periodo     string  `json:"periodicidad"`
	Proximo     string  `json:"proximo_cobro"`
	SinRegistro bool    `json:"cobro_sin_registrar,omitempty"`
	AlMes       float64 `json:"coste_mensual_eur"`
	Categoria   string  `json:"categoria"`
	Aviso       *int    `json:"aviso_dias_antes,omitempty"`
	Cuenta      string  `json:"cuenta,omitempty"`
	Nota        string  `json:"nota,omitempty"`
}

func subOutOf(c cats, x subscription, today time.Time) subOut {
	o := subOut{ID: x.ID, Nombre: x.Name, Importe: x.Amount, Periodo: x.Period, Proximo: isoDay(x.Next), SinRegistro: x.Next.Before(today),
		AlMes: round2(x.monthly()), Categoria: c.name(x.CatID), Cuenta: x.Account, Nota: x.Note}
	if x.Notice >= 0 {
		n := x.Notice
		o.Aviso = &n
	}
	return o
}

// ---- Entradas y salidas de cada tool ----

type addIn struct {
	Tipo      string  `json:"tipo,omitempty" jsonschema:"gasto (por defecto) o ingreso"`
	Importe   float64 `json:"importe" jsonschema:"en euros, positivo"`
	Concepto  string  `json:"concepto" jsonschema:"qué es: «gasolina», «Mercadona», «cena con amigos», «nómina»"`
	Fecha     string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD; si no la dice, hoy"`
	Categoria string  `json:"categoria,omitempty" jsonschema:"nombre de una categoría suya, solo si la dice; vacía: la eligen sus reglas por el concepto"`
	Cuenta    string  `json:"cuenta,omitempty" jsonschema:"cuenta o medio de pago, si lo dice (tarjeta, efectivo…)"`
	Nota      string  `json:"nota,omitempty"`
}

type addOut struct {
	Apuntado    movOut      `json:"apuntado"`
	Presupuesto *budgetLine `json:"presupuesto_de_su_categoria_este_mes,omitempty"`
}

type listIn struct {
	Mes       string `json:"mes,omitempty" jsonschema:"AAAA-MM; vacío, el de hoy"`
	Categoria string `json:"categoria,omitempty" jsonschema:"solo los de esta categoría"`
	Texto     string `json:"texto,omitempty" jsonschema:"solo los que tengan esto en el concepto o la nota"`
	Limite    int    `json:"limite,omitempty" jsonschema:"cuántos, los más recientes (por defecto 50, máximo 300)"`
}

type listOut struct {
	Mes         string   `json:"mes"`
	Movimientos []movOut `json:"movimientos"`
	Total       int      `json:"total"`
	Gastos      float64  `json:"gastos_eur"`
	Ingresos    float64  `json:"ingresos_eur"`
}

type monthIn struct {
	Mes string `json:"mes,omitempty" jsonschema:"AAAA-MM; vacío, el de hoy"`
}

type catSumOut struct {
	Categoria   string  `json:"categoria"`
	Gastado     float64 `json:"gastado_eur"`
	Ingresos    float64 `json:"ingresos_eur,omitempty"`
	Presupuesto float64 `json:"presupuesto_eur,omitempty"`
	MesAnterior float64 `json:"gastado_mes_anterior_eur"`
	Movs        int     `json:"movimientos"`
}

type monthOut struct {
	Mes      string  `json:"mes"`
	Gastos   float64 `json:"gastos_eur"`
	Ingresos float64 `json:"ingresos_eur"`
}

type summaryOut struct {
	Mes              string      `json:"mes"`
	Gastos           float64     `json:"gastos_eur"`
	Ingresos         float64     `json:"ingresos_eur"`
	Balance          float64     `json:"balance_eur"`
	MesAnterior      monthOut    `json:"mes_anterior"`
	VariacionGastos  *float64    `json:"variacion_gastos_pct,omitempty"`
	PorCategoria     []catSumOut `json:"por_categoria"`
	UltimosMeses     []monthOut  `json:"ultimos_meses"`
	Suscripciones    float64     `json:"suscripciones_al_mes_eur"`
	CategoriasPasada []string    `json:"presupuesto_pasado_en,omitempty"`
}

type budgetIn struct {
	Mes         string   `json:"mes,omitempty" jsonschema:"AAAA-MM; vacío, el de hoy"`
	Categoria   string   `json:"categoria,omitempty" jsonschema:"para cambiar su presupuesto (con «presupuesto»)"`
	Presupuesto *float64 `json:"presupuesto,omitempty" jsonschema:"nuevo presupuesto al mes de esa categoría, en euros; 0 lo quita. Solo si la persona lo pide"`
}

type budgetOut struct {
	Mes          string       `json:"mes"`
	Presupuesto  float64      `json:"presupuesto_total_eur"`
	Gastado      float64      `json:"gastado_en_categorias_con_presupuesto_eur"`
	Quedan       float64      `json:"quedan_eur"`
	GastosTotal  float64      `json:"gastos_del_mes_eur"`
	DiasQuedan   *int         `json:"dias_que_quedan_del_mes,omitempty"`
	Categorias   []budgetLine `json:"categorias"`
	Cambiado     string       `json:"cambiado,omitempty"`
	SinPresupues bool         `json:"sin_presupuestos,omitempty"`
}

type newSubIn struct {
	Nombre    string  `json:"nombre" jsonschema:"Netflix, gimnasio, seguro del coche…"`
	Importe   float64 `json:"importe" jsonschema:"de cada cobro, en euros"`
	Periodo   string  `json:"periodicidad" jsonschema:"semanal, mensual, bimestral, trimestral, semestral o anual"`
	Proximo   string  `json:"proximo_cobro" jsonschema:"AAAA-MM-DD del próximo cobro; si no lo sabe, pregúntalo"`
	Aviso     *int    `json:"aviso_dias_antes,omitempty" jsonschema:"días antes del cobro para avisar por Telegram (0 el mismo día; por defecto 3; -1 sin aviso)"`
	Categoria string  `json:"categoria,omitempty" jsonschema:"por defecto, Suscripciones"`
	Cuenta    string  `json:"cuenta,omitempty"`
	Nota      string  `json:"nota,omitempty"`
}

type subsIn struct {
	RegistrarCobro int64     `json:"registrar_cobro,omitempty" jsonschema:"id de una suscripción: apunta su cobro como gasto y pasa al siguiente. Solo si la persona dice que ya se lo han cobrado"`
	Nueva          *newSubIn `json:"nueva,omitempty" jsonschema:"para añadir una suscripción o cargo que se repite"`
}

type subsOut struct {
	Suscripciones []subOut `json:"suscripciones"`
	AlMes         float64  `json:"coste_mensual_estimado_eur"`
	AlAno         float64  `json:"coste_anual_estimado_eur"`
	Registrado    *movOut  `json:"cobro_registrado,omitempty"`
	Creada        *subOut  `json:"creada,omitempty"`
}

type delIn struct {
	ID  int64  `json:"id" jsonschema:"id del movimiento (de gastos_lista) o de la suscripción (de gastos_suscripciones)"`
	Que string `json:"que,omitempty" jsonschema:"movimiento (por defecto) o suscripcion"`
}

type delOut struct {
	Borrado     bool   `json:"borrado"`
	Que         string `json:"que"`
	Descripcion string `json:"descripcion"`
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "gastos_apuntar", Description: "Apunta un gasto o un ingreso de la persona: importe en euros, concepto y, si los dice, fecha, categoría, cuenta o nota. La categoría, si no la dice, la eligen sus reglas por el concepto. Devuelve lo apuntado y cómo va el presupuesto de esa categoría este mes. Solo anota: no mueve dinero"},
		func(ctx context.Context, req *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, addOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, addOut{}, err
			}
			today := svc.today()
			day := today
			if strings.TrimSpace(in.Fecha) != "" {
				if day, err = parseDay(in.Fecha); err != nil {
					return nil, addOut{}, errors.New("la fecha va como AAAA-MM-DD")
				}
			}
			c, err := svc.loadCats(ctx, who.User)
			if err != nil {
				return nil, addOut{}, toolError(err)
			}
			mi := movInput{Kind: in.Tipo, Day: day, Amount: in.Importe, Concept: in.Concepto, Account: in.Cuenta, Note: in.Nota, Origin: originAssistant}
			if strings.TrimSpace(in.Categoria) != "" {
				x, err := toolCat(c, in.Categoria)
				if err != nil {
					return nil, addOut{}, err
				}
				mi.CatID = x.ID
			}
			if in.Importe < 0 {
				mi.Amount = -in.Importe
			}
			m, err := svc.addMovement(ctx, who.User, mi)
			if err != nil {
				return nil, addOut{}, toolError(err)
			}
			out := addOut{Apuntado: movOutOf(c, m)}
			if m.Kind == kindExpense {
				sum, _, err := svc.monthSummary(ctx, who.User, m.Day)
				if err != nil {
					return nil, out, toolError(err)
				}
				for _, cs := range sum.ByCat {
					if cs.ID == m.CatID && cs.Budget > 0 && monthOf(m.Day).Equal(monthOf(today)) {
						b := budgetLineOf(cs)
						out.Presupuesto = &b
					}
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "gastos_lista", Description: "Los gastos e ingresos apuntados de un mes (por defecto, este), del más reciente al más antiguo, con su id, fecha, importe, concepto y categoría; se pueden filtrar por categoría o por un texto del concepto. También da lo que suman"},
		func(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, listOut{}, err
			}
			month, err := toolMonth(in.Mes, svc.today())
			if err != nil {
				return nil, listOut{}, err
			}
			c, err := svc.loadCats(ctx, who.User)
			if err != nil {
				return nil, listOut{}, toolError(err)
			}
			only := int64(-1)
			if strings.TrimSpace(in.Categoria) != "" {
				x, err := toolCat(c, in.Categoria)
				if err != nil {
					return nil, listOut{}, err
				}
				only = x.ID
			}
			movs, err := svc.store.movements(ctx, who.User, month, month.AddDate(0, 1, 0))
			if err != nil {
				return nil, listOut{}, toolError(err)
			}
			limit := in.Limite
			if limit <= 0 {
				limit = 50
			}
			limit = min(limit, 300)
			text := strings.Join(seal.Words(in.Texto), " ")
			out := listOut{Mes: isoMonth(month), Movimientos: []movOut{}}
			for _, m := range movs {
				if only >= 0 && m.CatID != only {
					continue
				}
				if text != "" && !strings.Contains(strings.Join(seal.Words(m.Concept+" "+m.Note), " "), text) {
					continue
				}
				out.Total++
				if m.Kind == kindIncome {
					out.Ingresos += m.Amount
				} else {
					out.Gastos += m.Amount
				}
				if len(out.Movimientos) < limit {
					out.Movimientos = append(out.Movimientos, movOutOf(c, m))
				}
			}
			out.Gastos, out.Ingresos = round2(out.Gastos), round2(out.Ingresos)
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "gastos_resumen", Description: "Las cuentas de un mes (por defecto, este): gastos, ingresos y balance; lo gastado por categoría con su presupuesto y lo del mes anterior; la variación frente al mes anterior; los totales de los últimos 6 meses y lo que suman las suscripciones al mes. Todo calculado por el programa"},
		func(ctx context.Context, req *mcp.CallToolRequest, in monthIn) (*mcp.CallToolResult, summaryOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, summaryOut{}, err
			}
			month, err := toolMonth(in.Mes, svc.today())
			if err != nil {
				return nil, summaryOut{}, err
			}
			sum, _, err := svc.monthSummary(ctx, who.User, month)
			if err != nil {
				return nil, summaryOut{}, toolError(err)
			}
			subs, err := svc.store.subscriptions(ctx, who.User)
			if err != nil {
				return nil, summaryOut{}, toolError(err)
			}
			mo, _ := subsCost(subs)
			out := summaryOut{Mes: isoMonth(month), Gastos: sum.Expenses, Ingresos: sum.Income, Balance: sum.Balance,
				MesAnterior:  monthOut{Mes: isoMonth(month.AddDate(0, -1, 0)), Gastos: sum.PrevExpenses, Ingresos: sum.PrevIncome},
				PorCategoria: []catSumOut{}, UltimosMeses: []monthOut{}, Suscripciones: round2(mo)}
			if sum.PrevExpenses > 0 {
				v := round2((sum.Expenses/sum.PrevExpenses - 1) * 100)
				out.VariacionGastos = &v
			}
			for _, cs := range sum.ByCat {
				if cs.Spent <= 0 && cs.Income <= 0 && cs.Prev <= 0 {
					continue
				}
				out.PorCategoria = append(out.PorCategoria, catSumOut{Categoria: cs.Name, Gastado: cs.Spent, Ingresos: cs.Income, Presupuesto: cs.Budget,
					MesAnterior: cs.Prev, Movs: cs.Count})
				if cs.over() {
					out.CategoriasPasada = append(out.CategoriasPasada, cs.Name)
				}
			}
			for _, h := range sum.History {
				out.UltimosMeses = append(out.UltimosMeses, monthOut{Mes: isoMonth(h.Month), Gastos: h.Expenses, Ingresos: h.Income})
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "gastos_presupuesto", Description: "Cómo va el presupuesto mensual de un mes (por defecto, este): por categoría, lo presupuestado, lo gastado, lo que queda y si se ha pasado, y los días que quedan del mes. Con «categoria» y «presupuesto» cambia el presupuesto al mes de esa categoría (0 lo quita), solo si la persona lo pide"},
		func(ctx context.Context, req *mcp.CallToolRequest, in budgetIn) (*mcp.CallToolResult, budgetOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, budgetOut{}, err
			}
			today := svc.today()
			month, err := toolMonth(in.Mes, today)
			if err != nil {
				return nil, budgetOut{}, err
			}
			out := budgetOut{Mes: isoMonth(month), Categorias: []budgetLine{}}
			if in.Presupuesto != nil {
				c, err := svc.loadCats(ctx, who.User)
				if err != nil {
					return nil, out, toolError(err)
				}
				if strings.TrimSpace(in.Categoria) == "" {
					return nil, out, errors.New("di de qué categoría es el presupuesto")
				}
				x, err := toolCat(c, in.Categoria)
				if err != nil {
					return nil, out, err
				}
				v := *in.Presupuesto
				if v < 0 {
					v = 0
				}
				if err := svc.setBudgets(ctx, who.User, map[int64]float64{x.ID: v}); err != nil {
					return nil, out, toolError(err)
				}
				out.Cambiado = x.Name + ": " + euros(round2(v)) + " al mes"
				if v == 0 {
					out.Cambiado = x.Name + ": sin presupuesto"
				}
			}
			sum, _, err := svc.monthSummary(ctx, who.User, month)
			if err != nil {
				return nil, out, toolError(err)
			}
			out.Presupuesto, out.Gastado, out.Quedan, out.GastosTotal = sum.Budget, sum.BudgetSpent, round2(sum.Budget-sum.BudgetSpent), sum.Expenses
			out.SinPresupues = sum.Budget == 0
			if monthOf(today).Equal(month) {
				n := daysIn(month) - today.Day()
				out.DiasQuedan = &n
			}
			for _, cs := range sum.ByCat {
				if cs.Budget > 0 || cs.Spent > 0 {
					out.Categorias = append(out.Categorias, budgetLineOf(cs))
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "gastos_suscripciones", Description: "Las suscripciones y cargos que se repiten (Netflix, gimnasio, seguros…): importe, periodicidad, próximo cobro, si hay alguno sin registrar, y lo que cuestan en total al mes y al año. Con «nueva» añade una; con «registrar_cobro» (su id) apunta el cobro como gasto y pasa al siguiente"},
		func(ctx context.Context, req *mcp.CallToolRequest, in subsIn) (*mcp.CallToolResult, subsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, subsOut{}, err
			}
			c, err := svc.loadCats(ctx, who.User)
			if err != nil {
				return nil, subsOut{}, toolError(err)
			}
			today := svc.today()
			out := subsOut{Suscripciones: []subOut{}}
			if n := in.Nueva; n != nil {
				si := subInput{Name: n.Nombre, Amount: n.Importe, Period: n.Periodo, Notice: 3, Account: n.Cuenta, Note: n.Nota}
				if n.Aviso != nil {
					si.Notice = max(*n.Aviso, -1)
				}
				if strings.TrimSpace(n.Proximo) == "" {
					return nil, out, errors.New("falta la fecha del próximo cobro: pregúntasela")
				}
				if si.Next, err = parseDay(n.Proximo); err != nil {
					return nil, out, errors.New("proximo_cobro va como AAAA-MM-DD")
				}
				if strings.TrimSpace(n.Categoria) != "" {
					x, err := toolCat(c, n.Categoria)
					if err != nil {
						return nil, out, err
					}
					si.CatID = x.ID
				}
				x, err := svc.addSub(ctx, who.User, si)
				if err != nil {
					return nil, out, toolError(err)
				}
				o := subOutOf(c, x, today)
				out.Creada = &o
			}
			if in.RegistrarCobro != 0 {
				m, _, err := svc.chargeSub(ctx, who.User, in.RegistrarCobro)
				if errors.Is(err, errNotFound) {
					return nil, out, errors.New("no hay ninguna suscripción con ese id: míralo en la lista")
				} else if err != nil {
					return nil, out, toolError(err)
				}
				o := movOutOf(c, m)
				out.Registrado = &o
			}
			list, err := svc.store.subscriptions(ctx, who.User)
			if err != nil {
				return nil, out, toolError(err)
			}
			for _, x := range list {
				out.Suscripciones = append(out.Suscripciones, subOutOf(c, x, today))
			}
			mo, yr := subsCost(list)
			out.AlMes, out.AlAno = round2(mo), round2(yr)
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "gastos_borrar", Description: "Borra un movimiento (por su id, de gastos_lista) o una suscripción (que=suscripcion, id de gastos_suscripciones). No se puede deshacer: antes de llamarla, di qué es (concepto, importe y fecha) y espera a que la persona lo confirme de forma explícita"},
		func(ctx context.Context, req *mcp.CallToolRequest, in delIn) (*mcp.CallToolResult, delOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, delOut{}, err
			}
			switch strings.ToLower(strings.TrimSpace(in.Que)) {
			case "", "movimiento", "gasto", "ingreso":
				m, err := svc.store.movement(ctx, who.User, in.ID)
				if err == nil {
					err = svc.store.deleteMovement(ctx, who.User, in.ID)
				}
				if errors.Is(err, errNotFound) {
					return nil, delOut{}, errors.New("no existe ese movimiento: búscalo con gastos_lista")
				} else if err != nil {
					return nil, delOut{}, toolError(err)
				}
				return nil, delOut{Borrado: true, Que: "movimiento", Descripcion: m.Kind + " de " + euros(m.Amount) + " · " + m.Concept + " · " + isoDay(m.Day)}, nil
			case "suscripcion", "suscripción":
				x, err := svc.store.subscription(ctx, who.User, in.ID)
				if err == nil {
					err = svc.store.deleteSub(ctx, who.User, in.ID)
				}
				if errors.Is(err, errNotFound) {
					return nil, delOut{}, errors.New("no existe esa suscripción: búscala con gastos_suscripciones")
				} else if err != nil {
					return nil, delOut{}, toolError(err)
				}
				return nil, delOut{Borrado: true, Que: "suscripcion", Descripcion: x.Name + " · " + euros(x.Amount) + " " + strings.ToLower(periodLabel(x.Period))}, nil
			}
			return nil, delOut{}, errors.New("«que» es movimiento o suscripcion")
		})
}
