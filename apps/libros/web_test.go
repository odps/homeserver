package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"homeserver/platform/version"
)

// El service worker debe llevar la versión: si sus bytes no cambian entre
// despliegues, los dispositivos nunca reciben la interfaz nueva.
func TestServiceWorkerCarriesVersion(t *testing.T) {
	old := version.Version
	version.Version = "v-prueba-123"
	defer func() { version.Version = old }()
	mux := http.NewServeMux()
	serveWeb(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "const VERSION = 'v-prueba-123'") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw.js sin versión: %s", body[:200])
	}
}
