package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// restoreStub records the restic invocations the restore/dump commands make and
// replays scripted results so tests can drive them without a real restic.
type restoreStub struct {
	calls      [][]string
	restoreErr error
	dumpOut    []byte
	dumpErr    error
}

func (s *restoreStub) Restore(_ context.Context, args ...string) error {
	s.calls = append(s.calls, append([]string{"restore"}, args...))
	return s.restoreErr
}

func (s *restoreStub) Dump(_ context.Context, w io.Writer, args ...string) error {
	s.calls = append(s.calls, append([]string{"dump"}, args...))
	if len(s.dumpOut) > 0 {
		_, _ = w.Write(s.dumpOut)
	}
	return s.dumpErr
}

// withRestoreStub swaps newRestoreRunner for one returning the given stub and
// restores it when the test ends.
func withRestoreStub(t *testing.T, stub *restoreStub) {
	t.Helper()
	orig := newRestoreRunner
	newRestoreRunner = func(*config.Config, credentials.CredentialStore, *logging.Logger) (restoreRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newRestoreRunner = orig })
}

// writeRestoreConfig writes a valid config into the isolated XDG config dir so
// the restore/dump commands can load it.
func writeRestoreConfig(t *testing.T) {
	t.Helper()

	cfg := config.Default()
	cfg.Repository.URL = "rest:https://nas.local:8000/icebeam"
	cfg.Sets = []config.Set{{Name: "home", Paths: []string{"/home"}}}

	path, err := config.ConfigPath()
	require.NoError(t, err)
	require.NoError(t, cfg.SaveFile(path))
}

func TestRestoreConstructsArgsAndReportsTarget(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	target := filepath.Join(t.TempDir(), "out")

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	out, err := runCLI(t, "restore", "latest",
		"--target", target,
		"--include", "/etc",
		"--exclude", "/etc/shadow",
	)
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t,
		[]string{"restore", "latest", "--target", target, "--include", "/etc", "--exclude", "/etc/shadow"},
		stub.calls[0],
	)
	// The target is reported before restoring.
	assert.Contains(t, out, target)
	assert.Contains(t, out, "Restore complete")
}

func TestRestoreHandlesLatestSelector(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	target := filepath.Join(t.TempDir(), "out")

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target)
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"restore", "latest", "--target", target}, stub.calls[0])
}

func TestRestoreRequiresTarget(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest")
	require.Error(t, err)
	assert.Empty(t, stub.calls)
}

func TestRestoreRefusesNonEmptyTarget(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "existing.txt"), []byte("data"), 0o600))

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not empty")
	// The guard short-circuits before restic is invoked.
	assert.Empty(t, stub.calls)
}

func TestRestoreForceWritesIntoNonEmptyTarget(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "existing.txt"), []byte("data"), 0o600))

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target, "--force")
	require.NoError(t, err)
	require.Len(t, stub.calls, 1)
}

func TestRestoreAllowsEmptyTarget(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	// An empty existing directory is fine.
	target := t.TempDir()

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target)
	require.NoError(t, err)
	require.Len(t, stub.calls, 1)
}

func TestRestoreRejectsFileTarget(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	// A path that exists but is a file (not a directory) is always an error.
	target := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
	assert.Empty(t, stub.calls)
}

func TestRestoreMapsResticExitCode(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	target := filepath.Join(t.TempDir(), "out")

	stub := &restoreStub{
		restoreErr: &restic.ExitError{Code: restic.ExitRepoLocked, Command: "restore"},
	}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target)
	require.Error(t, err)

	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, restic.ExitRepoLocked, coder.ExitCode())
}

func TestRestoreNotConfigured(t *testing.T) {
	isolateXDG(t)

	target := filepath.Join(t.TempDir(), "out")

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "restore", "latest", "--target", target)
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrNotConfigured)
	assert.Empty(t, stub.calls)
}

func TestDumpStreamsToStdout(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	// Binary content including NUL and a high byte must pass through unchanged.
	content := []byte{0x00, 0x01, 0xff, 'h', 'i', '\n', 0x00}

	stub := &restoreStub{dumpOut: content}
	withRestoreStub(t, stub)

	out, err := runCLI(t, "dump", "latest", "/etc/hosts")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"dump", "latest", "/etc/hosts"}, stub.calls[0])
	// runCLI captures stdout via the command writer; the raw bytes are present.
	assert.Contains(t, out, "hi")
	assert.Equal(t, string(content), out)
}

func TestDumpRequiresSnapshotAndPath(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "dump", "latest")
	require.Error(t, err)
	assert.Empty(t, stub.calls)
}

func TestDumpMapsResticExitCode(t *testing.T) {
	isolateXDG(t)
	writeRestoreConfig(t)

	stub := &restoreStub{
		dumpErr: &restic.ExitError{Code: restic.ExitGeneric, Command: "dump"},
	}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "dump", "latest", "/some/dir")
	require.Error(t, err)

	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, restic.ExitGeneric, coder.ExitCode())
}

func TestDumpNotConfigured(t *testing.T) {
	isolateXDG(t)

	stub := &restoreStub{}
	withRestoreStub(t, stub)

	_, err := runCLI(t, "dump", "latest", "/etc/hosts")
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrNotConfigured)
	assert.Empty(t, stub.calls)
}
