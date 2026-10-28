#!/bin/sh
# Install AX programs. One download for the whole set: the release ships one
# archive per platform, so asking for one program or all of them costs the same.
#
#   install.sh                 # every program in the release
#   install.sh ax fsx bashx    # only these
#   install.sh update          # re-install what is already here, if it changed
#   install.sh uninstall ax    # remove these (no names: all installed programs)
#
# Environment:
#   AX_PREFIX / PREFIX   install root; binaries go to <root>/bin (default ~/.local)
#   VERSION              release tag to install (default: latest)
#   INSTALL_BASE_URL     directory holding ax-<os>-<arch>.tar.gz and SHA256SUMS
set -eu

action=install
case "${1:-}" in
    uninstall | update) action=$1; shift ;;
esac

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
archive="ax-$os-$arch.tar.gz"

if [ -n "${INSTALL_BASE_URL:-}" ]; then
    base="${INSTALL_BASE_URL%/}"
elif [ -n "${VERSION:-}" ]; then
    base="https://github.com/3-lines-studio/ax/releases/download/$VERSION"
else
    base="https://github.com/3-lines-studio/ax/releases/latest/download"
fi

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

valid_name() {
    case $1 in
        '' | [!a-z]* | *[!a-z0-9-]*) return 1 ;;
    esac
}

umask 022
tmp="${TMPDIR:-/tmp}/ax-install-$$"
if ! mkdir -p "$tmp"; then
    echo "install: cannot create temporary directory: $tmp" >&2
    exit 1
fi
trap 'rm -rf "$tmp"' 0 HUP INT TERM

for package in "$@"; do
    if ! valid_name "$package"; then
        echo "install: invalid package name: $package" >&2
        exit 1
    fi
done

if [ "$action" = uninstall ]; then
    if [ "$#" -eq 0 ]; then
        echo "install: name the programs to remove (ax fsx ...)" >&2
        exit 1
    fi
    for package in "$@"; do
        rm -f "$bindir/$package"
        echo "removed $bindir/$package"
    done
    exit 0
fi

echo "downloading $archive"
download "$base/$archive" "$tmp/$archive" || {
    echo "install: download failed: $base/$archive" >&2
    exit 1
}
download "$base/SHA256SUMS" "$tmp/SHA256SUMS" || {
    echo "install: checksum fetch failed: $base/SHA256SUMS" >&2
    exit 1
}
want=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/SHA256SUMS")
if [ -z "$want" ]; then
    echo "install: $archive is not listed in SHA256SUMS" >&2
    exit 1
fi
if [ "$want" != "$(sha256 "$tmp/$archive")" ]; then
    echo "install: checksum mismatch for $archive" >&2
    exit 1
fi

mkdir -p "$tmp/unpack"
tar -xzf "$tmp/$archive" -C "$tmp/unpack"

if [ "$#" -eq 0 ]; then
    if [ "$action" = update ]; then
        # Update without names refreshes what is already in $bindir.
        set -- $(for program in $(ls "$tmp/unpack" | sort); do
            [ -f "$bindir/$program" ] && echo "$program"
        done)
        if [ "$#" -eq 0 ]; then
            echo "install: nothing to update in $bindir" >&2
            exit 0
        fi
    else
        set -- $(ls "$tmp/unpack" | sort)
    fi
fi

todo=
for package in "$@"; do
    if [ ! -f "$tmp/unpack/$package" ]; then
        echo "install: $package is not in $archive" >&2
        exit 1
    fi
    if [ "$action" = update ] && cmp -s "$tmp/unpack/$package" "$bindir/$package"; then
        echo "$package is up to date"
        continue
    fi
    if [ "$action" = update ]; then
        echo "updating $package"
    fi
    todo="$todo $package"
done

if [ -z "$todo" ]; then
    exit 0
fi

mkdir -p "$bindir"
for package in $todo; do
    staged="$bindir/.$package.new.$$"
    cp "$tmp/unpack/$package" "$staged"
    chmod 0755 "$staged"
    mv "$staged" "$bindir/$package"
    echo "installed $package to $bindir/$package"
done

case ":$PATH:" in
    *":$bindir:"*) ;;
    *) echo "install: $bindir is not on PATH; add it with: export PATH=\"$bindir:\$PATH\"" >&2 ;;
esac

if [ -x "$bindir/webi" ]; then
    if [ ! -x "$bindir/ax" ]; then
        echo "install: webi needs ax; install it with: install.sh ax" >&2
    fi
    echo "next: webi -C ~/bots/me"
fi
