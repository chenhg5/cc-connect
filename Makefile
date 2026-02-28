BINARY  := cc-connect
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build run clean test lint

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/cc-connect

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

test:
	go test -v ./...

lint:
	golangci-lint run ./...
