package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXDGDirsHonorEnvOverrides(t *testing.T) {
	base := t.TempDir()
	configHome := filepath.Join(base, "cfg")
	stateHome := filepath.Join(base, "state")
	cacheHome := filepath.Join(base, "cache")

	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"config", ConfigDir, filepath.Join(configHome, appName)},
		{"state", StateDir, filepath.Join(stateHome, appName)},
		{"cache", CacheDir, filepath.Join(cacheHome, appName)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestXDGDirsFallBackToHomeDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Unset overrides so the defaults are exercised.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"config", ConfigDir, filepath.Join(home, ".config", appName)},
		{"state", StateDir, filepath.Join(home, ".local", "state", appName)},
		{"cache", CacheDir, filepath.Join(home, ".cache", appName)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestXDGIgnoresRelativeEnvValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A relative XDG value is not spec-compliant and must be ignored.
	t.Setenv("XDG_CONFIG_HOME", "relative/path")

	got, err := ConfigDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".config", appName), got)
}

func TestConfigPath(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	got, err := ConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(configHome, appName, configFileName), got)
}
