// gateway es la única puerta pública del servidor. Pide contraseña y código
// TOTP antes de dejar pasar, sirve el panel de inicio y reparte por ruta:
// /<app>/… va al puerto de esa app según apps.json. Tailscale Funnel (u otro
// proxy) le envía todo el tráfico.
//
//	gateway                         sirve (configuración abajo)
//	gateway passwd                  fija la contraseña web (la pide sin eco)
//	gateway totp                    configura el segundo factor y genera códigos de recuperación
//	gateway token new <n> [apps] [días] [usuario]  token para el agente o un cliente MCP
//	                                (apps separadas por comas o *; días 0 = sin caducidad;
//	                                con usuario, actúa en nombre de esa cuenta de app)
//	gateway token list              lista los tokens
//	gateway token revoke <n>        revoca un token
//
// Configuración por variables de entorno (las dos primeras, en env/gateway.env):
//
//	GATEWAY_PASSWORD_HASH  hash argon2id de la contraseña
//	GATEWAY_TOTP_SECRET    secreto TOTP en base32
//	GATEWAY_ADDR           dirección de escucha (por defecto 127.0.0.1:8100)
//	APPS_FILE              apps.json de la versión activa (por defecto ~/srv/current/apps.json)
//	SRV_DIR                directorio base (por defecto ~/srv)
//	SVDIR                  servicios runit (por defecto $PREFIX/var/service)
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	_ "homeserver/platform/netfix" // DNS en Android: los avisos salen a api.telegram.org
	"homeserver/platform/version"
)

func main() {
	home, _ := os.UserHomeDir()
	srv := env("SRV_DIR", filepath.Join(home, "srv"))
	state := filepath.Join(srv, "state")
	envFile := filepath.Join(srv, "env", "gateway.env")
	tokens := &tokenStore{path: filepath.Join(state, "gateway-tokens.json")}
	recovery := &recoveryStore{path: filepath.Join(state, "gateway-recovery.json")}

	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0:
		err = serve(srv, tokens, recovery)
	case args[0] == "passwd":
		err = passwd(envFile)
	case args[0] == "totp":
		err = setupTOTP(envFile, recovery)
	case len(args) >= 3 && len(args) <= 6 && args[0] == "token" && args[1] == "new":
		err = newToken(tokens, args[2:])
	case len(args) == 2 && args[0] == "token" && args[1] == "list":
		err = listTokens(tokens)
	case len(args) == 3 && args[0] == "token" && args[1] == "revoke":
		err = tokens.revoke(args[2])
	default:
		err = errors.New("uso: gateway [passwd | totp | token new <nombre> [apps] [días] [usuario] | token list | token revoke <nombre>]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve(srv string, tokens *tokenStore, recovery *recoveryStore) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	hash := os.Getenv("GATEWAY_PASSWORD_HASH")
	if hash == "" {
		// Espera antes de salir para que runit no la reinicie cada segundo.
		log.Error("falta GATEWAY_PASSWORD_HASH: ejecuta «gateway passwd»")
		time.Sleep(time.Minute)
		return errors.New("sin contraseña configurada")
	}
	var totp *totpVerifier
	if s := os.Getenv("GATEWAY_TOTP_SECRET"); s != "" {
		var err error
		if totp, err = newTOTPVerifier(s); err != nil {
			return err
		}
	} else {
		log.Warn("sin GATEWAY_TOTP_SECRET: nadie podrá entrar hasta ejecutar «gateway totp»")
	}
	state := filepath.Join(srv, "state")
	sessions, err := newSessionStore(filepath.Join(state, "gateway-sessions.json"), sessionTTL, sessionIdle)
	if err != nil {
		return err
	}
	appSessions, err := newSessionStore(filepath.Join(state, "gateway-app-sessions.json"), 0, appSessionIdle)
	if err != nil {
		return err
	}
	apps, err := loadApps(env("APPS_FILE", filepath.Join(srv, "current", "apps.json")))
	if err != nil {
		return err
	}
	// Ya no se usa: las sesiones viven en el servidor.
	os.Remove(filepath.Join(state, "gateway.key"))
	g := &gateway{
		passwordHash: hash,
		totp:         totp,
		recovery:     recovery,
		sessions:     sessions,
		appSessions:  appSessions,
		users:        &userStore{path: filepath.Join(state, "gateway-users.json")},
		ipLimit:      newLimiter(5, 15*time.Minute),
		globalLimit:  newLimiter(30, 15*time.Minute),
		userLimit:    newLimiter(10, 15*time.Minute),
		tokens:       tokens,
		apps:         apps,
		audit:        &auditLog{path: filepath.Join(state, "audit.jsonl"), log: log},
		log:          log,
		srvDir:       srv,
		alerts:       newAlerter(srv),
		svDir:        env("SVDIR", filepath.Join(env("PREFIX", "/data/data/com.termux/files/usr"), "var", "service")),
	}
	addr := env("GATEWAY_ADDR", "127.0.0.1:8100")
	log.Info("escuchando", "addr", addr, "version", version.Version, "apps", len(apps), "totp", totp != nil)
	go g.runAlerts(context.Background())
	return g.listen(addr)
}

// setupTOTP crea un secreto nuevo, enseña el QR, pide un código para comprobar
// que la app lo ha leído bien y genera códigos de recuperación nuevos.
func setupTOTP(envFile string, recovery *recoveryStore) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("totp necesita una terminal (ssh -t)")
	}
	secret := newTOTPSecret()
	uri := otpauthURI(secret)
	qr, err := qrText(uri)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Escanea este código con tu app de autenticación (Aegis, Google Authenticator…):\n\n%s\n", qr)
	fmt.Fprintf(os.Stderr, "Si no puedes escanearlo, introduce la clave a mano: %s\n\n", secret)
	v, _ := newTOTPVerifier(secret)
	in := bufio.NewReader(os.Stdin)
	for tries := 0; ; tries++ {
		fmt.Fprint(os.Stderr, "Escribe el código de 6 dígitos que muestra la app: ")
		line, err := in.ReadString('\n')
		if err != nil {
			return err
		}
		if v.verify(strings.TrimSpace(line), time.Now()) {
			break
		}
		if tries == 2 {
			return errors.New("el código no coincide; no se ha cambiado nada")
		}
		fmt.Fprintln(os.Stderr, "No coincide. Revisa que la hora del móvil sea correcta.")
	}
	if err := setEnv(envFile, "GATEWAY_TOTP_SECRET", secret); err != nil {
		return err
	}
	codes, err := recovery.generate(8)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "\nSegundo factor guardado. Códigos de recuperación (cada uno sirve una vez).")
	fmt.Fprintln(os.Stderr, "Guárdalos en tu gestor de contraseñas; no se vuelven a mostrar:")
	for _, c := range codes {
		fmt.Fprintln(os.Stderr, "  "+c)
	}
	fmt.Fprintln(os.Stderr, "\nReinicia la pasarela para aplicarlo: sv restart gateway")
	return nil
}

func newToken(tokens *tokenStore, args []string) error {
	name, apps, days, user := args[0], []string{"*"}, 90, ""
	if len(args) > 1 {
		apps = strings.Split(args[1], ",")
	}
	if len(args) > 2 {
		var err error
		if days, err = strconv.Atoi(args[2]); err != nil || days < 0 {
			return errors.New("días no válidos")
		}
	}
	if len(args) > 3 {
		user = args[3]
	}
	tok, err := tokens.create(name, user, apps, time.Duration(days)*24*time.Hour)
	if err != nil {
		return err
	}
	fmt.Printf("Token %q creado para %s. Guárdalo ahora, no se vuelve a mostrar:\n%s\n", name, strings.Join(apps, ","), tok)
	return nil
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

func listTokens(tokens *tokenStore) error {
	m, err := tokens.list()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := m[n]
		exp := "sin caducidad"
		if e.Expires != nil {
			exp = "caduca " + e.Expires.Format("2006-01-02")
		}
		fmt.Printf("%-20s %-15s creado %s, %s\n", n, strings.Join(e.Apps, ","), e.Created.Format("2006-01-02"), exp)
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
