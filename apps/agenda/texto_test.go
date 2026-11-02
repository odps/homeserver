package main

import "testing"

func TestValidText(t *testing.T) {
	for in, want := range map[string]string{
		"Año nuevo":      "Año nuevo",
		"A\xd1O NUEVO":   "AÑO NUEVO",
		"Caf\xe9 y café": "Café y café",
		"":               "",
	} {
		if got := validText(in); got != want {
			t.Errorf("validText(%q) = %q, quiero %q", in, got, want)
		}
	}
}
