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
	"homeserver/platform/seal"
)

// Tools de Talia. Las rachas, los porcentajes y las medias los calcula el
// programa (como en la web): la IA solo los cuenta. Respuestas en JSON con
// claves en español. La nota del diario es muy personal: solo sale con
// diario_ver, cuando se pide expresamente para unas fechas.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }

// parseWhen lee una fecha de Talia: vacía u «hoy», «ayer», «anteayer» o
// AAAA-MM-DD.
func parseWhen(s string, today time.Time) (time.Time, error) {
	switch seal.Fold(strings.TrimSpace(s)) {
	case "", "hoy":
		return today, nil
	case "ayer":
		return today.Add(-dayUnit), nil
	case "anteayer":
		return today.Add(-2 * dayUnit), nil
	}
	d, err := parseDay(s)
	if err != nil {
		return d, bad("La fecha va como AAAA-MM-DD (o «hoy», «ayer»).")
	}
	return d, nil
}

// parseWeekdays lee días de la semana por su nombre («lunes», «mié», «X»).
func parseWeekdays(list []string) ([]int, error) {
	var out []int
	for _, raw := range list {
		for _, w := range seal.Words(raw) {
			found := -1
			for i, name := range weekdays {
				n := seal.Fold(name)
				if w == n || w == n+"s" || len([]rune(w)) >= 2 && strings.HasPrefix(n, w) {
					found = i
					break
				}
			}
			if found < 0 && len(w) == 1 {
				for i, s := range shortDays {
					if strings.EqualFold(s, w) {
						found = i
					}
				}
			}
			if found < 0 {
				return nil, bad("No entiendo el día «" + w + "»: usa lunes, martes, miércoles…")
			}
			out = append(out, found)
		}
	}
	return out, nil
}

// ---- Salidas ----

type habitOut struct {
	ID           int64   `json:"id"`
	Nombre       string  `json:"nombre"`
	Emoji        string  `json:"emoji,omitempty"`
	Tipo         string  `json:"tipo"`
	Objetivo     float64 `json:"objetivo,omitempty"`
	Unidad       string  `json:"unidad,omitempty"`
	Frecuencia   string  `json:"frecuencia"`
	Toca         bool    `json:"toca"`
	Hecho        bool    `json:"hecho"`
	Cantidad     float64 `json:"cantidad,omitempty"`
	Racha        int     `json:"racha_actual"`
	RachaMejor   int     `json:"racha_mejor"`
	RachaEn      string  `json:"racha_en"` // días o semanas
	Semana       string  `json:"esta_semana"`
	Recordatorio string  `json:"recordatorio,omitempty"`
	Archivado    bool    `json:"archivado,omitempty"`
}

// habitOutOf es un hábito visto el día day (con las rachas de hoy).
func habitOutOf(h habit, m marks, today, day time.Time) habitOut {
	st := statsOf(h, m, today)
	o := habitOut{ID: h.ID, Nombre: h.Name, Emoji: h.Emoji, Tipo: "sí o no", Unidad: h.Unit, Frecuencia: freqText(h.Freq),
		Hecho: h.done(m, day), Cantidad: r2(m[isoDay(day)]), Racha: st.Current, RachaMejor: st.Best, RachaEn: "días",
		Recordatorio: h.Remind, Archivado: h.Archived}
	if h.Kind == kindAmount {
		o.Tipo, o.Objetivo = "cantidad", h.Target
	} else {
		o.Cantidad = 0
	}
	if h.weekly() {
		o.RachaEn = "semanas"
		o.Semana = itoa(st.WeekDone) + " de " + itoa(st.WeekTotal) + " veces"
	} else {
		o.Semana = itoa(st.WeekDone) + " de " + itoa(st.WeekTotal) + " días"
	}
	if day.Equal(today) {
		o.Toca = st.ScheduledToday
	} else {
		o.Toca = h.weekly() || h.scheduled(day)
	}
	return o
}

func itoa(n int) string { return strconv.Itoa(n) }

type todayIn struct {
	Fecha string `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD, «hoy» o «ayer»; por defecto, hoy"`
}

type todayOut struct {
	Fecha         string     `json:"fecha"`
	Habitos       []habitOut `json:"habitos"`
	DiarioEscrito bool       `json:"diario_escrito"`
}

type markIn struct {
	Nombre   string   `json:"nombre" jsonschema:"nombre del hábito (vale una parte: «agua»)"`
	Cantidad *float64 `json:"cantidad,omitempty" jsonschema:"en los de cantidad: cuánto se suma (2 vasos, 10 min). Sin ella, se da por cumplido el objetivo del día"`
	Fijar    bool     `json:"fijar,omitempty" jsonschema:"true: la cantidad es el total del día, no se suma"`
	Deshacer bool     `json:"deshacer,omitempty" jsonschema:"true: quita lo marcado ese día"`
	Fecha    string   `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD, «hoy» o «ayer»; por defecto, hoy"`
}

type markOut struct {
	Fecha  string   `json:"fecha"`
	Habito habitOut `json:"habito"`
}

type createIn struct {
	Nombre       string   `json:"nombre"`
	Emoji        string   `json:"emoji,omitempty" jsonschema:"un emoji, opcional"`
	Tipo         string   `json:"tipo,omitempty" jsonschema:"si_no (por defecto) o cantidad"`
	Objetivo     float64  `json:"objetivo,omitempty" jsonschema:"en los de cantidad: lo de cada día (8, 20)"`
	Unidad       string   `json:"unidad,omitempty" jsonschema:"en los de cantidad: vasos, min, páginas…"`
	Frecuencia   string   `json:"frecuencia,omitempty" jsonschema:"diaria (por defecto), dias (unos días de la semana) o semana (N veces por semana, cualquier día)"`
	Dias         []string `json:"dias,omitempty" jsonschema:"con frecuencia dias: lunes, martes, miércoles…"`
	Veces        int      `json:"veces,omitempty" jsonschema:"con frecuencia semana: veces por semana (1 a 7)"`
	Recordatorio string   `json:"recordatorio,omitempty" jsonschema:"HH:MM: avisa por Telegram a esa hora si aún no está hecho"`
}

type statsIn struct {
	Nombre string `json:"nombre,omitempty" jsonschema:"un hábito; vacío, todos"`
}

type statOut struct {
	Nombre     string   `json:"nombre"`
	Frecuencia string   `json:"frecuencia"`
	Racha      int      `json:"racha_actual"`
	RachaMejor int      `json:"racha_mejor"`
	RachaEn    string   `json:"racha_en"`
	Semana     string   `json:"esta_semana"`
	MesPct     *float64 `json:"este_mes_pct,omitempty"`
	Ult30Pct   *float64 `json:"ultimos_30_dias_pct,omitempty"`
	Diario     *corrOut `json:"con_el_diario,omitempty"`
}

type corrOut struct {
	DiasHecho        int      `json:"dias_hecho"`
	DiasSinHacer     int      `json:"dias_sin_hacer"`
	AnimoHecho       *float64 `json:"animo_medio_hecho,omitempty"`
	AnimoSinHacer    *float64 `json:"animo_medio_sin_hacer,omitempty"`
	EnergiaHecho     *float64 `json:"energia_media_hecho,omitempty"`
	EnergiaSinHacer  *float64 `json:"energia_media_sin_hacer,omitempty"`
	SuenoHecho       *float64 `json:"sueno_medio_hecho,omitempty"`
	SuenoSinHacer    *float64 `json:"sueno_medio_sin_hacer,omitempty"`
	FaltanDatos      bool     `json:"faltan_datos,omitempty"`
	ExplicacionDatos string   `json:"nota,omitempty"`
}

type statsOut struct {
	Habitos     []statOut `json:"habitos"`
	Periodo     string    `json:"periodo_diario"`
	SuenoAnimo  string    `json:"sueno_y_animo,omitempty"`
	Explicacion string    `json:"como_se_calcula"`
}

type journalIn struct {
	Fecha          string   `json:"fecha,omitempty" jsonschema:"AAAA-MM-DD, «hoy» o «ayer»; por defecto, hoy"`
	Animo          *int     `json:"animo,omitempty" jsonschema:"1 (muy mal) a 5 (muy bien)"`
	Energia        *int     `json:"energia,omitempty" jsonschema:"1 (agotado) a 5 (a tope)"`
	Sueno          *float64 `json:"sueno,omitempty" jsonschema:"horas dormidas la noche anterior"`
	Nota           string   `json:"nota,omitempty" jsonschema:"texto que se añade a la nota del día"`
	ReemplazarNota bool     `json:"reemplazar_nota,omitempty" jsonschema:"true: la nota sustituye a la que hubiera en vez de añadirse"`
	Etiquetas      []string `json:"etiquetas,omitempty" jsonschema:"se añaden a las del día"`
}

type journalOut struct {
	Fecha     string   `json:"fecha"`
	Animo     int      `json:"animo,omitempty"`
	Energia   int      `json:"energia,omitempty"`
	Sueno     *float64 `json:"sueno,omitempty"`
	Etiquetas []string `json:"etiquetas,omitempty"`
	TieneNota bool     `json:"tiene_nota"`
}

type viewIn struct {
	Desde        string `json:"desde" jsonschema:"AAAA-MM-DD (o «hoy», «ayer»)"`
	Hasta        string `json:"hasta,omitempty" jsonschema:"AAAA-MM-DD; por defecto, el mismo día que desde"`
	IncluirNotas bool   `json:"incluir_notas,omitempty" jsonschema:"true solo si la persona pide lo que escribió esos días"`
}

type dayOut struct {
	Fecha     string   `json:"fecha"`
	Animo     int      `json:"animo,omitempty"`
	Energia   int      `json:"energia,omitempty"`
	Sueno     *float64 `json:"sueno,omitempty"`
	Etiquetas []string `json:"etiquetas,omitempty"`
	TieneNota bool     `json:"tiene_nota,omitempty"`
	Nota      string   `json:"nota,omitempty"`
}

type viewOut struct {
	Desde        string   `json:"desde"`
	Hasta        string   `json:"hasta"`
	Dias         []dayOut `json:"dias"`
	AnimoMedio   *float64 `json:"animo_medio,omitempty"`
	EnergiaMedia *float64 `json:"energia_media,omitempty"`
	SuenoMedio   *float64 `json:"sueno_medio,omitempty"`
}

const maxViewDays = 93

func avgPtr(a avg) *float64 {
	if v, ok := a.value(); ok {
		v = r2(v)
		return &v
	}
	return nil
}

func pctPtr(r ratio) *float64 {
	if p, ok := r.pct(); ok {
		p = math.Round(p)
		return &p
	}
	return nil
}

func entryOut(day time.Time, d entryData) journalOut {
	return journalOut{Fecha: isoDay(day), Animo: d.Mood, Energia: d.Energy, Sueno: d.Sleep, Etiquetas: d.Tags, TieneNota: d.Note != ""}
}

func addTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "habitos_hoy", Description: "Los hábitos de la persona para hoy (u otro día): cuáles tocan, si están hechos, la cantidad apuntada, la racha actual y la mejor (calculadas por el programa) y si ha escrito el diario (sin su contenido)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in todayIn) (*mcp.CallToolResult, todayOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, todayOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, todayOut{}, err
			}
			day, err := parseWhen(in.Fecha, v.today)
			if err == nil {
				err = svc.checkDay(day)
			}
			if err != nil {
				return nil, todayOut{}, err
			}
			out := todayOut{Fecha: isoDay(day), Habitos: []habitOut{}}
			for _, h := range v.active() {
				out.Habitos = append(out.Habitos, habitOutOf(h, v.m(h), v.today, day))
			}
			if out.DiarioEscrito, err = svc.store.hasEntry(ctx, who.User, day); err != nil {
				return nil, todayOut{}, err
			}
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "habitos_marcar", Description: "Marca un hábito como hecho (hoy u otro día), suma o fija la cantidad en los de cantidad, o lo desmarca. Devuelve cómo queda y su racha"},
		func(ctx context.Context, req *mcp.CallToolRequest, in markIn) (*mcp.CallToolResult, markOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, markOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, markOut{}, err
			}
			h, err := findHabit(v.habits, in.Nombre)
			if err != nil {
				return nil, markOut{}, err
			}
			day, err := parseWhen(in.Fecha, v.today)
			if err != nil {
				return nil, markOut{}, err
			}
			m := v.m(h)
			cur := m[isoDay(day)]
			var amount float64
			switch {
			case in.Deshacer:
				amount = 0
			case h.Kind != kindAmount:
				amount = 1
			case in.Cantidad == nil:
				amount = math.Max(cur, h.target())
			case in.Fijar:
				amount = *in.Cantidad
			default:
				amount = cur + *in.Cantidad
			}
			if err := svc.setAmount(ctx, who.User, h, day, amount, "asistente"); err != nil {
				return nil, markOut{}, err
			}
			if m, err = svc.store.habitMarks(ctx, who.User, h.ID); err != nil {
				return nil, markOut{}, err
			}
			return nil, markOut{Fecha: isoDay(day), Habito: habitOutOf(h, m, v.today, day)}, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "habitos_crear", Description: "Crea un hábito: de sí o no o de cantidad con objetivo diario (8 vasos de agua, 20 min de lectura); diario, unos días de la semana o N veces por semana; con recordatorio opcional. Los entrenamientos del gimnasio van en Entreno, no aquí"},
		func(ctx context.Context, req *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, habitOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, habitOut{}, err
			}
			days, err := parseWeekdays(in.Dias)
			if err != nil {
				return nil, habitOut{}, err
			}
			kind := in.Tipo
			if kind == "" && in.Objetivo > 0 {
				kind = kindAmount
			}
			fk := in.Frecuencia
			if fk == "" && len(days) > 0 {
				fk = freqDays
			} else if fk == "" && in.Veces > 0 {
				fk = freqWeekly
			}
			h, err := svc.createHabit(ctx, who.User, habitInput{Name: in.Nombre, Emoji: in.Emoji, Kind: kind, Target: in.Objetivo, Unit: in.Unidad,
				Freq: freq{Kind: fk, Days: days, Times: in.Veces}, Remind: in.Recordatorio})
			if err != nil {
				return nil, habitOut{}, err
			}
			today := svc.today()
			return nil, habitOutOf(h, marks{}, today, today), nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "habitos_estadisticas", Description: "Rachas y porcentaje cumplido (esta semana, este mes, últimos 30 días) de cada hábito, y medias de ánimo, energía y sueño del diario los días que se hizo y los que no (últimos 90 días). Son medias simples calculadas por el programa: no prueban causas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in statsIn) (*mcp.CallToolResult, statsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, statsOut{}, err
			}
			v, err := svc.load(ctx, who.User)
			if err != nil {
				return nil, statsOut{}, err
			}
			list := v.active()
			if strings.TrimSpace(in.Nombre) != "" {
				h, err := findHabit(v.habits, in.Nombre)
				if err != nil {
					return nil, statsOut{}, err
				}
				list = []habit{h}
			}
			from := v.today.Add(-(corrDays - 1) * dayUnit)
			entries, err := svc.store.entries(ctx, who.User, from, v.today)
			if err != nil {
				return nil, statsOut{}, err
			}
			out := statsOut{Habitos: []statOut{}, Periodo: isoDay(from) + " a " + isoDay(v.today),
				Explicacion: "Racha: días seguidos hechos de los que tocan (en los semanales, semanas seguidas cumplidas); hoy sin hacer no la corta. Porcentajes: lo hecho de lo previsto; hoy y la semana en curso solo cuentan si ya están cumplidos."}
			corrs := correlations(list, v.marks, entries, from, v.today)
			for i, h := range list {
				st := v.stats(h)
				o := statOut{Nombre: h.Name, Frecuencia: freqText(h.Freq), Racha: st.Current, RachaMejor: st.Best, RachaEn: "días",
					MesPct: pctPtr(st.Month), Ult30Pct: pctPtr(st.Last30), Semana: itoa(st.WeekDone) + " de " + itoa(st.WeekTotal)}
				if h.weekly() {
					o.RachaEn = "semanas"
				}
				c := corrs[i]
				co := &corrOut{DiasHecho: c.MoodDone.N, DiasSinHacer: c.MoodOther.N}
				if c.enough() {
					co.AnimoHecho, co.AnimoSinHacer = avgPtr(c.MoodDone), avgPtr(c.MoodOther)
					co.EnergiaHecho, co.EnergiaSinHacer = avgPtr(c.EnergyDone), avgPtr(c.EnergyOther)
					co.SuenoHecho, co.SuenoSinHacer = avgPtr(c.SleepDone), avgPtr(c.SleepOther)
				} else {
					co.FaltanDatos = true
					co.ExplicacionDatos = "hacen falta al menos 3 días con el ánimo apuntado haciéndolo y 3 sin hacerlo"
				}
				o.Diario = co
				out.Habitos = append(out.Habitos, o)
			}
			out.SuenoAnimo = sleepCorrText(entries, from)
			return nil, out, nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "diario_apuntar", Description: "Apunta en el diario de un día (hoy por defecto) el ánimo (1–5), la energía (1–5), las horas de sueño, una nota y etiquetas. Lo que no se pasa se queda como estaba; la nota se añade a la que hubiera. No devuelve la nota"},
		func(ctx context.Context, req *mcp.CallToolRequest, in journalIn) (*mcp.CallToolResult, journalOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, journalOut{}, err
			}
			day, err := parseWhen(in.Fecha, svc.today())
			if err == nil {
				err = svc.checkDay(day)
			}
			if err != nil {
				return nil, journalOut{}, err
			}
			e, _, err := svc.store.entry(ctx, who.User, day)
			if err != nil {
				return nil, journalOut{}, err
			}
			d := e.entryData
			if in.Animo != nil {
				d.Mood = *in.Animo
			}
			if in.Energia != nil {
				d.Energy = *in.Energia
			}
			if in.Sueno != nil {
				s := *in.Sueno
				d.Sleep = &s
			}
			if note := strings.TrimSpace(in.Nota); note != "" || in.ReemplazarNota {
				if in.ReemplazarNota || d.Note == "" {
					d.Note = note
				} else {
					d.Note += "\n" + note
				}
			}
			d.Tags = append(d.Tags, in.Etiquetas...)
			d.Origin = "asistente"
			if d.empty() {
				return nil, journalOut{}, bad("Di qué apuntar: ánimo, energía, sueño, una nota o etiquetas.")
			}
			if err := svc.saveEntry(ctx, who.User, day, d); err != nil {
				return nil, journalOut{}, err
			}
			saved, _, err := svc.store.entry(ctx, who.User, day)
			if err != nil {
				return nil, journalOut{}, err
			}
			return nil, entryOut(day, saved.entryData), nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "diario_ver", Description: "El diario entre dos fechas (como mucho 93 días): ánimo, energía, sueño y etiquetas de cada día, con las medias. La nota de cada día es muy personal: pide incluir_notas solo si la persona quiere saber lo que escribió esos días"},
		func(ctx context.Context, req *mcp.CallToolRequest, in viewIn) (*mcp.CallToolResult, viewOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, viewOut{}, err
			}
			today := svc.today()
			if strings.TrimSpace(in.Desde) == "" {
				return nil, viewOut{}, bad("Di desde qué fecha (AAAA-MM-DD).")
			}
			from, err := parseWhen(in.Desde, today)
			if err != nil {
				return nil, viewOut{}, err
			}
			to := from
			if strings.TrimSpace(in.Hasta) != "" {
				if to, err = parseWhen(in.Hasta, today); err != nil {
					return nil, viewOut{}, err
				}
			}
			if to.Before(from) {
				from, to = to, from
			}
			if to.Sub(from) >= maxViewDays*dayUnit {
				return nil, viewOut{}, bad("Como mucho 93 días de una vez.")
			}
			list, err := svc.store.entries(ctx, who.User, from, to)
			if err != nil {
				return nil, viewOut{}, err
			}
			out := viewOut{Desde: isoDay(from), Hasta: isoDay(to), Dias: []dayOut{}}
			var mood, energy, sleep avg
			for _, e := range list {
				o := dayOut{Fecha: isoDay(e.Day), Animo: e.Mood, Energia: e.Energy, Sueno: e.Sleep, Etiquetas: e.Tags, TieneNota: e.Note != ""}
				if in.IncluirNotas {
					o.Nota = e.Note
				}
				if e.Mood > 0 {
					mood.add(float64(e.Mood))
				}
				if e.Energy > 0 {
					energy.add(float64(e.Energy))
				}
				if e.Sleep != nil {
					sleep.add(*e.Sleep)
				}
				out.Dias = append(out.Dias, o)
			}
			out.AnimoMedio, out.EnergiaMedia, out.SuenoMedio = avgPtr(mood), avgPtr(energy), avgPtr(sleep)
			return nil, out, nil
		})
}
