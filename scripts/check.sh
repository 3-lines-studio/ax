#!/bin/sh
# Quality gate. Same steps as `make check`, without requiring make.
#
#   scripts/check.sh          full gate (CI on code changes)
#   scripts/check.sh --fast   formatting, module and catalog only (docs-only
#                             changes, quick local loop)
set -eu

cd "$(dirname "$0")/.."

mode=full
case "${1:-}" in
"") ;;
--fast) mode=fast ;;
*)
    echo "usage: check.sh [--fast]" >&2
    exit 2
    ;;
esac

bin=bin
programs=$(ls cmd)

step() {
    printf '\n== %s\n' "$1"
}

step "gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
    echo "gofmt required:" >&2
    echo "$unformatted" >&2
    exit 1
fi

step "go mod tidy"
go mod tidy -diff

step "catalog"
python3 scripts/render-catalog.py --check

if [ "$mode" = fast ]; then
    echo
    echo "fast checks passed"
    exit 0
fi

step "go vet"
go vet ./...

step "build"
mkdir -p "$bin"
for program in $programs; do
    go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "$bin/$program" "./cmd/$program"
done

step "size"
./scripts/check-size.sh "$bin"

step "test"
go test ./...

step "race"
cc=$(go env CC)
if command -v "$cc" >/dev/null 2>&1; then
    CGO_ENABLED=1 go test -race ./...
else
    echo "race: skipped: C compiler $cc not found (CI runs this step)"
fi

step "provider contract"
python3 scripts/provider-contract.py "$bin/fsx" "$bin/bashx" "$bin/skillx"

step "harness"
python3 scripts/harness.py --bin "$bin/ax"
