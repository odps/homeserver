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
| `deploy/apps.json` | Apps que se despliegan y su puerto, y servicios auxiliares que se reinician tras cada despliegue |
| `deploy/build.sh` | Compilación para android/arm64, linux/arm64 y linux/amd64 |
| `deploy/termux/bootstrap.sh` | Preparación inicial del móvil |

## Añadir una app

1. Copiar `apps/hola` a `apps/<nombre>` (minúsculas, números y `_`).
2. Añadirla a `deploy/apps.json` con un puerto libre (8101, 8102…).
3. Push a `main`. El updater crea su rol, su schema y su servicio runit.

Cada app expone sus tools en `http://<servidor>:<puerto>/mcp` (MCP streamable HTTP, sin sesión) y prefija sus nombres con el de la app (`hola_saludar`).

Las migraciones deben ser compatibles con la versión anterior (añadir columnas o tablas, no borrarlas en el mismo cambio): la vuelta atrás cambia el binario, no la base de datos. Antes de cada cambio de versión queda un `pg_dump` en `~/srv/backups`.

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
