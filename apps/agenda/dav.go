package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"homeserver/platform/app"
)

// CalDAV y CardDAV para que el calendario, las tareas y los contactos se
// sincronicen con el móvil (DAVx5 en Android, o el propio iPhone). Rutas:
//
//	<base>/dav/                                  raíz
//	<base>/dav/<usuario>/                        la cuenta (principal)
//	<base>/dav/<usuario>/calendarios/<slug>/     calendario (eventos y tareas)
//	<base>/dav/<usuario>/contactos/<slug>/       libreta de contactos
//
// La pasarela autentica (Basic con el usuario y un token de la app) y pone
// las cabeceras de identidad; aquí solo se comprueba que cada cual vea lo suyo.

const (
	calHome  = "calendarios"
	cardHome = "contactos"
)

type davKey struct{}

type davCtx struct {
	user   string
	prefix string // ruta pública de la raíz DAV, sin barra final
}

func davFrom(ctx context.Context) davCtx { return ctx.Value(davKey{}).(davCtx) }

func (d davCtx) principal() string       { return d.prefix + "/" + d.user + "/" }
func (d davCtx) home(kind string) string { return d.principal() + kind + "/" }

type davServer struct {
	store store
	now   func() time.Time
}

func newDAV(s store) *davServer {
	return &davServer{store: s, now: time.Now}
}

func (d *davServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	who := app.User(r)
	if !who.IsUser() {
		http.Error(w, "entra con tu usuario y un token de la app", http.StatusUnauthorized)
		return
	}
	if err := d.store.ensureUser(r.Context(), who.User); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Se trabaja con las rutas públicas (con el prefijo de la pasarela): son
	// las que el cliente ve en las respuestas.
	base := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
	r.URL.Path = base + r.URL.Path
	r.URL.RawPath = ""
	dc := davCtx{user: who.User, prefix: base + "/dav"}
	rest := strings.Trim(strings.TrimPrefix(path.Clean(r.URL.Path), dc.prefix), "/")
	parts := strings.Split(rest, "/")
	if rest != "" && parts[0] != who.User {
		http.Error(w, "no es tu cuenta", http.StatusForbidden)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), davKey{}, dc))
	cal := &caldav.Handler{Backend: calBackend{d}, Prefix: dc.prefix}
	card := &carddav.Handler{Backend: cardBackend{d}, Prefix: dc.prefix}
	switch {
	case len(parts) >= 2 && parts[1] == calHome:
		cal.ServeHTTP(w, r)
	case len(parts) >= 2 && parts[1] == cardHome:
		card.ServeHTTP(w, r)
	case len(parts) >= 2:
		http.NotFound(w, r)
	case r.Method == http.MethodOptions:
		// La raíz y la cuenta sirven a los dos protocolos.
		w.Header().Set("DAV", "1, 3, calendar-access, addressbook")
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT")
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "PROPFIND":
		// Cada cliente pregunta por su carpeta (calendarios o contactos):
		// lo que pide en el cuerpo dice a qué protocolo va.
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte("urn:ietf:params:xml:ns:carddav")) {
			card.ServeHTTP(w, r)
		} else {
			cal.ServeHTTP(w, r)
		}
	default:
		cal.ServeHTTP(w, r)
	}
}

var slugRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)

// split separa la ruta de un objeto en colección y fichero.
func split(ctx context.Context, kind, p string) (slug, name string, err error) {
	dc := davFrom(ctx)
	rest, ok := strings.CutPrefix(p, dc.home(kind))
	if !ok {
		return "", "", webdav.NewHTTPError(http.StatusNotFound, errors.New("ruta fuera de tu cuenta"))
	}
	slug, name, _ = strings.Cut(strings.Trim(rest, "/"), "/")
	if !slugRe.MatchString(slug) || (name != "" && (!slugRe.MatchString(name) || strings.Contains(name, "/"))) {
		return "", "", webdav.NewHTTPError(http.StatusNotFound, errors.New("ruta no válida"))
	}
	return slug, name, nil
}

func httpErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return webdav.NewHTTPError(http.StatusNotFound, err)
	case errors.Is(err, errConflict):
		return webdav.NewHTTPError(http.StatusPreconditionFailed, err)
	}
	return webdav.NewHTTPError(http.StatusBadRequest, err)
}

func etagParam(m webdav.ConditionalMatch) (string, bool) {
	if !m.IsSet() {
		return "", false
	}
	if m.IsWildcard() {
		return "", true
	}
	t, _ := m.ETag()
	return t, false
}

// ---- CalDAV ----

type calBackend struct{ d *davServer }

func (b calBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return davFrom(ctx).principal(), nil
}

func (b calBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return davFrom(ctx).home(calHome), nil
}

func (b calBackend) calendar(ctx context.Context, c collection) caldav.Calendar {
	comps := c.Components
	if len(comps) == 0 {
		comps = []string{ical.CompEvent, ical.CompToDo}
	}
	return caldav.Calendar{Path: davFrom(ctx).home(calHome) + c.Slug + "/", Name: c.Name, MaxResourceSize: 1 << 20, SupportedComponentSet: comps}
}

func (b calBackend) CreateCalendar(ctx context.Context, cal *caldav.Calendar) error {
	slug, _, err := split(ctx, calHome, cal.Path)
	if err != nil {
		return err
	}
	name := cal.Name
	if name == "" {
		name = slug
	}
	return httpErr(b.d.store.createCollection(ctx, collection{Owner: davFrom(ctx).user, Kind: "cal", Slug: slug, Name: name,
		Components: []string{ical.CompEvent, ical.CompToDo}}))
}

func (b calBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	list, err := b.d.store.collections(ctx, davFrom(ctx).user, "cal")
	if err != nil {
		return nil, err
	}
	out := []caldav.Calendar{}
	for _, c := range list {
		out = append(out, b.calendar(ctx, c))
	}
	return out, nil
}

func (b calBackend) GetCalendar(ctx context.Context, p string) (*caldav.Calendar, error) {
	slug, _, err := split(ctx, calHome, p)
	if err != nil {
		return nil, err
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "cal", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	cal := b.calendar(ctx, c)
	return &cal, nil
}

func (b calBackend) toObject(ctx context.Context, o object) (caldav.CalendarObject, error) {
	cal, err := decodeCal(o.Data)
	if err != nil {
		return caldav.CalendarObject{}, err
	}
	return caldav.CalendarObject{
		Path: davFrom(ctx).home(calHome) + o.Calendar + "/" + o.Name, ModTime: o.Updated,
		ContentLength: int64(len(o.Data)), ETag: o.ETag, Data: cal,
	}, nil
}

func (b calBackend) GetCalendarObject(ctx context.Context, p string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	slug, name, err := split(ctx, calHome, p)
	if err != nil {
		return nil, err
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "cal", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	o, err := b.d.store.objectByName(ctx, c.ID, name)
	if err != nil {
		return nil, httpErr(err)
	}
	co, err := b.toObject(ctx, o)
	return &co, err
}

func (b calBackend) ListCalendarObjects(ctx context.Context, p string, _ *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	slug, _, err := split(ctx, calHome, p)
	if err != nil {
		return nil, err
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "cal", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	list, err := b.d.store.listObjects(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	out := make([]caldav.CalendarObject, 0, len(list))
	for _, o := range list {
		co, err := b.toObject(ctx, o)
		if err != nil {
			continue
		}
		out = append(out, co)
	}
	return out, nil
}

func (b calBackend) QueryCalendarObjects(ctx context.Context, p string, q *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	all, err := b.ListCalendarObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	return caldav.Filter(q, all)
}

func (b calBackend) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	slug, name, err := split(ctx, calHome, p)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, webdav.NewHTTPError(http.StatusMethodNotAllowed, errors.New("falta el nombre del fichero"))
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "cal", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	ifMatch, _ := etagParam(opts.IfMatch)
	_, noneWildcard := etagParam(opts.IfNoneMatch)
	o, err := b.d.store.putObject(ctx, c.ID, name, cal, ifMatch, noneWildcard, b.d.now())
	if err != nil {
		return nil, httpErr(err)
	}
	o.Calendar = slug
	co, err := b.toObject(ctx, o)
	return &co, err
}

func (b calBackend) DeleteCalendarObject(ctx context.Context, p string) error {
	slug, name, err := split(ctx, calHome, p)
	if err != nil {
		return err
	}
	if name == "" {
		return webdav.NewHTTPError(http.StatusForbidden, errors.New("los calendarios se borran desde la app"))
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "cal", slug)
	if err != nil {
		return httpErr(err)
	}
	return httpErr(b.d.store.deleteObject(ctx, c.ID, name, ""))
}

// ---- CardDAV ----

type cardBackend struct{ d *davServer }

func (b cardBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return davFrom(ctx).principal(), nil
}

func (b cardBackend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return davFrom(ctx).home(cardHome), nil
}

func (b cardBackend) book(ctx context.Context, c collection) carddav.AddressBook {
	return carddav.AddressBook{Path: davFrom(ctx).home(cardHome) + c.Slug + "/", Name: c.Name, MaxResourceSize: 2 << 20,
		SupportedAddressData: []carddav.AddressDataType{{ContentType: vcard.MIMEType, Version: "3.0"}, {ContentType: vcard.MIMEType, Version: "4.0"}}}
}

func (b cardBackend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	list, err := b.d.store.collections(ctx, davFrom(ctx).user, "card")
	if err != nil {
		return nil, err
	}
	out := []carddav.AddressBook{}
	for _, c := range list {
		out = append(out, b.book(ctx, c))
	}
	return out, nil
}

func (b cardBackend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	slug, _, err := split(ctx, cardHome, p)
	if err != nil {
		return nil, err
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "card", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	ab := b.book(ctx, c)
	return &ab, nil
}

func (b cardBackend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	slug, _, err := split(ctx, cardHome, ab.Path)
	if err != nil {
		return err
	}
	name := ab.Name
	if name == "" {
		name = slug
	}
	return httpErr(b.d.store.createCollection(ctx, collection{Owner: davFrom(ctx).user, Kind: "card", Slug: slug, Name: name}))
}

func (b cardBackend) DeleteAddressBook(ctx context.Context, p string) error {
	return webdav.NewHTTPError(http.StatusForbidden, errors.New("las libretas se borran desde la app"))
}

func (b cardBackend) toObject(ctx context.Context, slug string, r cardRow) (carddav.AddressObject, error) {
	c, err := decodeCard(r.Data)
	if err != nil {
		return carddav.AddressObject{}, err
	}
	return carddav.AddressObject{Path: davFrom(ctx).home(cardHome) + slug + "/" + r.Name, ModTime: r.Updated,
		ContentLength: int64(len(r.Data)), ETag: r.ETag, Card: c}, nil
}

func (b cardBackend) GetAddressObject(ctx context.Context, p string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	slug, name, err := split(ctx, cardHome, p)
	if err != nil {
		return nil, err
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "card", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	r, err := b.d.store.cardByName(ctx, c.ID, name)
	if err != nil {
		return nil, httpErr(err)
	}
	ao, err := b.toObject(ctx, slug, r)
	return &ao, err
}

func (b cardBackend) ListAddressObjects(ctx context.Context, p string, _ *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	slug, _, err := split(ctx, cardHome, p)
	if err != nil {
		return nil, err
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "card", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	list, err := b.d.store.listCards(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	out := make([]carddav.AddressObject, 0, len(list))
	for _, r := range list {
		if ao, err := b.toObject(ctx, slug, r); err == nil {
			out = append(out, ao)
		}
	}
	return out, nil
}

func (b cardBackend) QueryAddressObjects(ctx context.Context, p string, q *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	all, err := b.ListAddressObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	return carddav.Filter(q, all)
}

func (b cardBackend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	slug, name, err := split(ctx, cardHome, p)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, webdav.NewHTTPError(http.StatusMethodNotAllowed, errors.New("falta el nombre del fichero"))
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "card", slug)
	if err != nil {
		return nil, httpErr(err)
	}
	ifMatch, _ := etagParam(opts.IfMatch)
	_, noneWildcard := etagParam(opts.IfNoneMatch)
	r, err := b.d.store.putCard(ctx, c.ID, name, card, ifMatch, noneWildcard, b.d.now())
	if err != nil {
		return nil, httpErr(err)
	}
	ao, err := b.toObject(ctx, slug, r)
	return &ao, err
}

func (b cardBackend) DeleteAddressObject(ctx context.Context, p string) error {
	slug, name, err := split(ctx, cardHome, p)
	if err != nil {
		return err
	}
	if name == "" {
		return b.DeleteAddressBook(ctx, p)
	}
	c, err := b.d.store.collection(ctx, davFrom(ctx).user, "card", slug)
	if err != nil {
		return httpErr(err)
	}
	return httpErr(b.d.store.deleteCard(ctx, c.ID, name, ""))
}
