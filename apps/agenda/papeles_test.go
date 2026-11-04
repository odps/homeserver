package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeadlines(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	svc.store.ensureUser(ctx, "ana")
	svc.store.ensureUser(ctx, "luis")
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, madrid)
	svc.now = func() time.Time { return now }
	day := func(s string) time.Time { d, _ := time.ParseInLocation("2006-01-02", s, madrid); return d }

	list := []deadline{
		{ID: 1, Title: "Caduca: DNI", Date: "2027-03-15", Alarms: []int{60, 30, 7}},
		{ID: 2, Title: "Caduca: ITV", Date: "2027-01-05", Alarms: []int{30, 2}},
	}
	if n, r, err := svc.syncDeadlines(ctx, "ana", list); err != nil || n != 2 || r != 0 {
		t.Fatalf("sync: %d %d %v", n, r, err)
	}
	l, _ := svc.agenda(ctx, "ana", day("2027-03-15"), day("2027-03-16"))
	if len(l) != 1 || !l[0].AllDay || l[0].Title != "Caduca: DNI" || l[0].Calendar != deadlinesSlug {
		t.Fatalf("evento: %+v", l)
	}
	if n, _, _ := svc.syncDeadlines(ctx, "ana", list); n != 0 {
		t.Fatal("rehace lo que no ha cambiado")
	}

	// El aviso de 2 días de la ITV, el 3 de enero a las 9:00 (el de 30 ya pasó).
	fire := func(at string) []string {
		now, _ = time.ParseInLocation("2006-01-02 15:04", at, madrid)
		svc.fireAlarms(ctx)
		got, _ := svc.store.takeNotices(ctx, "ana", now)
		var out []string
		for _, n := range got {
			out = append(out, n.Text)
		}
		return out
	}
	if got := fire("2027-01-03 08:59"); len(got) != 0 {
		t.Fatalf("antes de hora: %v", got)
	}
	if got := fire("2027-01-03 09:00"); len(got) != 1 || !strings.Contains(got[0], "Caduca: ITV") || !strings.Contains(got[0], "todo el día") {
		t.Fatalf("aviso ITV: %v", got)
	}
	// 60 días antes del DNI: 14 de enero.
	if got := fire("2027-01-14 09:01"); len(got) != 1 || !strings.Contains(got[0], "Caduca: DNI") {
		t.Fatalf("aviso DNI: %v", got)
	}
	if got := fire("2027-02-13 09:05"); len(got) != 1 {
		t.Fatalf("aviso de 30 días: %v", got)
	}

	// Cambia la fecha (renovado): se rehace; lo que ya no está, se borra.
	now = time.Date(2027, 2, 20, 12, 0, 0, 0, madrid)
	list = []deadline{{ID: 1, Title: "Caduca: DNI", Date: "2037-03-15", Alarms: []int{60}}}
	if n, r, err := svc.syncDeadlines(ctx, "ana", list); err != nil || n != 1 || r != 1 {
		t.Fatalf("renovado: %d %d %v", n, r, err)
	}
	if got := fire("2027-03-08 09:00"); len(got) != 0 {
		t.Fatalf("avisa del DNI viejo: %v", got)
	}
	if l, _ := svc.agenda(ctx, "luis", day("2027-01-01"), day("2038-01-01")); len(l) != 0 {
		t.Fatalf("luis ve los vencimientos de ana: %+v", l)
	}
	for _, bad := range []deadline{{ID: 1, Title: "X", Date: "2027-13-01"}, {ID: 0, Title: "X", Date: "2027-01-01"},
		{ID: 1, Title: "X", Date: "2027-01-01", Alarms: []int{400}}, {ID: 1, Title: "", Date: "2027-01-01"}} {
		if _, _, err := svc.syncDeadlines(ctx, "ana", []deadline{bad}); err == nil {
			t.Fatalf("acepta %+v", bad)
		}
	}

	// Por HTTP: solo desde dentro y solo a quien usa la agenda.
	w := &web{svc: svc}
	post := func(body string, gateway bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/interno/papeles/vencimientos", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5000"
		if gateway {
			r.Header.Set("X-Forwarded-Prefix", "/agenda")
		}
		rec := httptest.NewRecorder()
		w.deadlinesPost(rec, r)
		return rec
	}
	if rec := post(`{"usuario":"ana","vencimientos":[]}`, true); rec.Code != http.StatusForbidden {
		t.Fatalf("por la pasarela: %d", rec.Code)
	}
	if rec := post(`{"usuario":"nadie","vencimientos":[]}`, false); !strings.Contains(rec.Body.String(), `"agenda":false`) {
		t.Fatalf("sin agenda: %s", rec.Body)
	}
	if rec := post(`{"usuario":"ana","vencimientos":[]}`, false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"quitados":1`) {
		t.Fatalf("vaciar: %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"usuario":"ana","vencimientos":[{"id":5,"titulo":"X","fecha":"mal"}]}`, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("fecha mala: %d", rec.Code)
	}
}
