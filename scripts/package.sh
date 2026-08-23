#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

cargo +nightly build --release

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
    linux | darwin) ;;
    *) echo "package: unsupported os: $os" >&2; exit 1 ;;
esac

arch=$(uname -m)
case $arch in
    x86_64 | amd64) arch=x86_64 ;;
    aarch64 | arm64) arch=aarch64 ;;
    *) echo "package: unsupported arch: $arch" >&2; exit 1 ;;
esac

out="ax-$os-$arch"
cp target/release/ax "$out"

if command -v sha256sum >/dev/null 2>&1; then
    hash=$(sha256sum "$out" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    hash=$(shasum -a 256 "$out" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
    hash=$(openssl dgst -sha256 "$out" | awk '{print $NF}')
else
    echo "package: sha256sum, shasum, or openssl is required" >&2
    exit 1
fi
printf '%s  %s\n' "$hash" "$out" > "$out.sha256"

echo "$out"
