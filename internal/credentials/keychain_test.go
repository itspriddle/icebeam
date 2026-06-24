package credentials

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	keyring "github.com/zalando/go-keyring"
)

// fakeKeyring is an in-memory keyring for tests. When unavailable is set, every
// operation returns errUnavailable, simulating a host with no secret service.
type fakeKeyring struct {
	store       map[string]map[string]string
	unavailable bool
}

var errUnavailable = errors.New("secret service unavailable")

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{store: make(map[string]map[string]string)}
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	if f.unavailable {
		return "", errUnavailable
	}
	if users, ok := f.store[service]; ok {
		if v, ok := users[user]; ok {
			return v, nil
		}
	}
	return "", keyring.ErrNotFound
}

func (f *fakeKeyring) Set(service, user, password string) error {
	if f.unavailable {
		return errUnavailable
	}
	if f.store[service] == nil {
		f.store[service] = make(map[string]string)
	}
	f.store[service][user] = password
	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	if f.unavailable {
		return errUnavailable
	}
	if users, ok := f.store[service]; ok {
		if _, ok := users[user]; ok {
			delete(users, user)
			return nil
		}
	}
	return keyring.ErrNotFound
}

func TestKeychainStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := newKeychainStore(newFakeKeyring())

	_, err := store.Get(RepoPassword)
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, store.Set(RepoPassword, "s3cr3t"))

	got, err := store.Get(RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got)

	require.NoError(t, store.Delete(RepoPassword))
	_, err = store.Get(RepoPassword)
	require.ErrorIs(t, err, ErrNotFound)

	require.ErrorIs(t, store.Delete(RepoPassword), ErrNotFound)
}

func TestKeychainStoreUsesIcebeamService(t *testing.T) {
	t.Parallel()

	kr := newFakeKeyring()
	store := newKeychainStore(kr)
	require.NoError(t, store.Set(RepoPassword, "s3cr3t"))

	assert.Equal(t, "s3cr3t", kr.store[serviceName][RepoPassword],
		"secrets must be stored under the icebeam service name")
}

func TestKeychainStoreSurfacesNonNotFoundErrors(t *testing.T) {
	t.Parallel()

	kr := newFakeKeyring()
	kr.unavailable = true
	store := newKeychainStore(kr)

	_, err := store.Get(RepoPassword)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound, "a transport failure must not masquerade as ErrNotFound")

	require.Error(t, store.Set(RepoPassword, "x"))

	err = store.Delete(RepoPassword)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound)
}

func TestKeychainStoreBackendName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, BackendKeychain, newKeychainStore(newFakeKeyring()).Backend())
}

func TestKeychainAvailable(t *testing.T) {
	t.Parallel()

	assert.True(t, keychainAvailable(newFakeKeyring()), "a responsive secret service is available")

	withSecret := newFakeKeyring()
	require.NoError(t, withSecret.Set(serviceName, probeKey, "x"))
	assert.True(t, keychainAvailable(withSecret), "a successful read also means available")

	down := newFakeKeyring()
	down.unavailable = true
	assert.False(t, keychainAvailable(down), "a failing probe means unavailable")
}
