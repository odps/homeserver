package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// validName limita los nombres de app a identificadores seguros para usarlos
// como schema, rol, servicio y fichero.
var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// ensureApps aprovisiona las apps que aún no lo están y deja su servicio runit
// escrito. Es idempotente.
func (u *updater) ensureApps(ctx context.Context, m manifest) error {
	for _, a := range m.Apps {
		if err := u.provision(ctx, a.Name); err != nil {
			return fmt.Errorf("aprovisionar %s: %w", a.Name, err)
		}
		if err := u.writeService(a); err != nil {
			return fmt.Errorf("servicio %s: %w", a.Name, err)
		}
	}
	return nil
}

// provision crea el rol app_<nombre> con una contraseña aleatoria y el schema
// <nombre> de su propiedad, y guarda DATABASE_URL en env/<nombre>.env. Si el
// fichero ya existe, la app está aprovisionada.
func (u *updater) provision(ctx context.Context, name string) error {
	envFile := u.path("env", name+".env")
	if _, err := os.Stat(envFile); err == nil {
		return nil
	}
	b := make([]byte, 24)
	rand.Read(b)
	pass := hex.EncodeToString(b) // solo hexadecimal: seguro dentro de un literal SQL
	roleName := "app_" + name
	role, schema := pgx.Identifier{roleName}.Sanitize(), pgx.Identifier{name}.Sanitize()

	conn, err := pgx.Connect(ctx, u.cfg.AdminDSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", roleName).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("%s ROLE %s LOGIN PASSWORD '%s'", verb, role, pass)); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION %s", schema, role)); err != nil {
		return err
	}

	// La app se conecta por TCP local con su rol y contraseña, a la misma base.
	db := conn.Config().Database
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(roleName, pass),
		Host:     fmt.Sprintf("127.0.0.1:%d", conn.Config().Port),
		Path:     "/" + db,
		RawQuery: "sslmode=disable",
	}
	u.log.Info("app aprovisionada", "app", name, "rol", roleName)
	return writeFile(envFile, "DATABASE_URL="+dsn.String()+"\n", 0o600)
}

// writeService escribe el servicio runit de la app. Se reescribe en cada
// despliegue por si cambia el puerto.
func (u *updater) writeService(a appSpec) error {
	dir := filepath.Join(u.cfg.SvDir, a.Name)
	sh := filepath.Join(u.cfg.Prefix, "bin", "sh")
	run := fmt.Sprintf(`#!%s
# Generado por el updater de homeserver; no editar.
exec 2>&1
set -a
. %s
set +a
export ADDR=":%d"
exec %s
`, sh, u.path("env", a.Name+".env"), a.Port, u.path("current", "bin", a.Name))
	// svlogger de termux-services guarda el log en $PREFIX/var/log/sv/<servicio>.
	logRun := fmt.Sprintf("#!%s\nexec %s \"$@\"\n", sh, filepath.Join(u.cfg.Prefix, "share", "termux-services", "svlogger"))

	if err := os.MkdirAll(filepath.Join(dir, "log"), 0o755); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "log", "run"), logRun, 0o755); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "run"), run, 0o755)
}

// writeFile solo escribe si el contenido cambia, de forma atómica.
func writeFile(path, content string, mode os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
