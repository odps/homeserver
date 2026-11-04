package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
)

type service struct {
	store   store
	log     *slog.Logger
	imports *imports
	photos  *photoFiles      // fotos de progreso, cifradas en disco
	off     *offClient       // Open Food Facts (productos envasados)
	now     func() time.Time // nil: time.Now
}

func (s *service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// recordResult es lo que se ha apuntado de un texto.
type recordResult struct {
	SessionID int64         `json:"sesion"`
	Saved     []exerciseLog `json:"apuntados"`
	Unknown   []string      `json:"sin_reconocer,omitempty"`
	Notes     []string      `json:"notas,omitempty"`
}

// record apunta un texto en el formato de las notas en la sesión de ese día
// (la crea si no hay). Los nombres que no reconoce no se guardan: se
// devuelven para asignarlos.
func (s *service) record(ctx context.Context, owner, text string, at time.Time, source string) (recordResult, error) {
	nm, err := s.store.matcherFor(ctx, owner)
	if err != nil {
		return recordResult{}, err
	}
	exs, notes := parseText(text, nm)
	var res recordResult
	for _, e := range exs {
		if e.Key == "" {
			res.Unknown = append(res.Unknown, e.Name)
			continue
		}
		res.Saved = append(res.Saved, e)
	}
	res.Notes = notes
	if len(res.Saved) == 0 {
		if len(res.Unknown) > 0 {
			return res, fmt.Errorf("no reconozco %s: dime a qué ejercicio corresponde", strings.Join(res.Unknown, ", "))
		}
		return res, errors.New("no he encontrado ningún ejercicio (escribe, por ejemplo: «Lat machine 3x10 50kg rir 2»)")
	}
	if id, ok := s.store.sessionOn(ctx, owner, at, ""); ok {
		res.SessionID = id
		return res, s.store.addToSession(ctx, owner, id, notes, res.Saved)
	}
	res.SessionID, err = s.store.saveSession(ctx, owner, at, source, notes, res.Saved)
	return res, err
}

// ---- Importación ----

type importPreview struct {
	ID       string
	Owner    string
	Created  time.Time
	Sessions []importSession
	Unknown  map[string]int // nombre → veces
	Already  int            // días que ya estaban importados
}

// imports guarda las vistas previas media hora (en memoria: el fichero no
// se guarda en ningún sitio).
type imports struct {
	mu sync.Mutex
	m  map[string]*importPreview
}

func newImports() *imports { return &imports{m: map[string]*importPreview{}} }

func (im *imports) put(p *importPreview) {
	im.mu.Lock()
	defer im.mu.Unlock()
	for id, x := range im.m {
		if time.Since(x.Created) > 30*time.Minute || x.Owner == p.Owner {
			delete(im.m, id)
		}
	}
	im.m[p.ID] = p
}

func (im *imports) get(owner, id string) (*importPreview, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	p, ok := im.m[id]
	if !ok || p.Owner != owner || time.Since(p.Created) > 30*time.Minute {
		return nil, false
	}
	return p, true
}

func (im *imports) drop(id string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	delete(im.m, id)
}

// previewImport lee una exportación de WhatsApp y prepara la vista previa.
func (s *service) previewImport(ctx context.Context, owner, text string) (*importPreview, error) {
	nm, err := s.store.matcherFor(ctx, owner)
	if err != nil {
		return nil, err
	}
	sessions := chatSessions(parseChat(text, madrid), nm)
	if len(sessions) == 0 {
		return nil, errors.New("no he encontrado entrenos en el fichero: ¿es la exportación del chat (.txt o .md)?")
	}
	b := make([]byte, 8)
	rand.Read(b)
	p := &importPreview{ID: hex.EncodeToString(b), Owner: owner, Created: time.Now(), Sessions: sessions, Unknown: map[string]int{}}
	for _, x := range sessions {
		for n, c := range unknownNames(x.Exercises) {
			p.Unknown[n] += c
		}
		if _, ok := s.store.sessionOn(ctx, owner, x.Day, "whatsapp"); ok {
			p.Already++
		}
	}
	s.imports.put(p)
	return p, nil
}

// confirmImport guarda la importación. mapping dice qué hacer con cada
// nombre sin reconocer: una clave del catálogo, «nuevo» (ejercicio propio) o
// «» (no importarlo). Los días ya importados se saltan.
func (s *service) confirmImport(ctx context.Context, owner, id string, mapping map[string]string) (saved, skipped int, err error) {
	p, ok := s.imports.get(owner, id)
	if !ok {
		return 0, 0, errors.New("la vista previa ha caducado: vuelve a subir el fichero")
	}
	keys := map[string]string{}
	for name := range p.Unknown {
		switch to := mapping[name]; {
		case to == "":
		case to == "nuevo":
			key, err := s.store.addCustom(ctx, owner, name, "reps")
			if err != nil {
				return 0, 0, err
			}
			keys[name] = key
			s.store.setAlias(ctx, owner, name, key)
		default:
			if _, ok := lookup(to); !ok {
				return 0, 0, fmt.Errorf("ejercicio desconocido: %s", to)
			}
			keys[name] = to
			if err := s.store.setAlias(ctx, owner, name, to); err != nil {
				return 0, 0, err
			}
		}
	}
	for _, x := range p.Sessions {
		if _, ok := s.store.sessionOn(ctx, owner, x.Day, "whatsapp"); ok {
			skipped++
			continue
		}
		exs := make([]exerciseLog, 0, len(x.Exercises))
		for _, e := range x.Exercises {
			if e.Key == "" {
				e.Key = keys[e.Name]
			}
			if e.Key != "" {
				exs = append(exs, e)
			}
		}
		if len(exs) == 0 {
			continue
		}
		if _, err := s.store.saveSession(ctx, owner, x.Start, "whatsapp", x.Notes, exs); err != nil {
			return saved, skipped, err
		}
		saved++
	}
	s.imports.drop(id)
	return saved, skipped, nil
}

// ---- Cálculos ----

// e1RM es el máximo estimado a una repetición (Epley), contando el RIR como
// repeticiones que quedaban.
func e1RM(st setLog) float64 {
	if st.Kg <= 0 || st.Reps <= 0 {
		return 0
	}
	reps := st.Reps
	if st.RIR != nil {
		reps += *st.RIR
	}
	if reps == 1 {
		return st.Kg
	}
	return math.Round(st.Kg*(1+float64(reps)/30)*10) / 10
}

// best es la mejor serie de un ejercicio (por 1RM estimado; sin carga, por
// repeticiones, segundos o metros).
func best(e exerciseLog) (setLog, float64) {
	var top setLog
	topV := -1.0
	for _, st := range e.Sets {
		v := e1RM(st)
		if v == 0 {
			v = float64(st.Reps) + float64(st.Seconds) + st.Meters
			if st.Kg > 0 {
				v += st.Kg * 1000
			}
		}
		if v > topV {
			top, topV = st, v
		}
	}
	return top, e1RM(top)
}

// summarize resume las series: «3×10 · 50 kg · RIR 2», juntando las iguales.
func summarize(e exerciseLog) string {
	if len(e.Sets) == 0 {
		return "sin series"
	}
	var parts []string
	for i := 0; i < len(e.Sets); {
		j := i
		for j < len(e.Sets) && sameSet(e.Sets[i], e.Sets[j]) {
			j++
		}
		parts = append(parts, setText(j-i, e.Sets[i], e.PerSide))
		i = j
	}
	return strings.Join(parts, " + ")
}

func sameSet(a, b setLog) bool {
	ra, rb := -1, -1
	if a.RIR != nil {
		ra = *a.RIR
	}
	if b.RIR != nil {
		rb = *b.RIR
	}
	return a.Reps == b.Reps && a.Seconds == b.Seconds && a.Meters == b.Meters && a.Kg == b.Kg && ra == rb
}

func setText(n int, st setLog, perSide bool) string {
	var s string
	switch {
	case st.Reps == 0 && st.Seconds == 0 && st.Meters == 0:
		// «Curl 5kg»: la carga sin repeticiones.
		if st.Kg > 0 {
			s = trimNum(st.Kg) + " kg"
			if perSide {
				s += " por lado"
			}
		} else {
			s = fmt.Sprintf("%d serie", n)
		}
		if st.RIR != nil {
			s += fmt.Sprintf(" · RIR %d", *st.RIR)
		}
		return s
	case st.Seconds > 0:
		s = fmt.Sprintf("%d×%d s", n, st.Seconds)
	case st.Meters > 0:
		s = fmt.Sprintf("%d×%s m", n, trimNum(st.Meters))
	default:
		s = fmt.Sprintf("%d×%d", n, st.Reps)
	}
	if st.Kg > 0 {
		s += " · " + trimNum(st.Kg) + " kg"
		if perSide {
			s += " por lado"
		}
	}
	if st.RIR != nil {
		s += fmt.Sprintf(" · RIR %d", *st.RIR)
	}
	return s
}

func trimNum(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}
