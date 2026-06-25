package restic

import (
	"fmt"
	"strings"
)

// restic's documented exit codes. See the restic manual ("Exit Codes"). The
// higher codes arrived over the 0.17.x line: 10 (repo-not-exist) and 11
// (repo-locked) in 0.17.0, 12 (wrong-password) in 0.17.1. Older restic (icebeam
// supports down to 0.16.0; see config.defaultMinVersion) collapses all of them
// onto the generic code 1, so the predicates below fall back to matching restic's
// fatal-message text when the code is 1 (see the match* helpers and
// ExitError.output). On restic 0.17.0+ the exact-code path wins.
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
	// output is a bounded tail of restic's diagnostic output, used to classify
	// repo-state failures on restic <0.17.x where the exit code is the generic 1.
	output string
}

// Error implements error.
func (e *ExitError) Error() string {
	return fmt.Sprintf("restic: %s exited with status %d%s", e.Command, e.Code, describeCode(e.Code))
}

// Unwrap exposes the underlying *exec.ExitError.
func (e *ExitError) Unwrap() error { return e.err }

// IsRepoLocked reports whether the failure was a repository lock contention.
func (e *ExitError) IsRepoLocked() bool {
	return e.Code == ExitRepoLocked || (e.Code == ExitGeneric && matchRepoLocked(e.output))
}

// IsRepoNotExist reports whether the repository does not exist.
func (e *ExitError) IsRepoNotExist() bool {
	return e.Code == ExitRepoNotExist || (e.Code == ExitGeneric && matchRepoNotExist(e.output))
}

// IsWrongPassword reports whether the repository password was rejected.
func (e *ExitError) IsWrongPassword() bool {
	return e.Code == ExitWrongPassword || (e.Code == ExitGeneric && matchWrongPassword(e.output))
}

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

// restic before 0.17.x reports repository-state failures with the generic exit
// code 1 and distinguishes them only in its fatal-message text. The matchers
// below detect those messages so the predicates stay accurate on older restic.
// The substrings are restic's English-only output (it is not localized) and were
// verified against restic 0.16.4; on 0.17.0+ the exact-code path runs first, so
// these only fire on the older releases (and on 0.17.0 wrong-password, which used
// code 1 before exit code 12 landed in 0.17.1).

// matchRepoNotExist detects "unable to open config file" / "Is there a repository
// at the following location?".
func matchRepoNotExist(out string) bool {
	return containsFold(out, "unable to open config file") ||
		containsFold(out, "is there a repository at the following location")
}

// matchRepoLocked detects "unable to create lock in backend: repository is
// already locked ...".
func matchRepoLocked(out string) bool {
	return containsFold(out, "repository is already locked")
}

// matchWrongPassword detects "wrong password or no key found".
func matchWrongPassword(out string) bool {
	return containsFold(out, "wrong password or no key found")
}

// containsFold reports whether s contains substr, ignoring ASCII case. substr
// must already be lowercase.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), substr)
}
