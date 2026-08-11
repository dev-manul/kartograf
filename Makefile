GOTAGS := sqlite_fts5
GOFLAGS := -tags $(GOTAGS)

.PHONY: build install test vet fmt check

build:
	go build $(GOFLAGS) ./...

install:
	go install $(GOFLAGS) ./cmd/kartograf

test:
	go test $(GOFLAGS) ./...

vet:
	go vet $(GOFLAGS) ./...

fmt:
	gofmt -l . | grep -v testdata || true

check: build vet test fmt
