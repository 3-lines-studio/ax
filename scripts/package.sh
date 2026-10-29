#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

release=false
dest=
while [ $# -gt 0 ]; do
    case $1 in
    --release)
        release=true
        shift
        ;;
    --dest)
        if [ -z "${2:-}" ]; then
            echo "package: --dest needs a directory" >&2
            exit 1
        fi
        dest=$2
        shift 2
        ;;
    *)
        break
        ;;
    esac
done

if [ "$release" = true ] && [ -n "$dest" ]; then
    echo "package: --release and --dest are mutually exclusive" >&2
    exit 1
fi

case "${1:-}" in
"" | all)
    set -- $(ls cmd)
    ;;
esac

if [ "$release" = true ]; then
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

if [ -n "$dest" ]; then
    mkdir -p "$dest"
else
    mkdir -p dist
fi
release_files=
for target in $targets; do
    goos=${target%%:*}
    rest=${target#*:}
    goarch=${rest%%:*}
    arch=${rest#*:}
    if [ "$release" = true ]; then
        stage="dist/.stage-$goos-$arch"
        rm -rf "$stage"
        mkdir -p "$stage"
    fi
    binaries=
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
        if [ "$release" = true ]; then
            out="$stage/$program"
        elif [ -n "$dest" ]; then
            out="$dest/$program"
        else
            out="dist/$program-$goos-$arch"
        fi
        CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build \
            -trimpath -buildvcs=false -ldflags='-s -w -buildid=' \
            -o "$out" "./cmd/$program"
        if [ -n "$dest" ]; then
            printf '%s\n' "$out"
            continue
        fi
        if [ "$release" = true ]; then
            binaries="$binaries $program"
        else
            printf '%s  %s\n' "$(sha256 "$out")" "$(basename "$out")" >"$out.sha256"
            printf '%s\n' "$out"
        fi
    done
    if [ "$release" = true ]; then
        archive="dist/ax-$goos-$arch.tar.gz"
        tar -czf "$archive" -C "$stage" $binaries
        rm -rf "$stage"
        printf '%s\n' "$archive"
        release_files="$release_files $(basename "$archive")"
    fi
done

if [ "$release" = true ]; then
    : >dist/SHA256SUMS
    for name in $release_files; do
        printf '%s  %s\n' "$(sha256 "dist/$name")" "$name" >>dist/SHA256SUMS
    done
    cat dist/SHA256SUMS
fi
