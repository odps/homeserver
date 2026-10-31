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
| `apps/libros` | Biblioteca y lector (EPUB, PDF, MOBI, AZW3, FB2, FBZ, CBZ) con progreso sincronizado entre dispositivos, PWA sin conexión y búsqueda en bibliotecas públicas legales (Project Gutenberg, Wikisource e Internet Archive con licencia libre); lector basado en [foliate-js](https://github.com/johnfactotum/foliate-js) (MIT), incluido en `web/vendor/foliate` |
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

Cada app recibe `DATA_DIR` (`~/srv/data/<app>`) para sus ficheros. Con `max_upload_mb` en `apps.json`, la pasarela admite subidas más grandes y da plazos largos a esa app.

Cada app escucha solo en `127.0.0.1` y expone sus tools en `/mcp` (MCP streamable HTTP, sin sesión); desde fuera del móvil se llega por la pasarela, en `/<app>/mcp`, y prefija sus nombres con el de la app (`hola_saludar`).

Las migraciones deben ser compatibles con la versión anterior (añadir columnas o tablas, no borrarlas en el mismo cambio): la vuelta atrás cambia el binario, no la base de datos. Antes de cada cambio de versión queda un `pg_dump` en `~/srv/backups`.

## Acceso público

Tailscale Funnel publica `https://xperia-web.tail36c376.ts.net` y envía todo a la pasarela (`127.0.0.1:8100`), que pide login antes de llegar al panel (`/`) o a las apps (`/hola/`, …). Son dos ámbitos separados, cada uno con su cookie y sus sesiones. El tráfico no pasa por Cloudflare, que en España se bloquea durante los partidos. En el móvil, Tailscale corre en modo userspace como un nodo aparte (`xperia-web`, servicio `tailscale_web`) compilado con `GOOS=android`, porque la app de Android no hace Funnel.

- **Panel (administrador):** contraseña (`gateway passwd`, argon2id) y código TOTP (`gateway totp`, con 8 códigos de recuperación de un solo uso).
  - Las sesiones viven en el servidor (30 días como máximo, 7 sin uso) y se pueden cerrar desde el panel.
  - Límites de intentos fallidos: 5 por IP y 30 en total cada 15 minutos, y como mucho 2 cálculos argon2 a la vez.
- **Apps (usuarios):** cuentas propias, con usuario y contraseña y sin 2FA, que se gestionan en la sección «Usuarios» del panel.
  - Cada cuenta tiene sus apps permitidas y un rol: `usuario`, o `admin`, que gestiona el contenido compartido de todos.
  - Se entra por `/<app>/_login`, dentro de la ruta de la app, para que una app instalada no salga de su ventana. Cada persona cambia su contraseña y cierra sus otros dispositivos en `/<app>/_cuenta`.
  - La sesión dura mientras se use: 90 días sin uso, con la cookie renovada al navegar.
  - Una sesión de app no abre el panel ni las apps que esa cuenta no tenga permitidas.
  - Límites de intentos: los mismos que el panel, más 10 fallos por cuenta cada 15 minutos.
  - El manifiesto y los iconos de cada app son públicos, porque el navegador los pide sin cookies al instalarla.
- **Agente y clientes MCP:** `Authorization: Bearer <token>`. Cada token vale para unas apps concretas, caduca y puede actuar en nombre de un usuario (`gateway token new <nombre> [apps] [días] [usuario]` o desde el panel). Los tokens no abren el panel.
- **Identidad hacia las apps:** reciben `X-Auth-User`, `X-Auth-Name` (codificado como URL) y `X-Auth-Role`, nunca el token ni las cookies. La pasarela borra esas cabeceras si las manda el cliente, y en Go se leen con `app.User(r)`.
- **Protecciones de las peticiones:**
  - Las que no son GET deben venir del mismo origen (`Origin`/`Sec-Fetch-Site`), y los formularios del panel llevan token CSRF.
  - CSP estricta en la pasarela, y otra básica (sin iframes) para las apps que no pongan la suya.
  - HSTS.
  - Límites de tamaño y de tiempo por petición, y como mucho 64 peticiones simultáneas.
- **Auditoría** en `~/srv/state/audit.jsonl`: logins del panel y de las apps, fallos, tokens, usuarios y acciones del panel.
- Las apps deben usar enlaces relativos o respetar `X-Forwarded-Prefix`. Las redirecciones absolutas se corrigen solas.

## Panel

`/` es el panel de inicio. Es una PWA instalable, con tema claro u oscuro según el sistema, sin CDN externos ni scripts en línea. Tiene cuatro secciones:
- **Apps:** estado por `/health`, versión, abrir y reiniciar.
- **Servicios runit.**
- **Móvil:** batería, temperatura, memoria, disco, wifi y eventos de `netwatch`.
- **Despliegues:** versión activa, historial del updater, versiones descartadas y copias de la base de datos.
- **Seguridad:** sesiones, tokens y auditoría.

Cada sección se refresca sola.

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
