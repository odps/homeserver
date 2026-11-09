package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"homeserver/platform/app"
)

// Tools para el asistente: la agenda, las tareas, los contactos y el correo
// de la persona del token. Las fechas van en hora de Madrid.

func identity(req *mcp.CallToolRequest) (app.Identity, error) {
	if req.Extra != nil {
		if who := app.UserFrom(req.Extra.Header); who.IsUser() {
			return who, nil
		}
	}
	return app.Identity{}, errors.New("este token no está asociado a ninguna persona: créalo «en nombre de» un usuario en el panel")
}

// parseWhen admite «2026-11-05», «2026-11-05 17:00», «2026-11-05T17:00» y RFC 3339.
func parseWhen(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(madrid), false, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, madrid); err == nil {
			return t, false, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, madrid); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("fecha %q no válida: usa AAAA-MM-DD o AAAA-MM-DD HH:MM", s)
}

func ptr[T any](v T) *T { return &v }

type rangeIn struct {
	Desde      string `json:"desde,omitempty" jsonschema:"primer día (AAAA-MM-DD); por defecto, hoy"`
	Hasta      string `json:"hasta,omitempty" jsonschema:"último día incluido (AAAA-MM-DD); por defecto, 7 días después de desde"`
	SinRutinas bool   `json:"sin_rutinas,omitempty" jsonschema:"true para no incluir las rutinas (trabajo, club…), que salen marcadas con rutina"`
	ConPasado  bool   `json:"con_pasado,omitempty" jsonschema:"true para incluir también lo que ya ha terminado hoy (por defecto, desde ahora)"`
}

type entriesOut struct {
	Ahora     string  `json:"ahora,omitempty"` // fecha y hora a la que se ha mirado
	Elementos []entry `json:"elementos"`
}

type eventIn struct {
	Titulo     string `json:"titulo" jsonschema:"título del evento"`
	Inicio     string `json:"inicio" jsonschema:"comienzo: AAAA-MM-DD HH:MM, o AAAA-MM-DD para todo el día"`
	Fin        string `json:"fin,omitempty" jsonschema:"fin (AAAA-MM-DD HH:MM); si falta, una hora después (o el mismo día si es de día completo)"`
	Lugar      string `json:"lugar,omitempty" jsonschema:"dirección o sitio; se enlaza a Google Maps"`
	Notas      string `json:"notas,omitempty"`
	Repetir    string `json:"repetir,omitempty" jsonschema:"regla RRULE si se repite, p. ej. FREQ=WEEKLY;BYDAY=MO o FREQ=MONTHLY;COUNT=6"`
	AvisoMin   *int   `json:"aviso_min,omitempty" jsonschema:"minutos de antelación del aviso; 0 sin aviso; si falta, el de los ajustes"`
	Calendario string `json:"calendario,omitempty" jsonschema:"nombre del calendario; por defecto, el principal"`
}

type changeIn struct {
	ID       int64   `json:"id" jsonschema:"id del evento o tarea"`
	Titulo   *string `json:"titulo,omitempty"`
	Inicio   *string `json:"inicio,omitempty" jsonschema:"nuevo comienzo (eventos) o vencimiento (tareas): AAAA-MM-DD HH:MM o AAAA-MM-DD"`
	Fin      *string `json:"fin,omitempty"`
	Lugar    *string `json:"lugar,omitempty" jsonschema:"vacío para quitarlo"`
	Notas    *string `json:"notas,omitempty" jsonschema:"vacío para quitarlas"`
	Repetir  *string `json:"repetir,omitempty" jsonschema:"RRULE nueva, o vacío para que deje de repetirse"`
	AvisoMin *int    `json:"aviso_min,omitempty" jsonschema:"minutos de antelación; 0 quita el aviso"`
}

type idIn struct {
	ID int64 `json:"id"`
}

type taskIn struct {
	Titulo   string `json:"titulo"`
	Vence    string `json:"vence,omitempty" jsonschema:"AAAA-MM-DD HH:MM (avisa a esa hora) o AAAA-MM-DD; vacío si no vence"`
	Notas    string `json:"notas,omitempty"`
	AvisoMin *int   `json:"aviso_min,omitempty" jsonschema:"minutos antes del vencimiento para avisar; por defecto avisa a la hora exacta si tiene hora"`
}

type doneIn struct {
	ID    int64 `json:"id"`
	Hecha *bool `json:"hecha,omitempty" jsonschema:"false para reabrirla; por defecto true"`
}

type contactsIn struct {
	Texto string `json:"texto,omitempty" jsonschema:"nombre, empresa, correo, teléfono o parte de la dirección; vacío lista los primeros"`
}

type contactsOut struct {
	Contactos []contact `json:"contactos"`
}

type contactIn struct {
	ID          int64    `json:"id,omitempty" jsonschema:"id para cambiar uno existente; 0 o ausente para crear"`
	Nombre      *string  `json:"nombre,omitempty"`
	Empresa     *string  `json:"empresa,omitempty"`
	Cargo       *string  `json:"cargo,omitempty"`
	Correos     []string `json:"correos,omitempty" jsonschema:"lista completa de correos (sustituye a la anterior)"`
	Telefonos   []string `json:"telefonos,omitempty" jsonschema:"lista completa de teléfonos (sustituye a la anterior)"`
	Direcciones []string `json:"direcciones,omitempty" jsonschema:"lista completa de direcciones postales (sustituye a la anterior)"`
	Cumpleanos  *string  `json:"cumpleanos,omitempty" jsonschema:"AAAA-MM-DD, o --MM-DD sin año"`
	Notas       *string  `json:"notas,omitempty"`
}

type mailSearchIn struct {
	Texto      string   `json:"texto,omitempty" jsonschema:"palabras en el remitente, asunto o texto"`
	De         string   `json:"de,omitempty" jsonschema:"nombre o dirección del remitente"`
	NoLeidos   bool     `json:"no_leidos,omitempty"`
	Dias       int      `json:"dias,omitempty" jsonschema:"solo los de los últimos N días"`
	Categorias []string `json:"categorias,omitempty" jsonschema:"personal, importante, notificacion o comercial. Por defecto personal e importante; las otras, solo si la persona lo pide expresamente"`
}

type mailListOut struct {
	Correos []message      `json:"correos"`
	Ocultos map[string]int `json:"ocultos,omitempty" jsonschema:"cuántos de las otras categorías encajan con la búsqueda y no se han devuelto"`
	Cuentas []string       `json:"cuentas"`
}

type draftIn struct {
	Para       string `json:"para,omitempty" jsonschema:"destinatarios separados por comas (Nombre <dir@x.com> o dir@x.com); en una respuesta, por defecto el remitente"`
	Cc         string `json:"cc,omitempty"`
	Asunto     string `json:"asunto,omitempty" jsonschema:"en una respuesta, por defecto Re: y el asunto original"`
	Texto      string `json:"texto" jsonschema:"cuerpo del correo en texto plano, con la firma si procede"`
	ResponderA int64  `json:"responder_a,omitempty" jsonschema:"id del correo al que responde (mantiene el hilo)"`
	Cuenta     string `json:"cuenta,omitempty" jsonschema:"dirección de la cuenta desde la que se escribe; por defecto la del correo respondido o la primera"`
}

type draftOut struct {
	Cuenta  string `json:"cuenta"`
	Carpeta string `json:"carpeta"`
	Aviso   string `json:"aviso"`
}

type routeIn struct {
	Destino string `json:"destino" jsonschema:"dirección o sitio"`
	Origen  string `json:"origen,omitempty" jsonschema:"por defecto, la casa de los ajustes o la ubicación actual"`
}

type linksOut struct {
	Mapa string `json:"mapa"`
	Ruta string `json:"ruta"`
}

type summaryOut struct {
	Resumen string `json:"resumen"`
}

func addTools(server *mcp.Server, svc *service) {
	addMoreTools(server, svc)
	addShipmentTools(server, svc)
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_ver", Description: "Eventos (cada repetición por separado) y tareas con vencimiento entre dos días, por orden, con lugar, enlace a Maps y avisos. Por defecto empieza ahora: lo que ya ha terminado hoy no sale (lo que está en curso sí), y se añaden las tareas vencidas sin hacer"},
		func(ctx context.Context, req *mcp.CallToolRequest, in rangeIn) (*mcp.CallToolResult, entriesOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entriesOut{}, err
			}
			from := dayStart(svc.now())
			if in.Desde != "" {
				t, _, err := parseWhen(in.Desde)
				if err != nil {
					return nil, entriesOut{}, err
				}
				from = dayStart(t)
			}
			to := from.AddDate(0, 0, 7)
			if in.Hasta != "" {
				t, _, err := parseWhen(in.Hasta)
				if err != nil {
					return nil, entriesOut{}, err
				}
				to = dayStart(t).AddDate(0, 0, 1)
			}
			if !to.After(from) || to.Sub(from) > 400*24*time.Hour {
				return nil, entriesOut{}, errors.New("el intervalo debe ir hacia delante y durar menos de un año")
			}
			now := svc.now()
			l, err := svc.remaining(ctx, who.User, from, to, in.ConPasado)
			if in.SinRutinas {
				l = slices.DeleteFunc(l, func(e entry) bool { return e.Routine })
			}
			return nil, entriesOut{Ahora: now.In(madrid).Format("2006-01-02 15:04"), Elementos: orEmpty(l)}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_buscar", Description: "Busca eventos y tareas (pasados o futuros) por palabras del título, lugar o notas"},
		func(ctx context.Context, req *mcp.CallToolRequest, in contactsIn) (*mcp.CallToolResult, entriesOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entriesOut{}, err
			}
			l, err := svc.searchEntries(ctx, who.User, in.Texto)
			return nil, entriesOut{Elementos: orEmpty(l)}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_crear_evento", Description: "Crea un evento en el calendario (se sincroniza con el móvil). Devuelve el evento creado"},
		func(ctx context.Context, req *mcp.CallToolRequest, in eventIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			start, allDay, err := parseWhen(in.Inicio)
			if err != nil {
				return nil, entry{}, err
			}
			ev := eventInput{Title: &in.Titulo, Start: &start, AllDay: &allDay, Location: &in.Lugar, Notes: &in.Notas, Repeat: &in.Repetir}
			if in.Fin != "" {
				end, _, err := parseWhen(in.Fin)
				if err != nil {
					return nil, entry{}, err
				}
				if allDay {
					end = end.AddDate(0, 0, 1) // «hasta el día X» incluye ese día
				}
				ev.End = &end
			}
			if in.AvisoMin != nil {
				ev.Alarms = minutes(*in.AvisoMin)
			}
			e, err := svc.create(ctx, who.User, in.Calendario, ical.CompEvent, ev)
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_modificar", Description: "Cambia un evento o una tarea por su id; solo se tocan los campos que se indiquen"},
		func(ctx context.Context, req *mcp.CallToolRequest, in changeIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			cur, err := svc.entry(ctx, who.User, in.ID)
			if err != nil {
				return nil, entry{}, err
			}
			ev := eventInput{Title: in.Titulo, Location: in.Lugar, Notes: in.Notas, Repeat: in.Repetir}
			if in.Inicio != nil {
				t, allDay, err := parseWhen(*in.Inicio)
				if err != nil {
					return nil, entry{}, err
				}
				if cur.Kind == "tarea" {
					ev.Due, ev.AllDay = &t, &allDay
				} else {
					ev.Start, ev.AllDay = &t, &allDay
					if in.Fin == nil && cur.Start != nil && cur.End != nil {
						end := t.Add(cur.End.Sub(*cur.Start)) // mover sin cambiar la duración
						ev.End = &end
					}
				}
			}
			if in.Fin != nil {
				t, allDay, err := parseWhen(*in.Fin)
				if err != nil {
					return nil, entry{}, err
				}
				if allDay {
					t = t.AddDate(0, 0, 1)
				}
				ev.End = &t
			}
			if in.AvisoMin != nil {
				ev.Alarms = minutes(*in.AvisoMin)
			}
			e, err := svc.update(ctx, who.User, in.ID, ev)
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_borrar", Description: "Borra un evento (con todas sus repeticiones) o una tarea por su id. Confirma antes con la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			e, err := svc.remove(ctx, who.User, in.ID)
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "tareas_ver", Description: "Tareas pendientes (las vencidas, marcadas), con su id"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, entriesOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entriesOut{}, err
			}
			l, err := svc.tasks(ctx, who.User)
			return nil, entriesOut{Elementos: orEmpty(l)}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "tareas_crear", Description: "Crea una tarea o recordatorio («recuérdame…»); con hora de vencimiento, avisa por Telegram"},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			ev := eventInput{Title: &in.Titulo, Notes: &in.Notas}
			if in.Vence != "" {
				t, allDay, err := parseWhen(in.Vence)
				if err != nil {
					return nil, entry{}, err
				}
				ev.Due, ev.AllDay = &t, &allDay
				switch {
				case in.AvisoMin != nil:
					ev.Alarms = minutes(*in.AvisoMin)
				case !allDay:
					ev.Alarms = &[]int{0}
				}
			}
			e, err := svc.create(ctx, who.User, "", ical.CompToDo, ev)
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "tareas_completar", Description: "Marca una tarea como hecha (o la reabre)"},
		func(ctx context.Context, req *mcp.CallToolRequest, in doneIn) (*mcp.CallToolResult, entry, error) {
			who, err := identity(req)
			if err != nil {
				return nil, entry{}, err
			}
			done := in.Hecha == nil || *in.Hecha
			e, err := svc.update(ctx, who.User, in.ID, eventInput{Done: &done})
			return nil, e, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "contactos_buscar", Description: "Busca contactos por nombre, empresa, correo, teléfono o dirección; devuelve sus datos y enlaces a Maps de sus direcciones"},
		func(ctx context.Context, req *mcp.CallToolRequest, in contactsIn) (*mcp.CallToolResult, contactsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, contactsOut{}, err
			}
			l, err := svc.contacts(ctx, who.User, in.Texto, 20)
			return nil, contactsOut{Contactos: l}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "contactos_guardar", Description: "Crea un contacto o cambia uno (con id). Las listas sustituyen a las anteriores: para añadir un teléfono, manda también los que ya tenía"},
		func(ctx context.Context, req *mcp.CallToolRequest, in contactIn) (*mcp.CallToolResult, contact, error) {
			who, err := identity(req)
			if err != nil {
				return nil, contact{}, err
			}
			c, err := svc.saveContact(ctx, who.User, in.ID, contactInput{Name: in.Nombre, Org: in.Empresa, Role: in.Cargo,
				Emails: in.Correos, Phones: in.Telefonos, Addresses: in.Direcciones, Birthday: in.Cumpleanos, Notes: in.Notas})
			return nil, c, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "contactos_borrar", Description: "Borra un contacto por su id. Confirma antes con la persona"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, contact, error) {
			who, err := identity(req)
			if err != nil {
				return nil, contact{}, err
			}
			c, err := svc.removeContact(ctx, who.User, in.ID)
			return nil, c, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "correo_buscar", Description: "Busca en el correo recibido (bandeja de entrada de los últimos meses), clasificado sin IA por sus metadatos: personal, importante (seguridad, condiciones, pagos, citas, trámites aunque vengan de listas), notificacion y comercial. Por defecto solo devuelve personal e importante y cuenta los demás en «ocultos»; pide notificacion o comercial solo si la persona lo pide expresamente. Busca palabras enteras o por su comienzo (desde 3 letras). Devuelve id, remitente, asunto, fecha, categoría, si está leído y el principio del texto"},
		func(ctx context.Context, req *mcp.CallToolRequest, in mailSearchIn) (*mcp.CallToolResult, mailListOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, mailListOut{}, err
			}
			q := mailQuery{Text: in.Texto, From: in.De, UnreadOnly: in.NoLeidos, Limit: 25, Categories: []string{catPersonal, catImportant}}
			if len(in.Categorias) > 0 {
				q.Categories = nil
				for _, c := range in.Categorias {
					if !slices.Contains(allCategories, c) {
						return nil, mailListOut{}, fmt.Errorf("categoría desconocida %q: usa %s", c, strings.Join(allCategories, ", "))
					}
					q.Categories = append(q.Categories, c)
				}
			}
			if in.Dias > 0 {
				q.Since = svc.now().AddDate(0, 0, -in.Dias)
			}
			l, err := svc.store.searchMessages(ctx, who.User, q)
			if err != nil {
				return nil, mailListOut{}, err
			}
			counts, _ := svc.store.countMessages(ctx, who.User, q)
			for i := range l {
				l[i].Body = truncate(l[i].Body, 300)
			}
			accts, _ := svc.store.accounts(ctx, who.User)
			out := mailListOut{Correos: l, Cuentas: []string{}}
			if out.Correos == nil {
				out.Correos = []message{}
			}
			for c, n := range counts {
				if !slices.Contains(q.Categories, c) {
					if out.Ocultos == nil {
						out.Ocultos = map[string]int{}
					}
					out.Ocultos[c] = n
				}
			}
			for _, a := range accts {
				out.Cuentas = append(out.Cuentas, a.Address)
			}
			return nil, out, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "correo_leer", Description: "El correo completo (texto, destinatarios y adjuntos) por su id"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, message, error) {
			who, err := identity(req)
			if err != nil {
				return nil, message{}, err
			}
			m, err := svc.store.messageByID(ctx, who.User, in.ID)
			return nil, m, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "correo_borrador", Description: "Deja un borrador en la carpeta Borradores del correo para que la persona lo revise y lo envíe. Nunca se envía nada"},
		func(ctx context.Context, req *mcp.CallToolRequest, in draftIn) (*mcp.CallToolResult, draftOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, draftOut{}, err
			}
			d := draftInput{FromName: who.Name, To: in.Para, Cc: in.Cc, Subject: in.Asunto, Body: in.Texto}
			account := in.Cuenta
			if in.ResponderA != 0 {
				m, err := svc.store.messageByID(ctx, who.User, in.ResponderA)
				if err != nil {
					return nil, draftOut{}, err
				}
				d.ReplyTo = &m
				if d.To == "" {
					d.To = m.FromAddr
					if m.FromName != "" {
						d.To = fmt.Sprintf("%q <%s>", m.FromName, m.FromAddr)
					}
				}
				if account == "" {
					account = m.Account
				}
			}
			if strings.TrimSpace(d.To) == "" || strings.TrimSpace(d.Body) == "" {
				return nil, draftOut{}, errors.New("faltan el destinatario o el texto")
			}
			a, err := svc.pickAccount(ctx, who.User, account)
			if err != nil {
				return nil, draftOut{}, err
			}
			folder, err := svc.mail.saveDraft(ctx, a, d)
			if err != nil {
				return nil, draftOut{}, err
			}
			return nil, draftOut{Cuenta: a.Address, Carpeta: folder, Aviso: "Borrador guardado; no se ha enviado."}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "mapa_ruta", Description: "Enlaces de Google Maps para ver un sitio y para ir hasta él"},
		func(ctx context.Context, req *mcp.CallToolRequest, in routeIn) (*mcp.CallToolResult, linksOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, linksOut{}, err
			}
			if strings.TrimSpace(in.Destino) == "" {
				return nil, linksOut{}, errors.New("falta el destino")
			}
			origin := in.Origen
			if origin == "" {
				p, _ := svc.store.prefs(ctx, who.User)
				origin = p.Home
			}
			return nil, linksOut{Mapa: mapsLink(in.Destino), Ruta: routeLink(origin, in.Destino)}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "agenda_resumen", Description: "Lo que le queda del día de hoy (los eventos ya terminados no salen, los que están en curso sí), con tareas, cumpleaños, correo sin leer y lo de las otras apps. Úsala para «¿qué tengo hoy?»"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, summaryOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, summaryOut{}, err
			}
			text, err := svc.buildSummary(ctx, who.User, svc.now(), false, true)
			return nil, summaryOut{Resumen: text}, err
		})
}

// minutes es la lista de avisos de aviso_min (0 o menos: sin aviso).
func minutes(n int) *[]int {
	if n <= 0 {
		return &[]int{}
	}
	return &[]int{n}
}

func orEmpty(l []entry) []entry {
	if l == nil {
		return []entry{}
	}
	return l
}

// pickAccount elige la cuenta de correo por su dirección (o la primera).
func (s *service) pickAccount(ctx context.Context, owner, address string) (mailAccount, error) {
	list, err := s.store.accounts(ctx, owner)
	if err != nil {
		return mailAccount{}, err
	}
	if len(list) == 0 {
		return mailAccount{}, errors.New("no hay ninguna cuenta de correo conectada (Agenda → Ajustes)")
	}
	for _, a := range list {
		if address == "" || strings.EqualFold(a.Address, strings.TrimSpace(address)) {
			return a, nil
		}
	}
	return mailAccount{}, fmt.Errorf("no hay ninguna cuenta %s", address)
}
