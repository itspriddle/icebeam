BINARY := icebeam
BIN_DIR := bin
PKG := github.com/itspriddle/icebeam
CMD := ./cmd/icebeam

# Version metadata injected at build time. Real values are supplied by the
# release pipeline (US-012); these defaults cover local builds.
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(PKG)/internal/version.Version=$(VERSION) \
           -X $(PKG)/internal/version.Commit=$(COMMIT) \
           -X $(PKG)/internal/version.Date=$(DATE)

.PHONY: build test vet lint check clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)

test:
	go test -race -cover ./...

vet:
	go vet ./...

lint:
	golangci-lint run

# check runs the full Quality Gate (matches the PRD's ## Quality Gate section).
check: test vet lint

clean:
	rm -rf $(BIN_DIR)
