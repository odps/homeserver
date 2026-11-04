package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Agenda: los avisos de vencimiento los manda la Agenda, como eventos de todo
// el día en su calendario «Vencimientos» con recordatorios. Tras cada cambio
// (y con el botón de la web) se le manda la lista completa de caducidades de
// la persona. Las dos apps hablan por 127.0.0.1, sin pasar por la pasarela.
// Solo sale el título y el titular: nunca el número ni las notas.

type agendaClient struct {
	base string
	http *http.Client
}

func newAgenda() *agendaClient {
	base := os.Getenv("AGENDA_URL")
	if base == "" {
		base = "http://127.0.0.1:8105"
	}
	return &agendaClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

// expiry es lo que se manda de cada documento con caducidad.
type expiry struct {
	ID     int64  `json:"id"`
	Title  string `json:"titulo"`
	Date   string `json:"fecha"`
	Alerts []int  `json:"avisos"`
}

// agendaResult es lo que contesta la Agenda.
type agendaResult struct {
	Agenda  bool   `json:"agenda"` // false: la persona no usa la Agenda
	Changed int    `json:"cambiados"`
	Removed int    `json:"quitados"`
	Error   string `json:"error"`
}

var errNoAgenda = errors.New("la agenda no responde")

func (a *agendaClient) push(ctx context.Context, owner string, list []expiry) (agendaResult, error) {
	var res agendaResult
	if list == nil {
		list = []expiry{}
	}
	body, err := json.Marshal(map[string]any{"usuario": owner, "vencimientos": list})
	if err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/interno/papeles/vencimientos", bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return res, errNoAgenda
	}
	defer resp.Body.Close()
	derr := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&res)
	if resp.StatusCode != http.StatusOK {
		if res.Error == "" {
			res.Error = resp.Status
		}
		return res, fmt.Errorf("agenda: %s", res.Error)
	}
	if derr != nil {
		return res, errors.New("agenda: respuesta no válida")
	}
	if res.Error != "" {
		return res, fmt.Errorf("agenda: %s", res.Error)
	}
	return res, nil
}

// expiries son los documentos con caducidad, por fecha.
func expiries(list []doc) []expiry {
	out := []expiry{}
	for _, d := range list {
		if d.Expires == "" {
			continue
		}
		title := "Caduca: " + d.Title
		if d.Holder != "" {
			title += " (" + d.Holder + ")"
		}
		alerts := d.Alerts
		if alerts == nil {
			alerts = []int{}
		}
		out = append(out, expiry{ID: d.ID, Title: title, Date: d.Expires, Alerts: alerts})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// syncAgenda manda a la Agenda las caducidades de la persona tal como están.
func (s *service) syncAgenda(ctx context.Context, owner string) (agendaResult, error) {
	if s.agenda == nil {
		return agendaResult{}, errNoAgenda
	}
	list, err := s.store.docs(ctx, owner)
	if err != nil {
		return agendaResult{}, err
	}
	return s.agenda.push(ctx, owner, expiries(list))
}

// syncAgendaQuiet es syncAgenda tras un cambio: si la Agenda falla, queda en
// el log (sin datos) y se arregla con el botón de la web.
func (s *service) syncAgendaQuiet(ctx context.Context, owner string) {
	if s.agenda == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.syncAgenda(ctx, owner); err != nil {
		s.log.Warn("vencimientos a la agenda", "err", err)
	}
}
