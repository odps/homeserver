package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// Solo a mano: NOTICIAS_LIVE=1 go test -run Live -v ./apps/noticias
func TestLiveFeeds(t *testing.T) {
	if os.Getenv("NOTICIAS_LIVE") == "" {
		t.Skip("solo con NOTICIAS_LIVE=1")
	}
	f := newFetcher(false)
	for _, g := range append(generalSources, generalSource{"gn", "Google News", topicFeedURL("inteligencia artificial")}) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		p, err := f.fetch(ctx, g.URL)
		cancel()
		first := ""
		if len(p.Items) > 0 {
			first = p.Items[0].Title + " | " + p.Items[0].Source + " | " + p.Items[0].Published.Format(time.RFC3339)
		}
		t.Logf("%-24s %3d %v %s", g.Name, len(p.Items), err, first)
	}
}
