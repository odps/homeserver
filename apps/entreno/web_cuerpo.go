package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"homeserver/platform/app"
)

// Cuerpo en la web: el peso (con su media de 7 días y la gráfica), las
// medidas, el formulario de cada día y las fotos de progreso. Las fotos se
// suben leyendo el multipart en memoria (nada de ficheros temporales en
// claro) y solo se sirven a su dueño, sin caché.

const (
	maxShownPhotos = 60      // miniaturas en la página
	maxPhotoField  = 1 << 10 // fecha, pose y nota de la subida
	maxPhotoParts  = 20
)

// bodyCard es la tarjeta del peso en el inicio.
type bodyCard struct {
	Last, LastDate, Avg7, Week string
}

type bodyView struct {
	Trend    *trendStats
	Chart    *chartView
	Measures []measureLine
	Logs     []bodyLine
	Weight   weightForm
	Upload   photoForm
	Poses    []option

	Compare    []poseCompare // antes y ahora, por pose
	PhotoDays  []photoDay
	PhotoCount int
	PhotoMore  int // fotos que no se enseñan
}

type trendStats struct {
	Last, LastDate, Avg7, Week, Month string
	Count                             int
}

type measureLine struct {
	Label, Last, LastDate string
	Prev                  string // cambio frente a la anterior
	First, FirstDate      string // cambio frente a la primera
}

type bodyLine struct {
	ISO, Date, Text, Note string
}

type weightForm struct{ Date, Kg string }

type photoForm struct{ Date, Pose, Note string }

type photoThumb struct {
	ID, Date, Pose, Alt string
}

type poseCompare struct {
	Pose          string
	Before, After photoThumb
}

type photoDay struct {
	ISO, Date string
	Photos    []photoThumb
}

// bodyDayForm es el formulario de un día, tal como se escribió.
type bodyDayForm struct {
	ISO, Date       string
	Exists, Confirm bool
	Kg, Fat, Note   string
	Sites           []siteField
}

type siteField struct {
	Key, Label, Hint, Value string
}

type photoPage struct {
	ID, Date, Pose, Note string
	W, H                 int
	Confirm              bool
}

// sentence pone en mayúscula la primera letra y el punto final («la nota
// pasa de 300 caracteres» → «La nota pasa de 300 caracteres.»).
func sentence(err error) string {
	s := err.Error()
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// dayFor es el día de un formulario (vacío: hoy), a mediodía de Madrid. Se
// deja hasta mañana, como en las tools del asistente.
func (w *web) dayFor(f string) (time.Time, error) {
	now := w.now()
	if f == "" {
		return dayOf(now).Add(12 * time.Hour), nil
	}
	d, err := time.ParseInLocation("2006-01-02", f, madrid)
	if err != nil || d.Year() < 2000 {
		return time.Time{}, errors.New("La fecha no es válida.")
	}
	if d.After(now.AddDate(0, 0, 1)) {
		return time.Time{}, errors.New("La fecha no puede ser de después de mañana.")
	}
	return d.Add(12 * time.Hour), nil
}

func kgText(v float64) string { return trimNum(v) + " kg" }

func (w *web) bodyCardOf(t *weightTrend) *bodyCard {
	if t == nil {
		return nil
	}
	c := &bodyCard{Last: kgText(t.Last), LastDate: w.shortDate(t.LastDay)}
	if t.Avg7 > 0 {
		c.Avg7 = kgText(t.Avg7)
	}
	if t.Week != nil {
		c.Week = signed(*t.Week) + " kg"
	}
	return c
}

func (w *web) thumbOf(p photo) photoThumb {
	date, pose := w.dayMonth(p.Day), poseLabel(p.Pose)
	return photoThumb{ID: p.ID, Date: date, Pose: pose, Alt: "Foto de progreso: " + strings.ToLower(pose) + ", " + date}
}

// bodyViewOf prepara la página (logs y fotos, de lo más reciente a lo más
// antiguo).
func (w *web) bodyViewOf(logs []bodyLog, photos []photo, today time.Time) *bodyView {
	v := &bodyView{Poses: poses}
	if t := trend(logs, today); t != nil {
		v.Trend = &trendStats{Last: kgText(t.Last), LastDate: w.shortDate(t.LastDay), Count: t.Count}
		if t.Avg7 > 0 {
			v.Trend.Avg7 = kgText(t.Avg7)
		}
		if t.Week != nil {
			v.Trend.Week = signed(*t.Week) + " kg"
		}
		if t.Month != nil {
			v.Trend.Month = signed(*t.Month) + " kg"
		}
	}

	// Gráfica del peso: los últimos 180 días, del más antiguo al más reciente.
	cut := dayOf(today).AddDate(0, 0, -180).Format("2006-01-02")
	var days []time.Time
	var vals []float64
	for i := len(logs) - 1; i >= 0; i-- {
		if b := logs[i]; b.Kg != nil && b.Day.Format("2006-01-02") >= cut {
			days, vals = append(days, b.Day), append(vals, *b.Kg)
		}
	}
	if len(vals) >= 2 {
		v.Chart = w.chart(days, vals, "kg")
		v.Chart.Label = fmt.Sprintf("Peso de los últimos %d pesajes, de %s a %s kg", len(vals), trimNum(vals[0]), trimNum(vals[len(vals)-1]))
	}

	for _, m := range measureRows(logs) {
		l := measureLine{Label: m.Label, Last: trimNum(m.Last) + " cm", LastDate: w.dayMonth(m.LastDay)}
		if m.Prev != nil {
			l.Prev = signed(round1(m.Last-*m.Prev)) + " cm"
		}
		if m.Times > 1 {
			l.First, l.FirstDate = signed(round1(m.Last-m.First))+" cm", w.dayMonth(m.FirstDay)
		}
		v.Measures = append(v.Measures, l)
	}

	cut = dayOf(today).AddDate(0, 0, -30).Format("2006-01-02")
	for _, b := range logs {
		iso := b.Day.Format("2006-01-02")
		if iso < cut {
			break
		}
		v.Logs = append(v.Logs, bodyLine{ISO: iso, Date: w.shortDate(b.Day), Text: bodyText(b), Note: b.Note})
	}

	// Fotos: la primera y la última de cada pose, y las más recientes por día.
	v.PhotoCount = len(photos)
	byPose := map[string][]photo{}
	for _, p := range photos {
		byPose[p.Pose] = append(byPose[p.Pose], p)
	}
	for _, ps := range poses {
		if list := byPose[ps.Key]; len(list) >= 2 {
			v.Compare = append(v.Compare, poseCompare{Pose: ps.Name, Before: w.thumbOf(list[len(list)-1]), After: w.thumbOf(list[0])})
		}
	}
	shown := photos[:min(len(photos), maxShownPhotos)]
	v.PhotoMore = len(photos) - len(shown)
	for _, p := range shown {
		iso := p.Day.Format("2006-01-02")
		if n := len(v.PhotoDays); n == 0 || v.PhotoDays[n-1].ISO != iso {
			v.PhotoDays = append(v.PhotoDays, photoDay{ISO: iso, Date: w.shortDate(p.Day)})
		}
		last := &v.PhotoDays[len(v.PhotoDays)-1]
		last.Photos = append(last.Photos, w.thumbOf(p))
	}
	return v
}

// ---- Página ----

func (w *web) bodyPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	today := w.now().Format("2006-01-02")
	w.renderBody(rw, r, who, pageData{Flash: flash(r)}, weightForm{Date: today}, photoForm{Date: today, Pose: "frente"})
}

func (w *web) renderBody(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, wf weightForm, pf photoForm) {
	ctx := r.Context()
	today := w.now()
	logs, err := w.svc.store.bodyLogs(ctx, who.User, time.Time{}, dayOf(today).AddDate(0, 0, 1))
	if err != nil {
		w.fail(rw, "cuerpo", err)
		return
	}
	photos, err := w.svc.store.photoList(ctx, who.User)
	if err != nil {
		w.fail(rw, "cuerpo", err)
		return
	}
	d.Name, d.Tab = who.Name, "cuerpo"
	d.Body = w.bodyViewOf(logs, photos, today)
	d.Body.Weight, d.Body.Upload = wf, pf
	w.render(rw, r, "cuerpo.html", d)
}

// weightPost apunta el peso de un día (lo demás de ese día se queda).
func (w *web) weightPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	r.Body = http.MaxBytesReader(rw, r.Body, 8<<10)
	if r.ParseForm() != nil {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	wf := weightForm{Date: strings.TrimSpace(r.PostForm.Get("fecha")), Kg: strings.TrimSpace(r.PostForm.Get("peso"))}
	pf := photoForm{Date: w.now().Format("2006-01-02"), Pose: "frente"}
	bad := func(msg string) { w.renderBody(rw, r, who, pageData{Error: msg}, wf, pf) }
	day, err := w.dayFor(wf.Date)
	if err != nil {
		bad(err.Error())
		return
	}
	kg, err := parseNumber(wf.Kg)
	if err != nil || wf.Kg == "" {
		bad("Escribe el peso en kg, por ejemplo 78,4.")
		return
	}
	upd := bodyLog{Kg: &kg}
	if err := upd.check(); err != nil {
		bad(sentence(err))
		return
	}
	if _, err := w.svc.store.saveBody(r.Context(), who.User, day, upd, true); err != nil {
		w.fail(rw, "peso", err)
		return
	}
	back(rw, r, "/cuerpo", "peso")
}

// ---- Un día ----

func (w *web) dayFormOf(day time.Time, b bodyLog, exists bool) *bodyDayForm {
	f := &bodyDayForm{ISO: day.Format("2006-01-02"), Date: w.longDate(day), Exists: exists, Note: b.Note}
	if b.Kg != nil {
		f.Kg = trimNum(*b.Kg)
	}
	if b.Fat != nil {
		f.Fat = trimNum(*b.Fat)
	}
	for _, s := range measureSites {
		sf := siteField{Key: s.Key, Label: s.Label, Hint: s.Hint}
		if v, ok := b.Measures[s.Key]; ok {
			sf.Value = trimNum(v)
		}
		f.Sites = append(f.Sites, sf)
	}
	return f
}

func (w *web) bodyDayPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	day, err := w.dayFor(r.URL.Query().Get("fecha"))
	if err != nil {
		notFound(rw)
		return
	}
	b, ok, err := w.svc.store.bodyOn(r.Context(), who.User, day)
	if err != nil {
		w.fail(rw, "cuerpo dia", err)
		return
	}
	f := w.dayFormOf(day, b, ok)
	f.Confirm = ok && r.URL.Query().Get("borrar") == "1"
	w.render(rw, r, "cuerpo_dia.html", pageData{Name: who.Name, Tab: "cuerpo", Flash: flash(r), Day: f})
}

// bodyDayPost sustituye lo apuntado ese día por lo del formulario (todo
// vacío: se borra el día).
func (w *web) bodyDayPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(rw, r.Body, 16<<10)
	if r.ParseForm() != nil {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	day, err := w.dayFor(r.PostForm.Get("fecha"))
	if err != nil {
		notFound(rw)
		return
	}
	get := func(k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }
	f := w.dayFormOf(day, bodyLog{}, false)
	f.Kg, f.Fat, f.Note = get("peso"), get("grasa"), get("nota")
	upd := bodyLog{Note: f.Note}
	ok := true
	num := func(s string) *float64 {
		if s == "" {
			return nil
		}
		v, err := parseNumber(s)
		if err != nil {
			ok = false
			return nil
		}
		return &v
	}
	upd.Kg, upd.Fat = num(f.Kg), num(f.Fat)
	for i, s := range f.Sites {
		f.Sites[i].Value = get("m_" + s.Key)
		if v := num(f.Sites[i].Value); v != nil {
			if upd.Measures == nil {
				upd.Measures = map[string]float64{}
			}
			upd.Measures[s.Key] = *v
		}
	}
	msg := ""
	if !ok {
		msg = "Revisa los números: usa cifras, con coma o punto."
	} else if err := upd.check(); err != nil {
		msg = sentence(err)
	}
	if msg != "" {
		_, f.Exists, _ = w.svc.store.bodyOn(ctx, who.User, day)
		w.render(rw, r, "cuerpo_dia.html", pageData{Name: who.Name, Tab: "cuerpo", Error: msg, Day: f})
		return
	}
	if _, err := w.svc.store.saveBody(ctx, who.User, day, upd, false); err != nil {
		w.fail(rw, "cuerpo dia", err)
		return
	}
	if upd.empty() {
		back(rw, r, "/cuerpo", "dia-borrado")
		return
	}
	back(rw, r, "/cuerpo", "guardado")
}

func (w *web) bodyDayDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	r.Body = http.MaxBytesReader(rw, r.Body, 4<<10)
	if r.ParseForm() != nil {
		http.Error(rw, "El formulario no es válido.", http.StatusBadRequest)
		return
	}
	f := r.PostForm.Get("fecha")
	day, err := w.dayFor(f)
	if f == "" || err != nil {
		notFound(rw)
		return
	}
	if err := w.svc.store.deleteBody(r.Context(), who.User, day); err != nil {
		w.fail(rw, "cuerpo borrar", err)
		return
	}
	back(rw, r, "/cuerpo", "dia-borrado")
}

// ---- Fotos ----

// readPhotoForm lee la subida en memoria: la foto y los campos de texto, en
// cualquier orden. Nada pasa por el disco.
func readPhotoForm(r *http.Request) (photoForm, []byte, error) {
	var f photoForm
	var raw []byte
	tooBig := errors.New("La foto pasa de 15 MB.")
	readErr := func(err error) error {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return tooBig
		}
		return errors.New("No se ha podido leer la foto. Prueba otra vez.")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return f, nil, errors.New("Elige una foto.")
	}
	for n := 0; ; n++ {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return f, nil, readErr(err)
		}
		if n >= maxPhotoParts {
			return f, nil, errors.New("El formulario no es válido.")
		}
		switch name := part.FormName(); name {
		case "foto":
			b, err := io.ReadAll(io.LimitReader(part, maxPhoto+1))
			if err != nil {
				return f, nil, readErr(err)
			}
			if len(b) > maxPhoto {
				return f, nil, tooBig
			}
			if len(b) > 0 {
				raw = b
			}
		case "fecha", "pose", "nota":
			b, err := io.ReadAll(io.LimitReader(part, maxPhotoField+1))
			if err != nil {
				return f, nil, readErr(err)
			}
			if len(b) > maxPhotoField || !utf8.Valid(b) {
				return f, nil, errors.New("La nota es demasiado larga.")
			}
			v := strings.TrimSpace(string(b))
			switch name {
			case "fecha":
				f.Date = v
			case "pose":
				f.Pose = v
			case "nota":
				f.Note = v
			}
		}
		part.Close()
	}
	if raw == nil {
		return f, nil, errors.New("Elige una foto.")
	}
	return f, raw, nil
}

func (w *web) photoUpload(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	r.Body = http.MaxBytesReader(rw, r.Body, maxPhoto+1<<20)
	f, raw, err := readPhotoForm(r)
	if !validPose(f.Pose) {
		f.Pose = "otra"
	}
	wf := weightForm{Date: w.now().Format("2006-01-02")}
	bad := func(msg string) { w.renderBody(rw, r, who, pageData{Error: msg}, wf, f) }
	if err != nil {
		bad(err.Error())
		return
	}
	day, err := w.dayFor(f.Date)
	if err != nil {
		bad(err.Error())
		return
	}
	if _, err := w.svc.savePhoto(r.Context(), who.User, day, f.Pose, f.Note, raw); err != nil {
		var pe photoErr
		if errors.As(err, &pe) {
			bad(sentence(err))
			return
		}
		w.fail(rw, "foto subir", err)
		return
	}
	http.Redirect(rw, r, "/cuerpo?ok=foto#fotos", http.StatusSeeOther)
}

func (w *web) photoView(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	p, err := w.svc.store.photoMeta(r.Context(), who.User, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "foto", err)
		return
	}
	d := pageData{Name: who.Name, Tab: "cuerpo", Flash: flash(r), Photo: &photoPage{ID: p.ID, Date: w.longDate(p.Day), Pose: poseLabel(p.Pose),
		Note: p.Note, W: p.W, H: p.H, Confirm: r.URL.Query().Get("borrar") == "1"}}
	w.render(rw, r, "foto.html", d)
}

// photoFile sirve la imagen (o la miniatura) solo a su dueño, sin caché.
func (w *web) photoFile(mini bool) func(http.ResponseWriter, *http.Request, app.Identity) {
	return func(rw http.ResponseWriter, r *http.Request, who app.Identity) {
		if w.svc.photos == nil {
			notFound(rw)
			return
		}
		img, err := w.svc.photoImage(r.Context(), who.User, r.PathValue("id"), mini)
		if errors.Is(err, errNotFound) {
			notFound(rw)
			return
		}
		if err != nil {
			w.fail(rw, "foto imagen", err)
			return
		}
		h := rw.Header()
		h.Set("Content-Type", "image/jpeg")
		h.Set("Content-Length", strconv.Itoa(len(img)))
		h.Set("Cache-Control", "private, no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'")
		rw.Write(img)
	}
}

func (w *web) photoDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	if w.svc.photos == nil {
		notFound(rw)
		return
	}
	err := w.svc.deletePhoto(r.Context(), who.User, r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "foto borrar", err)
		return
	}
	http.Redirect(rw, r, "/cuerpo?ok=foto-borrada#fotos", http.StatusSeeOther)
}
