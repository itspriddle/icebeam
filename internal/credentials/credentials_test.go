package credentials

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenReturnsFileStore(t *testing.T) {
	t.Parallel()

	store, err := Open(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, BackendFile, store.Backend())
}

func TestResticPasswordEnvFileBackendUsesPasswordFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := newFileStore(dir)
	require.NoError(t, store.Set(RepoPassword, "s3cr3t"))

	env, err := ResticPasswordEnv(store)
	require.NoError(t, err)
	require.Len(t, env, 1)
	assert.Equal(t, "RESTIC_PASSWORD_FILE="+store.PasswordFilePath(), env[0])

	// The password itself must never appear in the env entry for the file
	// backend (restic reads it from the file).
	assert.NotContains(t, env[0], "s3cr3t")
}

func TestResticPasswordEnvErrorsWhenMissing(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()

	_, err := ResticPasswordEnv(store)
	require.Error(t, err)
}

// TestSecretsNeverInCommandArgs documents the core invariant: nothing this
// package produces for restic is a command-line argument. The password is
// surfaced only via the environment (RESTIC_PASSWORD / RESTIC_PASSWORD_FILE),
// which does not leak through the process table the way argv does.
func TestSecretsNeverInCommandArgs(t *testing.T) {
	t.Parallel()

	const password = "p@ssw0rd-leak-canary"

	for _, tc := range []struct {
		name  string
		store CredentialStore
	}{
		{"file", newFileStore(t.TempDir())},
		{"memory", newMemoryStore()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tc.store.Set(RepoPassword, password))

			env, err := ResticPasswordEnv(tc.store)
			require.NoError(t, err)

			// Each entry is a KEY=value env assignment, not an argv flag.
			for _, e := range env {
				assert.NotContains(t, e, "--password")
				assert.Contains(t, e, "=", "env entries must be KEY=value")
			}
		})
	}
}
