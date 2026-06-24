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

# Cross-compilation. `make build` with no platform infers GOOS/GOARCH from the
# host toolchain; naming a platform cross-compiles a static (CGO_ENABLED=0)
# binary into bin/icebeam-<os>-<arch>. Examples:
#
#   make build                          # native        -> bin/icebeam
#   make build linux-arm64              # ARM server     -> bin/icebeam-linux-arm64
#   make build linux-amd64              # x86 / Synology -> bin/icebeam-linux-amd64
#   make build linux-amd64 linux-arm64  # both in one invocation
#
# uname-style aliases are accepted: x86_64 -> amd64, aarch64/arm -> arm64.
PLATFORMS := \
	linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 \
	linux-x86_64 linux-aarch64 linux-arm darwin-x86_64 darwin-aarch64

# Platform names present on the command line (empty for a plain host build).
PLATFORM_GOALS := $(filter $(PLATFORMS),$(MAKECMDGOALS))

.PHONY: build test vet lint fmt fmt-check tidy tidy-check surface check-surface \
        vuln race-test check release-check clean $(PLATFORMS)

build:
ifeq ($(strip $(PLATFORM_GOALS)),)
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)
else
	@mkdir -p $(BIN_DIR)
	@for plat in $(PLATFORM_GOALS); do \
		os=$${plat%%-*}; arch=$${plat##*-}; \
		case $$arch in \
			x86_64)      arch=amd64 ;; \
			aarch64|arm) arch=arm64 ;; \
		esac; \
		out=$(BIN_DIR)/$(BINARY)-$$os-$$arch; \
		echo "Building $$out (GOOS=$$os GOARCH=$$arch, CGO disabled)"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -ldflags "$(LDFLAGS)" -o $$out $(CMD) || exit 1; \
	done
endif

# Platform names are no-op goals so `make build linux-arm64` doesn't try to
# build a literal "linux-arm64" target; the real work happens in `build` above.
$(PLATFORMS):
	@:

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
