package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/version"
)

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.Execute()

	return out.String(), err
}

func TestVersionFlagPrintsVersionString(t *testing.T) {
	out, err := runRoot(t, "--version")
	require.NoError(t, err)

	assert.Contains(t, out, version.Version)
	assert.Contains(t, out, "icebeam")
}

func TestHelpListsCommandGroups(t *testing.T) {
	out, err := runRoot(t, "--help")
	require.NoError(t, err)

	for _, name := range []string{
		"init", "run", "backup", "forget", "prune", "check",
		"snapshots", "ls", "find", "restore", "dump", "schedule",
	} {
		assert.Contains(t, out, name, "help output should list the %q command", name)
	}
}

func TestSnapshotsHasListAlias(t *testing.T) {
	root := NewRootCommand()

	cmd, _, err := root.Find([]string{"list"})
	require.NoError(t, err)
	assert.Equal(t, "snapshots", cmd.Name())
}

func TestStubCommandsReportNotImplemented(t *testing.T) {
	_, err := runRoot(t, "schedule")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")
}
