// Package netfix hace que la resolución DNS funcione en Android (Termux).
// Android no tiene /etc/resolv.conf y el resolvedor de Go sin cgo pregunta
// entonces a localhost:53, donde no hay nadie. Si falta ese fichero, usa los
// servidores de $PREFIX/etc/resolv.conf o, si no, los de Google.
// En Linux normal (Raspberry Pi, VPS) no hace nada.
//
// Basta con importarlo: import _ "homeserver/platform/netfix"
package netfix

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

func init() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}
	servers := nameservers(filepath.Join(os.Getenv("PREFIX"), "etc", "resolv.conf"))
	if len(servers) == 0 {
		servers = []string{"8.8.8.8", "8.8.4.4"}
	}
	var next atomic.Uint32
	var d net.Dialer
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			s := servers[int(next.Add(1))%len(servers)]
			return d.DialContext(ctx, network, net.JoinHostPort(s, "53"))
		},
	}
}

func nameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
			out = append(out, f[1])
		}
	}
	return out
}
