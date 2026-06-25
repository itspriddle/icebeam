# icebeam

A single static Go binary that wraps the official [`restic`](https://restic.net)
backup program to manage backups on macOS and Linux servers (including older
Synology DSM).

restic is a superb backup engine, but it leaves the operational glue to you.
icebeam owns those parts: a declarative TOML config of *what* to back up, secure
credential storage, a persistent log, an `init` setup wizard, and OS-native
scheduling so a single `icebeam run` can be driven by launchd or systemd.

icebeam shells out to your installed `restic` binary — it never vendors or
imports restic's Go packages, so you stay on whatever restic version you trust.

## Features

- **Declarative config** — describe your repository, retention policy, and one
  or more named backup *sets* (paths + excludes + tags) in a single TOML file.
- **Secure credentials** — repository and REST-server passwords live in the OS
  keychain (macOS Keychain / Linux Secret Service) with an automatic `0600`
  password-file fallback for systems without one. Secrets never touch argv,
  config, logs, or stdout — they reach restic only through the environment.
- **Guided setup** — `icebeam init` walks a fresh machine from nothing to an
  initialized repository, and is fully scriptable via flags.
- **OS-native scheduling** — generate and install a launchd agent or a systemd
  service+timer (or `--print` the unit for locked-down hosts).
- **Persistent, redacted log** — every run is recorded as structured JSON under
  the XDG state directory, with size-based rotation.
- **Scheduler-friendly exit codes** — distinct codes for success, partial
  failure, and total failure so a scheduler can act on results.
- **No orphans** — a cancelled run signals restic's whole process group, leaving
  nothing behind.
- **XDG paths everywhere**, including macOS (`~/.config`, `~/.local/state`,
  `~/.cache` — never `~/Library`).

## Requirements

- A `restic` binary on `PATH` (or pointed at via config), **version 0.16.0 or
  newer** (the version stock Ubuntu ships). To tell a missing repository, a locked
  repository, and a wrong password apart, icebeam prefers the distinct exit codes
  restic added in 0.17.0 and falls back to matching restic's message text on older
  releases.
- Supported platforms: `darwin/{arm64,amd64}` and `linux/{amd64,arm64}`. The
  Synology target is a static `linux/amd64` build.
- Go 1.26+ to build from source.

Install restic first if you don't have it:

```sh
brew install restic        # macOS
apt install restic         # Debian/Ubuntu
```

## Install

Build from source:

```sh
git clone https://github.com/itspriddle/icebeam
cd icebeam
make build          # produces ./bin/icebeam
```

The build injects version, commit, and date via ldflags. Copy `bin/icebeam`
somewhere on your `PATH`.

## Quick start

```sh
# 1. Guided setup: prompts for repo URL, password, and a first backup set,
#    writes config.toml, stores secrets, and runs `restic init` if needed.
icebeam init

# 2. Back up every configured set right now.
icebeam run

# 3. Back up on a schedule (launchd on macOS, systemd on Linux).
icebeam schedule install --interval daily
```

`init` is scriptable — every prompt has a flag, and `--password-stdin` reads the
repository password from stdin:

```sh
echo "$REPO_PASSWORD" | icebeam init \
  --repo "rest:https://nas.local:8000/icebeam" \
  --set home \
  --path "$HOME/Documents" --path "$HOME/Projects" \
  --exclude "node_modules" \
  --tag laptop \
  --password-stdin
```

## Configuration

Config is TOML at `$XDG_CONFIG_HOME/icebeam/config.toml` (default
`~/.config/icebeam/config.toml`), created with `0600` permissions. One
repository per machine, global backup and retention options, and one or more
`[[set]]` blocks.

```toml
[repository]
url = "rest:https://nas.local:8000/icebeam"

[credentials]
# auto (default): keychain when available, else a 0600 password file.
# keychain: force the OS secret service. file: force the password file.
backend = "auto"

[backup]
exclude         = ["*.tmp", ".DS_Store"]   # applied to every set
exclude_caches  = true                       # honor CACHEDIR.TAG
one_file_system = false                       # don't cross mount points

[retention]
# Used by `icebeam forget`. Omitted/zero values are not passed to restic.
keep_daily   = 7
keep_weekly  = 4
keep_monthly = 6
keep_yearly  = 1

[restic]
# binary: explicit path to restic; empty = find on PATH.
binary      = ""
min_version = "0.16.0"

[log]
# file: override the default log path; empty = XDG state dir.
file  = ""
level = "info"          # debug, info, warn, or error

[[set]]
name    = "home"        # must be a safe identifier; also used as a restic tag scope
paths   = ["/Users/me/Documents", "/Users/me/Projects"]
exclude = ["node_modules", "*.log"]   # merged with the global excludes
tags    = ["laptop"]

[[set]]
name  = "photos"
paths = ["/Volumes/Photos"]
```

The config is validated on load: the repository URL must be non-empty, at least
one set must be defined, each set needs at least one path, and set names must be
unique safe identifiers (letters/digits/`_`/`-`, starting with a letter or `_`).
A missing config produces a clear "not configured" error pointing at
`icebeam init`.

## Commands

Run `icebeam <command> --help` for full flag details.

### Backing up

| Command | Description |
| --- | --- |
| `icebeam init` | Guided setup: write config, store credentials, init the repo. |
| `icebeam run` | Scheduler entrypoint — back up **all** sets; exits non-zero if any failed. |
| `icebeam backup [set...]` | Back up the named set(s), or all sets when none are named. |

Both `run` and `backup` attempt every set even when one fails, so a single bad
path doesn't skip the rest.

### Maintenance

| Command | Description |
| --- | --- |
| `icebeam forget [--no-prune] [--dry-run]` | Apply the retention policy (grouped by host+tags); prunes by default. |
| `icebeam prune` | Reclaim space from removed snapshots, standalone. |
| `icebeam check [--read-data-subset "10%"]` | Verify repository integrity (metadata-only by default). |

### Browsing & restoring

| Command | Description |
| --- | --- |
| `icebeam snapshots` (alias `list`) | List snapshots; `--json`, filter with `--tag`/`--host`. |
| `icebeam ls <snapshotID> [path]` | List a snapshot's contents (`latest` selects the newest). |
| `icebeam find <pattern>` | Find files across snapshots. |
| `icebeam restore <snapshotID> --target <dir>` | Restore a snapshot; `--include`/`--exclude`/`--force`. |
| `icebeam dump <snapshotID> <path>` | Stream a single file from a snapshot to stdout. |

`restore` refuses to write into a non-empty target unless you pass `--force`.

### Scheduling

| Command | Description |
| --- | --- |
| `icebeam schedule install [--interval hourly\|daily\|weekly] [--print]` | Install (or print) the OS scheduler unit for `icebeam run`. |
| `icebeam schedule uninstall` | Remove the installed unit. |
| `icebeam schedule status` | Report whether the unit is installed (and its next/last run where available). |

On Linux you can pass a raw `--calendar` (a systemd `OnCalendar` expression)
instead of `--interval`. `--print` emits the generated launchd plist or systemd
units to stdout so you can install them by hand on locked-down hosts — and is the
fallback path on platforms icebeam can't auto-schedule (e.g. a Synology without
`systemctl --user`).

## Credential backends

icebeam stores three named secrets — the repository password and optional
REST-server HTTP username/password — outside the plaintext config:

- **`auto`** (default) — use the OS secret service when available, otherwise
  fall back to a `0600` password file in the config directory.
- **`keychain`** — force the OS secret service (errors if unavailable).
- **`file`** — force the password file (e.g. on a headless NAS).

The active backend is reported by `init` so you always know where your secrets
live. Secrets reach restic via `RESTIC_PASSWORD`/`RESTIC_PASSWORD_FILE` and
`RESTIC_REST_USERNAME`/`RESTIC_REST_PASSWORD` — never via the command line.

## Files & paths

All paths follow the XDG spec on every platform (the `$XDG_*` vars override the
defaults below):

| Purpose | Default location |
| --- | --- |
| Config | `~/.config/icebeam/config.toml` |
| Password-file fallback | `~/.config/icebeam/` (`0600`) |
| Log | `~/.local/state/icebeam/icebeam.log` |
| restic cache | restic's own default (under `~/.cache`) |
| launchd agent | `~/Library/LaunchAgents/com.itspriddle.icebeam.plist` |
| systemd units | `~/.config/systemd/user/` |

The log is structured JSON, rotated to `icebeam.log.1` once it passes 10 MiB.
On a TTY, a concise human summary is also mirrored to stderr; under a scheduler
the log file is the system of record.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Generic failure (usage, config, or a single command failing). |
| `2` | Total failure — every attempted set failed. |
| `3` | Partial failure — at least one set succeeded and at least one failed. |

Failures from restic carry restic's own exit code through where it's meaningful,
so a scheduler can distinguish e.g. a locked repository from a wrong password.

## Development

```sh
make build    # → bin/icebeam (version/commit/date injected via ldflags)
make check    # the full quality gate: test + vet + lint
make test     # go test -race -cover ./...
make lint     # golangci-lint run
make vet      # go vet ./...
```

Run `make check` before every commit. CI additionally runs `govulncheck` and
cross-compiles all release targets with `CGO_ENABLED=0`.

### Architecture

Layering, outermost to innermost:

```
cmd/icebeam → internal/cli → internal/restic → internal/{config,credentials,logging,schedule}
```

`internal/restic` is the **sole** gateway to the restic binary: it owns binary
discovery, one-time minimum-version gating, environment construction, output
streaming to the logger, context cancellation (no orphaned processes), and
translation of restic's exit codes into typed Go errors. Every higher-level
command builds an argument vector and hands it to the Runner; nothing else calls
`os/exec` for restic.

## License

[MIT](LICENSE)
