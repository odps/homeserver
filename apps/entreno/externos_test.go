package main

import (
	"strings"
	"testing"
)

func TestFichasExternas(t *testing.T) {
	e, _ := lookup("lat_pulldown")
	d := details(e)
	if d.DescLang != "es" || d.Desc == "" || len(d.Images) == 0 && e.Fedb == "" || len(d.Sources) == 0 {
		t.Fatalf("ficha del jalón: %+v", d)
	}
	for _, im := range d.Images {
		if !strings.HasPrefix(im.URL, "https://wger.de/") && !strings.HasPrefix(im.URL, fedbImages) || im.Credit == "" {
			t.Errorf("imagen sin origen permitido o sin autoría: %+v", im)
		}
	}
	// Los de wger que no están en el catálogo propio también existen y se
	// reconocen por su nombre.
	all := allExercises()
	if len(all) < 800 {
		t.Fatalf("catálogo ampliado: %d", len(all))
	}
	var extra exercise
	for _, x := range all {
		if strings.HasPrefix(x.Key, "w") && x.Name != "" {
			extra = x
			break
		}
	}
	if got, ok := lookup(extra.Key); !ok || got.Name != extra.Name {
		t.Fatalf("lookup %s", extra.Key)
	}
	if key, ok := newMatcher(nil).exact(extra.Name); !ok || key == "" {
		t.Fatalf("no reconoce %q", extra.Name)
	}
	// Los nombres propios mandan sobre los de wger.
	if key, _ := newMatcher(nil).resolve("Lat machine"); key != "lat_pulldown" {
		t.Fatalf("lat machine → %s", key)
	}
	// Solo se bajan imágenes de los sitios permitidos.
	c := &imageCache{dir: t.TempDir()}
	for _, u := range []string{"http://wger.de/a.png", "https://evil.example/a.png", "https://wger.de/a.svg"} {
		if _, _, err := c.get(u); err == nil {
			t.Errorf("acepta %s", u)
		}
	}
}
