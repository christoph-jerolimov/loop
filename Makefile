.PHONY: build test lint install

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/christoph-jerolimov/loop/internal/cli.Version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/loop ./cmd/loop

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/loop
