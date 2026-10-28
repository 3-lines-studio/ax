GO ?= go
PREFIX ?= $(HOME)/.local
BIN := bin

PROGRAMS := $(notdir $(wildcard cmd/*))

LDFLAGS := -s -w -buildid=

.PHONY: all build test race vet fmt tidy check catalog catalog-check size contract harness eval install dist clean FORCE

all: build

build: $(addprefix $(BIN)/,$(PROGRAMS))

$(BIN)/%: FORCE
	@mkdir -p $(BIN)
	$(GO) build -trimpath -buildvcs=false -ldflags='$(LDFLAGS)' -o $@ ./cmd/$*

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt required:"; echo "$$out"; exit 1; fi

tidy:
	$(GO) mod tidy -diff

check:
	./scripts/check.sh

catalog:
	python3 scripts/render-catalog.py

catalog-check:
	python3 scripts/render-catalog.py --check

size: build
	./scripts/check-size.sh $(BIN)

contract: build
	python3 scripts/provider-contract.py $(BIN)/fsx $(BIN)/bashx $(BIN)/skillx

harness: $(BIN)/ax
	python3 scripts/harness.py --bin $(BIN)/ax

eval: $(BIN)/ax
	AX_BIN=$(BIN)/ax python3 scripts/eval.py

install: build
	install -d "$(PREFIX)/bin"
	@for program in $(PROGRAMS); do \
		install -m 0755 "$(BIN)/$$program" "$(PREFIX)/bin/$$program"; \
	done

dist:
	./scripts/package.sh all

clean:
	rm -rf $(BIN) dist

FORCE:
