#!/bin/sh
# Build release artifacts for the current platform.
#
#   scripts/package.sh all
#   scripts/package.sh ax fsx
#
# Writes dist/<program>-<os>-<arch> and .sha256 next to it. The release
# workflow runs this on each target runner and merges dist/ into one release.
set -eu

cd "$(dirname "$0")/.."

case "${1:-}" in
"" | all)
    set -- $(ls cmd)
    ;;
esac

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
linux | darwin) ;;
*)
    echo "package: unsupported os: $os" >&2
    exit 1
    ;;
esac

arch=$(uname -m)
case $arch in
x86_64 | amd64)
    goarch=amd64
    arch=x86_64
    ;;
aarch64 | arm64)
    goarch=arm64
    arch=aarch64
    ;;
*)
    echo "package: unsupported arch: $arch" >&2
    exit 1
    ;;
esac

case $os in
linux) goos=linux ;;
darwin) goos=darwin ;;
esac

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        echo "package: sha256sum, shasum, or openssl is required" >&2
        return 1
    fi
}

mkdir -p dist
for program in "$@"; do
    case $program in
    '' | [!a-z]* | *[!a-z0-9-]*)
        echo "package: invalid program name: $program" >&2
        exit 1
        ;;
    esac
    if [ ! -d "cmd/$program" ]; then
        echo "package: no such program: $program" >&2
        exit 1
    fi
    out="dist/$program-$os-$arch"
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build \
        -trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
        -o "$out" "./cmd/$program"
    printf '%s  %s\n' "$(sha256 "$out")" "$(basename "$out")" >"$out.sha256"
    printf '%s\n' "$out"
done
