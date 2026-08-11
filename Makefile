# kartograf build. cgo обязателен (tree-sitter, mattn/go-sqlite3),
# FTS5 включается build-тегом sqlite_fts5 — без него сборка падает
# намеренно (см. internal/core/store/fts5guard.go).

GOTAGS  := sqlite_fts5
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
GOFLAGS := -tags $(GOTAGS) $(LDFLAGS)
BIN     := bin/kartograf

.PHONY: all build install test vet fmt check clean version

all: build

## build: собрать бинарь в bin/kartograf
build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/kartograf

## install: установить в $GOPATH/bin (его смотрят MCP-конфиги)
install:
	go install $(GOFLAGS) ./cmd/kartograf

## test: юнит- и интеграционные тесты
test:
	go test -tags $(GOTAGS) ./...

## vet: статический анализ
vet:
	go vet -tags $(GOTAGS) ./...

## fmt: проверка форматирования (testdata-фикстуры игнорируются)
fmt:
	@out=$$(gofmt -l . | grep -v testdata); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

## check: полная проверка перед коммитом
check: vet test fmt build

## clean: убрать артефакты сборки
clean:
	rm -rf bin

## version: показать версию, которая будет зашита в бинарь
version:
	@echo $(VERSION)
