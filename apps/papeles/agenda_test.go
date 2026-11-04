package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeAgenda hace de Agenda y guarda lo que recibe.
type fakeAgenda struct {
	mu   sync.Mutex
	got  []string
	srv  *httptest.Server
	code int    // respuesta para todos (0: 200)
	body string // respuesta (vacío: según el usuario)
}

func newFakeAgenda(t *testing.T) *fakeAgenda {
	f := &fakeAgenda{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/interno/papeles/vencimientos" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"ruta"}`, http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var in struct {
			User string `json:"usuario"`
		}
		json.Unmarshal(b, &in)
		f.mu.Lock()
		f.got = append(f.got, string(b))
		code, body := f.code, f.body
		f.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
		}
		switch {
		case body != "":
			w.Write([]byte(body))
		case in.User == "sinagenda":
			w.Write([]byte(`{"agenda":false}`))
		default:
			w.Write([]byte(`{"agenda":true,"cambiados":1,"quitados":0}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAgenda) set(code int, body string) {
	f.mu.Lock()
	f.code, f.body = code, body
	f.mu.Unlock()
}

func (f *fakeAgenda) client() *agendaClient {
	return &agendaClient{base: f.srv.URL, http: f.srv.Client()}
}

func (f *fakeAgenda) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.got...)
}

func TestExpiries(t *testing.T) {
	list := []doc{
		{ID: 3, Type: "itv", Title: "ITV", Expires: "2027-05-01", Alerts: []int{30, 7}, Number: "1234-ABC"},
		{ID: 1, Type: "dni", Title: "DNI", Holder: "Ana Pérez", Number: "12345678Z", Expires: "2027-03-01", Alerts: []int{60, 30, 7}, Notes: "nota privada"},
		{ID: 2, Type: "factura", Title: "Factura de la nevera"},
		{ID: 4, Type: "titulo", Title: "Título de grado", Expires: "2027-03-01"},
	}
	got := expiries(list)
	b, _ := json.Marshal(got)
	want := `[{"id":1,"titulo":"Caduca: DNI (Ana Pérez)","fecha":"2027-03-01","avisos":[60,30,7]},` +
		`{"id":4,"titulo":"Caduca: Título de grado","fecha":"2027-03-01","avisos":[]},` +
		`{"id":3,"titulo":"Caduca: ITV","fecha":"2027-05-01","avisos":[30,7]}]`
	if string(b) != want {
		t.Fatalf("vencimientos:\n%s\n%s", b, want)
	}
	if b, _ := json.Marshal(expiries(nil)); string(b) != "[]" {
		t.Fatalf("vacío: %s", b)
	}
}

func TestAgendaSync(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	fa := newFakeAgenda(t)
	svc.agenda = fa.client()

	// Guardar avisa a la Agenda; nunca el número ni las notas.
	id, err := svc.saveDoc(ctx, "ana", sampleDNI())
	if err != nil {
		t.Fatal(err)
	}
	svc.saveDoc(ctx, "ana", doc{Type: "garantia", Title: "Garantía del portátil", Number: "SN-ZXQ-778", Notes: "ticket en el cajón"})
	sent := fa.sent()
	if len(sent) != 2 {
		t.Fatalf("envíos: %d", len(sent))
	}
	last := sent[1]
	for _, bad := range []string{"12345678Z", "SN-ZXQ", "comisaría", "cajón", "Policía", "portátil"} {
		if strings.Contains(last, bad) {
			t.Errorf("manda «%s» a la Agenda: %s", bad, last)
		}
	}
	if !strings.Contains(last, `"usuario":"ana"`) || !strings.Contains(last, `"titulo":"Caduca: DNI de Ana (Ana Pérez)"`) ||
		!strings.Contains(last, `"fecha":"2031-03-01"`) || !strings.Contains(last, `"avisos":[60,30,7]`) || !strings.Contains(last, `"id":`+strconv.FormatInt(id, 10)) {
		t.Fatalf("lo mandado: %s", last)
	}

	// Renovar y borrar también.
	if _, err := svc.renewDoc(ctx, "ana", id, "2041-03-01"); err != nil {
		t.Fatal(err)
	}
	if s := fa.sent(); len(s) != 3 || !strings.Contains(s[2], "2041-03-01") {
		t.Fatalf("tras renovar: %v", s)
	}
	if err := svc.deleteDoc(ctx, "ana", id); err != nil {
		t.Fatal(err)
	}
	if s := fa.sent(); len(s) != 4 || !strings.Contains(s[3], `"vencimientos":[]`) {
		t.Fatalf("tras borrar: %v", s)
	}
	// Lo de otro no se manda.
	svc.saveDoc(ctx, "luis", doc{Type: "pasaporte", Title: "Pasaporte de Luis", Expires: "2030-01-01"})
	if s := fa.sent(); !strings.Contains(s[4], `"usuario":"luis"`) || strings.Contains(s[4], "Ana") {
		t.Fatalf("luis: %s", s[4])
	}

	res, err := svc.syncAgenda(ctx, "ana")
	if err != nil || !res.Agenda || res.Changed != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if res, err := svc.syncAgenda(ctx, "sinagenda"); err != nil || res.Agenda {
		t.Fatalf("sin agenda: %+v %v", res, err)
	}
	fa.set(http.StatusBadRequest, `{"error":"usuario desconocido"}`)
	if _, err := svc.syncAgenda(ctx, "ana"); err == nil || !strings.Contains(err.Error(), "usuario desconocido") {
		t.Fatalf("error de la agenda: %v", err)
	}
	fa.set(0, `no es json`)
	if _, err := svc.syncAgenda(ctx, "ana"); err == nil {
		t.Fatal("acepta una respuesta rota")
	}
	fa.set(0, "")

	// El botón de la web.
	e := newWebEnvWith(t, svc)
	if code, loc, _ := e.post("/agenda", "ana", url.Values{}); code != http.StatusSeeOther || loc != "/?ok=agenda" {
		t.Fatalf("botón: %d %s", code, loc)
	}
	if _, body := e.get("/?ok=agenda", "ana"); !strings.Contains(body, "Vencimientos al día en la Agenda") {
		t.Fatal("falta el aviso del botón")
	}
	if _, loc, _ := e.post("/agenda", "sinagenda", url.Values{}); loc != "/?ok=agenda-no" {
		t.Fatalf("sin agenda: %s", loc)
	}
	if _, body := e.get("/?ok=agenda-no", "sinagenda"); !strings.Contains(body, "No usas la Agenda") {
		t.Fatal("falta el aviso de sin agenda")
	}
	// Si la Agenda no está, guardar funciona igual y el botón lo dice.
	fa.srv.Close()
	if code, loc, _ := e.post("/doc/0", "ana", url.Values{"tipo": {"itv"}, "titulo": {"ITV del coche"}, "caducidad": {"2027-06-01"}}); code != http.StatusSeeOther || !strings.Contains(loc, "ok=guardado") {
		t.Fatalf("guardar sin agenda: %d %s", code, loc)
	}
	if _, loc, _ := e.post("/agenda", "ana", url.Values{}); loc != "/?ok=agenda-error" {
		t.Fatalf("agenda caída: %s", loc)
	}
}
