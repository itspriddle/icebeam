package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validConfig returns a minimal config that passes validation, for tests that
// want to mutate a single field.
func validConfig() Config {
	cfg := Default()
	cfg.Repository.URL = "rest:https://nas.local:8000/icebeam-test"
	cfg.Sets = []Set{
		{
			Name:    "home",
			Paths:   []string{"/Users/josh/Documents"},
			Exclude: []string{"**/node_modules"},
			Tags:    []string{"home"},
		},
	}
	return cfg
}

func TestSaveLoadRoundTrip(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	want := validConfig()
	want.Backup = Backup{
		Exclude:       []string{"**/.cache"},
		ExcludeCaches: true,
		OneFileSystem: true,
	}
	want.Retention = Retention{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12, KeepYearly: 3}

	require.NoError(t, want.Save())

	got, err := Load()
	require.NoError(t, err)
	assert.Equal(t, want, *got)
}

func TestSaveFileSetsRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}

	dir := filepath.Join(t.TempDir(), "icebeam")
	path := filepath.Join(dir, configFileName)

	cfg := validConfig()
	require.NoError(t, cfg.SaveFile(path))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(fileMode), fi.Mode().Perm(), "config file must be 0600")

	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(dirMode), di.Mode().Perm(), "config dir must be 0700")
}

func TestLoadMissingReturnsNotConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := Load()
	require.ErrorIs(t, err, ErrNotConfigured)
}

func TestLoadMalformedIsDistinctFromNotConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)
	require.NoError(t, os.WriteFile(path, []byte("this is = not valid toml ]["), fileMode))

	_, err := LoadFile(path)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotConfigured)
	assert.Contains(t, err.Error(), "parse config")
}

func TestLoadAppliesDefaults(t *testing.T) {
	// A hand-edited config omitting min_version and log level should still
	// receive icebeam's defaults on load.
	path := filepath.Join(t.TempDir(), configFileName)
	contents := `
[repository]
url = "rest:https://nas.local:8000/icebeam-test"

[[set]]
name = "home"
paths = ["/Users/josh/Documents"]
`
	require.NoError(t, os.WriteFile(path, []byte(contents), fileMode))

	cfg, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, defaultMinVersion, cfg.Restic.MinVersion)
	assert.Equal(t, defaultLogLevel, cfg.Log.Level)
}

func TestDefaultPopulatesBaseValues(t *testing.T) {
	t.Parallel()

	cfg := Default()
	assert.Equal(t, defaultMinVersion, cfg.Restic.MinVersion)
	assert.Equal(t, defaultLogLevel, cfg.Log.Level)
	assert.Empty(t, cfg.Repository.URL)
	assert.Empty(t, cfg.Sets)
}

func TestSaveFileRejectsInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), configFileName)

	cfg := Default() // missing repo URL and sets
	err := cfg.SaveFile(path)
	require.Error(t, err)

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "invalid config must not be written to disk")
}
