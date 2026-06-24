package restic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotsParsesJSONArray(t *testing.T) {
	t.Parallel()

	// The stub asserts `snapshots --json` is the start of its argv (RunJSON
	// prepends --json) and emits a snapshots array.
	stub := writeStub(t, `
[ "$1" = "--json" ] || { echo "missing --json" >&2; exit 2; }
[ "$2" = "snapshots" ] || { echo "missing snapshots subcommand" >&2; exit 2; }
cat <<'JSON'
[
  {"id":"abc123deadbeef","short_id":"abc123de","time":"2026-06-23T10:00:00Z","hostname":"macbook","tags":["home"],"paths":["/Users/josh"]}
]
JSON
exit 0
`)
	r := newRunner(t, stub, nil)

	snapshots, err := r.Snapshots(context.Background(), "--tag", "home")
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, "abc123deadbeef", snapshots[0].ID)
	assert.Equal(t, "abc123de", snapshots[0].ShortID)
	assert.Equal(t, "macbook", snapshots[0].Hostname)
	assert.Equal(t, []string{"home"}, snapshots[0].Tags)
	assert.Equal(t, []string{"/Users/josh"}, snapshots[0].Paths)
}

func TestSnapshotsForwardsFilterArgs(t *testing.T) {
	t.Parallel()

	// The stub asserts the subcommand and the forwarded tag/host filters appear in
	// argv after the leading --json; the only way to reach the success exit is for
	// all of them to be present in order.
	stub := writeStub(t, `
[ "$1" = "--json" ] || { echo "missing --json" >&2; exit 2; }
[ "$2" = "snapshots" ] || { echo "missing snapshots subcommand" >&2; exit 2; }
[ "$3" = "--tag" ] || { echo "missing --tag" >&2; exit 2; }
[ "$4" = "home" ] || { echo "missing tag value" >&2; exit 2; }
[ "$5" = "--host" ] || { echo "missing --host" >&2; exit 2; }
[ "$6" = "macbook" ] || { echo "missing host value" >&2; exit 2; }
echo '[]'
exit 0
`)
	r := newRunner(t, stub, nil)

	snapshots, err := r.Snapshots(context.Background(), "--tag", "home", "--host", "macbook")
	require.NoError(t, err)
	assert.Empty(t, snapshots)
}

func TestFindParsesJSONArray(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, `
[ "$1" = "--json" ] || { echo "missing --json" >&2; exit 2; }
[ "$2" = "find" ] || { echo "missing find subcommand" >&2; exit 2; }
cat <<'JSON'
[
  {"hits":1,"snapshot":"abc123deadbeef","matches":[{"path":"/Users/josh/secret.txt","type":"file","size":256,"mtime":"2026-06-23T10:00:00Z"}]}
]
JSON
exit 0
`)
	r := newRunner(t, stub, nil)

	results, err := r.Find(context.Background(), "secret.txt")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, 1, results[0].Hits)
	assert.Equal(t, "abc123deadbeef", results[0].Snapshot)
	require.Len(t, results[0].Matches, 1)
	assert.Equal(t, "/Users/josh/secret.txt", results[0].Matches[0].Path)
	assert.Equal(t, uint64(256), results[0].Matches[0].Size)
}

func TestLSParsesNDJSONStream(t *testing.T) {
	t.Parallel()

	// `ls --json` is a newline-delimited stream: a leading snapshot message
	// followed by one node message per entry. The stub also asserts the snapshot
	// selector is forwarded as the first non-flag arg.
	stub := writeStub(t, `
[ "$1" = "ls" ] || { echo "missing ls subcommand" >&2; exit 2; }
[ "$2" = "--json" ] || { echo "missing --json" >&2; exit 2; }
[ "$3" = "latest" ] || { echo "missing snapshot selector" >&2; exit 2; }
echo '{"message_type":"snapshot","id":"abc123deadbeef","short_id":"abc123de","hostname":"macbook","paths":["/Users/josh"]}'
echo '{"message_type":"node","path":"/Users/josh/notes.txt","type":"file","size":1024,"permissions":"-rw-r--r--","mtime":"2026-06-23T10:00:00Z"}'
echo '{"message_type":"node","path":"/Users/josh/sub","type":"dir","size":0,"permissions":"drwxr-xr-x"}'
exit 0
`)
	r := newRunner(t, stub, nil)

	result, err := r.LS(context.Background(), "latest")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "abc123deadbeef", result.Snapshot.ID)
	assert.Equal(t, "macbook", result.Snapshot.Hostname)
	require.Len(t, result.Nodes, 2)
	assert.Equal(t, "/Users/josh/notes.txt", result.Nodes[0].Path)
	assert.Equal(t, uint64(1024), result.Nodes[0].Size)
	assert.Equal(t, "-rw-r--r--", result.Nodes[0].Mode)
	assert.Equal(t, "dir", result.Nodes[1].Type)
}

func TestLSEmptyStream(t *testing.T) {
	t.Parallel()

	// A snapshot with no contents emits only the snapshot message; the node list
	// is empty (and not an error).
	stub := writeStub(t, `
echo '{"message_type":"snapshot","id":"abc123deadbeef"}'
exit 0
`)
	r := newRunner(t, stub, nil)

	result, err := r.LS(context.Background(), "latest")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Nodes)
}

func TestLSSurfacesExitError(t *testing.T) {
	t.Parallel()

	// A non-existent repository (restic exit 10) must surface as an *ExitError so
	// the caller can map it to an icebeam exit code.
	stub := writeStub(t, `
echo "repository does not exist" >&2
exit 10
`)
	r := newRunner(t, stub, nil)

	_, err := r.LS(context.Background(), "latest")
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.True(t, exitErr.IsRepoNotExist())
}
