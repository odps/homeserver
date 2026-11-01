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
	urls := map[string]string{}
	for _, g := range generalSources {
		urls[g.URL] = g.Name
	}
	for _, p := range presets {
		for _, u := range p.Feeds() {
			urls[u] = p.Name
		}
	}
	urls[redditFeedURL("worldnews")] = "Reddit"
	for u, name := range urls {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		p, err := f.fetch(ctx, u)
		cancel()
		first := ""
		if len(p.Items) > 0 {
			first = p.Items[0].Title + " | " + p.Items[0].Source
		}
		t.Logf("%-28s %3d %v %s", name, len(p.Items), err, first)
	}
}
