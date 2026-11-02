package seal

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(dir, KeyFile))
	if st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("permisos de la clave: %v", st.Mode())
	}
	c := b.SealString("t/c", "Cita en el dentista")
	if bytes.Contains(c, []byte("dentista")) {
		t.Fatal("el texto se ve en claro")
	}
	if s, err := b.OpenString("t/c", c); err != nil || s != "Cita en el dentista" {
		t.Fatalf("OpenString = %q, %v", s, err)
	}
	if _, err := b.Open("t/otra", c); err == nil {
		t.Fatal("se ha descifrado con otro contexto")
	}
	c[len(c)-1] ^= 1
	if _, err := b.Open("t/c", c); err == nil {
		t.Fatal("se ha aceptado un dato alterado")
	}
	// La misma clave se vuelve a leer del fichero.
	b2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := b2.OpenString("t/c", b.SealString("t/c", "x")); s != "x" {
		t.Fatal("la clave guardada no es la misma")
	}
	os.WriteFile(filepath.Join(dir, KeyFile), []byte("corta"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Fatal("quería un error con la clave dañada")
	}
	var v struct{ A string }
	j, _ := b.SealJSON("t/j", struct{ A string }{"ñ"})
	if err := b.OpenJSON("t/j", j, &v); err != nil || v.A != "ñ" {
		t.Fatalf("JSON = %+v, %v", v, err)
	}
}

func TestIndexAndQuery(t *testing.T) {
	b, _ := Load("")
	idx := b.Index(nil, "x", "Reunión con María en Córdoba, juan.perez@gmail.com, de 9 a 10, extraordinariamente")
	has := func(q string) bool {
		for _, t := range b.Query("x", q) {
			if !slices.Contains(idx, t) {
				return false
			}
		}
		return true
	}
	for _, q := range []string{"reunion", "Reu", "maria cordoba", "MARÍA", "juan.perez@gmail.com", "perez", "de", "9", "extraordinariamente", "extraordinarios"} {
		if !has(q) {
			t.Errorf("%q no se encuentra", q)
		}
	}
	for _, q := range []string{"re", "madrid", "maria sevilla", "d"} {
		if has(q) {
			t.Errorf("%q no debería encontrarse", q)
		}
	}
	if b.Token("x", "a") == b.Token("y", "a") {
		t.Fatal("los campos no separan las huellas")
	}
	other, _ := Load("")
	if other.Token("x", "a") == b.Token("x", "a") {
		t.Fatal("dos claves dan la misma huella")
	}
	// Sin repetir y con tope.
	again := b.Index(idx, "x", "reunión")
	if len(again) != len(idx) {
		t.Fatal("se han repetido huellas")
	}
}
