package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"homeserver/platform/dbtest"
	"homeserver/platform/seal"
)

// testNow es el «hoy» de los tests: miércoles 4 de noviembre de 2026.
var testNow = time.Date(2026, 11, 4, 10, 0, 0, 0, madrid)

func newTestService(t *testing.T) *service {
	t.Helper()
	box, _ := seal.Load("")
	pool := dbtest.Migrated(t, migrations)
	return &service{store: store{pool, box}, files: newFileStore(t.TempDir(), box), log: slog.New(slog.DiscardHandler),
		now: func() time.Time { return testNow }}
}

// noPlain falla si algún byte de la tabla contiene el texto en claro. Los
// textos no deben ser solo letras a-f y cifras: el volcado de bytea es
// hexadecimal y daría falsos positivos.
func noPlain(t *testing.T, pool *pgxpool.Pool, table string, texts ...string) {
	t.Helper()
	var dump string
	pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(x::text, ' '), '') FROM `+table+` x`).Scan(&dump)
	if dump == "" {
		t.Fatalf("%s está vacía: el test no comprueba nada", table)
	}
	for _, s := range texts {
		if strings.Contains(strings.ToLower(dump), strings.ToLower(s)) {
			t.Errorf("%s guarda «%s» en claro", table, s)
		}
	}
}

func amount(c int64) *int64 { return &c }

func sampleDNI() doc {
	return doc{Type: "dni", Title: "DNI de Ana", Holder: "Ana Pérez", Number: "12345678Z", Issuer: "Policía Nacional",
		Issued: "2021-03-01", Expires: "2031-03-01", Alerts: []int{60, 30, 7}, Notes: "Renovar en comisaría con cita previa"}
}

func TestStoreOwnerAndPrivacy(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	st := svc.store

	id, err := st.saveDoc(ctx, "ana", sampleDNI())
	if err != nil || id == 0 {
		t.Fatalf("guardar: %d %v", id, err)
	}
	seg := doc{Type: "seguro_coche", Title: "Seguro del coche", Number: "POL-998877", Issuer: "Seguro Ejemplo", Expires: "2026-11-20",
		Months: 12, Alerts: []int{7, 30, 30}, Amount: amount(45050)}
	sid, err := st.saveDoc(ctx, "ana", seg)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.doc(ctx, "ana", sid)
	if err != nil || d.Title != "Seguro del coche" || d.Number != "POL-998877" || *d.Amount != 45050 || d.Months != 12 ||
		len(d.Alerts) != 2 || d.Alerts[0] != 30 || d.Alerts[1] != 7 || d.Created.IsZero() {
		t.Fatalf("leer: %+v %v", d, err)
	}
	// Cambiar.
	d.Issuer = "Seguro Ejemplo Mutua"
	if _, err := st.saveDoc(ctx, "ana", d); err != nil {
		t.Fatal(err)
	}
	if d, _ = st.doc(ctx, "ana", sid); d.Issuer != "Seguro Ejemplo Mutua" {
		t.Fatalf("cambiado: %+v", d)
	}
	// Un error de validación no guarda nada.
	bad := sampleDNI()
	bad.Expires = "2020-01-01"
	var de docErr
	if _, err := st.saveDoc(ctx, "ana", bad); !errors.As(err, &de) {
		t.Fatalf("caducidad antes de la emisión: %v", err)
	}

	// Nada en claro.
	noPlain(t, st.db, "docs", "DNI de Ana", "Ana Pérez", "12345678Z", "Policía", "comisaría", "POL-998877", "Seguro Ejemplo", "2031-03-01", "seguro_coche")

	// Cada uno ve solo lo suyo.
	luisID, _ := st.saveDoc(ctx, "luis", doc{Type: "pasaporte", Title: "Pasaporte de Luis", Number: "PAX123456"})
	if l, _ := st.docs(ctx, "luis"); len(l) != 1 || l[0].ID != luisID {
		t.Fatalf("luis ve: %+v", l)
	}
	if l, _ := st.docs(ctx, "ana"); len(l) != 2 {
		t.Fatalf("ana ve: %+v", l)
	}
	if _, err := st.doc(ctx, "luis", id); !errors.Is(err, errNotFound) {
		t.Fatal("luis lee el DNI de ana")
	}
	steal := sampleDNI()
	steal.ID = id
	steal.Title = "Mío"
	if _, err := st.saveDoc(ctx, "luis", steal); !errors.Is(err, errNotFound) {
		t.Fatalf("luis cambia el DNI de ana: %v", err)
	}
	if _, err := st.deleteDoc(ctx, "luis", id); !errors.Is(err, errNotFound) {
		t.Fatalf("luis borra el DNI de ana: %v", err)
	}
	if d, _ := st.doc(ctx, "ana", id); d.Title != "DNI de Ana" {
		t.Fatalf("el DNI de ana ha cambiado: %+v", d)
	}
	if _, err := st.deleteDoc(ctx, "ana", id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.doc(ctx, "ana", id); !errors.Is(err, errNotFound) {
		t.Fatal("sigue tras borrarlo")
	}
}

func TestValidation(t *testing.T) {
	ok := sampleDNI()
	if err := ok.clean(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(d *doc){
		"sin tipo":         func(d *doc) { d.Type = "carnet_de_baile" },
		"sin título":       func(d *doc) { d.Title = "  " },
		"título largo":     func(d *doc) { d.Title = strings.Repeat("a", maxTitle+1) },
		"número largo":     func(d *doc) { d.Number = strings.Repeat("9", maxNumber+1) },
		"notas largas":     func(d *doc) { d.Notes = strings.Repeat("x", maxNotes+1) },
		"fecha mala":       func(d *doc) { d.Expires = "2031-02-30" },
		"fecha rara":       func(d *doc) { d.Issued = "01/03/2021" },
		"antes de emisión": func(d *doc) { d.Expires = "2021-02-28" },
		"muchos avisos":    func(d *doc) { d.Alerts = []int{1, 2, 3, 4, 5, 6} },
		"aviso 0":          func(d *doc) { d.Alerts = []int{0} },
		"aviso 400":        func(d *doc) { d.Alerts = []int{400} },
		"meses":            func(d *doc) { d.Months = 121 },
		"importe":          func(d *doc) { d.Amount = amount(-1) },
	}
	for name, f := range cases {
		d := sampleDNI()
		f(&d)
		var de docErr
		if err := d.clean(); !errors.As(err, &de) {
			t.Errorf("%s: acepta %+v (%v)", name, d, err)
		}
	}
	d := sampleDNI()
	d.Title = "  DNI\nde   Ana\t"
	d.Issued = ""
	if err := d.clean(); err != nil || d.Title != "DNI de Ana" {
		t.Fatalf("limpiar: %q %v", d.Title, err)
	}

	for in, want := range map[string]string{"": "60,30,7", "0": "", "no": "", "7, 30": "30,7", "15;15 1": "15,1"} {
		got, err := parseAlerts(in, "dni")
		if err != nil || alertsJoin(got) != want {
			t.Errorf("avisos %q = %v %v", in, got, err)
		}
	}
	if got, _ := parseAlerts("", "itv"); alertsJoin(got) != "30,7" {
		t.Errorf("avisos de la ITV = %v", got)
	}
	if got, _ := parseAlerts("", "factura"); len(got) != 0 {
		t.Errorf("avisos de una factura = %v", got)
	}
	for _, bad := range []string{"mañana", "1,2,3,4,5,6", "0,7", "366"} {
		if _, err := parseAlerts(bad, "dni"); err == nil {
			t.Errorf("acepta avisos %q", bad)
		}
	}
	for in, want := range map[string]int64{"45,50": 4550, "45.5": 4550, "1.200": 120000, "1.234,56 €": 123456, "300": 30000} {
		got, err := parseAmount(in)
		if err != nil || got == nil || *got != want {
			t.Errorf("importe %q = %v %v", in, got, err)
		}
	}
	if got, err := parseAmount(" "); got != nil || err != nil {
		t.Errorf("importe vacío = %v %v", got, err)
	}
	for _, bad := range []string{"-3", "doce", "1e40"} {
		if _, err := parseAmount(bad); err == nil {
			t.Errorf("acepta el importe %q", bad)
		}
	}
	if euros(123456) != "1.234,56 €" || euros(30000) != "300 €" || euros(5) != "0,05 €" || amountText(amount(4550)) != "45,50" {
		t.Errorf("euros: %s %s %s", euros(123456), euros(30000), euros(5))
	}
}

func alertsJoin(a []int) string { return strings.ReplaceAll(alertsText(a), " ", "") }

func TestStatusAndRenew(t *testing.T) {
	today := testNow
	cases := []struct {
		exp   string
		state string
		days  int
	}{
		{"", stNone, 0},
		{"2026-11-03", stExpired, -1},
		{"2025-11-04", stExpired, -365},
		{"2026-11-04", stSoon, 0},
		{"2026-11-05", stSoon, 1},
		{"2027-02-02", stSoon, 90},
		{"2027-02-03", stValid, 91},
		{"2027-04-01", stValid, 148}, // pasa por el cambio de hora
	}
	for _, c := range cases {
		state, days := doc{Expires: c.exp}.status(today)
		if state != c.state || days != c.days {
			t.Errorf("%s: %s %d, quería %s %d", c.exp, state, days, c.state, c.days)
		}
	}
	// Ya entrada la noche sigue siendo el mismo día en Madrid.
	if _, days := (doc{Expires: "2026-11-05"}).status(time.Date(2026, 11, 4, 23, 30, 0, 0, madrid)); days != 1 {
		t.Errorf("por la noche: %d", days)
	}

	for in, want := range map[string]string{"2026-01-31": "2026-02-28", "2024-01-31": "2024-02-29", "2026-11-04": "2026-12-04", "2026-12-15": "2027-01-15"} {
		d, _ := time.ParseInLocation(dateFmt, in, madrid)
		if got := addMonths(d, 1).Format(dateFmt); got != want {
			t.Errorf("%s + 1 mes = %s", in, got)
		}
	}

	itv := doc{Type: "itv", Title: "ITV", Issued: "2025-11-20", Expires: "2026-11-20", Months: 12}
	if itv.nextRenewal() != "2027-11-20" {
		t.Fatalf("siguiente: %s", itv.nextRenewal())
	}
	if err := itv.renew(""); err != nil || itv.Expires != "2027-11-20" {
		t.Fatalf("renovar por periodo: %s %v", itv.Expires, err)
	}
	if err := itv.renew("2028-01-10"); err != nil || itv.Expires != "2028-01-10" {
		t.Fatalf("renovar con fecha: %s %v", itv.Expires, err)
	}
	if err := itv.renew("2020-01-01"); err == nil {
		t.Fatal("acepta una caducidad anterior a la emisión")
	}
	dni := sampleDNI()
	if err := dni.renew(""); !errors.Is(err, errNoNewDate) {
		t.Fatalf("sin periodo ni fecha: %v", err)
	}
	if err := dni.renew("2041-03-01"); err != nil || dni.Expires != "2041-03-01" {
		t.Fatalf("DNI renovado: %s %v", dni.Expires, err)
	}
	none := doc{Type: "itv", Title: "ITV", Months: 12}
	if err := none.renew(""); err == nil {
		t.Fatal("renueva sin caducidad")
	}
}

func TestSearchFold(t *testing.T) {
	d := doc{Type: "seguro_hogar", Title: "Póliza casa", Holder: "Ana Pérez", Issuer: "Seguro Ejemplo"}
	for q, want := range map[string]bool{"poliza": true, "PÉREZ ana": true, "hogar": true, "ejemplo casa": true, "coche": false, "": true} {
		if got := d.matches(seal.Words(q)); got != want {
			t.Errorf("%q: %v", q, got)
		}
	}
}
