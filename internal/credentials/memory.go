package credentials

import "errors"

// errNotMemoryStore is returned by CopyInto when its source is not an in-memory
// store, since only the in-memory store exposes the full set of collected
// secrets to copy.
var errNotMemoryStore = errors.New("credentials: CopyInto source must be an in-memory store")

// memoryStore holds secrets in memory only. It exists so the setup flow can
// collect the repository password (and optional REST credentials) and run a
// restic connection probe against the target repository *before* anything is
// written to disk or the OS keychain. A wrong password is then caught with an
// in-place retry instead of a half-written config.
//
// Because memoryStore is not the concrete *fileStore, ResticPasswordEnv routes
// it through the RESTIC_PASSWORD branch — the collected-but-unpersisted password
// reaches restic via the child environment, never argv, exactly like the
// keychain backend (see restic.go). The probe therefore composes with the
// existing Runner.env unchanged.
type memoryStore struct {
	secrets map[string]string
}

// newMemoryStore returns an empty in-memory store.
func newMemoryStore() *memoryStore {
	return &memoryStore{secrets: make(map[string]string)}
}

// NewMemoryStore returns an in-memory CredentialStore seeded with the given
// secrets. Keys are the secret names (RepoPassword, RESTUsername, RESTPassword);
// a nil or empty map yields an empty store. The returned store is intended for
// the pre-persist connection probe and must not outlive setup — call CopyInto to
// persist its contents to a real backend once the probe succeeds.
func NewMemoryStore(secrets map[string]string) CredentialStore {
	s := newMemoryStore()
	for name, value := range secrets {
		s.secrets[name] = value
	}
	return s
}

// Backend reports the in-memory backend in use.
func (*memoryStore) Backend() string { return BackendMemory }

// Get returns the secret stored under name, or ErrNotFound if absent.
func (s *memoryStore) Get(name string) (string, error) {
	v, ok := s.secrets[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set stores value under name, overwriting any existing value.
func (s *memoryStore) Set(name, value string) error {
	s.secrets[name] = value
	return nil
}

// Delete removes the secret stored under name, returning ErrNotFound if absent.
func (s *memoryStore) Delete(name string) error {
	if _, ok := s.secrets[name]; !ok {
		return ErrNotFound
	}
	delete(s.secrets, name)
	return nil
}

// CopyInto writes every secret held by src into dst, overwriting any existing
// values under the same names. It is the persist step run after a successful
// probe: collected secrets move from the in-memory store into the real backend
// (keychain or file). src may be any CredentialStore but is typically a
// memoryStore; only stores that expose their full set of secrets can be copied,
// so a non-memory src returns an error rather than silently copying nothing.
func CopyInto(src, dst CredentialStore) error {
	ms, ok := src.(*memoryStore)
	if !ok {
		return errNotMemoryStore
	}
	for name, value := range ms.secrets {
		if err := dst.Set(name, value); err != nil {
			return err
		}
	}
	return nil
}
