# homeserver

Apps personales pequeñas con tools MCP para un agente de IA, pensadas para correr 24/7 en un móvil con Termux y migrables tal cual a una Raspberry Pi o un VPS.

- **Go**: un proceso por app, binarios estáticos (`CGO_ENABLED=0`).
- **PostgreSQL**: una sola base de datos, con un schema y un rol por app.
- **Despliegue pull**: cada push a `main` pasa los tests y publica una Release. El updater del servidor la descarga, verifica el checksum, hace un `pg_dump`, cambia de versión y comprueba `/health`; si falla, vuelve a la versión anterior.

## Estructura

| Ruta | Contenido |
| --- | --- |
| `platform/app` | Arranque común: configuración, base de datos, migraciones, `/health`, `/mcp`, parada ordenada |
| `platform/db` | Migraciones SQL por schema |
| `platform/dbtest` | Schema temporal por test |
| `apps/<app>` | Cada app: `main.go`, `migrations/NNN_*.sql` |
| `cmd/updater` | Updater pull con vuelta atrás |
| `cmd/netwatch` | Vigilante de red: reinicia la wifi del móvil si no llega al router |
| `cmd/gateway` | Pasarela pública con login: `/<app>/…` → puerto de la app; sesión web o token `Bearer` |
| `deploy/apps.json` | Apps que se despliegan y su puerto, y servicios auxiliares que se reinician tras cada despliegue |
| `deploy/build.sh` | Compilación para android/arm64, linux/arm64 y linux/amd64 |
| `deploy/termux/bootstrap.sh` | Preparación inicial del móvil |

## Añadir una app

1. Copiar `apps/hola` a `apps/<nombre>` (minúsculas, números y `_`).
2. Añadirla a `deploy/apps.json` con un puerto libre (8101, 8102…).
3. Push a `main`. El updater crea su rol, su schema y su servicio runit.

Cada app escucha solo en `127.0.0.1` y expone sus tools en `/mcp` (MCP streamable HTTP, sin sesión); desde fuera del móvil se llega por la pasarela, en `/<app>/mcp`, y prefija sus nombres con el de la app (`hola_saludar`).

Las migraciones deben ser compatibles con la versión anterior (añadir columnas o tablas, no borrarlas en el mismo cambio): la vuelta atrás cambia el binario, no la base de datos. Antes de cada cambio de versión queda un `pg_dump` en `~/srv/backups`.

## Acceso público

Tailscale Funnel publica `https://xperia-web.tail36c376.ts.net` y envía todo a la pasarela (`127.0.0.1:8100`), que pide login antes de llegar a las apps (`/hola/`, …). El tráfico no pasa por Cloudflare, que en España se bloquea durante los partidos. En el móvil, Tailscale corre en modo userspace como un nodo aparte (`xperia-web`, servicio `tailscale_web`) compilado con `GOOS=android`, porque la app de Android no hace Funnel.

- Navegador: contraseña única (`gateway passwd`, hash argon2id) y cookie de sesión de 30 días. Hay un límite de 5 intentos fallidos por IP cada 15 minutos.
- Agente y clientes MCP: `Authorization: Bearer <token>` (`gateway token new <nombre>`). Las apps no reciben ni el token ni la cookie, solo `X-Auth-User`.
- Las apps deben usar enlaces relativos o respetar `X-Forwarded-Prefix`. Las redirecciones absolutas se corrigen solas.

## Desarrollo

```bash
go vet ./...
TEST_DATABASE_URL=postgres://... go test ./...   # sin la variable, los tests de base de datos se saltan
deploy/build.sh v0-local
```

## En el servidor (Termux)

```bash
sv status $PREFIX/var/service/*                  # estado de los servicios
tail -f $PREFIX/var/log/sv/homeserver-updater/current
sv restart hola
```
