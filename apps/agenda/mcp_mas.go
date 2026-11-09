package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools de rutinas, huecos libres y ajustes de la agenda.

type routineIn struct {
	Titulo   string   `json:"titulo" jsonschema:"p. ej. Trabajo, Entreno del club, Prácticas"`
	Dias     []string `json:"dias" jsonschema:"días de la semana: lunes, martes… o laborables, fin de semana, todos"`
	Inicio   string   `json:"inicio" jsonschema:"hora de comienzo HH:MM"`
	Fin      string   `json:"fin" jsonschema:"hora de fin HH:MM (si es anterior al comienzo, acaba al día siguiente)"`
	Desde    string   `json:"desde,omitempty" jsonschema:"primer día AAAA-MM-DD; por defecto, hoy"`
	Hasta    string   `json:"hasta,omitempty" jsonschema:"último día AAAA-MM-DD (fin de las prácticas, de la temporada…); vacío si no acaba"`
	Lugar    string   `json:"lugar,omitempty"`
	Notas    string   `json:"notas,omitempty"`
	AvisoMin int      `json:"aviso_min,omitempty" jsonschema:"minutos de antelación del aviso; por defecto, sin aviso"`
}

type skipIn struct {
	ID  int64  `json:"id" jsonschema:"id del evento que se repite o de la rutina"`
	Dia string `json:"dia" jsonschema:"día que no toca, AAAA-MM-DD"`
}

type slotsIn struct {
	Desde      string `json:"desde,omitempty" jsonschema:"primer día AAAA-MM-DD; por defecto, hoy"`
	Hasta      string `json:"hasta,omitempty" jsonschema:"último día AAAA-MM-DD; por defecto, el mismo que desde"`
	Franja     string `json:"franja,omitempty" jsonschema:"horas del día en que buscar, HH:MM-HH:MM; por defecto 08:00-22:00"`
	MinMinutos int    `json:"min_minutos,omitempty" jsonschema:"duración mínima del hueco; por defecto 30"`
	SinRutinas bool   `json:"sin_rutinas,omitempty" jsonschema:"true para no contar las rutinas como ocupado"`
}

type slotsOut struct {
	Huecos []slot `json:"huecos"`
}

// settingsView son los ajustes que el asistente puede ver (sin secretos).
type settingsView struct {
	ResumenManana   bool            `json:"resumen_manana"`
	ResumenHora     string          `json:"resumen_hora"`
	ResumenConIA    bool            `json:"resumen_con_ia"`
	ResumenCartera  bool            `json:"resumen_con_cartera"`
	ResumenApps     map[string]bool `json:"resumen_secciones_apps"`
	Avisos          bool            `json:"avisos"`
	AvisoPorDefecto int             `json:"aviso_por_defecto_min"`
	Casa            string          `json:"casa"`
	CiudadTiempo    string          `json:"ciudad_tiempo"`
	Importantes     []string        `json:"remitentes_importantes"`
	Ignorados       []string        `json:"remitentes_ignorados"`
	Cuentas         []string        `json:"cuentas_correo"`
	Calendarios     []string        `json:"calendarios"`
}

type settingsIn struct {
	ResumenManana   *bool           `json:"resumen_manana,omitempty" jsonschema:"resumen de la mañana por Telegram"`
	ResumenHora     *string         `json:"resumen_hora,omitempty" jsonschema:"HH:MM"`
	ResumenConIA    *bool           `json:"resumen_con_ia,omitempty" jsonschema:"incluir lo importante del correo resumido por la IA"`
	ResumenCartera  *bool           `json:"resumen_con_cartera,omitempty" jsonschema:"incluir el precio de los activos que sigue en la app Cartera"`
	ResumenApps     map[string]bool `json:"resumen_secciones_apps,omitempty" jsonschema:"secciones de otras apps en el resumen (habitos, gastos, casa, coche, series, leer): true para ponerla, false para quitarla; solo las que cambian"`
	Avisos          *bool           `json:"avisos,omitempty" jsonschema:"avisos de eventos y tareas por Telegram"`
	AvisoPorDefecto *int            `json:"aviso_por_defecto_min,omitempty" jsonschema:"minutos de antelación al crear un evento; 0 sin aviso"`
	Casa            *string         `json:"casa,omitempty" jsonschema:"dirección de casa, origen de las rutas; vacío para quitarla"`
	CiudadTiempo    *string         `json:"ciudad_tiempo,omitempty" jsonschema:"ciudad del tiempo en el resumen de la mañana; vacío: la de la dirección de casa"`
	Importantes     *[]string       `json:"remitentes_importantes,omitempty" jsonschema:"lista COMPLETA de direcciones o dominios siempre importantes (sustituye a la actual)"`
	Ignorados       *[]string       `json:"remitentes_ignorados,omitempty" jsonschema:"lista COMPLETA de direcciones o dominios a ignorar (sustituye a la actual)"`
}

type senderRuleIn struct {
	Direccion string `json:"direccion" jsonschema:"dirección de correo del remitente"`
	Como      string `json:"como" jsonschema:"importante, ignorar o automatico (lo quita de las listas)"`
}

func dayArg(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return dayStart(def), nil
	}
	t, _, err := parseWhen(s)
	return dayStart(t), err
}

func addMoreTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "rutinas_ver", Description: "Rutinas de la persona: franjas que se repiten cada semana (trabajo, club, prácticas…), con días, horas, lugar y vigencia"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, entriesOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entriesOut{}, err
			}
			l, err := svc.routines(ctx, who.User)
			return nil, entriesOut{Elementos: orEmpty(l)}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "rutinas_crear", Description: "Crea una rutina: una franja que se repite cada semana (trabajo, entrenamientos, prácticas…). Va al calendario «Rutinas», se sincroniza con el móvil y cuenta como ocupado al buscar huecos. Para cambiarla usa agenda_modificar; para quitar un día suelto, agenda_saltar; para borrarla, agenda_borrar"},
		func(ctx context.Context, req *mcp.CallToolRequest, in routineIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			r := routineInput{Title: in.Titulo, Location: in.Lugar, Notes: in.Notas, Days: in.Dias, Start: in.Inicio, End: in.Fin, AlarmMin: in.AvisoMin}
			if r.From, err = dayArg(in.Desde, svc.now()); err != nil {
				return nil, entry{}, err
			}
			if in.Hasta != "" {
				until, err := dayArg(in.Hasta, svc.now())
				if err != nil {
					return nil, entry{}, err
				}
				r.Until = &until
			}
			e, err := svc.createRoutine(ctx, who.User, r)
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_saltar", Description: "Quita un solo día de un evento que se repite o de una rutina (p. ej. «este viernes no hay entreno»); el resto de la serie sigue igual"},
		func(ctx context.Context, req *mcp.CallToolRequest, in skipIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			day, err := dayArg(in.Dia, svc.now())
			if err != nil || in.Dia == "" {
				return nil, entry{}, errors.New("falta el día (AAAA-MM-DD)")
			}
			e, err := svc.skip(ctx, who.User, in.ID, day)
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_huecos", Description: "Huecos libres entre eventos y rutinas, por días, para proponer cuándo hacer algo o quedar. Los eventos de todo el día y las tareas no ocupan"},
		func(ctx context.Context, req *mcp.CallToolRequest, in slotsIn) (*mcp.CallToolResult, slotsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, slotsOut{}, err
			}
			from, err := dayArg(in.Desde, svc.now())
			if err != nil {
				return nil, slotsOut{}, err
			}
			to, err := dayArg(in.Hasta, from)
			if err != nil {
				return nil, slotsOut{}, err
			}
			a, b := "08:00", "22:00"
			if in.Franja != "" {
				if _, err := fmt.Sscanf(in.Franja, "%5s-%5s", &a, &b); err != nil {
					return nil, slotsOut{}, errors.New("franja no válida: usa HH:MM-HH:MM")
				}
			}
			minDur := 30 * time.Minute
			if in.MinMinutos > 0 {
				minDur = time.Duration(in.MinMinutos) * time.Minute
			}
			l, err := svc.freeSlots(ctx, who.User, from, to, a, b, minDur, !in.SinRutinas)
			return nil, slotsOut{Huecos: l}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_ajustes_ver", Description: "Ajustes de la agenda de la persona: resumen de la mañana, avisos, dirección de casa, remitentes importantes o ignorados del correo, cuentas y calendarios"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, settingsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, settingsView{}, err
			}
			v, err := svc.settingsView(ctx, who.User)
			return nil, v, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_ajustes_cambiar", Description: "Cambia ajustes de la agenda. Pasa solo lo que la persona haya pedido cambiar expresamente; lo demás queda igual. Las listas de remitentes sustituyen a las actuales (para añadir o quitar uno, mejor correo_remitente). Las cuentas de correo se conectan en la web, nunca por aquí"},
		func(ctx context.Context, req *mcp.CallToolRequest, in settingsIn) (*mcp.CallToolResult, settingsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, settingsView{}, err
			}
			if err := svc.changeSettings(ctx, who.User, in); err != nil {
				return nil, settingsView{}, err
			}
			v, err := svc.settingsView(ctx, who.User)
			return nil, v, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "correo_remitente", Description: "Marca un remitente como siempre importante, a ignorar (no se lee con IA ni sale en el resumen), o lo devuelve a la clasificación automática. Reclasifica el correo ya guardado"},
		func(ctx context.Context, req *mcp.CallToolRequest, in senderRuleIn) (*mcp.CallToolResult, settingsView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, settingsView{}, err
			}
			como := in.Como
			if como == "automatico" || como == "automático" {
				como = ""
			}
			if err := svc.markSender(ctx, who.User, in.Direccion, como); err != nil {
				return nil, settingsView{}, err
			}
			v, err := svc.settingsView(ctx, who.User)
			return nil, v, err
		})
}

func (s *service) settingsView(ctx context.Context, owner string) (settingsView, error) {
	p, err := s.store.prefs(ctx, owner)
	if err != nil {
		return settingsView{}, err
	}
	v := settingsView{ResumenManana: p.SummaryOn, ResumenHora: p.SummaryAt, ResumenConIA: p.SummaryAI, ResumenCartera: !p.NoWallet, Avisos: p.RemindersOn,
		AvisoPorDefecto: p.DefaultMin, Casa: p.Home, CiudadTiempo: p.weatherCity(), Importantes: orNoStrings(p.MailImportant), Ignorados: orNoStrings(p.MailIgnore),
		Cuentas: []string{}, Calendarios: []string{}, ResumenApps: map[string]bool{}}
	for _, e := range s.extras {
		v.ResumenApps[e.Name] = p.extraOn(e.Name)
	}
	accts, _ := s.store.accounts(ctx, owner)
	for _, a := range accts {
		state := "al día"
		if a.Error != "" {
			state = "error: " + a.Error
		}
		v.Cuentas = append(v.Cuentas, a.Address+" ("+state+")")
	}
	cals, _ := s.store.collections(ctx, owner, "cal")
	for _, c := range cals {
		v.Calendarios = append(v.Calendarios, c.Name)
	}
	return v, nil
}

func orNoStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// changeSettings aplica solo lo que venga en in, con las mismas reglas que la
// web.
func (s *service) changeSettings(ctx context.Context, owner string, in settingsIn) error {
	p, err := s.store.prefs(ctx, owner)
	if err != nil {
		return err
	}
	if in.ResumenHora != nil {
		if _, err := time.Parse("15:04", *in.ResumenHora); err != nil {
			return errors.New("hora del resumen no válida: usa HH:MM")
		}
		p.SummaryAt = *in.ResumenHora
	}
	if in.AvisoPorDefecto != nil {
		if *in.AvisoPorDefecto < 0 || *in.AvisoPorDefecto > 7*24*60 {
			return errors.New("el aviso por defecto va de 0 a 10080 minutos")
		}
		p.DefaultMin = *in.AvisoPorDefecto
	}
	for dst, src := range map[*bool]*bool{&p.SummaryOn: in.ResumenManana, &p.SummaryAI: in.ResumenConIA, &p.RemindersOn: in.Avisos} {
		if src != nil {
			*dst = *src
		}
	}
	if in.ResumenCartera != nil {
		p.NoWallet = !*in.ResumenCartera
	}
	for name, on := range in.ResumenApps {
		if !slices.ContainsFunc(s.extras, func(e extra) bool { return e.Name == name }) {
			return errors.New("sección del resumen desconocida: " + name)
		}
		p.setExtra(name, on)
	}
	if in.CiudadTiempo != nil {
		p.WeatherCity = strings.TrimSpace(*in.CiudadTiempo)
	}
	if in.Casa != nil {
		p.Home = *in.Casa
	}
	if err := s.store.savePrefs(ctx, owner, p); err != nil {
		return err
	}
	if in.Importantes != nil || in.Ignorados != nil {
		imp, ign := p.MailImportant, p.MailIgnore
		if in.Importantes != nil {
			imp = senderList(strings.Join(*in.Importantes, "\n"))
		}
		if in.Ignorados != nil {
			ign = senderList(strings.Join(*in.Ignorados, "\n"))
		}
		return s.setMailRules(ctx, owner, imp, ign)
	}
	return nil
}
