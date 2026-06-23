package restic

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelStub returns the path to a stub restic that publishes the PIDs of both
// itself and a long-lived grandchild it spawns, then blocks. The grandchild
// inherits the output pipe and keeps it open, reproducing the real bug where a
// restic descendant blocks the reader after the direct child is killed.
//
// Cancellation must terminate the whole process group: both PIDs must be gone.
// The stub writes its own pid to pidFile and the grandchild's pid to childFile.
func cancelStub(t *testing.T, pidFile, childFile string) string {
	t.Helper()

	// Start a grandchild sleep that holds stdout open, record its pid, then block
	// on it. The grandchild keeps the stdout pipe's write-end open so a reader
	// that drains to EOF would hang unless the descendant is also reaped.
	body := `
sleep 300 &
child=$!
printf '%s' "$child" > ` + childFile + `
printf '%s' "$$" > ` + pidFile + `
wait "$child"
`
	return writeStub(t, body)
}

// waitForPID polls pidFile until it contains a parseable PID and returns it,
// failing the test if it never appears. This replaces a fixed time.Sleep so the
// test starts deterministically rather than racing the wall clock.
func waitForPID(t *testing.T, pidFile string) int {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile) //nolint:gosec // test temp-dir path, not arbitrary input
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process never published its pid to %s", pidFile)
	return 0
}

// processGone reports whether the given pid is no longer running. syscall.Kill
// with signal 0 probes liveness without sending a signal; ESRCH means the
// process is gone, EPERM means it exists but isn't ours (still alive).
func processGone(pid int) bool {
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

// requireGone asserts the pid disappears promptly. With the process-group kill
// in place this happens within WaitDelay; allow generous slack for slow CI.
func requireGone(t *testing.T, pid int, what string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if processGone(pid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s (pid %d) was not terminated by cancellation — orphan left behind", what, pid)
}

// assertCancelled asserts an operation returned with the context cancellation,
// not a restic exit error.
func assertCancelled(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var exitErr *ExitError
	assert.False(t, errors.As(err, &exitErr), "cancellation should not be reported as a restic exit error")
}

// runCancelTest drives one streaming entrypoint to cancellation: it starts the
// op against the cancel stub, waits for the stub and its grandchild to publish
// their pids, cancels, and asserts the op returns context.Canceled promptly and
// both processes are actually gone (no orphan).
func runCancelTest(t *testing.T, op func(ctx context.Context, r *Runner) error) {
	t.Helper()

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	childFile := filepath.Join(dir, "child")
	stub := cancelStub(t, pidFile, childFile)
	r := newRunner(t, stub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- op(ctx, r) }()

	// Start deterministically: wait until both the stub and its grandchild have
	// published their pids before cancelling (no fixed sleep, no wall-clock race).
	stubPID := waitForPID(t, pidFile)
	childPID := waitForPID(t, childFile)

	cancel()

	select {
	case err := <-done:
		assertCancelled(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled restic operation did not return promptly")
	}

	// The whole process group must be reaped: neither the direct child nor its
	// descendant may survive (the original bug orphaned the descendant).
	requireGone(t, stubPID, "restic stub")
	requireGone(t, childPID, "restic descendant")
}

func TestRunCancellation(t *testing.T) {
	t.Parallel()

	runCancelTest(t, func(ctx context.Context, r *Runner) error {
		return r.Run(ctx, "backup")
	})
}

func TestBackupCancellation(t *testing.T) {
	t.Parallel()

	runCancelTest(t, func(ctx context.Context, r *Runner) error {
		_, err := r.Backup(ctx, "/data")
		return err
	})
}

func TestDumpCancellation(t *testing.T) {
	t.Parallel()

	runCancelTest(t, func(ctx context.Context, r *Runner) error {
		var buf bytes.Buffer
		return r.Dump(ctx, &buf, "latest", "/etc/hosts")
	})
}

func TestLSCancellation(t *testing.T) {
	t.Parallel()

	runCancelTest(t, func(ctx context.Context, r *Runner) error {
		_, err := r.LS(ctx, "latest")
		return err
	})
}

func TestRunJSONCancellation(t *testing.T) {
	t.Parallel()

	runCancelTest(t, func(ctx context.Context, r *Runner) error {
		var out []any
		return r.RunJSON(ctx, &out, "snapshots")
	})
}
