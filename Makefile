VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@latest

.PHONY: all build test race lint fmt tidy proto clean

all: lint test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/vaultline ./cmd/vaultline

test:
	go test ./...

race:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
	go vet ./...
	go run $(STATICCHECK) ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

# Protobuf generation arrives with the first .proto file (F3).
proto:
	@if [ -d proto ]; then buf generate; else echo "no proto/ yet"; fi

clean:
	go clean
	rm -f bin/vaultline
