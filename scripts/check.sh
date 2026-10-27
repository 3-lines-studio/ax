#!/bin/sh
# Full quality gate. Same steps as `make check`, without requiring make.
set -eu

cd "$(dirname "$0")/.."

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

step "go vet"
go vet ./...

step "catalog"
python3 scripts/render-catalog.py --check

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
