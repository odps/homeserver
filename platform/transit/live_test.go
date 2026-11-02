package transit

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// Contra los servicios de verdad, con sitios públicos (Plaça de l'Ajuntament y
// Xàtiva), nunca la ubicación de nadie:
// TRANSIT_LIVE=1 go test ./platform/transit -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("TRANSIT_LIVE") == "" {
		t.Skip("sin TRANSIT_LIVE")
	}
	c, ctx := New(), context.Background()

	bus, err := c.Stops(ctx, Bus)
	if err != nil || len(bus) < 500 {
		t.Fatalf("paradas de la EMT: %d %v", len(bus), err)
	}
	metro, err := c.Stops(ctx, Metro)
	if err != nil || len(metro) < 100 {
		t.Fatalf("estaciones de metro: %d %v", len(metro), err)
	}
	t.Logf("%d paradas de la EMT y %d estaciones de Metrovalencia", len(bus), len(metro))

	s, err := c.Find(ctx, Bus, "plaza del ayuntamiento", "")
	if err != nil || len(s) == 0 {
		t.Fatalf("buscar parada: %v %v", s, err)
	}
	t.Logf("«plaza del ayuntamiento»: %d paradas; la primera %s %s (%v)", len(s), s[0].ID, s[0].Name, s[0].Lines)
	near, _ := c.Near(ctx, Bus, 39.4699, -0.3763, 250, 5, "")
	t.Logf("cerca de la plaza: %d paradas", len(near))

	b, err := c.Arrivals(ctx, Bus, "2314")
	if err != nil {
		t.Fatalf("llegadas EMT: %v", err)
	}
	t.Logf("EMT 2314 %s: %d llegadas", b.Name, len(b.Arrivals))
	for _, a := range b.Arrivals {
		t.Logf("  %s → %s: %s", a.Line, a.Toward, when(a))
	}

	m, err := c.Find(ctx, Metro, "Xàtiva", "")
	if err != nil || len(m) == 0 {
		t.Fatalf("buscar estación: %v %v", m, err)
	}
	mb, err := c.Arrivals(ctx, Metro, m[0].ID)
	if err != nil {
		t.Fatalf("salidas de metro: %v", err)
	}
	t.Logf("Metro %s (%s): %d salidas", m[0].Name, m[0].ID, len(mb.Arrivals))
	for _, a := range mb.Arrivals {
		t.Logf("  %s → %s: %s", a.Line, a.Toward, when(a))
	}
}

func when(a Arrival) string {
	switch {
	case a.Minutes != nil && a.Time != "":
		return fmt.Sprintf("%s (%d min)", a.Time, *a.Minutes)
	case a.Minutes != nil:
		return fmt.Sprintf("%d min", *a.Minutes)
	}
	return a.Note
}
