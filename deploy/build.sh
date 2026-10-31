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
	cp deploy/apps.json "$stage/"
	echo "$tag" > "$stage/VERSION"
	tar -czf "dist/homeserver_${os}_${arch}.tar.gz" -C "$stage" .
done

rm -rf dist/stage
(cd dist && sha256sum *.tar.gz > checksums.txt)
ls -l dist
