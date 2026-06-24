package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
)

// runReconfigureCmd drives the reconfigure command with the given args and a
// stdin string, returning combined output and the execution error.
func runReconfigureCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"reconfigure"}, args...))

	err := root.Execute()
	return out.String(), err
}

func TestReconfigureErrorsWhenUnconfigured(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	out, err := runReconfigureCmd(t, "",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file",
	)
	require.ErrorIs(t, err, config.ErrNotConfigured)

	// Nothing was probed and nothing was written: reconfigure bails before any work.
	assert.Empty(t, stub.calls)
	assert.NotContains(t, out, "icebeam run")

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	_, statErr := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(statErr), "config must not be written when unconfigured")
}

func TestReconfigureEditsAValueOnConfiguredMachine(t *testing.T) {
	isolateXDG(t)

	// Existing repo verified on the seed run, then again on the reconfigure probe.
	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	cfgPath := seedExistingConfig(t, stub)

	// Reconfigure interactively, accepting every pre-filled default with empty
	// input except the set name, which is changed to "laptop".
	stdin := strings.Join([]string{
		"",       // repo URL → keep
		"laptop", // set name → change
		"",       // paths → keep
		"",       // REST username → keep (none stored → blank)
		"",       // REST password → keep (none stored → blank)
		"",       // repo password → keep existing
	}, "\n") + "\n"

	out, err := runReconfigureCmd(t, stdin)
	require.NoError(t, err)
	assert.Contains(t, out, "Existing config found")

	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	// Changed value applied; carried-forward values preserved.
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "laptop", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/data"}, cfg.Sets[0].Paths)
	assert.Equal(t, []string{"**/node_modules"}, cfg.Sets[0].Exclude)
	assert.Equal(t, []string{"home"}, cfg.Sets[0].Tags)

	// The stored repository password is left intact.
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "pw", got)
}

func TestReconfigureAcceptsValueFlags(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	cfgPath := seedExistingConfig(t, stub)

	// A flag-supplied repo URL overrides the loaded default and suppresses its
	// prompt; only the kept password prompt remains. The changed repo URL forces a
	// re-verifying probe.
	out, err := runReconfigureCmd(t, "\n",
		"--repo", "rest:https://nas.local:8000/flagged",
	)
	require.NoError(t, err)
	assert.NotContains(t, out, "Repository URL")

	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/flagged", cfg.Repository.URL)
	// Carried-forward set values from the loaded config are unchanged.
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "home", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/data"}, cfg.Sets[0].Paths)
}
