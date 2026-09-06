PREFIX ?= $(HOME)/.local
BIN := ax

.PHONY: check build run dev harness install clean

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test ./...
	go test -race ./...
	$(MAKE) build
	python3 scripts/harness.py --bin $(BIN)

build:
	go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o $(BIN) .

run: build
	./$(BIN)

dev:
	go build -o $(BIN) .
	./$(BIN)

harness:
	$(MAKE) build
	python3 scripts/harness.py --bin $(BIN)

install: build
	install -d "$(PREFIX)/bin"
	install -m 0755 $(BIN) "$(PREFIX)/bin/ax"

clean:
	rm -f $(BIN)
