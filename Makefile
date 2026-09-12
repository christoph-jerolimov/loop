.PHONY: build test lint install

build:
	go build -o bin/loop ./cmd/loop

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

install:
	go install ./cmd/loop
