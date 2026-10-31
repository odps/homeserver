// gateway es la única puerta pública del servidor. Pide login antes de dejar
// pasar a las apps y reparte por ruta: /<app>/… va al puerto de esa app según
// apps.json. Tailscale Funnel (u otro proxy) le envía todo el tráfico.
//
//	gateway                    sirve (configuración abajo)
//	gateway passwd             fija la contraseña web (la pide sin eco)
//	gateway token new <nombre> crea un token para el agente o un cliente MCP
//	gateway token list         lista los tokens
//	gateway token revoke <n>   revoca un token
//
// Configuración por variables de entorno:
//
//	GATEWAY_ADDR           dirección de escucha (por defecto 127.0.0.1:8100)
//	GATEWAY_PASSWORD_HASH  hash argon2id de la contraseña (lo escribe passwd)
//	APPS_FILE              apps.json de la versión activa (por defecto ~/srv/current/apps.json)
//	SRV_DIR                directorio base (por defecto ~/srv)
//
// La web usa una cookie de sesión firmada; las peticiones con
// "Authorization: Bearer <token>" se aceptan sin cookie.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"homeserver/platform/version"
)

func main() {
	home, _ := os.UserHomeDir()
	srv := env("SRV_DIR", filepath.Join(home, "srv"))
	tokens := tokenStore{path: filepath.Join(srv, "state", "gateway-tokens.json")}

	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0:
		err = serve(srv, tokens)
	case args[0] == "passwd":
		err = passwd(filepath.Join(srv, "env", "gateway.env"))
	case len(args) == 3 && args[0] == "token" && args[1] == "new":
		var tok string
		if tok, err = tokens.create(args[2]); err == nil {
			fmt.Printf("Token %q creado. Guárdalo ahora, no se vuelve a mostrar:\n%s\n", args[2], tok)
		}
	case len(args) == 2 && args[0] == "token" && args[1] == "list":
		err = listTokens(tokens)
	case len(args) == 3 && args[0] == "token" && args[1] == "revoke":
		err = tokens.revoke(args[2])
	default:
		err = errors.New("uso: gateway [passwd | token new <nombre> | token list | token revoke <nombre>]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(srv string, tokens tokenStore) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	hash := os.Getenv("GATEWAY_PASSWORD_HASH")
	if hash == "" {
		// Espera antes de salir para que runit no la reinicie cada segundo.
		log.Error("falta GATEWAY_PASSWORD_HASH: ejecuta «gateway passwd»")
		time.Sleep(time.Minute)
		return errors.New("sin contraseña configurada")
	}
	key, err := loadKey(filepath.Join(srv, "state", "gateway.key"))
	if err != nil {
		return err
	}
	apps, err := loadApps(env("APPS_FILE", filepath.Join(srv, "current", "apps.json")))
	if err != nil {
		return err
	}
	g := &gateway{
		passwordHash: hash,
		sessions:     sessions{key: key, ttl: 30 * 24 * time.Hour},
		limiter:      newLimiter(5, 15*time.Minute),
		tokens:       tokens,
		apps:         apps,
		log:          log,
	}
	addr := env("GATEWAY_ADDR", "127.0.0.1:8100")
	log.Info("escuchando", "addr", addr, "version", version.Version, "apps", len(apps))
	return g.listen(addr)
}

// passwd pide la contraseña dos veces sin eco y guarda su hash en gateway.env.
func passwd(envFile string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("passwd necesita una terminal (ssh -t)")
	}
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	p1, err := read("Contraseña nueva: ")
	if err != nil {
		return err
	}
	if len([]rune(p1)) < 12 {
		return errors.New("usa al menos 12 caracteres")
	}
	p2, err := read("Repítela: ")
	if err != nil {
		return err
	}
	if p1 != p2 {
		return errors.New("no coinciden")
	}
	if err := setEnv(envFile, "GATEWAY_PASSWORD_HASH", hashPassword(p1)); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Guardada. Reinicia la pasarela: sv restart gateway")
	return nil
}

// setEnv sustituye o añade KEY=valor en un fichero .env con permisos 600. El
// valor va entre comillas simples porque el hash lleva caracteres «$».
func setEnv(path, key, value string) error {
	var lines []string
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if !strings.HasPrefix(sc.Text(), key+"=") {
				lines = append(lines, sc.Text())
			}
		}
		f.Close()
	}
	lines = append(lines, key+"='"+value+"'")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func listTokens(tokens tokenStore) error {
	m, err := tokens.load()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("%-20s creado %s\n", n, m[n].Created.Format("2006-01-02 15:04"))
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
