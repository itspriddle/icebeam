package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := newFileStore(t.TempDir())

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

func TestFileStoreSetsRestrictivePermissions(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}

	dir := filepath.Join(t.TempDir(), "icebeam")
	store := newFileStore(dir)

	require.NoError(t, store.Set(RepoPassword, "s3cr3t"))

	fi, err := os.Stat(store.PasswordFilePath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(fileMode), fi.Mode().Perm(), "credential file must be 0600")

	di, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(dirMode), di.Mode().Perm(), "credential dir must be 0700")
}

func TestFileStoreReenforcesPermsOnExistingFile(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}

	dir := t.TempDir()
	store := newFileStore(dir)
	require.NoError(t, store.Set(RepoPassword, "first"))

	// Loosen perms behind the store's back, then re-Set.
	require.NoError(t, os.Chmod(store.PasswordFilePath(), 0o644)) //nolint:gosec // deliberately loosened to verify Set re-enforces 0600
	require.NoError(t, store.Set(RepoPassword, "second"))

	fi, err := os.Stat(store.PasswordFilePath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(fileMode), fi.Mode().Perm(), "re-Set must restore 0600")
}

func TestFileStoreAllSecretNames(t *testing.T) {
	t.Parallel()

	store := newFileStore(t.TempDir())

	for _, name := range []string{RepoPassword, RESTUsername, RESTPassword} {
		require.NoError(t, store.Set(name, name+"-value"), name)
		got, err := store.Get(name)
		require.NoError(t, err, name)
		assert.Equal(t, name+"-value", got, name)
	}

	// Distinct names get distinct files.
	entries, err := os.ReadDir(store.dir)
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

func TestFileStoreRejectsUnknownSecretName(t *testing.T) {
	t.Parallel()

	store := newFileStore(t.TempDir())

	_, err := store.Get("bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown secret name")

	require.Error(t, store.Set("bogus", "x"))
	require.Error(t, store.Delete("bogus"))
}

func TestFileStoreBackendName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, BackendFile, newFileStore(t.TempDir()).Backend())
}
