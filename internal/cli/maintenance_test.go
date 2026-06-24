package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// maintenanceStub records the restic invocations the maintenance commands make
// and replays a scripted result so tests can drive forget/prune/check without a
// real restic.
type maintenanceStub struct {
	calls [][]string
	err   error
}

func (s *maintenanceStub) Run(_ context.Context, args ...string) error {
	s.calls = append(s.calls, args)
	return s.err
}

// withMaintenanceStub swaps newMaintenanceRunner for one returning the given stub
// and restores it when the test ends.
func withMaintenanceStub(t *testing.T, stub *maintenanceStub) {
	t.Helper()
	orig := newMaintenanceRunner
	newMaintenanceRunner = func(*config.Config, credentials.CredentialStore, *logging.Logger) (maintenanceRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newMaintenanceRunner = orig })
}

// writeMaintenanceConfig writes a valid config with a known retention policy into
// the isolated XDG config dir so the maintenance commands can load it.
func writeMaintenanceConfig(t *testing.T) {
	t.Helper()

	cfg := config.Default()
	cfg.Repository.URL = "rest:https://nas.local:8000/icebeam"
	cfg.Credentials.Backend = credentials.BackendFile
	cfg.Retention = config.Retention{
		KeepDaily:   7,
		KeepWeekly:  4,
		KeepMonthly: 12,
		KeepYearly:  3,
	}
	cfg.Sets = []config.Set{{Name: "home", Paths: []string{"/home"}}}

	path, err := config.ConfigPath()
	require.NoError(t, err)
	require.NoError(t, cfg.SaveFile(path))
}

func TestForgetDerivesRetentionFlagsAndPrunes(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "forget")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	got := strings.Join(stub.calls[0], " ")
	// Retention from config becomes --keep-* flags; grouping by host,tags scopes
	// each set independently; --prune runs inline by default.
	assert.Equal(t,
		"forget --group-by host,tags "+
			"--keep-daily 7 --keep-weekly 4 --keep-monthly 12 --keep-yearly 3 "+
			"--prune",
		got,
	)
}

func TestForgetNoPruneOmitsPruneFlag(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "forget", "--no-prune")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.NotContains(t, stub.calls[0], "--prune")
}

func TestForgetDryRunShowsWithoutPruning(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "forget", "--dry-run")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	args := stub.calls[0]
	assert.Contains(t, args, "--dry-run")
	// A dry run never modifies the repository, so it must not request a prune.
	assert.NotContains(t, args, "--prune")
}

func TestForgetSkipsZeroRetentionValues(t *testing.T) {
	isolateXDG(t)

	// A config with no retention policy set: no --keep-* flags should appear.
	cfg := config.Default()
	cfg.Repository.URL = "rest:https://nas.local:8000/icebeam"
	cfg.Credentials.Backend = credentials.BackendFile
	cfg.Sets = []config.Set{{Name: "home", Paths: []string{"/home"}}}
	path, err := config.ConfigPath()
	require.NoError(t, err)
	require.NoError(t, cfg.SaveFile(path))

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err = runCLI(t, "forget", "--no-prune")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	got := strings.Join(stub.calls[0], " ")
	assert.NotContains(t, got, "--keep-")
}

func TestPruneRunsStandalone(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "prune")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"prune"}, stub.calls[0])
}

func TestCheckDefaultsToMetadataOnly(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "check")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	// No --read-data-subset by default: metadata-only verification.
	assert.Equal(t, []string{"check"}, stub.calls[0])
}

func TestCheckReadDataSubsetFlag(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "check", "--read-data-subset", "10%")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"check", "--read-data-subset", "10%"}, stub.calls[0])
}

func TestMaintenanceMapsResticExitCode(t *testing.T) {
	isolateXDG(t)
	writeMaintenanceConfig(t)

	// A locked repository (restic exit 11) must surface its exit code so a
	// scheduler can act on it.
	stub := &maintenanceStub{
		err: &restic.ExitError{Code: restic.ExitRepoLocked, Command: "forget"},
	}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "forget")
	require.Error(t, err)

	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, restic.ExitRepoLocked, coder.ExitCode())
}

func TestMaintenanceNotConfigured(t *testing.T) {
	isolateXDG(t)

	stub := &maintenanceStub{}
	withMaintenanceStub(t, stub)

	_, err := runCLI(t, "check")
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrNotConfigured)
	assert.Empty(t, stub.calls)
}
