.PHONY: build test vet lint clean install run

GO ?= go
GOLINT ?= golangci-lint
BIN := tocy
PREFIX ?= /usr/local

build:
	$(GO) build -o $(BIN) ./cmd/tocy

run: build
	./$(BIN)

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	$(GOLINT) run ./...

clean:
	rm -f $(BIN)
	$(GO) clean -testcache

install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/

uninstall:
	rm -f $(PREFIX)/bin/$(BIN)
