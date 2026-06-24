package cli

// icebeam process exit codes. They are deliberately distinct so a scheduler
// (launchd/systemd) can tell a clean run from a partial or total backup failure.
const (
	// exitOK indicates the command completed successfully.
	exitOK = 0
	// exitError is a generic failure (usage errors, config problems, a single
	// command that failed, etc.).
	exitError = 1
	// exitTotalFailure indicates a multi-set run in which every attempted set
	// failed.
	exitTotalFailure = 2
	// exitPartialFailure indicates a multi-set run in which at least one set
	// succeeded and at least one failed.
	exitPartialFailure = 3
)

// exitCoder is implemented by errors that carry a specific process exit code.
// Execute consults it so commands can signal partial/total failure distinctly.
type exitCoder interface {
	error
	ExitCode() int
}

// exitError is an error annotated with the process exit code it should produce.
type exitCodeError struct {
	code int
	err  error
}

// newExitError wraps err with the given exit code.
func newExitError(code int, err error) *exitCodeError {
	return &exitCodeError{code: code, err: err}
}

// Error implements error.
func (e *exitCodeError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped error for errors.Is/As.
func (e *exitCodeError) Unwrap() error { return e.err }

// ExitCode reports the process exit code this error maps to.
func (e *exitCodeError) ExitCode() int { return e.code }
