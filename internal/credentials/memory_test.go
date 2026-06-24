package credentials

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()

	_, err := store.Get(RepoPassword)
	require.ErrorIs(t, err, ErrNotFound, "missing secret must report ErrNotFound")

	require.NoError(t, store.Set(RepoPassword, "s3cr3t"))

	got, err := store.Get(RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got)

	// Overwrite is supported.
	require.NoError(t, store.Set(RepoPassword, "rotated"))
	got, err = store.Get(RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "rotated", got)

	require.NoError(t, store.Delete(RepoPassword))
	_, err = store.Get(RepoPassword)
	require.ErrorIs(t, err, ErrNotFound)

	require.ErrorIs(t, store.Delete(RepoPassword), ErrNotFound, "deleting a missing secret must report ErrNotFound")
}

func TestMemoryStoreBackendName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, BackendMemory, newMemoryStore().Backend())
}

func TestNewMemoryStoreSeedsSecrets(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore(map[string]string{
		RepoPassword: "s3cr3t",
		RESTUsername: "alice",
		RESTPassword: "hunter2",
	})

	for name, want := range map[string]string{
		RepoPassword: "s3cr3t",
		RESTUsername: "alice",
		RESTPassword: "hunter2",
	} {
		got, err := store.Get(name)
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
}

func TestNewMemoryStoreNilMapIsEmpty(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore(nil)

	_, err := store.Get(RepoPassword)
	require.ErrorIs(t, err, ErrNotFound)

	// An empty seed is still writable.
	require.NoError(t, store.Set(RepoPassword, "later"))
	got, err := store.Get(RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "later", got)
}

func TestNewMemoryStoreCopiesSeedMap(t *testing.T) {
	t.Parallel()

	seed := map[string]string{RepoPassword: "s3cr3t"}
	store := NewMemoryStore(seed)

	// Mutating the caller's map after construction must not affect the store.
	seed[RepoPassword] = "tampered"

	got, err := store.Get(RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got, "store must not alias the caller's seed map")
}

// TestResticPasswordEnvMemoryBackendUsesPasswordVar confirms the in-memory store
// routes through the RESTIC_PASSWORD branch (not RESTIC_PASSWORD_FILE), so the
// pre-persist probe can run with a collected-but-unwritten password.
func TestResticPasswordEnvMemoryBackendUsesPasswordVar(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore(map[string]string{RepoPassword: "s3cr3t"})

	env, err := ResticPasswordEnv(store)
	require.NoError(t, err)
	require.Len(t, env, 1)
	assert.Equal(t, "RESTIC_PASSWORD=s3cr3t", env[0])
	assert.NotContains(t, env[0], "_FILE", "memory backend must not emit RESTIC_PASSWORD_FILE")
}

func TestResticPasswordEnvMemoryBackendErrorsWhenMissing(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore(nil)

	_, err := ResticPasswordEnv(store)
	require.Error(t, err)
}

func TestCopyIntoPersistsAllSecrets(t *testing.T) {
	t.Parallel()

	src := NewMemoryStore(map[string]string{
		RepoPassword: "s3cr3t",
		RESTUsername: "alice",
		RESTPassword: "hunter2",
	})
	dst := newFileStore(t.TempDir())

	require.NoError(t, CopyInto(src, dst))

	for name, want := range map[string]string{
		RepoPassword: "s3cr3t",
		RESTUsername: "alice",
		RESTPassword: "hunter2",
	} {
		got, err := dst.Get(name)
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
}

func TestCopyIntoOverwritesExistingDestinationValues(t *testing.T) {
	t.Parallel()

	dst := newFileStore(t.TempDir())
	require.NoError(t, dst.Set(RepoPassword, "stale"))

	src := NewMemoryStore(map[string]string{RepoPassword: "fresh"})
	require.NoError(t, CopyInto(src, dst))

	got, err := dst.Get(RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "fresh", got)
}

func TestCopyIntoEmptySourceIsNoOp(t *testing.T) {
	t.Parallel()

	dst := newFileStore(t.TempDir())
	require.NoError(t, CopyInto(NewMemoryStore(nil), dst))

	_, err := dst.Get(RepoPassword)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCopyIntoRejectsNonMemorySource(t *testing.T) {
	t.Parallel()

	src := newFileStore(t.TempDir())
	require.NoError(t, src.Set(RepoPassword, "s3cr3t"))

	err := CopyInto(src, newFileStore(t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in-memory store")
}
