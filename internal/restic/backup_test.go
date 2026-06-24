package restic

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
)

// newRunnerWithLogger builds a Runner pointed at a stub binary that streams
// restic output to the given logger (newRunner uses a nil logger).
func newRunnerWithLogger(t *testing.T, binary string, logger *logging.Logger) *Runner {
	t.Helper()

	cfg := config.Default()
	cfg.Repository.URL = "rest:http://nas.local:8000/icebeam"
	cfg.Restic.Binary = binary
	cfg.Restic.MinVersion = ""

	store, err := credentials.Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Set(credentials.RepoPassword, "s3cr3t"))

	r, err := New(&cfg, store, logger)
	require.NoError(t, err)
	return r
}

func TestBackupParsesSummaryAndAppendsJSON(t *testing.T) {
	t.Parallel()

	// The stub asserts `backup --json` is the start of its argv, echoes a couple
	// of status lines, then emits the final summary message restic produces.
	stub := writeStub(t, `
[ "$1" = "backup" ] || { echo "missing backup subcommand" >&2; exit 2; }
[ "$2" = "--json" ] || { echo "missing --json" >&2; exit 2; }
echo '{"message_type":"status","percent_done":0.5}'
echo '{"message_type":"summary","files_new":3,"files_changed":1,"total_files_processed":42,"total_bytes_processed":2048,"data_added":1024,"snapshot_id":"abc123"}'
exit 0
`)
	r := newRunner(t, stub, nil)

	summary, err := r.Backup(context.Background(), "/data", "--tag", "home")
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, 3, summary.FilesNew)
	assert.Equal(t, 1, summary.FilesChanged)
	assert.Equal(t, 42, summary.TotalFilesProcessed)
	assert.Equal(t, uint64(2048), summary.TotalBytesProcessed)
	assert.Equal(t, uint64(1024), summary.DataAdded)
	assert.Equal(t, "abc123", summary.SnapshotID)
}

func TestBackupStreamsNonSummaryOutputToLogger(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, `
echo '{"message_type":"status","percent_done":0.5,"current_files":["/data/file"]}'
echo 'open repository' >&2
echo '{"message_type":"summary","snapshot_id":"deadbeef","total_files_processed":1}'
exit 0
`)

	var buf bytes.Buffer
	logger := logging.NewWithWriter(&buf, slog.LevelDebug, logging.Options{})
	r := newRunnerWithLogger(t, stub, logger)

	summary, err := r.Backup(context.Background(), "/data")
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", summary.SnapshotID)

	out := buf.String()
	// Non-summary status line and stderr both reach the logger.
	assert.Contains(t, out, "percent_done")
	assert.Contains(t, out, "open repository")
	// The summary line is consumed for the totals, not forwarded verbatim.
	assert.NotContains(t, out, `"message_type":"summary"`)
}

func TestBackupMapsIncompleteToExitError(t *testing.T) {
	t.Parallel()

	// restic exits 3 when a backup completed but some source files were
	// unreadable; the summary is still produced and must be returned alongside
	// the *ExitError so callers can report progress.
	stub := writeStub(t, `
echo '{"message_type":"summary","snapshot_id":"partial1","total_files_processed":5}'
exit 3
`)
	r := newRunner(t, stub, nil)

	summary, err := r.Backup(context.Background(), "/data")
	require.Error(t, err)

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.True(t, exitErr.IsIncompleteBackup())

	require.NotNil(t, summary)
	assert.Equal(t, "partial1", summary.SnapshotID)
}

func TestBackupSurfacesHardFailure(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, "exit 1\n")
	r := newRunner(t, stub, nil)

	_, err := r.Backup(context.Background(), "/data")
	require.Error(t, err)

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitGeneric, exitErr.Code)
}
