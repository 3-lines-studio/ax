#!/bin/sh
# Build release artifacts.
#
#   scripts/package.sh all              # every program, current platform
#   scripts/package.sh ax fsx           # selected programs, current platform
#   scripts/package.sh --release all    # every program, all release targets
#
# Writes dist/<program>-<os>-<arch> and .sha256 next to it. `--release` builds
# linux/x86-64, linux/arm64 and darwin/arm64 from any host (CGO disabled), so
# CI needs one Linux runner instead of a matrix.
set -eu

cd "$(dirname "$0")/.."

release=false
case "${1:-}" in
--release)
    release=true
    shift
    ;;
esac

case "${1:-}" in
"" | all)
    set -- $(ls cmd)
    ;;
esac

if [ "$release" = true ]; then
    # os:goarch:artifact-arch
    targets="linux:amd64:x86_64 linux:arm64:aarch64 darwin:arm64:aarch64"
else
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case $os in
    linux | darwin) ;;
    *)
        echo "package: unsupported os: $os" >&2
        exit 1
        ;;
    esac
    case $(uname -m) in
    x86_64 | amd64)
        goarch=amd64
        arch=x86_64
        ;;
    aarch64 | arm64)
        goarch=arm64
        arch=aarch64
        ;;
    *)
        echo "package: unsupported arch: $(uname -m)" >&2
        exit 1
        ;;
    esac
    targets="$os:$goarch:$arch"
fi

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
for target in $targets; do
    goos=${target%%:*}
    rest=${target#*:}
    goarch=${rest%%:*}
    arch=${rest#*:}
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
        out="dist/$program-$goos-$arch"
        CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build \
            -trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
            -o "$out" "./cmd/$program"
        printf '%s  %s\n' "$(sha256 "$out")" "$(basename "$out")" >"$out.sha256"
        printf '%s\n' "$out"
    done
done
