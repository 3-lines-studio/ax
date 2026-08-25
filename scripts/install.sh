#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
    set -- ax taxi
fi

requested="$*"
set --
for package in $requested; do
    case $package in
        axi) set -- "$@" ax axis fsx bashx skillx attachx axi ;;
        *) set -- "$@" "$package" ;;
    esac
done

prefix="${AX_PREFIX:-${PREFIX:-$HOME/.local}}"
bindir="$prefix/bin"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
    linux | darwin) ;;
    *) echo "install: unsupported os: $os" >&2; exit 1 ;;
esac
arch=$(uname -m)
case $arch in
    x86_64 | amd64) arch=x86_64 ;;
    aarch64 | arm64) arch=aarch64 ;;
    *) echo "install: unsupported arch: $arch" >&2; exit 1 ;;
esac
download() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$1" -O "$2"
    else
        echo "install: curl or wget is required" >&2
        return 1
    fi
}

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        echo "install: sha256sum, shasum, or openssl is required" >&2
        return 1
    fi
}

version_for() {
    case $1 in
        ax) printf '%s' "${AX_VERSION:-${VERSION:-}}" ;;
        axi) printf '%s' "${AXI_VERSION:-${VERSION:-}}" ;;
        taxi) printf '%s' "${TAXI_VERSION:-${VERSION:-}}" ;;
        *) printf '%s' "${VERSION:-}" ;;
    esac
}

umask 077
tmp="${TMPDIR:-/tmp}/ax-install-$$"
if ! mkdir "$tmp"; then
    echo "install: cannot create temporary directory: $tmp" >&2
    exit 1
fi
trap 'rm -rf "$tmp"' 0 HUP INT TERM

for package in "$@"; do
    case $package in
        '' | [!a-z]* | *[!a-z0-9-]*)
            echo "install: invalid package name: $package" >&2
            exit 1
            ;;
    esac
    version=$(version_for "$package")
    if [ -n "${INSTALL_BASE_URL:-}" ]; then
        base="$INSTALL_BASE_URL/$package"
    elif [ -n "$version" ]; then
        base="https://github.com/3-lines-studio/$package/releases/download/$version"
    else
        base="https://github.com/3-lines-studio/$package/releases/latest/download"
    fi
    artifact="$package-$os-$arch"
    echo "downloading $artifact"
    download "$base/$artifact" "$tmp/$package" || {
        echo "install: download failed: $base/$artifact" >&2
        exit 1
    }
    download "$base/$artifact.sha256" "$tmp/$package.sha256" || {
        echo "install: checksum fetch failed: $base/$artifact.sha256" >&2
        exit 1
    }
    want=$(awk '{print $1}' "$tmp/$package.sha256")
    got=$(sha256 "$tmp/$package")
    if [ "$want" != "$got" ]; then
        echo "install: checksum mismatch for $package" >&2
        exit 1
    fi
done

mkdir -p "$bindir"
for package in "$@"; do
    staged="$bindir/.$package.new.$$"
    cp "$tmp/$package" "$staged"
    chmod 0755 "$staged"
    mv "$staged" "$bindir/$package"
    echo "installed $package to $bindir/$package"
done

case ":$PATH:" in
    *":$bindir:"*) ;;
    *) echo "install: $bindir is not on PATH" >&2 ;;
esac
