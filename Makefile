GO ?= go
GOFMT ?= gofmt
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

.PHONY: build test test-race fmt-check vet check cross clean

build:
	mkdir -p dist
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/mnc-station ./cmd/mnc-station

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

fmt-check:
	@unformatted="$$($(GOFMT) -l .)"; test -z "$$unformatted" || { printf '%s\n' "$$unformatted" >&2; exit 1; }

vet:
	$(GO) vet ./...

check: fmt-check vet test

cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/mnc-station-linux-amd64 ./cmd/mnc-station
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/mnc-station-linux-arm64 ./cmd/mnc-station
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/mnc-station-windows-amd64.exe ./cmd/mnc-station

clean:
	rm -rf -- dist
