package main

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "127.8.9.10", "10.0.0.1", "172.16.5.4", "172.31.255.255", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "100.101.102.103", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255", "240.0.0.1", "198.18.0.1",
		"192.0.2.1", "::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::a00:1", "2002:a00:1::1", "2001:db8::1"}
	for _, s := range blocked {
		if !blockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s debería estar bloqueada", s)
		}
	}
	for _, s := range []string{"93.184.215.14", "1.1.1.1", "8.8.8.8", "100.63.255.255", "100.128.0.1", "172.32.0.1", "2a00:1450:4003::1", "2606:4700::1111"} {
		if blockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s no debería estar bloqueada", s)
		}
	}
	f := newFetcher()
	for addr, want := range map[string]bool{"127.0.0.1:443": true, "[::1]:443": true, "10.1.2.3:443": true, "93.184.215.14:443": false, "nombre:443": true} {
		if got := f.dialCheck(addr) != nil; got != want {
			t.Errorf("dialCheck(%s) = %v", addr, got)
		}
	}
}

func TestCleanURL(t *testing.T) {
	f := newFetcher()
	ok := map[string]string{
		"https://Ejemplo.ES/Ruta?a=1#parte":                            "https://ejemplo.es/Ruta?a=1",
		"  https://ejemplo.es  ":                                       "https://ejemplo.es/",
		"ejemplo.es/articulo":                                          "https://ejemplo.es/articulo",
		"http://ejemplo.es:80/x":                                       "http://ejemplo.es/x",
		"https://ejemplo.es:443/x":                                     "https://ejemplo.es/x",
		"https://ejemplo.es/x?utm_source=tw&utm_medium=social&id=7":    "https://ejemplo.es/x?id=7",
		"https://ejemplo.es/x?fbclid=abc":                              "https://ejemplo.es/x",
		"https://ejemplo.es/x?b=2&a=1":                                 "https://ejemplo.es/x?b=2&a=1",
		"https://93.184.215.14/x":                                      "https://93.184.215.14/x",
		"https://sub.dominio.ejemplo.es/a%20b":                         "https://sub.dominio.ejemplo.es/a%20b",
		"HTTPS://ejemplo.es/":                                          "https://ejemplo.es/",
		"https://ejemplo.es/ruta/con/muchas/partes/y-un-titulo-largo/": "https://ejemplo.es/ruta/con/muchas/partes/y-un-titulo-largo/",
	}
	for in, want := range ok {
		got, err := f.cleanURL(in)
		if err != nil || got != want {
			t.Errorf("cleanURL(%q) = %q, %v; quería %q", in, got, err, want)
		}
	}
	bad := map[string]*fetchError{
		"":                                     errBadURL,
		"javascript:alert(1)":                  errBadURL,
		"data:text/html,<script>":              errBadURL,
		"file:///etc/passwd":                   errBadURL,
		"ftp://ejemplo.es/x":                   errBadURL,
		"gopher://ejemplo.es/":                 errBadURL,
		"mailto:alguien@ejemplo.es":            errBadURL,
		"https://usuario:clave@ejemplo.es/":    errBadURL,
		"https://ejemplo.es/con espacio":       errBadURL,
		"https://" + strings.Repeat("a", 3000): errBadURL,
		"https://localhost/":                   errBlocked,
		"https://LOCALHOST./":                  errBlocked,
		"https://router.lan/":                  errBlocked,
		"https://impresora.local/":             errBlocked,
		"https://movil.tail1234.ts.net/":       errBlocked,
		"https://intranet/":                    errBlocked,
		"https://127.0.0.1/":                   errBlocked,
		"https://10.0.0.8/":                    errBlocked,
		"https://192.168.1.1/admin":            errBlocked,
		"https://169.254.169.254/latest/":      errBlocked,
		"https://[::1]/":                       errBlocked,
		"https://[::ffff:127.0.0.1]/":          errBlocked,
		"https://100.100.100.100/":             errBlocked,
		"https://0.0.0.0/":                     errBlocked,
		"https://ejemplo.es:8080/":             errPort,
		"https://ejemplo.es:22/":               errPort,
		"http://ejemplo.es:5432/":              errPort,
	}
	for in, want := range bad {
		if got, err := f.cleanURL(in); !errors.Is(err, want) {
			t.Errorf("cleanURL(%q) = %q, %v; quería %v", in, got, err, want)
		}
	}
}

func TestFetch(t *testing.T) {
	s := newSite(t)
	other := newSite(t) // otro servidor local, al que no se deja conectar
	f := testFetcher(s)
	ctx := context.Background()

	p, err := f.get(ctx, s.url("/articulo/tomates"))
	if err != nil || !strings.Contains(string(p.Body), "tomates") || p.URL.Path != "/articulo/tomates" {
		t.Fatalf("página normal: %v", err)
	}
	// Las redirecciones dentro del mismo sitio se siguen; la final es la buena.
	if p, err = f.get(ctx, s.url("/redir/tomates")); err != nil || p.URL.Path != "/articulo/tomates" {
		t.Fatalf("redirección: %v", err)
	}
	// Un enlace http:// (o una redirección a http://) se pide por HTTPS.
	if p, err = f.get(ctx, "http://"+s.addr()+"/articulo/pan"); err != nil || p.URL.Scheme != "https" {
		t.Fatalf("http → https: %v", err)
	}
	if p, err = f.get(ctx, s.url("/redir/http")); err != nil || p.URL.Scheme != "https" || p.URL.Path != "/articulo/pan" {
		t.Fatalf("redirección a http://: %v", err)
	}
	// Sin Content-Type, se mira si es HTML.
	if _, err = f.get(ctx, s.url("/sin-tipo")); err != nil {
		t.Fatalf("sin tipo: %v", err)
	}

	before := other.hits.Load()
	for path, want := range map[string]*fetchError{
		"/redir/privada":                 errBlocked,
		"/redir/metadatos":               errBlocked,
		"/redir/ftp":                     errBadURL,
		"/redir/bucle":                   errTooMany,
		"/redir/a/" + other.addr():       errBlocked, // otro puerto local
		"/redir/a/localhost:" + s.Port(): errBlocked,
		"/grande":                        errTooBig,
		"/pdf":                           errNotHTML,
		"/no-existe":                     statusError(404),
	} {
		_, err := f.get(ctx, s.url(path))
		if fe := asFetchError(err); fe == nil || fe.msg != want.msg {
			t.Errorf("%s: %v; quería %v", path, err, want)
		}
	}
	if other.hits.Load() != before {
		t.Fatal("ha llegado a conectarse al otro servidor local")
	}

	// Aunque el nombre pase el filtro, se mira la IP a la que de verdad se
	// conecta: un dominio que resuelve a la red local no vale.
	f2 := testFetcher(s)
	f2.allowURL["pagina.ejemplo.es:"+s.Port()] = true
	f2.fakeDNS["pagina.ejemplo.es"] = "127.0.0.1"
	delete(f2.allowDial, s.addr())
	if _, err := f2.get(ctx, "https://pagina.ejemplo.es:"+s.Port()+"/articulo/tomates"); asFetchError(err) != errBlocked {
		t.Fatalf("nombre que resuelve a local: %v", err)
	}
	f2.fakeDNS["pagina.ejemplo.es"] = "10.0.0.1"
	if _, err := f2.get(ctx, "https://pagina.ejemplo.es:"+s.Port()+"/articulo/tomates"); asFetchError(err) != errBlocked {
		t.Fatalf("nombre que resuelve a la red local: %v", err)
	}
	// Y sin permiso, el servidor local de los tests tampoco.
	if _, err := newFetcher().get(ctx, s.url("/articulo/tomates")); asFetchError(err) != errBlocked {
		t.Fatalf("servidor local sin permiso: %v", err)
	}

	// Un certificado que no es de confianza no se acepta.
	f3 := newFetcherWith(nil, 5*time.Second)
	f3.allowURL[s.addr()], f3.allowDial[s.addr()] = true, true
	if _, err := f3.get(ctx, s.url("/articulo/tomates")); asFetchError(err) != errNoHTTPS {
		t.Fatalf("certificado no válido: %v", err)
	}

	// El tiempo máximo.
	slow := testFetcher(s)
	slow.http.Timeout = time.Nanosecond
	if _, err := slow.get(ctx, s.url("/articulo/tomates")); asFetchError(err) != errTimeout {
		t.Fatalf("sin respuesta a tiempo: %v", err)
	}
}

func TestStatusError(t *testing.T) {
	for code, permanent := range map[int]bool{404: true, 410: true, 403: true, 429: false, 408: false, 500: false, 503: false} {
		if e := statusError(code); e.permanent != permanent || !strings.Contains(e.msg, "("+strconv.Itoa(code)+")") {
			t.Errorf("%d: %+v", code, e)
		}
	}
}
