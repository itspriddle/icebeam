package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Run executes a restic subcommand, streaming its combined output to the logger
// line by line, and returns when the process exits. A non-zero exit is reported
// as a *ExitError carrying the restic exit code (see exit code helpers such as
// IsRepoLocked). The context cancels the underlying process cleanly on
// SIGINT/SIGTERM (FR-12).
//
// args is the full restic argument vector beginning with the subcommand, e.g.
// {"snapshots", "--tag", "home"}. Secrets are never included in args; they reach
// restic through the environment built by env.
func (r *Runner) Run(ctx context.Context, args ...string) error {
	cmd, err := r.command(ctx, args)
	if err != nil {
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("restic: pipe stdout: %w", err)
	}
	cmd.Stderr = cmd.Stdout // combine streams onto one pipe

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restic: start %s: %w", commandName(args), err)
	}

	r.streamOutput(stdout, commandName(args))

	return r.wait(ctx, cmd, args)
}

// RunJSON executes a restic subcommand with --json appended, captures its stdout,
// and decodes it into out (a pointer). It is for restic subcommands that emit a
// single JSON document (e.g. `snapshots --json`). restic's progress/error output
// on stderr is streamed to the logger so it remains visible.
func (r *Runner) RunJSON(ctx context.Context, out any, args ...string) error {
	data, err := r.captureJSON(ctx, args)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("restic: parse %s --json output: %w", commandName(args), err)
	}
	return nil
}

// captureJSON runs a subcommand with --json and returns its raw stdout. stderr
// is streamed to the logger so progress/errors remain visible while stdout is
// reserved for the machine-readable document.
func (r *Runner) captureJSON(ctx context.Context, args []string) ([]byte, error) {
	jsonArgs := append([]string{"--json"}, args...)

	cmd, err := r.command(ctx, jsonArgs)
	if err != nil {
		return nil, err
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("restic: pipe stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("restic: start %s: %w", commandName(args), err)
	}

	r.streamOutput(stderr, commandName(args))

	if err := r.wait(ctx, cmd, args); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// command constructs the *exec.Cmd for a subcommand with the restic environment
// applied. The subcommand and its flags go in argv; secrets go only in env.
func (r *Runner) command(ctx context.Context, args []string) (*exec.Cmd, error) {
	if err := r.ensureVersion(ctx); err != nil {
		return nil, err
	}

	env, err := r.env(ctx)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, r.binary, args...) //nolint:gosec // binary resolved from config/PATH; args are subcommands/flags, never secrets
	cmd.Env = env
	return cmd, nil
}

// streamOutput reads the process output line by line and logs each line. With no
// logger, output is drained so the pipe never blocks the child.
func (r *Runner) streamOutput(out io.Reader, command string) {
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if r.logger != nil {
			r.logger.Info("restic output", "command", command, "line", line)
		}
	}
}

// wait waits for the process to exit and maps a non-zero exit into an *ExitError.
// A context cancellation surfaces as the context's error (wrapped) so callers
// can distinguish a user-requested abort from a restic failure with errors.Is.
func (r *Runner) wait(ctx context.Context, cmd *exec.Cmd, args []string) error {
	err := cmd.Wait()
	if err == nil {
		return nil
	}

	// exec.CommandContext kills the process when the context is cancelled; the
	// resulting Wait error is incidental, so report the cancellation instead.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("restic: %s cancelled: %w", commandName(args), ctxErr)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &ExitError{
			Code:    exitErr.ExitCode(),
			Command: commandName(args),
			err:     err,
		}
	}

	return fmt.Errorf("restic: %s: %w", commandName(args), err)
}

// commandName returns the subcommand name (the first non-flag argument) for use
// in error and log messages, falling back to "restic" when args is empty.
func commandName(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return defaultBinary
}
