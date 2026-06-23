package restic

import "fmt"

// restic's documented exit codes. See the restic manual ("Exit Codes"). Older
// restic releases collapsed several conditions onto code 1; the higher codes
// were introduced in restic 0.17+. icebeam treats the specific codes as
// authoritative when present.
const (
	// ExitSuccess indicates the command completed without error.
	ExitSuccess = 0
	// ExitGeneric is a generic failure (and, on older restic, the catch-all for
	// conditions that newer releases give a dedicated code).
	ExitGeneric = 1
	// ExitGoRuntime indicates the Go runtime itself errored.
	ExitGoRuntime = 2
	// ExitIncompleteBackup indicates a backup ran but could not read some
	// source files (the snapshot was still created).
	ExitIncompleteBackup = 3
	// ExitRepoNotExist indicates the repository does not exist.
	ExitRepoNotExist = 10
	// ExitRepoLocked indicates the repository could not be locked (another
	// process holds the lock).
	ExitRepoLocked = 11
	// ExitWrongPassword indicates the repository password was wrong.
	ExitWrongPassword = 12
)

// ExitError reports that a restic invocation exited with a non-zero status. Its
// Code is restic's exit code; the helper predicates (IsRepoLocked, etc.) classify
// the documented codes. Callers can match it with errors.As.
type ExitError struct {
	// Code is restic's process exit code.
	Code int
	// Command is the restic subcommand that failed (for messages).
	Command string
	// err is the underlying *exec.ExitError for unwrapping.
	err error
}

// Error implements error.
func (e *ExitError) Error() string {
	return fmt.Sprintf("restic: %s exited with status %d%s", e.Command, e.Code, describeCode(e.Code))
}

// Unwrap exposes the underlying *exec.ExitError.
func (e *ExitError) Unwrap() error { return e.err }

// IsRepoLocked reports whether the failure was a repository lock contention.
func (e *ExitError) IsRepoLocked() bool { return e.Code == ExitRepoLocked }

// IsRepoNotExist reports whether the repository does not exist.
func (e *ExitError) IsRepoNotExist() bool { return e.Code == ExitRepoNotExist }

// IsWrongPassword reports whether the repository password was rejected.
func (e *ExitError) IsWrongPassword() bool { return e.Code == ExitWrongPassword }

// IsIncompleteBackup reports whether a backup completed but skipped unreadable
// files (a partial success rather than a hard failure).
func (e *ExitError) IsIncompleteBackup() bool { return e.Code == ExitIncompleteBackup }

// describeCode returns a short human description for the documented exit codes,
// or an empty string for codes restic does not specifically document.
func describeCode(code int) string {
	switch code {
	case ExitGoRuntime:
		return " (Go runtime error)"
	case ExitIncompleteBackup:
		return " (backup could not read all source files)"
	case ExitRepoNotExist:
		return " (repository does not exist)"
	case ExitRepoLocked:
		return " (repository is locked by another process)"
	case ExitWrongPassword:
		return " (wrong repository password)"
	default:
		return ""
	}
}
