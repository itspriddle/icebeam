# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What icebeam is

A single static Go binary that wraps the official `restic` binary (via `os/exec`) to manage backups on macOS and Linux servers (including old Synology DSM). It owns the parts restic leaves to the user: a declarative TOML config of *what* to back up, secure credential storage, a persistent log, an `init` wizard, and OS-native scheduling so one `icebeam run` can be driven by launchd/systemd. It must **never** vendor or import restic's Go packages.

## Commands

```sh
make build         # → bin/icebeam, with version/commit/date injected via ldflags
make check         # the full Quality Gate: fmt-check + vet + lint + test + check-surface + tidy-check
make release-check # check + vuln + race-test (slower pre-release gates)
make test          # go test -race -cover ./...
make lint          # golangci-lint run (v2 config schema; CI pins v2.12.2)
make vet           # go vet ./...
make surface       # regenerate .surface after an intentional CLI command/flag change
make vuln          # govulncheck ./...

go test -race -run TestRunCancellation ./internal/restic/   # a single test
go test -race -count=10 ./internal/restic/                  # flush intermittent failures
go test ./internal/cli/ -run TestSurface -update            # = make surface
```

`make check` is the canonical green bar. CI (`.github/workflows/ci.yml`) re-runs the same gates as separate jobs rather than calling `make`, so keep the Makefile and the workflow in lockstep when you add or change a gate. The CLI **surface** is snapshotted in `.surface` (repo root): `internal/surface` walks the cobra tree into sorted `CMD`/`ARG`/`FLAG` lines, and `TestSurface` (in `internal/cli`) diffs it. An intentional command/flag change requires regenerating it with `make surface` and committing the result. CI also cross-compiles `darwin/{arm64,amd64}` + `linux/{amd64,arm64}` with `CGO_ENABLED=0` (the Synology target is static `linux/amd64`), runs `govulncheck` (installed `@latest` so freshly published advisories surface), and lints the workflow files themselves with actionlint + zizmor (`.github/workflows/actions.yml`); all `uses:` are pinned to commit SHAs and bumped by grouped, cooldown-gated Dependabot PRs (`.github/dependabot.yml`, `Deps:` prefix).

`.golangci.yml` uses the **v2 config schema** and enables a broad linter set on top of the standard one — security (`gosec`), error-wrapping (`errorlint`/`nilerr`/`errname`), context discipline (`noctx`), and test discipline (`testifylint`/`thelper`/`tparallel`/`usetesting`); test files relax `gosec`/`cyclop`/`bodyclose`/`unparam`. CI pins golangci-lint to v2.12.2 to match the local v2 toolchain.

## Architecture

Layering, outermost to innermost: `cmd/icebeam` → `internal/cli` (cobra command tree) → `internal/restic` (the one and only place that runs the binary) → `internal/{config,credentials,logging}`.

- **`internal/restic` is the sole gateway to the restic binary.** Every higher-level command drives a `*restic.Runner`; nothing else calls `os/exec` for restic. The Runner owns binary discovery (config `restic.binary` → PATH), one-time min-version gating, environment construction, output streaming to the logger, context cancellation, and translating restic's exit codes into typed Go errors (`*ExitError`, predicates like `IsRepoLocked`).
- **`internal/cli` orchestrates; `internal/restic` executes.** Commands load config, resolve the credential store, build the logger, then build argv and hand it to the Runner. Restic's argument vector starts with the subcommand (e.g. `{"backup", "/path", "--tag", "home"}`); see `backupArgs` in `backup.go` for the canonical builder.

### The testing seam (follow this pattern for any new restic-backed command)

Each command file declares (a) a **narrow interface** of just the Runner methods it needs and (b) a **package-level constructor var** that tests overwrite to inject a stub — no real restic binary in the suite. Example from `backup.go`:

```go
type backupRunner interface {
    Backup(ctx context.Context, args ...string) (*restic.BackupSummary, error)
}
var newBackupRunner = func(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (backupRunner, error) {
    return restic.New(cfg, store, logger)
}
```

The same shape recurs as `browseRunner`/`newBrowseRunner`, `restoreRunner`, `maintenanceRunner`, and `resticRunner` (init). `stderrIsTerminal` is likewise a package var so tests force TTY behavior. The Runner's own tests use a **stub restic binary** (a shell script on a temp PATH) to exercise env construction, version gating, JSON parsing, and cancellation.

### Hard invariants (don't regress these)

- **Secrets never touch argv, config, logs, or stdout.** They reach restic only via the environment: the persistent file store → `RESTIC_PASSWORD_FILE`; the in-memory setup-probe store → `RESTIC_PASSWORD`; REST creds → `RESTIC_REST_USERNAME/PASSWORD`. See `credentials/restic.go` (`ResticPasswordEnv`) and `restic/restic.go` (`env`). The `logging` package redacts secrets; route human/audit output through it.
- **The restic child inherits the ambient env**, with icebeam's overrides appended last so they win: `cmd.Env = append(os.Environ(), env...)`. This keeps `HOME` (restic cache), `PATH`, `TMPDIR`, and cert/proxy vars under launchd/systemd.
- **Cancellation must leave no orphan.** The runner sets `Setpgid` (`procattr_unix.go`, with a `procattr_other.go` no-op behind build tags), a `cmd.Cancel` that signals the whole process group, and `cmd.WaitDelay` so the output reader unblocks. This was a real bug once — preserve `setProcessGroup`, and test cancellation by polling the child's published PID, never a `time.Sleep`.
- **Exit codes are semantic** (`internal/cli/exit.go`): `0` ok, `1` generic, `2` total failure (every set failed), `3` partial failure. `icebeam run` attempts every set even when one fails. Errors carry codes via the `exitCoder` interface (`newExitError`); `Execute` maps them to the process exit code.
- **XDG paths on all platforms, including macOS** — `~/.config`, `~/.local/state`, `~/.cache`, never `~/Library`. Resolve via `internal/config` helpers (`ConfigDir`, etc.), honoring the `$XDG_*` env vars. Config/secret files are `0600`, dirs `0700`.
- **`gosec` G204 is intentionally excluded** (`.golangci.yml`) because wrapping the restic binary via `exec.Command` is the whole point — args are validated subcommands/flags, never raw shell. Don't reintroduce shell interpolation; keep new `exec` calls argv-based.

### Config & credentials shape

Config is TOML at `$XDG_CONFIG_HOME/icebeam/config.toml`: one `[repository]` per machine, global `[backup]`/`[retention]`, and one or more `[[set]]` blocks (name, paths, excludes, tags). `internal/config` types it, defaults it, and validates (rejects empty repo URL, a set with no paths, duplicate/unsafe set names — errors name the field). A missing config returns a typed "not configured" error so commands can point the user at `icebeam init`.

`credentials.Open(fileDir)` returns the persistent `CredentialStore`: a `0600`-file store (dir `0700`) mirroring restic's own `RESTIC_PASSWORD_FILE` model. There is **no** OS-keyring backend, no `--backend` flag, and no `[credentials] backend` selector in config — a legacy `backend` key in an existing config is silently ignored on load. The file store works everywhere icebeam runs (servers, Synology, a logged-out Mac under launchd/systemd) where an OS keyring is locked or absent; the security boundary is file ownership + permissions, the same trust model restic, SSH keys, and `.pgpass` rely on. `credentials.NewMemoryStore(...)` is retained for the pre-persist setup probe only (it routes through `RESTIC_PASSWORD`, not `_FILE`).
