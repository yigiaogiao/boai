#!/usr/bin/env bash
# Builds the LazyCat candidate package without Docker.
#
# The static Go server and the Vite frontend are copied into lazycat/content,
# which lzc-cli turns into content.tar. The official LazyCat runtime starts
# /lzcapp/pkg/content/boai; no image is built or pushed.
set -euo pipefail
cd "$(dirname "$0")/.."

GO="${GO:-go}"
LZC_CLI=()
if [ -n "${LZC_CLI_BIN:-}" ]; then
	LZC_CLI=("$LZC_CLI_BIN")
elif command -v lzc-cli >/dev/null 2>&1; then
	LZC_CLI=("$(command -v lzc-cli)")
elif [ -x "$HOME/.local/share/lzctools/node_modules/.bin/lzc-cli" ]; then
	LZC_CLI=("$HOME/.local/share/lzctools/node_modules/.bin/lzc-cli")
else
	LZC_CLI=(npx --yes @lazycatcloud/lzc-cli@2.0.9)
fi

version="$(sed -n 's/^version:[[:space:]]*//p' lazycat/package.yml | head -n1)"
if [ -z "$version" ]; then
	echo "lazycat/package.yml has no version" >&2
	exit 1
fi

echo "building the frontend"
npm --prefix web run build

echo "building the server for linux/amd64 (version $version)"
rm -rf lazycat/content
mkdir -p lazycat/content/web
# The runtime is musl-based Alpine. Link the Go binary against static libc so
# it does not need a glibc loader, and check before packing. The runtime may
# also ship without a CA bundle, so one is packed beside the binary.
ldflags="-s -w -X main.version=${version} -linkmode external -extldflags \"-static\""
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 "$GO" build -tags netgo,osusergo \
	-trimpath -ldflags "$ldflags" -o lazycat/content/boai ./cmd/boai
if ! file lazycat/content/boai | grep -q 'statically linked'; then
	echo "the server binary is not statically linked; it will not run on Alpine" >&2
	exit 1
fi

cp -R web/dist/. lazycat/content/web/
cp /etc/ssl/certs/ca-certificates.crt lazycat/content/ca-certificates.crt

find lazycat/content -type d -exec chmod 755 {} \;
find lazycat/content -type f -exec chmod 644 {} \;
chmod 755 lazycat/content/boai

echo "linting the project"
(cd lazycat && "${LZC_CLI[@]}" project lint . -f lzc-build.yml)

echo "packing"
mkdir -p lazycat/dist
(cd lazycat && "${LZC_CLI[@]}" project build . -f lzc-build.yml)
ls -lh lazycat/dist/*.lpk

# The user authorized automatic netdisk delivery on 2026-11-18: every new
# candidate is copied to the LazyCat netdisk so it can be side-loaded from the
# client without asking again. Skip with BOAI_SKIP_NETDISK_COPY=1.
if [ "${BOAI_SKIP_NETDISK_COPY:-0}" != "1" ] && [ -d /lzcapp/document ] && [ -w /lzcapp/document ]; then
	package="$(sed -n 's/^package:[[:space:]]*//p' lazycat/package.yml | head -n1)"
	lpk="lazycat/dist/${package}-v${version}.lpk"
	if [ ! -f "$lpk" ]; then
		echo "expected package $lpk was not generated" >&2
		exit 1
	fi
	dest="/lzcapp/document/$(basename "$lpk")"
	cp -f "$lpk" "$dest"
	if ! cmp -s "$lpk" "$dest"; then
		echo "netdisk copy verification failed for $dest" >&2
		exit 1
	fi
	echo "copied to the LazyCat netdisk: $dest"
	echo "SHA-256: $(sha256sum "$dest" | cut -d' ' -f1)"
fi
