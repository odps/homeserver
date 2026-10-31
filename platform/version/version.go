// Package version guarda la versión del binario, inyectada al compilar con
// -ldflags "-X homeserver/platform/version.Version=<tag>".
package version

var Version = "dev"
