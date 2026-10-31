#!/data/data/com.termux/files/usr/bin/sh
# Prepara Termux para homeserver. Se ejecuta una vez en el móvil y es seguro
# repetirlo: no pisa datos ni contraseñas existentes.
#
#   sh bootstrap.sh <owner/repo>
#
# Deja: PostgreSQL 18 como servicio runit con la base "homeserver", el servicio
# homeserver-updater y el arranque de runit en ~/.termux/boot.
set -eu
repo=${1:?uso: bootstrap.sh <owner/repo>}
srv=$HOME/srv
pgdata=$PREFIX/var/lib/postgresql
svdir=$PREFIX/var/service
admin_dsn="postgres://postgres@/homeserver?host=$PREFIX/tmp"

echo "== Paquetes"
pkg install -y termux-services postgresql

echo "== Directorios"
mkdir -p "$srv/releases" "$srv/env" "$srv/backups" "$srv/state"
chmod 700 "$srv" "$srv/env" "$srv/backups"

echo "== PostgreSQL"
if [ ! -f "$pgdata/PG_VERSION" ]; then
	initdb -D "$pgdata" -U postgres --auth-local=trust --auth-host=scram-sha-256 -E UTF8 --locale=C
	cat >> "$pgdata/postgresql.conf" <<-EOF

	# homeserver: ajustes para ~1,4 GB de RAM libre (benchmarks del 31-10-2026)
	listen_addresses = '127.0.0.1'
	max_connections = 30
	shared_buffers = 32MB
	work_mem = 2MB
	maintenance_work_mem = 16MB
	effective_cache_size = 256MB
	wal_buffers = 4MB
	max_wal_size = 256MB
	EOF
	# Solo el socket local (el usuario de Termux) entra sin contraseña; por TCP,
	# cada app con su rol y contraseña.
	cat > "$pgdata/pg_hba.conf" <<-EOF
	local all postgres           trust
	host  all all 127.0.0.1/32   scram-sha-256
	EOF
fi

# Arranca runit si no está en marcha y activa PostgreSQL.
set +eu
. "$PREFIX/etc/profile.d/start-services.sh"
set -eu
sv-enable postgres
for _ in $(seq 30); do pg_isready -q -h "$PREFIX/tmp" && break; sleep 1; done
psql -h "$PREFIX/tmp" -U postgres -tAc "SELECT 1 FROM pg_database WHERE datname = 'homeserver'" | grep -q 1 ||
	createdb -h "$PREFIX/tmp" -U postgres homeserver

echo "== Updater"
if [ ! -f "$srv/env/updater.env" ]; then
	umask 077
	cat > "$srv/env/updater.env" <<-EOF
	REPO=$repo
	ADMIN_DATABASE_URL=$admin_dsn
	EOF
	umask 022
fi

# Primera versión: la descarga a mano; desde ahí la gestiona el updater.
if [ ! -e "$srv/current" ]; then
	tmp=$(mktemp -d)
	curl -fsSL -o "$tmp/pkg.tar.gz" "https://github.com/$repo/releases/latest/download/homeserver_android_arm64.tar.gz"
	curl -fsSL -o "$tmp/checksums.txt" "https://github.com/$repo/releases/latest/download/checksums.txt"
	(cd "$tmp" && grep homeserver_android_arm64.tar.gz checksums.txt | sed 's/homeserver_android_arm64.tar.gz/pkg.tar.gz/' | sha256sum -c -)
	mkdir "$tmp/x" && tar -xzf "$tmp/pkg.tar.gz" -C "$tmp/x"
	tag=$(cat "$tmp/x/VERSION")
	mv "$tmp/x" "$srv/releases/$tag"
	ln -s "releases/$tag" "$srv/current"
	rm -rf "$tmp"
fi

mkdir -p "$svdir/homeserver-updater/log"
cat > "$svdir/homeserver-updater/run" <<EOF
#!$PREFIX/bin/sh
exec 2>&1
set -a
. $srv/env/updater.env
set +a
exec $srv/current/bin/updater
EOF
ln -sf "$PREFIX/share/termux-services/svlogger" "$svdir/homeserver-updater/log/run"
chmod 755 "$svdir/homeserver-updater/run"
sv-enable homeserver-updater

echo "== Arranque"
cat > "$HOME/.termux/boot/20-services" <<EOF
#!$PREFIX/bin/sh
# Arranca runit (PostgreSQL, updater y apps) tras el arranque del móvil.
termux-wake-lock
. $PREFIX/etc/profile.d/start-services.sh
EOF
chmod 700 "$HOME/.termux/boot/20-services"

echo "Listo. Logs en $PREFIX/var/log/sv/<servicio>/current"
