#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
    set -- ax axi
fi

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
command -v curl >/dev/null 2>&1 || { echo "install: curl is required" >&2; exit 1; }

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

version_for() {
    case $1 in
        ax) printf '%s' "${AX_VERSION:-${VERSION:-}}" ;;
        axi) printf '%s' "${AXI_VERSION:-${VERSION:-}}" ;;
        *) printf '%s' "${VERSION:-}" ;;
    esac
}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ax-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

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
    curl -fsSL "$base/$artifact" -o "$tmp/$package" || {
        echo "install: download failed: $base/$artifact" >&2
        exit 1
    }
    curl -fsSL "$base/$artifact.sha256" -o "$tmp/$package.sha256" || {
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
    install -m 0755 "$tmp/$package" "$bindir/$package"
    echo "installed $package to $bindir/$package"
done

case ":$PATH:" in
    *":$bindir:"*) ;;
    *) echo "install: $bindir is not on PATH" >&2 ;;
esac
