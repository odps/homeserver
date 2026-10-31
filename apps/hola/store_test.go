package main

import (
	"context"
	"testing"

	"homeserver/platform/dbtest"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	s := store{dbtest.Migrated(t, migrations)}

	for _, texto := range []string{"uno", "dos"} {
		if _, err := s.add(ctx, texto); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.list(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Texto != "dos" {
		t.Fatalf("list = %+v, quiero [dos uno]", got)
	}
	if _, err := s.add(ctx, ""); err == nil {
		t.Fatal("un texto vacío debería fallar")
	}
}
