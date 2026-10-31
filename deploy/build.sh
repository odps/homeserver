#!/usr/bin/env bash
# Compila todas las apps y el updater para cada plataforma y deja en dist/ un
# homeserver_<os>_<arch>.tar.gz por plataforma más checksums.txt.
# Uso: deploy/build.sh <tag>
set -euo pipefail
tag=${1:?uso: build.sh <tag>}
cd "$(dirname "$0")/.."

# android/arm64: el móvil (Termux); platform/netfix arregla el DNS allí.
# linux/*: Raspberry Pi y VPS.
targets=(android/arm64 linux/arm64 linux/amd64)
TAILSCALE_VERSION=v1.104.4
rm -rf dist && mkdir -p dist

for target in "${targets[@]}"; do
	os=${target%/*} arch=${target#*/}
	stage=dist/stage/${os}_${arch}
	mkdir -p "$stage/bin"
	for dir in apps/* cmd/*; do
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
			-ldflags "-s -w -X homeserver/platform/version.Version=$tag" \
			-o "$stage/bin/$(basename "$dir")" "./$dir"
	done
	# Solo en el móvil: Tailscale en modo userspace para Funnel. El binario
	# oficial para Linux muere en Android (seccomp bloquea faccessat2) y la app
	# de Android no hace Funnel, así que se compila con GOOS=android.
	if [ "$os" = android ]; then
		for c in tailscaled tailscale; do
			CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOFLAGS=-trimpath GOBIN= \
				go install -tags ts_omit_ssh,ts_omit_taildrop,ts_omit_systray -ldflags "-s -w" \
				"tailscale.com/cmd/$c@$TAILSCALE_VERSION"
			cp "$(go env GOPATH)/bin/${os}_${arch}/$c" "$stage/bin/"
		done
	fi
	cp deploy/apps.json "$stage/"
	echo "$tag" > "$stage/VERSION"
	tar -czf "dist/homeserver_${os}_${arch}.tar.gz" -C "$stage" .
done

rm -rf dist/stage
(cd dist && sha256sum *.tar.gz > checksums.txt)
ls -l dist
