package config

import (
	"os"
	"path/filepath"
)

// appName namespaces all icebeam XDG directories.
const appName = "icebeam"

// ConfigDir returns the icebeam config directory, honoring $XDG_CONFIG_HOME and
// defaulting to ~/.config/icebeam on all platforms (never macOS ~/Library).
func ConfigDir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

// StateDir returns the icebeam state directory, honoring $XDG_STATE_HOME and
// defaulting to ~/.local/state/icebeam on all platforms.
func StateDir() (string, error) {
	return xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// CacheDir returns the icebeam cache directory, honoring $XDG_CACHE_HOME and
// defaulting to ~/.cache/icebeam on all platforms.
func CacheDir() (string, error) {
	return xdgDir("XDG_CACHE_HOME", ".cache")
}

// ConfigPath returns the full path to the icebeam config file.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// configFileName is the basename of the icebeam config file.
const configFileName = "config.toml"

// xdgDir resolves an XDG base directory: it uses the env var when set to an
// absolute path, otherwise falls back to $HOME joined with the given default
// relative path. The result is always namespaced under the icebeam app name.
func xdgDir(envVar, defaultRel string) (string, error) {
	if base := os.Getenv(envVar); filepath.IsAbs(base) {
		return filepath.Join(base, appName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, defaultRel, appName), nil
}
