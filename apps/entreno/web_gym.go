package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"homeserver/platform/app"
)

// Modo gimnasio: la rutina de hoy con lo propuesto para cada ejercicio y una
// fila por serie. Sin JavaScript cada serie es un formulario normal; con
// gimnasio.js se apunta sin recargar (POST /api/serie), cuenta el descanso y
// guarda las series en el móvil si no hay conexión.

// cspGym es la única CSP que deja cargar un script: el de la propia app, y
// fetch solo al mismo origen. Nada en línea.
const cspGym = "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const maxSetBody = 4 << 10

// todayCard es la tarjeta «Hoy toca» del inicio.
type todayCard struct {
	ID      int64
	Name    string
	Count   int
	Stalled []string
	None    bool // aún no hay rutinas
}

type gymView struct {
	Empty     bool // no hay rutinas
	RoutineID int64
	Name      string
	Days      string
	Chosen    bool // elegida a mano, no la de hoy
	Others    []routineLink
	Items     []gymItem
}

type routineLink struct {
	ID      int64
	Name    string
	Current bool
}

type gymItem struct {
	Key, Name    string
	Last         string // «mar 27 oct: 3×10 · 50 kg · RIR 2»
	Goal, Advice string
	Stalled      bool
	RestSec      int
	Rest         string
	Field, Unit  string // reps/seg/m y su etiqueta
	Step         string // del campo de cantidad
	Done, Total  int
	Sets         []gymSet
}

// gymSet es una fila: hecha (Text) o pendiente (formulario con lo propuesto).
type gymSet struct {
	N               int
	Done            bool
	Text            string
	Amount, Kg, RIR string
	ID              string
}

// newSetID es el id aleatorio de una fila: si la web reenvía la serie, el
// servidor no la apunta dos veces.
func newSetID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

var reSetID = regexp.MustCompile(`^[0-9a-f]{8,32}$`)

// todayCard prepara la tarjeta del inicio (nil si falla: el inicio sigue).
func (w *web) todayCard(ctx context.Context, owner string) (*todayCard, error) {
	r, ok, err := w.svc.todayRoutine(ctx, owner, w.now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return &todayCard{None: true}, nil
	}
	plan, err := w.svc.plan(ctx, owner, r, w.now())
	if err != nil {
		return nil, err
	}
	c := &todayCard{ID: r.ID, Name: r.Name, Count: len(r.Items)}
	for _, p := range plan {
		if p.Stalled {
			c.Stalled = append(c.Stalled, p.Name)
		}
	}
	return c, nil
}

// ---- Página ----

func (w *web) gymPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	d := pageData{Name: who.Name, Tab: "gimnasio", Script: true, Gym: &gymView{}}
	if r.URL.Query().Get("error") == "serie" {
		d.Error = "Revisa la serie: los números no son válidos."
	}
	list, err := w.svc.store.routines(ctx, who.User)
	if err != nil {
		w.fail(rw, "gimnasio", err)
		return
	}
	q := r.URL.Query().Get("rutina")
	if len(list) == 0 && q == "" {
		d.Gym.Empty = true
		w.render(rw, r, "gimnasio.html", d)
		return
	}
	var cur routine
	if q != "" { // una de la persona; si no, no existe
		id, _ := strconv.ParseInt(q, 10, 64)
		found := false
		for _, x := range list {
			if x.ID == id {
				cur, found = x, true
			}
		}
		if !found {
			notFound(rw)
			return
		}
		d.Gym.Chosen = true
	} else {
		var ok bool
		if cur, ok, err = w.svc.todayRoutine(ctx, who.User, w.now()); err != nil {
			w.fail(rw, "gimnasio", err)
			return
		} else if !ok {
			cur = list[0]
		}
	}
	plan, err := w.svc.plan(ctx, who.User, cur, w.now())
	if err != nil {
		w.fail(rw, "gimnasio", err)
		return
	}
	g := d.Gym
	g.RoutineID, g.Name, g.Days = cur.ID, cur.Name, daysText(cur.Days)
	for _, x := range list {
		g.Others = append(g.Others, routineLink{ID: x.ID, Name: x.Name, Current: x.ID == cur.ID})
	}
	for _, p := range plan {
		g.Items = append(g.Items, w.gymItemOf(p))
	}
	w.render(rw, r, "gimnasio.html", d)
}

func (w *web) gymItemOf(p planItem) gymItem {
	it := gymItem{Key: p.Key, Name: p.Name, Goal: p.Goal, Advice: p.Advice, Stalled: p.Stalled, RestSec: p.RestSec, Rest: restText(p.RestSec),
		Field: "reps", Unit: "Reps", Step: "1", Total: p.Sets}
	switch p.Kind {
	case "time":
		it.Field, it.Unit = "seg", "Seg"
	case "distance":
		it.Field, it.Unit, it.Step = "m", "Metros", "any"
	}
	if p.Last != nil {
		it.Last = w.shortDate(p.Last.Day) + ": " + summarize(p.Last.Entry)
	} else {
		it.Last = p.LastText
	}
	for i, st := range p.DoneToday {
		it.Sets = append(it.Sets, gymSet{N: i + 1, Done: true, Text: doneText(st)})
	}
	it.Done = len(p.DoneToday)
	t := p.Target
	for n := it.Done; n < p.Sets; n++ {
		s := gymSet{N: n + 1, ID: newSetID()}
		switch it.Field {
		case "seg":
			s.Amount = posInt(t.Seconds)
		case "m":
			s.Amount = posNum(t.Meters)
		default:
			s.Amount = posInt(t.Reps)
		}
		s.Kg = posNum(t.Kg)
		if t.RIR != nil {
			s.RIR = strconv.Itoa(*t.RIR)
		}
		it.Sets = append(it.Sets, s)
	}
	return it
}

func posInt(v int) string {
	if v <= 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func posNum(v float64) string {
	if v <= 0 {
		return ""
	}
	return trimNum(v)
}

// doneText es una serie hecha: «10 reps · 50 kg · RIR 2», «45 s», «20 m».
func doneText(st setLog) string {
	var parts []string
	switch {
	case st.Seconds > 0:
		parts = append(parts, fmt.Sprintf("%d s", st.Seconds))
	case st.Meters > 0:
		parts = append(parts, trimNum(st.Meters)+" m")
	case st.Reps > 0:
		parts = append(parts, fmt.Sprintf("%d reps", st.Reps))
	}
	if st.Kg > 0 {
		parts = append(parts, trimNum(st.Kg)+" kg")
	}
	if st.RIR != nil {
		parts = append(parts, fmt.Sprintf("RIR %d", *st.RIR))
	}
	if len(parts) == 0 {
		return "Hecha"
	}
	return strings.Join(parts, " · ")
}

// ---- Apuntar una serie ----

// setInput es una serie tal como llega (formulario o JSON).
type setInput struct {
	Key  string   `json:"key"`
	Reps *int     `json:"reps"`
	Seg  *int     `json:"seg"`
	M    *float64 `json:"m"`
	Kg   *float64 `json:"kg"`
	RIR  *int     `json:"rir"`
	ID   string   `json:"id"`
	T    int64    `json:"t"` // cuándo se hizo (ms), para las que esperaban sin conexión
}

// setErr es un error de la serie que se enseña a la persona.
type setErr string

func (e setErr) Error() string { return string(e) }

const errSet = setErr("Serie no válida: revisa los números.")

// checkSet valida la serie y comprueba que el ejercicio es del catálogo o de
// la persona. El error es para enseñarlo.
func (w *web) checkSet(ctx context.Context, owner string, in setInput) (setLog, error) {
	if !reKey.MatchString(in.Key) {
		return setLog{}, setErr("Ese ejercicio no existe.")
	}
	if _, ok := lookup(in.Key); !ok {
		custom, err := w.svc.store.customNames(ctx, owner)
		if err != nil {
			return setLog{}, err
		}
		if custom[in.Key] == "" {
			return setLog{}, setErr("Ese ejercicio no existe.")
		}
	}
	if in.ID != "" && !reSetID.MatchString(in.ID) {
		return setLog{}, errSet
	}
	st := setLog{ID: in.ID}
	val := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	st.Reps, st.Seconds = val(in.Reps), val(in.Seg)
	if in.M != nil {
		st.Meters = *in.M
	}
	if in.Kg != nil {
		st.Kg = *in.Kg
	}
	if in.RIR != nil {
		r := *in.RIR
		st.RIR = &r
	}
	switch {
	case st.Reps < 0 || st.Reps > 1000 || st.Seconds < 0 || st.Seconds > 7200,
		badNum(st.Meters, 100000) || badNum(st.Kg, 1000),
		st.RIR != nil && (*st.RIR < 0 || *st.RIR > 10):
		return setLog{}, errSet
	case st.Reps == 0 && st.Seconds == 0 && st.Meters == 0:
		return setLog{}, setErr("Pon cuántas has hecho.")
	}
	st.Kg = math.Round(st.Kg*100) / 100
	st.Meters = math.Round(st.Meters*10) / 10
	return st, nil
}

func badNum(v, hi float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > hi }

// formSet lee la serie de un formulario.
func formSet(r *http.Request) (setInput, bool) {
	f := r.PostForm
	in := setInput{Key: f.Get("key"), ID: f.Get("id")}
	ok := true
	intP := func(name string) *int {
		s := strings.TrimSpace(f.Get(name))
		if s == "" {
			return nil
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			ok = false
		}
		return &v
	}
	numP := func(name string) *float64 {
		s := strings.TrimSpace(strings.ReplaceAll(f.Get(name), ",", "."))
		if s == "" {
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			ok = false
		}
		return &v
	}
	in.Reps, in.Seg, in.M, in.Kg, in.RIR = intP("reps"), intP("seg"), numP("m"), numP("kg"), intP("rir")
	return in, ok
}

// gymSetPost es la serie desde el formulario (sin JavaScript).
func (w *web) gymSetPost(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	r.Body = http.MaxBytesReader(rw, r.Body, maxSetBody)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return
	}
	to := "/gimnasio"
	if id, err := strconv.ParseInt(r.PostForm.Get("rutina"), 10, 64); err == nil && id > 0 {
		to += "?rutina=" + strconv.FormatInt(id, 10)
	}
	in, ok := formSet(r)
	st, err := w.checkSet(r.Context(), who.User, in)
	var se setErr
	switch {
	case !ok || (errors.As(err, &se) && reKey.MatchString(in.Key)):
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		http.Redirect(rw, r, to+sep+"error=serie#ex-"+in.Key, http.StatusSeeOther)
		return
	case errors.As(err, &se):
		http.Error(rw, se.Error(), http.StatusBadRequest)
		return
	case err != nil:
		w.fail(rw, "serie", err)
		return
	}
	if _, err := w.svc.logSet(r.Context(), who.User, in.Key, st, w.now()); err != nil {
		w.fail(rw, "serie", err)
		return
	}
	http.Redirect(rw, r, to+"#ex-"+in.Key, http.StatusSeeOther)
}

// apiSet es la serie desde gimnasio.js. Solo JSON del mismo origen.
func (w *web) apiSet(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	reply := func(code int, v any) {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		rw.WriteHeader(code)
		json.NewEncoder(rw).Encode(v)
	}
	fail := func(code int, msg string) { reply(code, map[string]any{"ok": false, "error": msg}) }
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		fail(http.StatusForbidden, "Petición de otro sitio.")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		fail(http.StatusUnsupportedMediaType, "Hace falta JSON.")
		return
	}
	var in setInput
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxSetBody))
	if err := dec.Decode(&in); err != nil {
		fail(http.StatusBadRequest, "Serie no válida.")
		return
	}
	st, err := w.checkSet(r.Context(), who.User, in)
	if err != nil {
		var se setErr
		if errors.As(err, &se) {
			fail(http.StatusBadRequest, se.Error())
			return
		}
		w.svc.log.Error("entreno web", "op", "api serie", "err", err)
		fail(http.StatusInternalServerError, "Algo ha fallado.")
		return
	}
	// Las que esperaban sin conexión van al día en que se hicieron (hasta
	// 36 horas atrás).
	now, at := w.now(), w.now()
	if in.T > 0 {
		if t := time.UnixMilli(in.T).In(madrid); t.After(now.Add(-36*time.Hour)) && t.Before(now.Add(time.Minute)) {
			at = t
		}
	}
	sid, err := w.svc.logSet(r.Context(), who.User, in.Key, st, at)
	if err != nil {
		w.svc.log.Error("entreno web", "op", "api serie", "err", err)
		fail(http.StatusInternalServerError, "Algo ha fallado.")
		return
	}
	reply(http.StatusOK, map[string]any{"ok": true, "sesion": sid})
}
