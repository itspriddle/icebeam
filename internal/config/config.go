// Package config defines icebeam's declarative configuration: the repository,
// global backup options, retention policy, named backup sets, restic settings,
// and logging. It reads and writes the config as TOML under XDG paths.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// File permissions for config files and their containing directories. Config may
// reference (but never stores) secrets, so we keep it owner-only.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// ErrNotConfigured is returned by Load when no config file exists yet. It is
// distinguishable from a parse error so commands can direct the user to run
// `icebeam init`.
var ErrNotConfigured = errors.New("icebeam is not configured; run `icebeam init`")

// Config is the top-level icebeam configuration.
//
// Secrets are stored separately as 0600 files (see internal/credentials), not in
// this config. A legacy [credentials] backend key from an older config is
// silently ignored on load — the file store is now the only persistent backend.
type Config struct {
	Repository Repository `toml:"repository"`
	Backup     Backup     `toml:"backup"`
	Retention  Retention  `toml:"retention"`
	Restic     Restic     `toml:"restic"`
	Log        Log        `toml:"log"`
	Sets       []Set      `toml:"set"`
}

// Repository identifies the restic repository for this machine. One repo per
// machine in v1.
type Repository struct {
	URL string `toml:"url"`
}

// Backup holds global backup options applied across all sets.
type Backup struct {
	Exclude       []string `toml:"exclude"`
	ExcludeCaches bool     `toml:"exclude_caches"`
	OneFileSystem bool     `toml:"one_file_system"`
}

// Retention is the snapshot retention policy applied by `icebeam forget`.
type Retention struct {
	KeepDaily   int `toml:"keep_daily"`
	KeepWeekly  int `toml:"keep_weekly"`
	KeepMonthly int `toml:"keep_monthly"`
	KeepYearly  int `toml:"keep_yearly"`
}

// Restic configures how icebeam locates and gates the restic binary.
type Restic struct {
	// Binary is an explicit path to restic; empty means "find on PATH".
	Binary string `toml:"binary"`
	// MinVersion is the minimum acceptable restic version.
	MinVersion string `toml:"min_version"`
}

// Log configures icebeam's persistent log.
type Log struct {
	// File overrides the default log path; empty means the XDG state dir.
	File string `toml:"file"`
	// Level is one of debug/info/warn/error.
	Level string `toml:"level"`
}

// Set is a named collection of paths to back up with its own excludes and tags.
type Set struct {
	Name    string   `toml:"name"`
	Paths   []string `toml:"paths"`
	Exclude []string `toml:"exclude"`
	Tags    []string `toml:"tags"`
}

// Default values applied to a fresh config.
const (
	// defaultMinVersion is the lowest restic icebeam supports. restic 0.17.0 is
	// the first release to emit the distinct exit codes (10 repository-not-exist,
	// 11 repository-locked, 12 wrong-password) that icebeam's *ExitError
	// predicates classify; older restic collapses those onto exit code 1, so the
	// predicates would silently degrade below this floor.
	defaultMinVersion = "0.17.0"
	defaultLogLevel   = "info"

	// Default retention policy established during setup so `icebeam forget` is
	// meaningful immediately. A fresh repository keeps the most recent backups at
	// progressively coarser granularity.
	DefaultKeepDaily   = 7
	DefaultKeepWeekly  = 4
	DefaultKeepMonthly = 12
	DefaultKeepYearly  = 3
)

// Default returns a Config populated with icebeam's default values. The
// repository URL and sets are intentionally left empty for the caller to fill.
func Default() Config {
	return Config{
		Restic: Restic{
			MinVersion: defaultMinVersion,
		},
		Log: Log{
			Level: defaultLogLevel,
		},
	}
}

// applyDefaults fills in any unset fields with their defaults. It is applied
// after loading so a hand-edited config that omits e.g. min_version still
// behaves sensibly.
func (c *Config) applyDefaults() {
	if c.Restic.MinVersion == "" {
		c.Restic.MinVersion = defaultMinVersion
	}
	if c.Log.Level == "" {
		c.Log.Level = defaultLogLevel
	}
}

// Load reads and parses the config from the default XDG path. A missing file
// yields ErrNotConfigured; a malformed file yields a wrapped parse error
// distinct from ErrNotConfigured.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFile(path)
}

// LoadFile reads and parses the config from a specific path. It applies defaults
// and validates the result before returning.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path derived from XDG config dir, not arbitrary input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save validates and writes the config to the default XDG path, creating the
// containing directory (0700) and the file (0600) with restrictive permissions.
func (c *Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return c.SaveFile(path)
}

// SaveFile validates and writes the config to a specific path, creating the
// containing directory (0700) and the file (0600) with restrictive permissions.
func (c *Config) SaveFile(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode) //nolint:gosec // path derived from XDG config dir, not arbitrary input
	if err != nil {
		return fmt.Errorf("open config %s: %w", path, err)
	}

	if err := toml.NewEncoder(f).Encode(c); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode config %s: %w", path, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close config %s: %w", path, err)
	}

	// OpenFile honors the mode only on creation; enforce perms on an
	// existing (possibly looser) file too.
	if err := os.Chmod(path, fileMode); err != nil {
		return fmt.Errorf("chmod config %s: %w", path, err)
	}

	return nil
}
