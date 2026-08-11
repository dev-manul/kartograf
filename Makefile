# kartograf build. cgo is mandatory (tree-sitter, mattn/go-sqlite3);
# FTS5 is enabled by the sqlite_fts5 build tag — without it the build
# fails on purpose (see internal/core/store/fts5guard.go).

GOTAGS  := sqlite_fts5
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
GOFLAGS := -tags $(GOTAGS) $(LDFLAGS)
BIN     := bin/kartograf

.PHONY: all build install test vet fmt check clean version

all: build

## build: build the binary into bin/kartograf
build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/kartograf

## install: install into $GOPATH/bin (MCP configs point there)
install:
	go install $(GOFLAGS) ./cmd/kartograf

## test: unit and integration tests
test:
	go test -tags $(GOTAGS) ./...

## vet: static analysis
vet:
	go vet -tags $(GOTAGS) ./...

## fmt: formatting check (testdata fixtures are ignored)
fmt:
	@out=$$(gofmt -l . | grep -v testdata); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

## check: full pre-commit check
check: vet test fmt build

## clean: remove build artifacts
clean:
	rm -rf bin

## version: print the version that will be stamped into the binary
version:
	@echo $(VERSION)
