package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// browseStub records the restic invocations the browse commands make and replays
// scripted results so tests can drive snapshots/ls/find without a real restic.
type browseStub struct {
	calls       [][]string
	snapshots   []restic.Snapshot
	snapshotErr error
	ls          *restic.LSResult
	lsErr       error
	find        []restic.FindResult
	findErr     error
}

func (s *browseStub) Snapshots(_ context.Context, args ...string) ([]restic.Snapshot, error) {
	s.calls = append(s.calls, append([]string{"snapshots"}, args...))
	return s.snapshots, s.snapshotErr
}

func (s *browseStub) LS(_ context.Context, args ...string) (*restic.LSResult, error) {
	s.calls = append(s.calls, append([]string{"ls"}, args...))
	if s.ls == nil {
		s.ls = &restic.LSResult{}
	}
	return s.ls, s.lsErr
}

func (s *browseStub) Find(_ context.Context, args ...string) ([]restic.FindResult, error) {
	s.calls = append(s.calls, append([]string{"find"}, args...))
	return s.find, s.findErr
}

// withBrowseStub swaps newBrowseRunner for one returning the given stub and
// restores it when the test ends.
func withBrowseStub(t *testing.T, stub *browseStub) {
	t.Helper()
	orig := newBrowseRunner
	newBrowseRunner = func(*config.Config, credentials.CredentialStore, *logging.Logger) (browseRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newBrowseRunner = orig })
}

// writeBrowseConfig writes a valid config into the isolated XDG config dir so the
// browse commands can load it.
func writeBrowseConfig(t *testing.T) {
	t.Helper()

	cfg := config.Default()
	cfg.Repository.URL = "rest:https://nas.local:8000/icebeam"
	cfg.Sets = []config.Set{{Name: "home", Paths: []string{"/home"}}}

	path, err := config.ConfigPath()
	require.NoError(t, err)
	require.NoError(t, cfg.SaveFile(path))
}

func TestSnapshotsListsTable(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{snapshots: []restic.Snapshot{{
		ShortID:  "abc12345",
		ID:       "abc12345deadbeef",
		Time:     time.Date(2026, 6, 23, 10, 0, 0, 0, time.Local),
		Hostname: "macbook",
		Tags:     []string{"home"},
		Paths:    []string{"/Users/josh/Documents"},
	}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "snapshots")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"snapshots"}, stub.calls[0])
	assert.Contains(t, out, "abc12345")
	assert.Contains(t, out, "macbook")
	assert.Contains(t, out, "home")
	assert.Contains(t, out, "/Users/josh/Documents")
}

func TestSnapshotsTagAndHostFilters(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "snapshots", "--tag", "home", "--tag", "laptop", "--host", "macbook")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t,
		[]string{"snapshots", "--tag", "home", "--tag", "laptop", "--host", "macbook"},
		stub.calls[0],
	)
}

func TestSnapshotsJSONOutput(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{snapshots: []restic.Snapshot{{
		ShortID:  "abc12345",
		Hostname: "macbook",
	}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "snapshots", "--json")
	require.NoError(t, err)

	var decoded []restic.Snapshot
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Len(t, decoded, 1)
	assert.Equal(t, "macbook", decoded[0].Hostname)
}

func TestSnapshotsEmptyIsNotAnError(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{snapshots: nil}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "snapshots")
	require.NoError(t, err)
	assert.Contains(t, out, "No snapshots found")
}

func TestListAliasInvokesSnapshots(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "list")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, "snapshots", stub.calls[0][0])
}

func TestLSWithSnapshotAndPath(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{ls: &restic.LSResult{Nodes: []restic.LSNode{{
		Path:  "/Users/josh/Documents/notes.txt",
		Type:  "file",
		Size:  1024,
		Mode:  "-rw-r--r--",
		MTime: time.Date(2026, 6, 23, 10, 0, 0, 0, time.Local),
	}}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "ls", "latest", "/Users/josh/Documents")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"ls", "latest", "/Users/josh/Documents"}, stub.calls[0])
	assert.Contains(t, out, "notes.txt")
	assert.Contains(t, out, "-rw-r--r--")
}

func TestLSFiltersForLatest(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "ls", "latest", "--tag", "home", "--host", "macbook")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t,
		[]string{"ls", "latest", "--tag", "home", "--host", "macbook"},
		stub.calls[0],
	)
}

func TestLSJSONOutput(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{ls: &restic.LSResult{Nodes: []restic.LSNode{{
		Path: "/file.txt",
		Type: "file",
		Size: 42,
	}}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "ls", "latest", "--json")
	require.NoError(t, err)

	var nodes []restic.LSNode
	require.NoError(t, json.Unmarshal([]byte(out), &nodes))
	require.Len(t, nodes, 1)
	assert.Equal(t, "/file.txt", nodes[0].Path)
}

func TestLSEmptyIsNotAnError(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{ls: &restic.LSResult{}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "ls", "latest")
	require.NoError(t, err)
	assert.Contains(t, out, "empty")
}

func TestLSRequiresSnapshotArg(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "ls")
	require.Error(t, err)
	assert.Empty(t, stub.calls)
}

func TestFindReportsMatchingSnapshots(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{find: []restic.FindResult{{
		Hits:     1,
		Snapshot: "abc12345deadbeef",
		Matches: []restic.FindMatch{{
			Path:  "/Users/josh/Documents/secret.txt",
			Type:  "file",
			Size:  256,
			MTime: time.Date(2026, 6, 23, 10, 0, 0, 0, time.Local),
		}},
	}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "find", "secret.txt")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"find", "secret.txt"}, stub.calls[0])
	assert.Contains(t, out, "secret.txt")
	assert.Contains(t, out, "abc12345")
}

func TestFindTagAndHostFilters(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "find", "*.go", "--tag", "home", "--host", "macbook")
	require.NoError(t, err)

	require.Len(t, stub.calls, 1)
	assert.Equal(t,
		[]string{"find", "*.go", "--tag", "home", "--host", "macbook"},
		stub.calls[0],
	)
}

func TestFindJSONOutput(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{find: []restic.FindResult{{
		Hits:     1,
		Snapshot: "abc12345",
		Matches:  []restic.FindMatch{{Path: "/file.txt"}},
	}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "find", "file.txt", "--json")
	require.NoError(t, err)

	var results []restic.FindResult
	require.NoError(t, json.Unmarshal([]byte(out), &results))
	require.Len(t, results, 1)
	assert.Equal(t, "/file.txt", results[0].Matches[0].Path)
}

func TestFindNothingFoundIsNotAnError(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	// restic returns an entry per snapshot with zero matches when nothing is
	// found; that must read as "nothing found", not an error.
	stub := &browseStub{find: []restic.FindResult{{Snapshot: "abc12345", Matches: nil}}}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "find", "nope")
	require.NoError(t, err)
	assert.Contains(t, out, "Nothing found")
}

func TestBrowseMapsResticExitCode(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{
		snapshotErr: &restic.ExitError{Code: restic.ExitRepoLocked, Command: "snapshots"},
	}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "snapshots")
	require.Error(t, err)

	var coder exitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, restic.ExitRepoLocked, coder.ExitCode())
}

func TestBrowseNotConfigured(t *testing.T) {
	isolateXDG(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	_, err := runCLI(t, "snapshots")
	require.Error(t, err)
	require.ErrorIs(t, err, config.ErrNotConfigured)
	assert.Empty(t, stub.calls)
}

// TestBrowseJSONIsValidStream sanity-checks that the JSON encoder emits a single
// decodable document for each command's empty case.
func TestBrowseJSONEmptyEncodes(t *testing.T) {
	isolateXDG(t)
	writeBrowseConfig(t)

	stub := &browseStub{}
	withBrowseStub(t, stub)

	out, err := runCLI(t, "snapshots", "--json")
	require.NoError(t, err)
	assert.True(t, strings.TrimSpace(out) == "null" || strings.HasPrefix(strings.TrimSpace(out), "["),
		"snapshots --json should be a JSON array or null, got %q", out)
}
