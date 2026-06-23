package credentials

import (
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

// systemKeyring is the production keyring, delegating to github.com/zalando/go-keyring
// which targets macOS Keychain and the Linux Secret Service.
type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// keychainStore stores secrets in the OS secret service under the icebeam
// service name.
type keychainStore struct {
	kr keyringProvider
}

func newKeychainStore(kr keyringProvider) *keychainStore {
	return &keychainStore{kr: kr}
}

// Backend reports the keychain backend in use.
func (*keychainStore) Backend() string { return BackendKeychain }

// Get returns the secret stored under name, translating the keyring's
// not-found error into ErrNotFound.
func (s *keychainStore) Get(name string) (string, error) {
	v, err := s.kr.Get(serviceName, name)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("credentials: read %q from keychain: %w", name, err)
	}
	return v, nil
}

// Set stores value under name in the keychain.
func (s *keychainStore) Set(name, value string) error {
	if err := s.kr.Set(serviceName, name, value); err != nil {
		return fmt.Errorf("credentials: write %q to keychain: %w", name, err)
	}
	return nil
}

// Delete removes the secret stored under name, translating the keyring's
// not-found error into ErrNotFound.
func (s *keychainStore) Delete(name string) error {
	if err := s.kr.Delete(serviceName, name); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("credentials: delete %q from keychain: %w", name, err)
	}
	return nil
}

// probeKey is the name used to test whether the secret service is reachable. It
// is never used to store a real secret.
const probeKey = "icebeam-availability-probe"

// keychainAvailable reports whether the OS secret service is reachable. It
// probes with a benign Get: a successful read or a clean "not found" both mean
// the service responded, whereas any other error (no D-Bus, no Secret Service,
// no Keychain access) means the service is unavailable and the caller should
// fall back to the file backend.
func keychainAvailable(kr keyringProvider) bool {
	_, err := kr.Get(serviceName, probeKey)
	if err == nil {
		return true
	}
	return errors.Is(err, keyring.ErrNotFound)
}
