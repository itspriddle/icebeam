package restic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitMessageMatchers(t *testing.T) {
	t.Parallel()

	// The positive strings are the exact fatal messages captured from restic
	// 0.16.4; the matchers must recognize them so the ExitError predicates stay
	// accurate on restic that lacks the 10/11/12 exit codes.
	t.Run("repo-not-exist", func(t *testing.T) {
		t.Parallel()
		assert.True(t, matchRepoNotExist("Fatal: unable to open config file: stat /x/config: no such file or directory"))
		assert.True(t, matchRepoNotExist("Is there a repository at the following location?"))
		assert.False(t, matchRepoNotExist("Fatal: connection refused"))
	})

	t.Run("repo-locked", func(t *testing.T) {
		t.Parallel()
		assert.True(t, matchRepoLocked("unable to create lock in backend: repository is already locked by PID 1 on host by user (UID 501, GID 20)"))
		assert.False(t, matchRepoLocked("Fatal: connection refused"))
	})

	t.Run("wrong-password", func(t *testing.T) {
		t.Parallel()
		assert.True(t, matchWrongPassword("Fatal: wrong password or no key found"))
		assert.True(t, matchWrongPassword("WRONG PASSWORD OR NO KEY FOUND")) // case-insensitive
		assert.False(t, matchWrongPassword("Fatal: connection refused"))
	})
}
