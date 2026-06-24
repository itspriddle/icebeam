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

.PHONY: build test vet lint fmt fmt-check tidy tidy-check surface check-surface \
        vuln race-test check release-check clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)

test:
	go test -race -cover ./...

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w .

# fmt-check fails (listing the offending files) if anything is unformatted.
fmt-check:
	@test -z "$$(gofmt -s -l . | tee /dev/stderr)" || (echo "Code is not formatted. Run 'make fmt'" && exit 1)

tidy:
	go mod tidy

# tidy-check verifies go.mod/go.sum are tidy without leaving changes behind.
tidy-check:
	go mod tidy
	git diff --exit-code go.mod go.sum

# surface regenerates the committed CLI surface snapshot after an intentional
# command/flag change.
surface:
	go test ./internal/cli/ -run TestSurface -update

# check-surface fails if the CLI surface drifts from the committed .surface.
check-surface:
	go test ./internal/cli/ -run TestSurface

vuln:
	govulncheck ./...

race-test:
	go test -race -count=1 ./...

# check runs the full Quality Gate (matches the PRD's ## Quality Gate section).
check: fmt-check vet lint test check-surface tidy-check

# release-check adds the slower pre-release gates (vulnerabilities, race) on top
# of the standard Quality Gate.
release-check: check vuln race-test

clean:
	rm -rf $(BIN_DIR)
