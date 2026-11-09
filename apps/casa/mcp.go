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

// Tools de Talia. Las cuentas (horas baratas, bloques, próximas fechas,
// consumos) las hace el programa, como en la web: la IA solo las cuenta.
// Respuestas en JSON con claves en español.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// toolError pasa los errores de lo escrito tal cual y esconde los internos.
func toolError(err error) error {
	switch {
	case err == nil:
		return nil
	case isInput(err), errors.Is(err, errNotFound), errors.Is(err, errFull):
		return err
	}
	return errors.New("algo ha fallado en la app Casa; prueba otra vez")
}

// ---- Luz ----

type luzIn struct {
	Dia string `json:"dia,omitempty" jsonschema:"hoy, manana o vacío (los dos)"`
}

type hourOut struct {
	Hora   string  `json:"hora"` // «14-15»
	Precio float64 `json:"eur_kwh"`
	Tramo  string  `json:"tramo"` // barata, media o cara (tercios del día)
}

type blockOut struct {
	Desde string  `json:"desde"` // «14:00»
	Hasta string  `json:"hasta"`
	Media float64 `json:"media_eur_kwh"`
}

type luzDayOut struct {
	Fecha      string    `json:"fecha"`
	Min        float64   `json:"minimo_eur_kwh"`
	Max        float64   `json:"maximo_eur_kwh"`
	Media      float64   `json:"media_eur_kwh"`
	Ahora      *hourOut  `json:"ahora,omitempty"`
	MasBaratas []hourOut `json:"horas_mas_baratas"`
	MasCaras   []hourOut `json:"horas_mas_caras"`
	Bloque2h   *blockOut `json:"bloque_mas_barato_2h,omitempty"`
	Bloque3h   *blockOut `json:"bloque_mas_barato_3h,omitempty"`
	DesdeAhora *blockOut `json:"mejor_bloque_3h_desde_ahora,omitempty"`
	Horas      []hourOut `json:"horas"`
}

type luzOut struct {
	Unidad string     `json:"unidad"`
	Hoy    *luzDayOut `json:"hoy,omitempty"`
	Manana *luzDayOut `json:"manana,omitempty"`
	Nota   string     `json:"nota,omitempty"`
}

func hhmm(t time.Time) string { return t.In(madrid).Format("15:04") }

func hourOutOf(h hourPrice, l *luzDay) hourOut {
	a, b := hourOf(h.T), hourOf(h.T.Add(time.Hour))
	if b == 0 {
		b = 24
	}
	return hourOut{Hora: strconv.Itoa(a) + "-" + strconv.Itoa(b), Precio: r3(h.P), Tramo: level(h.P, l.CheapMax, l.DearMin)}
}

func blockOutOf(b block) *blockOut {
	end := hhmm(b.End)
	if end == "00:00" {
		end = "24:00"
	}
	return &blockOut{Desde: hhmm(b.Start), Hasta: end, Media: r3(b.Avg)}
}

func luzDayOutOf(l *luzDay) *luzDayOut {
	if l == nil {
		return nil
	}
	o := &luzDayOut{Fecha: isoDay(l.Day), Min: r3(l.Lo), Max: r3(l.Hi), Media: r3(l.Avg), MasBaratas: []hourOut{}, MasCaras: []hourOut{}, Horas: []hourOut{}}
	if l.HasNow {
		h := hourOutOf(l.Now, l)
		o.Ahora = &h
	}
	for _, h := range l.Cheap {
		o.MasBaratas = append(o.MasBaratas, hourOutOf(h, l))
	}
	for _, h := range l.Dear {
		o.MasCaras = append(o.MasCaras, hourOutOf(h, l))
	}
	for _, h := range l.Hours {
		o.Horas = append(o.Horas, hourOutOf(h, l))
	}
	if l.Has2 {
		o.Bloque2h = blockOutOf(l.Block2)
	}
	if l.Has3 {
		o.Bloque3h = blockOutOf(l.Block3)
	}
	if l.HasRest3 {
		o.DesdeAhora = blockOutOf(l.Rest3)
	}
	return o
}

// ---- Mantenimiento ----

type taskOut struct {
	ID      int64  `json:"id"`
	Nombre  string `json:"nombre"`
	Cada    string `json:"cada"`
	Ultima  string `json:"ultima_vez,omitempty"`
	Proxima string `json:"proxima"`
	Estado  string `json:"estado"` // atrasada, hoy, pronto, al-dia
	Dias    int    `json:"dias_hasta_la_proxima"`
	Nota    string `json:"nota,omitempty"`
}

func taskOutOf(t task, today time.Time) taskOut {
	st, n := t.state(today)
	return taskOut{ID: t.ID, Nombre: t.Name, Cada: t.every(), Ultima: dayValue(t.Last), Proxima: isoDay(t.Due), Estado: st, Dias: n, Nota: t.Note}
}

type suggestionOut struct {
	Clave  string `json:"clave"`
	Nombre string `json:"nombre"`
	Cada   string `json:"cada"`
}

type tasksOut struct {
	Tareas      []taskOut       `json:"tareas"`
	Atrasadas   int             `json:"atrasadas"`
	Hoy         int             `json:"para_hoy"`
	Sugerencias []suggestionOut `json:"sugerencias_sin_anadir,omitempty"`
}

type doneIn struct {
	Tarea string `json:"tarea" jsonschema:"id o nombre (o parte del nombre) de la tarea"`
	Fecha string `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD en que se hizo; vacío: hoy"`
}

type taskResult struct {
	Tarea taskOut `json:"tarea"`
	Ya    bool    `json:"ya_estaba,omitempty"`
}

type newTaskIn struct {
	Sugerencia string `json:"sugerencia,omitempty" jsonschema:"clave de una sugerencia de casa_mantenimiento (caldera, gas, aire…); con ella no hace falta lo demás"`
	Nombre     string `json:"nombre,omitempty" jsonschema:"qué hay que hacer"`
	Cada       int    `json:"cada,omitempty" jsonschema:"cada cuántas unidades"`
	Unidad     string `json:"unidad,omitempty" jsonschema:"dias, semanas, meses o anios"`
	Ultima     string `json:"ultima,omitempty" jsonschema:"AAAA-MM-DD de la última vez, si la dice"`
	Proxima    string `json:"proxima,omitempty" jsonschema:"AAAA-MM-DD de la próxima, si la dice; si no, la última más el periodo, u hoy"`
	Nota       string `json:"nota,omitempty"`
}

// findTask busca una tarea por id o por nombre.
func findTask(list []task, s string) (task, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return task{}, bad("di qué tarea (su nombre o su id)")
	}
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		for _, t := range list {
			if t.ID == id {
				return t, nil
			}
		}
	}
	q := strings.ToLower(s)
	var exact, partial []task
	for _, t := range list {
		switch n := strings.ToLower(t.Name); {
		case n == q:
			exact = append(exact, t)
		case strings.Contains(n, q):
			partial = append(partial, t)
		}
	}
	found := exact
	if len(found) == 0 {
		found = partial
	}
	switch len(found) {
	case 0:
		return task{}, errNotFound
	case 1:
		return found[0], nil
	}
	var names []string
	for _, t := range found {
		names = append(names, t.Name+" ("+strconv.FormatInt(t.ID, 10)+")")
	}
	return task{}, bad("hay varias que encajan: " + strings.Join(names, ", ") + ". Usa el id")
}

func optDayArg(s, what string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	d, err := parseDay(s)
	if err != nil {
		return d, bad(what + " va como AAAA-MM-DD")
	}
	return d, nil
}

// ---- Lecturas ----

type readingIn struct {
	Contador string  `json:"contador" jsonschema:"luz, agua o gas"`
	Valor    float64 `json:"valor" jsonschema:"lo que marca el contador (no el consumo)"`
	Fecha    string  `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD; vacío: hoy"`
	Nota     string  `json:"nota,omitempty"`
}

type usageOut struct {
	Desde    string  `json:"desde"`
	Hasta    string  `json:"hasta"`
	Dias     int     `json:"dias"`
	Consumo  float64 `json:"consumo"`
	PorDia   float64 `json:"por_dia"`
	Reinicio bool    `json:"lectura_baja,omitempty"`
}

type readingOut struct {
	Fecha string  `json:"fecha"`
	Valor float64 `json:"valor"`
}

type meterOut struct {
	Contador        string      `json:"contador"`
	Unidad          string      `json:"unidad"`
	Ultima          *readingOut `json:"ultima_lectura,omitempty"`
	Lecturas        int         `json:"lecturas"`
	Periodos        []usageOut  `json:"periodos_recientes"`
	MediaDiaria     float64     `json:"media_diaria_ultimo_ano,omitempty"`
	ConsumoUltimoAn float64     `json:"consumo_ultimo_ano,omitempty"`
	DiasConDatos    int         `json:"dias_con_datos_ultimo_ano,omitempty"`
}

func usageOutOf(p usage) usageOut {
	return usageOut{Desde: isoDay(p.From), Hasta: isoDay(p.To), Dias: p.Days, Consumo: r3(p.Used), PorDia: r3(p.PerDay), Reinicio: p.Reset}
}

func meterOutOf(m meterView) meterOut {
	o := meterOut{Contador: m.Meter, Unidad: m.Unit, Lecturas: len(m.Readings), Periodos: []usageOut{},
		MediaDiaria: r3(m.AvgDay), ConsumoUltimoAn: r3(m.YearUsed), DiasConDatos: m.AvgDays}
	if r, ok := m.last(); ok {
		o.Ultima = &readingOut{Fecha: isoDay(r.Day), Valor: r.Value}
	}
	for i := max(0, len(m.Periods)-12); i < len(m.Periods); i++ {
		o.Periodos = append(o.Periodos, usageOutOf(m.Periods[i]))
	}
	return o
}

type addReadingOut struct {
	Apuntada   readingOut `json:"apuntada"`
	Sustituye  bool       `json:"sustituye_a_la_de_ese_dia,omitempty"`
	Contador   meterOut   `json:"contador"`
	DesdeAntes *usageOut  `json:"desde_la_anterior,omitempty"`
}

type usageIn struct {
	Contador string `json:"contador,omitempty" jsonschema:"luz, agua o gas; vacío: los tres"`
}

type usageListOut struct {
	Contadores []meterOut `json:"contadores"`
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "casa_luz", Description: "Precio de la luz (PVPC de Red Eléctrica, €/kWh sin impuestos) por horas de hoy y de mañana (mañana se publica hacia las 20:30): mínimo, máximo, media, la hora en curso, las 3 horas más baratas y más caras, el bloque más barato de 2 y de 3 horas (para la lavadora o el lavavajillas) y, hoy, el mejor bloque desde ahora. Calculado por el programa"},
		func(ctx context.Context, req *mcp.CallToolRequest, in luzIn) (*mcp.CallToolResult, luzOut, error) {
			if _, err := identity(req); err != nil {
				return nil, luzOut{}, err
			}
			today, tomorrow, err := svc.luz(ctx)
			if err != nil {
				return nil, luzOut{}, toolError(err)
			}
			out := luzOut{Unidad: "€/kWh, PVPC (tarifa regulada 2.0TD) sin impuestos; horas de Madrid"}
			dia := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(in.Dia, "ñ", "n")))
			if dia != "manana" {
				out.Hoy = luzDayOutOf(today)
				if today == nil {
					out.Nota = "Aún no están los precios de hoy: la app los está pidiendo a Red Eléctrica."
				}
			}
			if dia != "hoy" {
				out.Manana = luzDayOutOf(tomorrow)
				if tomorrow == nil {
					out.Nota = strings.TrimSpace(out.Nota + " Los de mañana aún no están publicados: salen hacia las 20:30.")
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "casa_mantenimiento", Description: "Las tareas de mantenimiento de la casa (caldera, filtros, revisión del gas…): cada cuánto, la última vez, la próxima y si está atrasada, toca hoy, pronto o al día. También las sugerencias que aún no ha añadido"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, tasksOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, tasksOut{}, err
			}
			list, err := svc.store.tasks(ctx, who.User)
			if err != nil {
				return nil, tasksOut{}, toolError(err)
			}
			today := dayOf(svc.clock())
			out := tasksOut{Tareas: []taskOut{}}
			have := map[string]bool{}
			for _, t := range list {
				o := taskOutOf(t, today)
				out.Tareas = append(out.Tareas, o)
				switch o.Estado {
				case stLate:
					out.Atrasadas++
				case stToday:
					out.Hoy++
				}
				have[t.Template] = true
			}
			for _, s := range suggestions {
				if !have[s.Key] {
					out.Sugerencias = append(out.Sugerencias, suggestionOut{Clave: s.Key, Nombre: s.Name, Cada: s.EveryLabel()})
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "casa_mantenimiento_hecho", Description: "Da por hecha una tarea de mantenimiento (hoy o el día que diga): la próxima se calcula sola"},
		func(ctx context.Context, req *mcp.CallToolRequest, in doneIn) (*mcp.CallToolResult, taskResult, error) {
			who, err := identity(req)
			if err != nil {
				return nil, taskResult{}, err
			}
			list, err := svc.store.tasks(ctx, who.User)
			if err != nil {
				return nil, taskResult{}, toolError(err)
			}
			t, err := findTask(list, in.Tarea)
			if errors.Is(err, errNotFound) {
				return nil, taskResult{}, errors.New("no tiene ninguna tarea así: mira casa_mantenimiento")
			}
			if err != nil {
				return nil, taskResult{}, toolError(err)
			}
			day, err := optDayArg(in.Fecha, "la fecha")
			if err != nil {
				return nil, taskResult{}, err
			}
			t, err = svc.markDone(ctx, who.User, t.ID, day)
			if err != nil {
				return nil, taskResult{}, toolError(err)
			}
			return nil, taskResult{Tarea: taskOutOf(t, dayOf(svc.clock()))}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "casa_mantenimiento_crear", Description: "Crea una tarea de mantenimiento de la casa: una sugerencia (por su clave) o una propia con su nombre y cada cuánto"},
		func(ctx context.Context, req *mcp.CallToolRequest, in newTaskIn) (*mcp.CallToolResult, taskResult, error) {
			who, err := identity(req)
			if err != nil {
				return nil, taskResult{}, err
			}
			today := dayOf(svc.clock())
			if k := strings.TrimSpace(in.Sugerencia); k != "" {
				t, existed, err := svc.addTemplate(ctx, who.User, strings.ToLower(k))
				if err != nil {
					return nil, taskResult{}, toolError(err)
				}
				if !existed && (in.Ultima != "" || in.Proxima != "") {
					ti := taskInput{Name: t.Name, Every: t.Every, Unit: t.Unit, Note: t.Note, Template: t.Template}
					if ti.Last, err = optDayArg(in.Ultima, "la última vez"); err != nil {
						return nil, taskResult{}, err
					}
					if ti.Due, err = optDayArg(in.Proxima, "la próxima"); err != nil {
						return nil, taskResult{}, err
					}
					if t, err = svc.updateTask(ctx, who.User, t.ID, ti); err != nil {
						return nil, taskResult{}, toolError(err)
					}
				}
				return nil, taskResult{Tarea: taskOutOf(t, today), Ya: existed}, nil
			}
			ti := taskInput{Name: in.Nombre, Every: in.Cada, Unit: strings.ToLower(strings.TrimSpace(in.Unidad)), Note: in.Nota}
			if ti.Unit == "años" {
				ti.Unit = unitYears
			}
			if ti.Unit == "días" {
				ti.Unit = unitDays
			}
			if ti.Last, err = optDayArg(in.Ultima, "la última vez"); err != nil {
				return nil, taskResult{}, err
			}
			if ti.Due, err = optDayArg(in.Proxima, "la próxima"); err != nil {
				return nil, taskResult{}, err
			}
			t, err := svc.addTask(ctx, who.User, ti)
			if err != nil {
				return nil, taskResult{}, toolError(err)
			}
			return nil, taskResult{Tarea: taskOutOf(t, today)}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "casa_lectura_apuntar", Description: "Apunta la lectura de un contador de casa (luz, agua o gas): lo que marca el contador, no el consumo. Devuelve lo gastado desde la lectura anterior"},
		func(ctx context.Context, req *mcp.CallToolRequest, in readingIn) (*mcp.CallToolResult, addReadingOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, addReadingOut{}, err
			}
			day, err := optDayArg(in.Fecha, "la fecha")
			if err != nil {
				return nil, addReadingOut{}, err
			}
			x, replaced, err := svc.addReading(ctx, who.User, readingInput{Meter: strings.ToLower(strings.TrimSpace(in.Contador)), Day: day, Value: in.Valor, Note: in.Nota})
			if err != nil {
				return nil, addReadingOut{}, toolError(err)
			}
			views, _, err := svc.meterViews(ctx, who.User)
			if err != nil {
				return nil, addReadingOut{}, toolError(err)
			}
			out := addReadingOut{Apuntada: readingOut{Fecha: isoDay(x.Day), Valor: x.Value}, Sustituye: replaced}
			for _, m := range views {
				if m.Meter != x.Meter {
					continue
				}
				out.Contador = meterOutOf(m)
				for _, p := range m.Periods {
					if p.To.Equal(x.Day) {
						u := usageOutOf(p)
						out.DesdeAntes = &u
					}
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "casa_consumos", Description: "Consumos de los contadores de casa (luz, agua, gas) según las lecturas apuntadas: última lectura, lo gastado entre lecturas y por día, y la media diaria del último año"},
		func(ctx context.Context, req *mcp.CallToolRequest, in usageIn) (*mcp.CallToolResult, usageListOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, usageListOut{}, err
			}
			want := strings.ToLower(strings.TrimSpace(in.Contador))
			if want != "" && !validMeter(want) {
				return nil, usageListOut{}, bad("el contador es luz, agua o gas")
			}
			views, _, err := svc.meterViews(ctx, who.User)
			if err != nil {
				return nil, usageListOut{}, toolError(err)
			}
			out := usageListOut{Contadores: []meterOut{}}
			for _, m := range views {
				if want == "" || m.Meter == want {
					out.Contadores = append(out.Contadores, meterOutOf(m))
				}
			}
			return nil, out, nil
		})
}
