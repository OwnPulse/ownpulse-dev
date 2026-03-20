.PHONY: build clean test lint install release

BINARY := opdev
SRC := ./src
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY) $(SRC)

install: build
	mv $(BINARY) $(shell go env GOPATH)/bin/$(BINARY)
	@echo "installed to $(shell go env GOPATH)/bin/$(BINARY)"

test:
	go test ./...

lint:
	go vet ./...
	@which golangci-lint > /dev/null && golangci-lint run || echo "golangci-lint not installed, skipping"

clean:
	rm -f $(BINARY)

# Cross-compile all release targets (used by CI)
release:
	GOOS=darwin  GOARCH=arm64  go build $(LDFLAGS) -o dist/$(BINARY)-darwin-arm64  $(SRC)
	GOOS=darwin  GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-darwin-amd64  $(SRC)
	GOOS=linux   GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64   $(SRC)
	GOOS=linux   GOARCH=arm64  go build $(LDFLAGS) -o dist/$(BINARY)-linux-arm64   $(SRC)
	GOOS=windows GOARCH=amd64  go build $(LDFLAGS) -o dist/$(BINARY)-windows-amd64.exe $(SRC)
