package main

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools de Talia. Los valores los calcula el programa (como en la web): la
// IA solo los cuenta. Respuestas en JSON con claves en español y los
// importes ya en euros. Nada de recomendaciones: solo datos.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }

// rp redondea un precio a 6 cifras significativas.
func rp(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	d := 5 - int(math.Floor(math.Log10(math.Abs(v))))
	if d < 0 {
		d = 0
	}
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

func rq(v float64) float64 { return math.Round(v*1e8) / 1e8 }

// findAsset busca un activo de la persona por id, por referencia de la
// fuente («kraken:XBTEUR»), por símbolo o por nombre.
func findAsset(v *view, s string) (asset, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return asset{}, errors.New("di qué activo (su símbolo, nombre o id)")
	}
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		if a, ok := v.asset(id); ok {
			return a, nil
		}
	}
	if src, id, ok := parseRef(s); ok {
		want := assetData{Source: src, SourceID: id}.key()
		for _, a := range v.assets {
			if a.key() == want {
				return a, nil
			}
		}
		return asset{}, errNotFound
	}
	var exact, partial []asset
	q := strings.ToLower(s)
	for _, a := range v.assets {
		switch {
		case strings.ToLower(a.Symbol) == q || strings.EqualFold(a.ISIN, s):
			exact = append(exact, a)
		case strings.Contains(strings.ToLower(a.Name), q):
			partial = append(partial, a)
		}
	}
	list := exact
	if len(list) == 0 {
		list = partial
	}
	switch len(list) {
	case 0:
		return asset{}, errNotFound
	case 1:
		return list[0], nil
	}
	var names []string
	for _, a := range list {
		names = append(names, a.Symbol+" ("+strconv.FormatInt(a.ID, 10)+", "+sourceName(a.Source)+")")
	}
	return asset{}, errors.New("hay varios que encajan: " + strings.Join(names, ", ") + ". Usa el id")
}

var errNotInPortfolio = errors.New("ese activo no está en la cartera: búscalo con cartera_precio y usa su «ref» para añadirlo")

// ---- Salidas ----

type posOut struct {
	ID          int64              `json:"id"`
	Simbolo     string             `json:"simbolo"`
	Nombre      string             `json:"nombre"`
	Tipo        string             `json:"tipo"`
	Cantidad    float64            `json:"cantidad"`
	Precio      float64            `json:"precio,omitempty"`
	Moneda      string             `json:"moneda"`
	ValorEUR    float64            `json:"valor_eur,omitempty"`
	CosteEUR    float64            `json:"coste_eur"`
	PrecioMedio float64            `json:"precio_medio_eur,omitempty"`
	Ganancia    float64            `json:"ganancia_no_realizada_eur,omitempty"`
	GananciaPct float64            `json:"ganancia_no_realizada_pct,omitempty"`
	Realizada   float64            `json:"ganancia_realizada_eur,omitempty"`
	Rendimiento float64            `json:"dividendos_y_recompensas_eur,omitempty"`
	HoyPct      float64            `json:"variacion_hoy_pct,omitempty"`
	Plataformas map[string]float64 `json:"plataformas,omitempty"`
	SinPrecio   bool               `json:"sin_precio,omitempty"`
}

func posOf(p *position) posOut {
	o := posOut{ID: p.Asset.ID, Simbolo: p.Asset.Symbol, Nombre: p.Asset.Name, Tipo: kindLabel(p.Asset.Kind), Cantidad: rq(p.Qty),
		Precio: rp(p.Price), Moneda: p.Quote.Currency, CosteEUR: r2(p.Cost), PrecioMedio: rp(p.Avg()), Realizada: r2(p.Realized),
		Rendimiento: r2(p.Income), SinPrecio: p.Qty > eps && !p.Priced}
	if o.Moneda == "" {
		o.Moneda = p.Asset.Currency
	}
	if p.Priced {
		o.ValorEUR, o.Ganancia, o.GananciaPct = r2(p.Value), r2(p.Unrealized), r2(p.UnrealPct)
	}
	if p.Quote.Prev > 0 {
		o.HoyPct = r2(p.Quote.change())
	}
	if len(p.Platforms) > 0 {
		o.Plataformas = map[string]float64{}
		for k, v := range p.Platforms {
			if k == "" {
				k = "sin plataforma"
			}
			o.Plataformas[k] = rq(v)
		}
	}
	return o
}

type shareOut struct {
	Nombre string  `json:"nombre"`
	Pct    float64 `json:"pct"`
	EUR    float64 `json:"eur"`
}

func sharesOut(list []share) []shareOut {
	out := []shareOut{}
	for _, s := range list {
		out = append(out, shareOut{Nombre: s.Key, Pct: r2(s.Pct), EUR: r2(s.Value)})
	}
	return out
}

type summaryOut struct {
	ValorEUR        float64               `json:"valor_eur"`
	InvertidoEUR    float64               `json:"invertido_eur"`
	NoRealizadaEUR  float64               `json:"ganancia_no_realizada_eur"`
	NoRealizadaPct  float64               `json:"ganancia_no_realizada_pct"`
	RealizadaEUR    float64               `json:"ganancia_realizada_fifo_eur"`
	Rendimientos    float64               `json:"dividendos_y_recompensas_eur"`
	Comisiones      float64               `json:"comisiones_sueltas_eur"`
	TotalEUR        float64               `json:"ganancia_total_eur"`
	HoyEUR          float64               `json:"variacion_hoy_eur"`
	HoyPct          float64               `json:"variacion_hoy_pct"`
	SemanaEUR       *float64              `json:"variacion_7_dias_eur,omitempty"`
	SemanaPct       *float64              `json:"variacion_7_dias_pct,omitempty"`
	Posiciones      []posOut              `json:"posiciones"`
	Reparto         map[string][]shareOut `json:"reparto"`
	Avisos          []string              `json:"avisos,omitempty"`
	FuentesConFallo []string              `json:"fuentes_con_fallo,omitempty"`
}

type opOut struct {
	ID         int64   `json:"id"`
	Fecha      string  `json:"fecha"`
	Hora       string  `json:"hora,omitempty"`
	Tipo       string  `json:"tipo"`
	Activo     string  `json:"activo"`
	ActivoID   int64   `json:"activo_id,omitempty"`
	Cantidad   float64 `json:"cantidad,omitempty"`
	Precio     float64 `json:"precio,omitempty"`
	Importe    float64 `json:"importe,omitempty"`
	Comision   float64 `json:"comision,omitempty"`
	Moneda     string  `json:"moneda"`
	Plataforma string  `json:"plataforma,omitempty"`
	Destino    string  `json:"plataforma_destino,omitempty"`
	Nota       string  `json:"nota,omitempty"`
}

func opOutOf(v *view, o operation) opOut {
	out := opOut{ID: o.ID, Fecha: isoDay(o.Day), Hora: o.Time, Tipo: o.Kind, ActivoID: o.AssetID, Cantidad: rq(o.Qty), Precio: rp(o.Price),
		Importe: r2(o.Amount), Comision: r2(o.Fee), Moneda: o.Currency, Plataforma: o.Platform, Destino: o.To, Nota: o.Note}
	if o.Kind == opBuy || o.Kind == opSell || o.Kind == opReward {
		out.Importe = r2(o.Qty * o.Price)
	}
	if a, ok := v.asset(o.AssetID); ok {
		out.Activo = a.Symbol
	} else {
		out.Activo = "general"
	}
	return out
}

type alertOut struct {
	ID          int64  `json:"id"`
	Activo      string `json:"activo"`
	Descripcion string `json:"descripcion"`
	Activa      bool   `json:"activa"`
	Saltada     bool   `json:"saltada_y_sin_rearmar,omitempty"`
	UltimoAviso string `json:"ultimo_aviso,omitempty"`
}

func alertsOut(v *view, list []alert) []alertOut {
	out := []alertOut{}
	for _, a := range list {
		sym, cur := "cartera entera", "EUR"
		if x, ok := v.asset(a.AssetID); ok {
			sym, cur = x.Symbol, x.Currency
			if q, ok := v.quotes[x.ID]; ok && q.Currency != "" {
				cur = q.Currency
			}
		}
		out = append(out, alertOut{ID: a.ID, Activo: sym, Descripcion: alertDesc(a, sym, cur), Activa: a.Active, Saltada: a.Active && a.Crossed, UltimoAviso: a.Last})
	}
	return out
}

// ---- Entradas ----

type assetIn struct {
	Activo string `json:"activo" jsonschema:"el activo: su símbolo (BTC), nombre, ISIN o id en la cartera"`
	Dias   int    `json:"dias,omitempty" jsonschema:"días de histórico de cierres (por defecto 30, máximo 365)"`
}

type assetOut struct {
	Posicion    posOut     `json:"posicion"`
	Fuente      string     `json:"fuente"`
	ISIN        string     `json:"isin,omitempty"`
	Seguimiento bool       `json:"en_seguimiento"`
	Cotizacion  string     `json:"cotizacion_de,omitempty"` // cuándo
	Historico   []histOut  `json:"historico"`
	Operaciones []opOut    `json:"operaciones"`
	Alertas     []alertOut `json:"alertas"`
}

type histOut struct {
	Fecha  string  `json:"fecha"`
	Cierre float64 `json:"cierre"`
}

type opsIn struct {
	Activo string `json:"activo,omitempty" jsonschema:"solo las de este activo (símbolo, nombre o id)"`
	Desde  string `json:"desde,omitempty" jsonschema:"AAAA-MM-DD"`
	Limite int    `json:"limite,omitempty" jsonschema:"cuántas, las más recientes (por defecto 30, máximo 200)"`
}

type opsOut struct {
	Operaciones []opOut `json:"operaciones"`
	Total       int     `json:"total"`
}

type addOpIn struct {
	Activo     string  `json:"activo,omitempty" jsonschema:"símbolo, nombre o id de un activo de la cartera, o la «ref» de cartera_precio (kraken:XBTEUR) si aún no está. Vacío solo en una comisión general"`
	Tipo       string  `json:"tipo" jsonschema:"compra, venta, dividendo, recompensa (staking), traspaso o comision"`
	Fecha      string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD. Si la persona no la dice, pregúntala"`
	Hora       string  `json:"hora,omitempty" jsonschema:"HH:MM, si la dice"`
	Cantidad   float64 `json:"cantidad,omitempty" jsonschema:"unidades del activo"`
	Precio     float64 `json:"precio,omitempty" jsonschema:"precio por unidad en la moneda de la operación"`
	Importe    float64 `json:"importe,omitempty" jsonschema:"importe total si no da el precio por unidad; en dividendos, el bruto; en comisiones, la comisión"`
	Comision   float64 `json:"comision,omitempty" jsonschema:"comisión (en dividendos, la retención)"`
	Moneda     string  `json:"moneda,omitempty" jsonschema:"EUR, USD…; vacía: la del activo"`
	Plataforma string  `json:"plataforma,omitempty" jsonschema:"DEGIRO, Binance, Kraken u otra"`
	Destino    string  `json:"plataforma_destino,omitempty" jsonschema:"en traspasos, la plataforma de destino"`
	Nota       string  `json:"nota,omitempty"`
}

type addOpOut struct {
	Apuntada opOut  `json:"apuntada"`
	Posicion posOut `json:"posicion,omitzero"`
}

type lookupIn struct {
	Consulta string `json:"consulta" jsonschema:"símbolo (BTC, AAPL), nombre o ISIN"`
}

type lookupOut struct {
	Resultados []pricedOut `json:"resultados"`
	Nota       string      `json:"nota,omitempty"`
}

type pricedOut struct {
	Ref       string  `json:"ref"`
	Simbolo   string  `json:"simbolo"`
	Nombre    string  `json:"nombre"`
	Tipo      string  `json:"tipo"`
	Mercado   string  `json:"mercado,omitempty"`
	Fuente    string  `json:"fuente"`
	Precio    float64 `json:"precio"`
	Moneda    string  `json:"moneda"`
	HoyPct    float64 `json:"variacion_hoy_pct,omitempty"`
	Hora      string  `json:"hora,omitempty"`
	EnCartera int64   `json:"id_en_cartera,omitempty"`
}

type watchIn struct {
	Anadir string `json:"anadir,omitempty" jsonschema:"activo a seguir: símbolo o id de la cartera, o la «ref» de cartera_precio"`
	Quitar string `json:"quitar,omitempty" jsonschema:"activo que dejar de seguir (símbolo o id)"`
}

type watchOut struct {
	Seguimiento []watchItem `json:"seguimiento"`
}

type watchItem struct {
	ID      int64   `json:"id"`
	Simbolo string  `json:"simbolo"`
	Nombre  string  `json:"nombre"`
	Precio  float64 `json:"precio,omitempty"`
	Moneda  string  `json:"moneda"`
	HoyPct  float64 `json:"variacion_hoy_pct,omitempty"`
	Mes     float64 `json:"variacion_30_dias_pct,omitempty"`
	LoTiene bool    `json:"lo_tiene,omitempty"`
}

type alertsListOut struct {
	Alertas []alertOut `json:"alertas"`
}

type alertIn struct {
	Activo  string  `json:"activo,omitempty" jsonschema:"símbolo o id del activo; vacío: la cartera entera"`
	Tipo    string  `json:"tipo" jsonschema:"encima (precio o valor por encima de), debajo, dia (variación del día de ± umbral % o más) o caida (caída de umbral % desde el máximo de N días; solo activos)"`
	Umbral  float64 `json:"umbral" jsonschema:"precio en la moneda del activo, valor en euros (cartera) o porcentaje"`
	Dias    int     `json:"dias,omitempty" jsonschema:"en caida: el máximo de cuántos días (por defecto 30)"`
	Repetir bool    `json:"repetir,omitempty" jsonschema:"true: avisa cada vez que vuelva a cruzar; false (por defecto): una vez y se apaga"`
}

type alertDelIn struct {
	ID int64 `json:"id" jsonschema:"id de la alerta (de cartera_alertas). Antes, confirma con la persona"`
}

type okOut struct {
	Borrada alertOut `json:"borrada"`
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "cartera_resumen", Description: "La cartera de inversión de la persona: valor en euros, lo invertido, ganancia no realizada, realizada (FIFO), dividendos y recompensas, variación de hoy y de 7 días, cada posición (cantidad, precio, valor, ganancia, plataformas) y el reparto por tipo, plataforma y moneda. Todo calculado por el programa"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, summaryOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, summaryOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, summaryOut{}, err
			}
			pf := v.pf
			out := summaryOut{ValorEUR: r2(pf.Value), InvertidoEUR: r2(pf.Cost), NoRealizadaEUR: r2(pf.Unrealized), NoRealizadaPct: r2(pf.UnrealPct),
				RealizadaEUR: r2(pf.Realized), Rendimientos: r2(pf.Income), Comisiones: r2(pf.Fees), TotalEUR: r2(pf.Total),
				HoyEUR: r2(pf.DayChange), HoyPct: r2(pf.DayPct), Posiciones: []posOut{}, Avisos: pf.Warnings,
				Reparto: map[string][]shareOut{"tipo": sharesOut(pf.ByKind), "plataforma": sharesOut(pf.ByPlatform), "moneda": sharesOut(pf.ByCurrency)}}
			if d, p, ok := change(v.series(dayOf(v.now).Add(-8*dayUnit)), 7); ok {
				d, p = r2(d), r2(p)
				out.SemanaEUR, out.SemanaPct = &d, &p
			}
			for _, p := range pf.Positions {
				if p.Qty > eps {
					out.Posiciones = append(out.Posiciones, posOf(p))
				}
			}
			for name, st := range svc.market.statuses() {
				if st.Err != "" {
					out.FuentesConFallo = append(out.FuentesConFallo, sourceName(name)+": "+st.Err)
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_activo", Description: "Detalle de un activo de la cartera: posición, precio medio, ganancias, plataformas, cierres diarios recientes, sus operaciones y sus alertas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in assetIn) (*mcp.CallToolResult, assetOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, assetOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, assetOut{}, err
			}
			a, err := findAsset(v, in.Activo)
			if errors.Is(err, errNotFound) {
				return nil, assetOut{}, errNotInPortfolio
			} else if err != nil {
				return nil, assetOut{}, err
			}
			p := v.pf.pos(a.ID)
			out := assetOut{Posicion: posOf(p), Fuente: sourceName(a.Source), ISIN: a.ISIN, Seguimiento: a.Watch,
				Historico: []histOut{}, Operaciones: []opOut{}, Alertas: []alertOut{}}
			if !p.Quote.At.IsZero() {
				out.Cotizacion = p.Quote.At.In(madrid).Format("2006-01-02 15:04")
			}
			days := in.Dias
			if days <= 0 {
				days = 30
			}
			days = min(days, 365)
			h := v.hist[a.ID].since(dayOf(v.now).Add(-time.Duration(days) * dayUnit))
			step := 1
			if h.len() > 60 {
				step = (h.len() + 59) / 60 // como mucho unos 60 puntos
			}
			for i := 0; i < h.len(); i += step {
				out.Historico = append(out.Historico, histOut{Fecha: isoDay(fromEpoch(h.Days[i])), Cierre: rp(h.Closes[i])})
			}
			for i := len(v.ops) - 1; i >= 0; i-- {
				if v.ops[i].AssetID == a.ID {
					out.Operaciones = append(out.Operaciones, opOutOf(v, v.ops[i]))
				}
			}
			alerts, err := svc.store.alerts(ctx, who.User)
			if err != nil {
				return nil, assetOut{}, err
			}
			var mine []alert
			for _, x := range alerts {
				if x.AssetID == a.ID {
					mine = append(mine, x)
				}
			}
			out.Alertas = alertsOut(v, mine)
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_operaciones", Description: "Las operaciones apuntadas (compras, ventas, dividendos, recompensas, traspasos, comisiones), de la más reciente a la más antigua"},
		func(ctx context.Context, req *mcp.CallToolRequest, in opsIn) (*mcp.CallToolResult, opsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, opsOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, opsOut{}, err
			}
			var only int64
			if strings.TrimSpace(in.Activo) != "" {
				a, err := findAsset(v, in.Activo)
				if errors.Is(err, errNotFound) {
					return nil, opsOut{}, errNotInPortfolio
				} else if err != nil {
					return nil, opsOut{}, err
				}
				only = a.ID
			}
			var from time.Time
			if in.Desde != "" {
				if from, err = parseDay(in.Desde); err != nil {
					return nil, opsOut{}, errors.New("desde va como AAAA-MM-DD")
				}
			}
			limit := in.Limite
			if limit <= 0 {
				limit = 30
			}
			limit = min(limit, 200)
			out := opsOut{Operaciones: []opOut{}}
			for i := len(v.ops) - 1; i >= 0; i-- {
				o := v.ops[i]
				if (only != 0 && o.AssetID != only) || o.Day.Before(from) {
					continue
				}
				out.Total++
				if len(out.Operaciones) < limit {
					out.Operaciones = append(out.Operaciones, opOutOf(v, o))
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_apuntar", Description: "Apunta una operación en la cartera: compra, venta, dividendo, recompensa (staking), traspaso entre plataformas o comisión. Hace falta la fecha y, según el tipo, la cantidad y el precio (o el importe): si la persona no los dice, pregúntalos antes. En una recompensa sin precio, el programa pone su valor de mercado de ese día. No opera en ningún sitio: solo lo anota"},
		func(ctx context.Context, req *mcp.CallToolRequest, in addOpIn) (*mcp.CallToolResult, addOpOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, addOpOut{}, err
			}
			if strings.TrimSpace(in.Fecha) == "" {
				return nil, addOpOut{}, errors.New("falta la fecha: pregúntasela")
			}
			day, err := parseDay(in.Fecha)
			if err != nil {
				return nil, addOpOut{}, errors.New("la fecha va como AAAA-MM-DD")
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, addOpOut{}, err
			}
			var aid int64
			if strings.TrimSpace(in.Activo) != "" {
				a, err := findAsset(v, in.Activo)
				if errors.Is(err, errNotFound) {
					if _, _, ok := parseRef(in.Activo); !ok {
						return nil, addOpOut{}, errNotInPortfolio
					}
					a, _, err = svc.addFound(ctx, who.User, in.Activo, "", "", false)
				}
				if err != nil {
					return nil, addOpOut{}, err
				}
				aid = a.ID
			}
			o, err := svc.addOp(ctx, who.User, opInput{AssetID: aid, Kind: strings.ToLower(strings.TrimSpace(in.Tipo)), Day: day, Time: in.Hora,
				Qty: in.Cantidad, Price: in.Precio, Total: in.Importe, Fee: in.Comision, Currency: in.Moneda, Platform: in.Plataforma,
				To: in.Destino, Note: in.Nota, Origin: originAssistant})
			if err != nil {
				return nil, addOpOut{}, err
			}
			v, err = svc.load(ctx, who.User)
			if err != nil {
				return nil, addOpOut{}, err
			}
			out := addOpOut{Apuntada: opOutOf(v, o)}
			if p := v.pf.pos(aid); p != nil {
				out.Posicion = posOf(p)
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_precio", Description: "Cotización actual de cualquier activo por símbolo, nombre o ISIN, aunque no esté en la cartera (cripto en Kraken o CoinGecko; acciones, ETF y fondos en Yahoo Finance). Devuelve su «ref» para añadirlo con cartera_apuntar o cartera_seguimiento"},
		func(ctx context.Context, req *mcp.CallToolRequest, in lookupIn) (*mcp.CallToolResult, lookupOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, lookupOut{}, err
			}
			list, err := svc.lookup(ctx, in.Consulta)
			out := lookupOut{Resultados: []pricedOut{}}
			if len(list) == 0 {
				if err != nil && !errors.Is(err, errNoMatch) {
					return nil, out, errors.New("las fuentes de precios no responden ahora; prueba más tarde")
				}
				out.Nota = "no se ha encontrado"
				return nil, out, nil
			}
			v, lerr := svc.load(ctx, who.User)
			if lerr != nil {
				return nil, out, lerr
			}
			for _, p := range list {
				po := pricedOut{Ref: p.ref(), Simbolo: p.Symbol, Nombre: p.Name, Tipo: kindLabel(p.Kind), Mercado: p.Market, Fuente: sourceName(p.Source),
					Precio: rp(p.Price), Moneda: p.quote.Currency}
				if p.Prev > 0 {
					po.HoyPct = r2(p.change())
				}
				if !p.At.IsZero() {
					po.Hora = p.At.In(madrid).Format("2006-01-02 15:04")
				}
				for _, a := range v.assets {
					if a.key() == (assetData{Source: p.Source, SourceID: p.SourceID}).key() {
						po.EnCartera = a.ID
					}
				}
				out.Resultados = append(out.Resultados, po)
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_seguimiento", Description: "Los activos que la persona sigue (los tenga o no) con su precio y su variación de hoy y de 30 días. Con «anadir» pone uno en seguimiento (con su «ref» de cartera_precio si aún no está en la cartera); con «quitar», lo deja de seguir"},
		func(ctx context.Context, req *mcp.CallToolRequest, in watchIn) (*mcp.CallToolResult, watchOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, watchOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, watchOut{}, err
			}
			if s := strings.TrimSpace(in.Anadir); s != "" {
				a, err := findAsset(v, s)
				switch {
				case errors.Is(err, errNotFound):
					if _, _, ok := parseRef(s); !ok {
						return nil, watchOut{}, errNotInPortfolio
					}
					_, _, err = svc.addFound(ctx, who.User, s, "", "", true)
				case err == nil:
					err = svc.store.setWatch(ctx, who.User, a.ID, true)
				}
				if err != nil {
					return nil, watchOut{}, err
				}
			}
			if s := strings.TrimSpace(in.Quitar); s != "" {
				a, err := findAsset(v, s)
				if errors.Is(err, errNotFound) {
					return nil, watchOut{}, errors.New("no lo sigue")
				} else if err != nil {
					return nil, watchOut{}, err
				}
				if err := svc.store.setWatch(ctx, who.User, a.ID, false); err != nil {
					return nil, watchOut{}, err
				}
			}
			if in.Anadir != "" || in.Quitar != "" {
				if v, err = svc.load(ctx, who.User); err != nil {
					return nil, watchOut{}, err
				}
			}
			out := watchOut{Seguimiento: []watchItem{}}
			today := dayOf(v.now)
			for _, p := range v.pf.Positions {
				if !p.Asset.Watch {
					continue
				}
				it := watchItem{ID: p.Asset.ID, Simbolo: p.Asset.Symbol, Nombre: p.Asset.Name, Precio: rp(p.Price), Moneda: p.Quote.Currency, LoTiene: p.Qty > eps}
				if it.Moneda == "" {
					it.Moneda = p.Asset.Currency
				}
				if p.Quote.Prev > 0 {
					it.HoyPct = r2(p.Quote.change())
				}
				if old, ok := v.hist[p.Asset.ID].at(today.Add(-30 * dayUnit)); ok && old > 0 && p.Price > 0 {
					it.Mes = r2((p.Price/old - 1) * 100)
				}
				out.Seguimiento = append(out.Seguimiento, it)
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_alertas", Description: "Las alertas de precio y de la cartera que tiene la persona (avisan por Telegram)"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, alertsListOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, alertsListOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, alertsListOut{}, err
			}
			list, err := svc.store.alerts(ctx, who.User)
			if err != nil {
				return nil, alertsListOut{}, err
			}
			return nil, alertsListOut{Alertas: alertsOut(v, list)}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_alerta_crear", Description: "Crea una alerta que avisa por Telegram: precio de un activo por encima o por debajo de un valor, variación del día de ± X % o más, caída de X % desde el máximo de N días, o valor total de la cartera por encima o por debajo (sin activo). Una vez (por defecto) o cada vez que vuelva a cruzar"},
		func(ctx context.Context, req *mcp.CallToolRequest, in alertIn) (*mcp.CallToolResult, alertOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, alertOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, alertOut{}, err
			}
			a := alert{Active: true, alertData: alertData{Kind: strings.ToLower(strings.TrimSpace(in.Tipo)), Threshold: math.Abs(in.Umbral), Days: in.Dias, Repeat: in.Repetir}}
			if strings.TrimSpace(in.Activo) != "" {
				x, err := findAsset(v, in.Activo)
				if errors.Is(err, errNotFound) {
					return nil, alertOut{}, errNotInPortfolio
				} else if err != nil {
					return nil, alertOut{}, err
				}
				a.AssetID = x.ID
			}
			if err := checkAlert(&a); err != nil {
				return nil, alertOut{}, err
			}
			if a.ID, err = svc.store.addAlert(ctx, who.User, a); err != nil {
				return nil, alertOut{}, err
			}
			svc.checkAlerts(ctx, who.User)
			return nil, alertsOut(v, []alert{a})[0], nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "cartera_alerta_borrar", Description: "Borra una alerta por su id (de cartera_alertas). Pide antes confirmación a la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in alertDelIn) (*mcp.CallToolResult, okOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, okOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, okOut{}, err
			}
			list, err := svc.store.alerts(ctx, who.User)
			if err != nil {
				return nil, okOut{}, err
			}
			for _, a := range list {
				if a.ID != in.ID {
					continue
				}
				if err := svc.store.deleteAlert(ctx, who.User, a.ID); err != nil {
					return nil, okOut{}, err
				}
				return nil, okOut{Borrada: alertsOut(v, []alert{a})[0]}, nil
			}
			return nil, okOut{}, errors.New("esa alerta no existe")
		})
}
