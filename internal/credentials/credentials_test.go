package credentials

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAutoSelectsKeychainWhenAvailable(t *testing.T) {
	t.Parallel()

	store, err := open(BackendAuto, t.TempDir(), newFakeKeyring())
	require.NoError(t, err)
	assert.Equal(t, BackendKeychain, store.Backend())
}

func TestOpenAutoFallsBackToFileWhenUnavailable(t *testing.T) {
	t.Parallel()

	kr := newFakeKeyring()
	kr.unavailable = true

	store, err := open(BackendAuto, t.TempDir(), kr)
	require.NoError(t, err)
	assert.Equal(t, BackendFile, store.Backend())
}

func TestOpenEmptyBackendBehavesLikeAuto(t *testing.T) {
	t.Parallel()

	store, err := open("", t.TempDir(), newFakeKeyring())
	require.NoError(t, err)
	assert.Equal(t, BackendKeychain, store.Backend())
}

func TestOpenForceFileBackend(t *testing.T) {
	t.Parallel()

	// Even with a perfectly available keychain, "file" forces the file backend.
	store, err := open(BackendFile, t.TempDir(), newFakeKeyring())
	require.NoError(t, err)
	assert.Equal(t, BackendFile, store.Backend())
}

func TestOpenForceKeychainBackend(t *testing.T) {
	t.Parallel()

	store, err := open(BackendKeychain, t.TempDir(), newFakeKeyring())
	require.NoError(t, err)
	assert.Equal(t, BackendKeychain, store.Backend())
}

func TestOpenForceKeychainErrorsWhenUnavailable(t *testing.T) {
	t.Parallel()

	kr := newFakeKeyring()
	kr.unavailable = true

	_, err := open(BackendKeychain, t.TempDir(), kr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unavailable")
}

func TestOpenRejectsUnknownBackend(t *testing.T) {
	t.Parallel()

	_, err := open("bogus", t.TempDir(), newFakeKeyring())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown backend")
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

func TestResticPasswordEnvKeychainBackendUsesPasswordVar(t *testing.T) {
	t.Parallel()

	store := newKeychainStore(newFakeKeyring())
	require.NoError(t, store.Set(RepoPassword, "s3cr3t"))

	env, err := ResticPasswordEnv(store)
	require.NoError(t, err)
	require.Len(t, env, 1)
	assert.Equal(t, "RESTIC_PASSWORD=s3cr3t", env[0])
}

func TestResticPasswordEnvErrorsWhenMissing(t *testing.T) {
	t.Parallel()

	store := newKeychainStore(newFakeKeyring())

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
		{"keychain", newKeychainStore(newFakeKeyring())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tc.store.Set(RepoPassword, password))

			env, err := ResticPasswordEnv(tc.store)
			require.NoError(t, err)

			// Each entry is a KEY=value env assignment, not an argv flag.
			for _, e := range env {
				assert.NotContains(t, e, "--password")
				assert.True(t, strings.Contains(e, "="), "env entries must be KEY=value")
			}
		})
	}
}
