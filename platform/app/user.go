package app

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Identity es quién hace la petición, según las cabeceras que fija la
// pasarela. Las apps solo escuchan en 127.0.0.1, así que nadie de fuera puede
// fijarlas: la pasarela borra las que mande el cliente.
type Identity struct {
	User string // nombre de usuario; vacío si no hay
	Name string // nombre visible
	Role string // admin, usuario o servicio (token sin usuario)
}

// IsUser indica si es una cuenta de persona (no un token de servicio).
func (i Identity) IsUser() bool {
	return i.User != "" && i.Role != "servicio" && !strings.HasPrefix(i.User, "token:")
}

// IsAdmin indica si la cuenta puede gestionar el contenido de todos en la app.
func (i Identity) IsAdmin() bool { return i.IsUser() && i.Role == "admin" }

// UserFrom lee la identidad de las cabeceras (de una petición HTTP o de una
// llamada MCP, que también las trae).
func UserFrom(h http.Header) Identity {
	name, err := url.QueryUnescape(h.Get("X-Auth-Name"))
	if err != nil || name == "" {
		name = h.Get("X-Auth-User")
	}
	return Identity{User: h.Get("X-Auth-User"), Name: name, Role: h.Get("X-Auth-Role")}
}

// User es UserFrom para una petición HTTP.
func User(r *http.Request) Identity { return UserFrom(r.Header) }

// Internal dice si la petición viene de otra app del propio servidor (desde
// 127.0.0.1 y sin pasar por la pasarela, que siempre pone X-Forwarded-Prefix).
// Lo usan las rutas /interno/… entre apps, que no llevan identidad.
func Internal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback() && r.Header.Get("X-Forwarded-Prefix") == ""
}
