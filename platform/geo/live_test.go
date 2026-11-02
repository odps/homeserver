package geo

import (
	"context"
	"os"
	"testing"
)

// Contra los servicios de verdad, con un punto público (Puerta del Sol):
// GEO_LIVE=1 go test ./platform/geo -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("GEO_LIVE") == "" {
		t.Skip("sin GEO_LIVE")
	}
	c, ctx := New(), context.Background()
	for _, q := range []string{"farmacia", "cajero", "Mercadona"} {
		ps, err := c.Places(ctx, q, 40.4169, -3.7035, 800)
		if err != nil || len(ps) == 0 {
			t.Errorf("%s: %v %v", q, ps, err)
			continue
		}
		t.Logf("%s: %d; el primero %s (%s) a %d m, horario %q", q, len(ps), ps[0].Name, ps[0].Street, ps[0].Meters, ps[0].Hours)
	}
	z, err := c.Zone(ctx, 40.4169, -3.7035)
	t.Logf("zona: %q %v", z, err)
	lat, lon, label, err := c.City(ctx, "Madrid")
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.Weather(ctx, lat, lon, 2)
	if err != nil || len(f.Days) != 2 {
		t.Fatalf("tiempo: %+v %v", f, err)
	}
	t.Logf("%s ahora %.0f °C, %s; hoy: %s; mañana: %s", label, f.Now, f.Sky, f.Days[0].Line(), f.Days[1].Line())
}
