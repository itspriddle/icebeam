package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// backupStub records the restic backup invocations and replays a scripted result
// per call so tests can drive the backup/run flow without a real restic.
type backupStub struct {
	calls   [][]string
	respond func(args []string) (*restic.BackupSummary, error)
}

func (s *backupStub) Backup(_ context.Context, args ...string) (*restic.BackupSummary, error) {
	s.calls = append(s.calls, args)
	if s.respond != nil {
		return s.respond(args)
	}
	return &restic.BackupSummary{}, nil
}

// withBackupStub swaps newBackupRunner for one returning the given stub and
// restores it when the test ends.
func withBackupStub(t *testing.T, stub *backupStub) {
	t.Helper()
	orig := newBackupRunner
	newBackupRunner = func(*config.Config, credentials.CredentialStore, *logging.Logger) (backupRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newBackupRunner = orig })
}

// writeBackupConfig writes a valid config with the given sets into the isolated
// XDG config dir so runBackup can load it.
func writeBackupConfig(t *testing.T, sets ...config.Set) {
	t.Helper()

	cfg := config.Default()
	cfg.Repository.URL = "rest:https://nas.local:8000/icebeam"
	cfg.Backup.Exclude = []string{"**/.cache"}
	cfg.Backup.ExcludeCaches = true
	cfg.Backup.OneFileSystem = true
	cfg.Sets = sets

	path, err := config.ConfigPath()
	require.NoError(t, err)
	require.NoError(t, cfg.SaveFile(path))
}

// runCLI drives the root command with the given args, returning combined output
// and the execution error.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.Execute()
	return out.String(), err
}

func TestBackupConstructsPerSetResticArgs(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t, config.Set{
		Name:    "home",
		Paths:   []string{"/Users/josh/Documents", "/Users/josh/Projects"},
		Exclude: []string{"**/node_modules"},
		Tags:    []string{"home", "laptop"},
	})

	stub := &backupStub{}
	withBackupStub(t, stub)

	_, err := runCLI(t, "backup", "home")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	got := strings.Join(stub.calls[0], " ")
	// Paths come first, then merged global+set excludes, then the global option
	// flags, then the set's tags.
	assert.Equal(t,
		"/Users/josh/Documents /Users/josh/Projects "+
			"--exclude **/.cache --exclude **/node_modules "+
			"--exclude-caches --one-file-system "+
			"--tag home --tag laptop",
		got,
	)
}

func TestBackupAllSetsByDefault(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t,
		config.Set{Name: "home", Paths: []string{"/home"}},
		config.Set{Name: "etc", Paths: []string{"/etc"}},
	)

	stub := &backupStub{}
	withBackupStub(t, stub)

	// No set arguments → all sets, in config order.
	_, err := runCLI(t, "backup")
	require.NoError(t, err)

	require.Len(t, stub.calls, 2)
	assert.Equal(t, []string{"/home", "--exclude", "**/.cache", "--exclude-caches", "--one-file-system"}, stub.calls[0])
	assert.Equal(t, []string{"/etc", "--exclude", "**/.cache", "--exclude-caches", "--one-file-system"}, stub.calls[1])
}

func TestRunBacksUpAllSets(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t,
		config.Set{Name: "home", Paths: []string{"/home"}},
		config.Set{Name: "etc", Paths: []string{"/etc"}},
	)

	stub := &backupStub{}
	withBackupStub(t, stub)

	_, err := runCLI(t, "run")
	require.NoError(t, err)
	require.Len(t, stub.calls, 2)
}

func TestRunContinuesPastFailureAndReportsPartial(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t,
		config.Set{Name: "home", Paths: []string{"/home"}},
		config.Set{Name: "etc", Paths: []string{"/etc"}},
		config.Set{Name: "data", Paths: []string{"/data"}},
	)

	// The middle set fails; the others succeed. Every set must still be attempted.
	stub := &backupStub{
		respond: func(args []string) (*restic.BackupSummary, error) {
			if args[0] == "/etc" {
				return nil, &restic.ExitError{Code: restic.ExitGeneric, Command: "backup"}
			}
			return &restic.BackupSummary{SnapshotID: "ok"}, nil
		},
	}
	withBackupStub(t, stub)

	_, err := runCLI(t, "run")
	require.Error(t, err)

	// All three sets were attempted despite the failure.
	require.Len(t, stub.calls, 3)

	// Partial failure exit code.
	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, exitPartialFailure, coder.ExitCode())
	assert.Contains(t, err.Error(), "etc")
}

func TestRunTotalFailureExitCode(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t,
		config.Set{Name: "home", Paths: []string{"/home"}},
		config.Set{Name: "etc", Paths: []string{"/etc"}},
	)

	stub := &backupStub{
		respond: func([]string) (*restic.BackupSummary, error) {
			return nil, &restic.ExitError{Code: restic.ExitGeneric, Command: "backup"}
		},
	}
	withBackupStub(t, stub)

	_, err := runCLI(t, "run")
	require.Error(t, err)
	require.Len(t, stub.calls, 2)

	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, exitTotalFailure, coder.ExitCode())
}

func TestBackupUnknownSetListsAvailable(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t,
		config.Set{Name: "home", Paths: []string{"/home"}},
		config.Set{Name: "etc", Paths: []string{"/etc"}},
	)

	stub := &backupStub{}
	withBackupStub(t, stub)

	_, err := runCLI(t, "backup", "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown set")
	assert.Contains(t, err.Error(), "nope")
	// The available set names are listed (sorted).
	assert.Contains(t, err.Error(), "etc, home")
	// No backup was attempted for an unknown set.
	assert.Empty(t, stub.calls)
}

func TestBackupNotConfigured(t *testing.T) {
	isolateXDG(t)

	stub := &backupStub{}
	withBackupStub(t, stub)

	_, err := runCLI(t, "backup")
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrNotConfigured)
}

func TestBackupPrintsSummaryWhenTTY(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t, config.Set{Name: "home", Paths: []string{"/home"}})

	stub := &backupStub{
		respond: func([]string) (*restic.BackupSummary, error) {
			return &restic.BackupSummary{
				TotalFilesProcessed: 42,
				TotalBytesProcessed: 2048,
				DataAdded:           1024,
				SnapshotID:          "abc123",
			}, nil
		},
	}
	withBackupStub(t, stub)

	// Force the TTY path so the concise human summary is emitted.
	orig := stderrIsTerminal
	stderrIsTerminal = func() bool { return true }
	t.Cleanup(func() { stderrIsTerminal = orig })

	out, err := runCLI(t, "backup", "home")
	require.NoError(t, err)
	assert.Contains(t, out, "home")
	assert.Contains(t, out, "42 files")
	assert.Contains(t, out, "2.0 KiB")
}

func TestBackupIncompleteIsFailure(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t, config.Set{Name: "home", Paths: []string{"/home"}})

	// restic exit code 3 (incomplete backup) is surfaced as a set failure so the
	// scheduler is alerted, even though a snapshot was created.
	stub := &backupStub{
		respond: func([]string) (*restic.BackupSummary, error) {
			return &restic.BackupSummary{SnapshotID: "partial"},
				&restic.ExitError{Code: restic.ExitIncompleteBackup, Command: "backup"}
		},
	}
	withBackupStub(t, stub)

	_, err := runCLI(t, "backup", "home")
	require.Error(t, err)

	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	// A single failing set is a total failure (all attempted sets failed).
	assert.Equal(t, exitTotalFailure, coder.ExitCode())
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, humanBytes(tc.n), "humanBytes(%d)", tc.n)
	}
}

// errSentinel is a non-ExitError failure to confirm generic errors still flow.
var errSentinel = errors.New("boom")

func TestBackupNonExitErrorIsFailure(t *testing.T) {
	isolateXDG(t)
	writeBackupConfig(t, config.Set{Name: "home", Paths: []string{"/home"}})

	stub := &backupStub{
		respond: func([]string) (*restic.BackupSummary, error) {
			return nil, errSentinel
		},
	}
	withBackupStub(t, stub)

	_, err := runCLI(t, "backup", "home")
	require.Error(t, err)
	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, exitTotalFailure, coder.ExitCode())
}
