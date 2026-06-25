package restic

import (
	"context"
	"fmt"
	"io"
)

// Restore runs `restic restore` with the given argument vector (the snapshot
// selector, --target, and any --include/--exclude filters), streaming restic's
// progress to the logger and returning its exit status. args is the vector after
// the subcommand, e.g. {"latest", "--target", "/tmp/restore"}.
//
// restore is a single streaming invocation, so it reuses the Run path: combined
// output goes to the logger and a non-zero exit surfaces as an *ExitError.
func (r *Runner) Restore(ctx context.Context, args ...string) error {
	full := append([]string{"restore"}, args...)
	return r.Run(ctx, full...)
}

// Dump runs `restic dump <snapshot> <path>` and streams the file's raw contents
// to w. args is the vector after the subcommand (the snapshot selector and the
// path within it, e.g. {"latest", "/etc/hosts"}); --json is never added because
// dump emits the file bytes verbatim on stdout.
//
// Unlike the line-oriented streaming used elsewhere, the stdout bytes are copied
// to w untouched so binary files pass through without corruption. restic's
// progress/errors on stderr are streamed to the logger. A non-zero exit surfaces
// as an *ExitError (e.g. an absent path or a directory target), so the caller can
// map restic's exit code to an icebeam exit code.
func (r *Runner) Dump(ctx context.Context, w io.Writer, args ...string) error {
	dumpArgs := append([]string{"dump"}, args...)

	cmd, err := r.command(ctx, dumpArgs)
	if err != nil {
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("restic: pipe stdout: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("restic: pipe stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restic: start dump: %w", err)
	}

	// Drain stderr to the logger so progress/errors stay visible without
	// polluting the binary stream copied to w. The goroutine returns when stderr
	// closes on process exit.
	tail := &outputTail{}
	stderrDone := make(chan struct{})
	go func() {
		r.streamOutput(stderr, "dump", tail)
		close(stderrDone)
	}()

	// Copy the raw file bytes through unchanged. Read to EOF before Wait (a
	// StdoutPipe constraint). A copy failure is reported, but the process is
	// still waited on so it is not left running.
	_, copyErr := io.Copy(w, stdout)
	<-stderrDone

	waitErr := r.wait(ctx, cmd, dumpArgs, tail)
	if waitErr != nil {
		return waitErr
	}
	if copyErr != nil {
		return fmt.Errorf("restic: stream dump output: %w", copyErr)
	}
	return nil
}
