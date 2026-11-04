package main

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"homeserver/platform/app"
)

// Rutinas en la web: la lista, el formulario (sin JavaScript: siempre lleva
// 3 filas vacías y «Guardar y añadir filas» guarda y vuelve) y las
// sugerencias sacadas del historial.

const (
	maxRoutineRows = 30 // filas que se leen de un formulario
	emptyRows      = 3
	maxItemNote    = 200
)

// routineView es una rutina para enseñar.
type routineView struct {
	ID    int64
	Index int // en las sugerencias
	Name  string
	Days  string // «lunes y jueves»
	Items []routineLine
}

type routineLine struct {
	Key, Name, Text, Note string
}

// routineForm es el formulario de una rutina.
type routineForm struct {
	ID      int64
	Name    string
	Days    []dayBox
	Rows    []itemRow
	Confirm bool // pide confirmar el borrado
}

type dayBox struct {
	Value   int
	Label   string
	Checked bool
}

// itemRow es una fila del formulario, tal como se escribió.
type itemRow struct {
	I                              int
	Key, Sets, Min, Max, RIR, Rest string
	Note                           string
}

// weekLabels va de lunes a domingo, con su valor (0 domingo … 6 sábado).
var weekLabels = []dayBox{{1, "lunes", false}, {2, "martes", false}, {3, "miércoles", false}, {4, "jueves", false},
	{5, "viernes", false}, {6, "sábado", false}, {0, "domingo", false}}

// daysText es «lunes, miércoles y viernes» (de lunes a domingo).
func daysText(days []int) string {
	ds := append([]int{}, days...)
	sort.Slice(ds, func(i, j int) bool { return weekOrder(ds[i]) < weekOrder(ds[j]) })
	var names []string
	for _, d := range ds {
		if d >= 0 && d <= 6 {
			names = append(names, weekdays[d])
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " y " + names[len(names)-1]
}

// restText es «45 s», «2 min» o «1 min 30 s».
func restText(sec int) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%d s", sec)
	case sec%60 == 0:
		return fmt.Sprintf("%d min", sec/60)
	}
	return fmt.Sprintf("%d min %d s", sec/60, sec%60)
}

// kindOfKey es el tipo de un ejercicio: reps, time o distance (los propios,
// por repeticiones, como en el plan).
func kindOfKey(key string) string {
	if e, ok := lookup(key); ok && e.Kind != "" {
		return e.Kind
	}
	return "reps"
}

func unitSuffix(kind string) string {
	switch kind {
	case "time":
		return " s"
	case "distance":
		return " m"
	}
	return ""
}

// itemText es «3 × 8–10 · RIR 2 · descanso 1 min 30 s».
func itemText(it routineItem) string {
	rng := strconv.Itoa(it.Min)
	if it.Max > it.Min {
		rng += "–" + strconv.Itoa(it.Max)
	}
	return fmt.Sprintf("%d × %s%s · RIR %d · descanso %s", it.Sets, rng, unitSuffix(kindOfKey(it.Key)), it.RIR, restText(it.RestSec))
}

func viewRoutine(r routine, custom map[string]string) routineView {
	v := routineView{ID: r.ID, Name: r.Name, Days: daysText(r.Days)}
	for _, it := range r.Items {
		v.Items = append(v.Items, routineLine{Key: it.Key, Name: exerciseName(it.Key, "", custom), Text: itemText(it), Note: it.Note})
	}
	return v
}

// ---- Lista ----

func (w *web) routinesPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	list, err := w.svc.store.routines(ctx, who.User)
	if err != nil {
		w.fail(rw, "rutinas", err)
		return
	}
	custom, _ := w.svc.store.customNames(ctx, who.User)
	d := pageData{Name: who.Name, Tab: "rutinas", Flash: flash(r)}
	for _, x := range list {
		d.Routines = append(d.Routines, viewRoutine(x, custom))
	}
	w.render(rw, r, "rutinas.html", d)
}

// ---- Detectar desde el historial ----

func (w *web) detectPage(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	d := pageData{Name: who.Name, Tab: "rutinas"}
	if err := w.fillSuggestions(r, who, &d); err != nil {
		w.fail(rw, "detectar", err)
		return
	}
	w.render(rw, r, "detectar.html", d)
}

func (w *web) fillSuggestions(r *http.Request, who app.Identity, d *pageData) error {
	list, err := w.svc.detectRoutines(r.Context(), who.User, w.now())
	if err != nil {
		return err
	}
	custom, _ := w.svc.store.customNames(r.Context(), who.User)
	d.Suggest = nil
	for i, x := range list {
		v := viewRoutine(x, custom)
		v.Index = i
		d.Suggest = append(d.Suggest, v)
	}
	return nil
}

// detectSave vuelve a calcular las sugerencias (con los mismos datos salen
// igual) y guarda la n o todas.
func (w *web) detectSave(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(rw, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "Petición no válida.", http.StatusBadRequest)
		return
	}
	list, err := w.svc.detectRoutines(ctx, who.User, w.now())
	if err != nil {
		w.fail(rw, "detectar", err)
		return
	}
	pick := list
	all := r.PostForm.Get("todas") == "1"
	if !all {
		n, err := strconv.Atoi(r.PostForm.Get("n"))
		if err != nil || n < 0 || n >= len(list) {
			d := pageData{Name: who.Name, Tab: "rutinas", Error: "Esa sugerencia ya no está: mira las de ahora."}
			if err := w.fillSuggestions(r, who, &d); err != nil {
				w.fail(rw, "detectar", err)
				return
			}
			w.render(rw, r, "detectar.html", d)
			return
		}
		pick = list[n : n+1]
	}
	var id int64
	for _, x := range pick {
		if id, err = w.svc.store.saveRoutine(ctx, who.User, x); err != nil {
			if msg := routineErr(err); msg != "" {
				d := pageData{Name: who.Name, Tab: "rutinas", Error: "No se ha podido guardar: " + msg}
				if err := w.fillSuggestions(r, who, &d); err != nil {
					w.fail(rw, "detectar", err)
					return
				}
				w.render(rw, r, "detectar.html", d)
				return
			}
			w.fail(rw, "detectar", err)
			return
		}
	}
	if all || id == 0 {
		back(rw, r, "/rutinas", "detectadas")
		return
	}
	back(rw, r, fmt.Sprintf("/rutina/%d", id), "guardada")
}

// routineErr es el mensaje para la persona si el error es de validación
// («» si es interno).
func routineErr(err error) string {
	msg := err.Error()
	if errors.Is(err, errRoutine) || strings.HasPrefix(msg, "el nombre") || strings.HasPrefix(msg, "ejercicio desconocido") {
		return capFirst(msg) + "."
	}
	return ""
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}

// ---- Formulario ----

func (w *web) routineNew(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	w.renderRoutineForm(rw, r, who, pageData{Name: who.Name, Tab: "rutinas"}, formOf(routine{}))
}

func (w *web) routineEdit(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	x, err := w.svc.store.routine(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "rutina", err)
		return
	}
	f := formOf(x)
	f.Confirm = r.URL.Query().Get("borrar") == "1"
	w.renderRoutineForm(rw, r, who, pageData{Name: who.Name, Tab: "rutinas", Flash: flash(r)}, f)
}

// formOf prepara el formulario de una rutina, con las filas vacías al final.
func formOf(x routine) routineForm {
	f := routineForm{ID: x.ID, Name: x.Name}
	for _, b := range weekLabels {
		b.Checked = containsInt(x.Days, b.Value)
		f.Days = append(f.Days, b)
	}
	for _, it := range x.Items {
		f.Rows = append(f.Rows, itemRow{Key: it.Key, Sets: strconv.Itoa(it.Sets), Min: strconv.Itoa(it.Min), Max: strconv.Itoa(it.Max),
			RIR: strconv.Itoa(it.RIR), Rest: strconv.Itoa(it.RestSec), Note: it.Note})
	}
	return f
}

func (w *web) renderRoutineForm(rw http.ResponseWriter, r *http.Request, who app.Identity, d pageData, f routineForm) {
	for range emptyRows {
		f.Rows = append(f.Rows, itemRow{})
	}
	for i := range f.Rows {
		f.Rows[i].I = i
	}
	custom, err := w.svc.store.customNames(r.Context(), who.User)
	if err != nil {
		w.fail(rw, "rutina", err)
		return
	}
	d.Form, d.Groups = &f, append(customGroup(custom), catalogGroups()...)
	w.render(rw, r, "rutina.html", d)
}

// routineSave guarda el formulario (id 0 = nueva).
func (w *web) routineSave(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	ctx := r.Context()
	var id int64
	if r.PathValue("id") != "0" {
		if id = pathID(r); id <= 0 {
			notFound(rw)
			return
		}
		if _, err := w.svc.store.routine(ctx, who.User, id); errors.Is(err, errNotFound) {
			notFound(rw)
			return
		} else if err != nil {
			w.fail(rw, "rutina", err)
			return
		}
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(rw, "El formulario es demasiado grande.", http.StatusRequestEntityTooLarge)
		return
	}
	custom, err := w.svc.store.customNames(ctx, who.User)
	if err != nil {
		w.fail(rw, "rutina", err)
		return
	}
	x, f, msg := readRoutineForm(r, id, custom)
	d := pageData{Name: who.Name, Tab: "rutinas"}
	if msg == "" {
		id, err = w.svc.store.saveRoutine(ctx, who.User, x)
		switch {
		case errors.Is(err, errNotFound):
			notFound(rw)
			return
		case err != nil:
			if msg = routineErr(err); msg == "" {
				w.fail(rw, "rutina", err)
				return
			}
		default:
			if r.PostForm.Get("accion") == "filas" {
				back(rw, r, fmt.Sprintf("/rutina/%d", id), "guardada")
				return
			}
			back(rw, r, "/rutinas", "guardada")
			return
		}
	}
	d.Error = msg
	w.renderRoutineForm(rw, r, who, d, f)
}

// readRoutineForm lee y valida el formulario. Devuelve la rutina, el
// formulario tal como vino (para volver a enseñarlo) y el error para la
// persona («» si está bien).
func readRoutineForm(r *http.Request, id int64, custom map[string]string) (routine, routineForm, string) {
	pf := r.PostForm
	x := routine{ID: id, Name: strings.TrimSpace(pf.Get("nombre"))}
	f := routineForm{ID: id, Name: x.Name}
	for _, v := range pf["dia"] {
		if d, err := strconv.Atoi(v); err == nil && d >= 0 && d <= 6 && !containsInt(x.Days, d) {
			x.Days = append(x.Days, d)
		}
	}
	for _, b := range weekLabels {
		b.Checked = containsInt(x.Days, b.Value)
		f.Days = append(f.Days, b)
	}
	n, _ := strconv.Atoi(pf.Get("filas"))
	n = clamp(n, 0, maxRoutineRows)
	msg := ""
	for i := range n {
		get := func(p string) string { return strings.TrimSpace(pf.Get(fmt.Sprintf("%s_%d", p, i))) }
		row := itemRow{Key: get("k"), Sets: get("s"), Min: get("mn"), Max: get("mx"), RIR: get("r"), Rest: get("d"), Note: get("n")}
		if row.Key == "" || get("quitar") == "1" {
			continue
		}
		f.Rows = append(f.Rows, row)
		if _, ok := lookup(row.Key); !ok && custom[row.Key] == "" {
			msg = firstMsg(msg, "Ese ejercicio no existe: elige uno de la lista.")
			continue
		}
		it := routineItem{Key: row.Key, Note: row.Note}
		ok := true
		num := func(s string, def int) int {
			if s == "" {
				return def
			}
			v, err := strconv.Atoi(s)
			if err != nil || v < 0 {
				ok = false
			}
			return v
		}
		it.Sets = num(row.Sets, 3)
		it.Min = num(row.Min, 8)
		it.Max = num(row.Max, max(it.Min, 1)+2)
		it.RIR = num(row.RIR, 2)
		it.RestSec = num(row.Rest, 90)
		if !ok {
			msg = firstMsg(msg, fmt.Sprintf("Revisa los números de «%s»: series, rango, RIR y descanso van en números enteros.", exerciseName(row.Key, "", custom)))
		}
		if utf8.RuneCountInString(it.Note) > maxItemNote {
			msg = firstMsg(msg, "Las notas de los ejercicios van cortas (200 letras como mucho).")
		}
		if it.Max < it.Min {
			it.Min, it.Max = it.Max, it.Min
		}
		x.Items = append(x.Items, it)
	}
	switch {
	case msg != "":
	case x.Name == "":
		msg = "Ponle un nombre a la rutina."
	case utf8.RuneCountInString(x.Name) > 60:
		msg = "El nombre es demasiado largo (60 letras como mucho)."
	case len(x.Items) == 0:
		msg = "Añade al menos un ejercicio."
	case len(x.Items) > 20:
		msg = "Como mucho 20 ejercicios por rutina."
	}
	return x, f, msg
}

func firstMsg(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (w *web) routineDelete(rw http.ResponseWriter, r *http.Request, who app.Identity) {
	id := pathID(r)
	if id <= 0 {
		notFound(rw)
		return
	}
	err := w.svc.store.deleteRoutine(r.Context(), who.User, id)
	if errors.Is(err, errNotFound) {
		notFound(rw)
		return
	}
	if err != nil {
		w.fail(rw, "borrar rutina", err)
		return
	}
	back(rw, r, "/rutinas", "rutina-borrada")
}
