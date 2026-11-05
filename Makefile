GO ?= go
GOFMT ?= gofmt
VERSION ?= $(shell bash -c 'source packaging/git-version.sh; station_display_version "$$PWD"')
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILDINFO := github.com/Monutchee/Provisioning-Station/internal/buildinfo
LDFLAGS := -s -w -X $(BUILDINFO).Version=$(VERSION) -X $(BUILDINFO).Commit=$(COMMIT) -X $(BUILDINFO).BuildDate=$(BUILD_DATE)

.PHONY: build test test-race fmt-check vet check cross clean
build:
	mkdir -p dist
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/mps ./cmd/mps
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/mpsd ./cmd/mpsd

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
	@for binary in mps mpsd; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$binary-linux-amd64 ./cmd/$$binary || exit; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$binary-linux-arm64 ./cmd/$$binary || exit; \
	  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$binary-windows-amd64.exe ./cmd/$$binary || exit; \
	done

clean:
	rm -rf -- dist
