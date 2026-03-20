.PHONY: build clean test lint install release

BINARY := opdev
SRC := ./src
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
PLATFORM ?= $(shell uname -s | tr A-Z a-z)/$(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')

# Build using Docker if go is not on PATH.
HAS_GO := $(shell command -v go 2>/dev/null)

build:
ifdef HAS_GO
	go build $(LDFLAGS) -o $(BINARY) $(SRC)
else
	DOCKER_BUILDKIT=1 docker build --platform $(PLATFORM) --build-arg VERSION=$(VERSION) --output type=local,dest=. --target export .
endif

install: build
	sudo install $(BINARY) /usr/local/bin/$(BINARY)
	@echo "installed to /usr/local/bin/$(BINARY)"

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
