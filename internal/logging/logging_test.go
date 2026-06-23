package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
)

func TestResolvePathDefaultsToStateDir(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	cfg := config.Default()
	path, err := ResolvePath(&cfg)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(stateHome, "icebeam", logFileName), path)
}

func TestResolvePathHonorsConfigOverride(t *testing.T) {
	// A config-supplied path wins over the XDG default.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	custom := filepath.Join(t.TempDir(), "custom", "my.log")
	cfg := config.Default()
	cfg.Log.File = custom

	path, err := ResolvePath(&cfg)
	require.NoError(t, err)
	assert.Equal(t, custom, path)
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{" debug ", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"verbose", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseLevel(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "log.level")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewWritesToConfiguredFileWithRestrictivePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}

	dir := filepath.Join(t.TempDir(), "icebeam")
	path := filepath.Join(dir, logFileName)

	cfg := config.Default()
	cfg.Log.File = path

	logger, err := New(&cfg, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Close() })

	assert.Equal(t, path, logger.Path())

	logger.Info("hello")
	require.NoError(t, logger.Close())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(fileMode), fi.Mode().Perm(), "log file must be 0600")

	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(dirMode), di.Mode().Perm(), "log dir must be 0700")

	data, err := os.ReadFile(path) //nolint:gosec // path is a test temp dir
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello")
}

func TestNewDefaultsToInfoLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, mustLevel(t, ""), Options{})

	logger.Debug("debug message")
	logger.Info("info message")

	out := buf.String()
	assert.NotContains(t, out, "debug message", "debug must be filtered at info level")
	assert.Contains(t, out, "info message")
}

func TestNewLevelFilteringAtError(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, mustLevel(t, "error"), Options{})

	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	out := buf.String()
	assert.NotContains(t, out, "info message")
	assert.NotContains(t, out, "warn message")
	assert.Contains(t, out, "error message")
}

func TestNewLevelFilteringAtDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, mustLevel(t, "debug"), Options{})

	logger.Debug("debug message")
	assert.Contains(t, buf.String(), "debug message")
}

func TestTTYMirrorsToStderr(t *testing.T) {
	var file, stderr bytes.Buffer
	logger := NewWithWriter(&file, slog.LevelInfo, Options{TTY: true, Stderr: &stderr})

	logger.Info("mirrored line")

	assert.Contains(t, file.String(), "mirrored line", "file always receives the record")
	assert.Contains(t, stderr.String(), "mirrored line", "TTY mirror must also reach stderr")
}

func TestNonTTYDoesNotMirrorToStderr(t *testing.T) {
	var file, stderr bytes.Buffer
	logger := NewWithWriter(&file, slog.LevelInfo, Options{TTY: false, Stderr: &stderr})

	logger.Info("file only")

	assert.Contains(t, file.String(), "file only")
	assert.Empty(t, stderr.String(), "non-TTY runs must not write to stderr")
}

func TestSecretValueIsRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, slog.LevelInfo, Options{})

	logger.Info("storing", "value", Secret("hunter2"))

	out := buf.String()
	assert.NotContains(t, out, "hunter2", "secret value must never be emitted")
	assert.Contains(t, out, redactedPlaceholder)
}

func TestSensitiveKeyIsRedactedEvenWhenPlainString(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, slog.LevelInfo, Options{})

	// A bare string under a sensitive key is redacted as a safety net.
	logger.Info("auth",
		"repo_password", "topsecret",
		"resticPassword", "alsosecret",
		"token", "abc123",
	)

	out := buf.String()
	assert.NotContains(t, out, "topsecret")
	assert.NotContains(t, out, "alsosecret")
	assert.NotContains(t, out, "abc123")
}

func TestNonSensitiveValuesAreNotRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, slog.LevelInfo, Options{})

	logger.Info("backup", "set", "home", "host", "macbook")

	out := buf.String()
	assert.Contains(t, out, "home")
	assert.Contains(t, out, "macbook")
	assert.NotContains(t, out, redactedPlaceholder)
}

func TestLogStartAndLogEnd(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, slog.LevelInfo, Options{})

	logger.LogStart("backup", []string{"home", "--tag", "daily"})
	logger.LogEnd("backup", 1500*time.Millisecond, nil)

	lines := nonEmptyLines(buf.String())
	require.Len(t, lines, 2)

	start := decodeRecord(t, lines[0])
	assert.Equal(t, "run start", start["msg"])
	assert.Equal(t, "backup", start["command"])

	end := decodeRecord(t, lines[1])
	assert.Equal(t, "run end", end["msg"])
	assert.Equal(t, "success", end["outcome"])
	assert.Equal(t, "1.5s", end["duration"])
}

func TestLogEndWithErrorReportsFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, slog.LevelInfo, Options{})

	logger.LogEnd("run", 2*time.Second, assert.AnError)

	rec := decodeRecord(t, strings.TrimSpace(buf.String()))
	assert.Equal(t, "ERROR", rec["level"])
	assert.Equal(t, "failure", rec["outcome"])
	assert.Contains(t, rec["error"], "assert.AnError")
}

func TestRotateWhenOverThreshold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "icebeam")
	path := filepath.Join(dir, logFileName)
	require.NoError(t, os.MkdirAll(dir, dirMode))

	// Seed a file just over the threshold.
	big := bytes.Repeat([]byte("x"), maxLogSize+1)
	require.NoError(t, os.WriteFile(path, big, fileMode))

	cfg := config.Default()
	cfg.Log.File = path

	logger, err := New(&cfg, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Close() })

	// The oversized file was rotated to "<name>.1" and a fresh file started.
	_, err = os.Stat(path + ".1")
	require.NoError(t, err, "oversized log should be rotated to .1")

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Less(t, fi.Size(), int64(maxLogSize), "a fresh log file should have been started")
}

func TestNoRotationWhenUnderThreshold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "icebeam")
	path := filepath.Join(dir, logFileName)
	require.NoError(t, os.MkdirAll(dir, dirMode))
	require.NoError(t, os.WriteFile(path, []byte("small\n"), fileMode))

	cfg := config.Default()
	cfg.Log.File = path

	logger, err := New(&cfg, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Close() })

	_, err = os.Stat(path + ".1")
	assert.True(t, os.IsNotExist(err), "small log must not be rotated")
}

func TestSecretStringRedacts(t *testing.T) {
	s := Secret("hunter2")
	assert.Equal(t, redactedPlaceholder, s.String())
	assert.NotContains(t, s.String(), "hunter2")
}

func TestIsTerminalNilAndRegularFile(t *testing.T) {
	assert.False(t, IsTerminal(nil))

	f, err := os.CreateTemp(t.TempDir(), "log")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	// A regular file is not a character device, so it is not a terminal.
	assert.False(t, IsTerminal(f))
}

func TestFanoutWithAttrsAndGroupPropagate(t *testing.T) {
	var file, stderr bytes.Buffer
	// A TTY logger uses the fanout handler, so With* exercise both branches.
	logger := NewWithWriter(&file, slog.LevelInfo, Options{TTY: true, Stderr: &stderr})

	scoped := logger.With("run_id", "abc").WithGroup("set")
	scoped.Info("done", "name", "home")

	for _, out := range []string{file.String(), stderr.String()} {
		assert.Contains(t, out, "abc", "WithAttrs must propagate through the fanout")
		assert.Contains(t, out, "home", "WithGroup must propagate through the fanout")
	}
}

// mustLevel parses a level for tests, failing on error.
func mustLevel(t *testing.T, s string) slog.Level {
	t.Helper()
	level, err := ParseLevel(s)
	require.NoError(t, err)
	return level
}

// decodeRecord parses one JSON log line into a string-keyed map.
func decodeRecord(t *testing.T, line string) map[string]string {
	t.Helper()
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &raw))
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = strings.TrimSpace(stringify(v))
	}
	return out
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
