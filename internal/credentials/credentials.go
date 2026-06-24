// Package credentials stores icebeam's secrets — the restic repository password
// and optional REST-server HTTP credentials — outside the plaintext config. It
// prefers the OS secret service (macOS Keychain / Linux Secret Service) and
// falls back to a 0600 password file on systems without one (e.g. old Synology).
package credentials

import (
	"errors"
	"fmt"
)

// serviceName namespaces icebeam's entries in the OS secret service.
const serviceName = "icebeam"

// Secret names addressable through a CredentialStore. They double as the keys
// used in both the keychain and the file backend.
const (
	// RepoPassword is the restic repository password.
	RepoPassword = "repo-password"
	// RESTUsername is the REST-server HTTP username (optional).
	RESTUsername = "rest-username"
	// RESTPassword is the REST-server HTTP password (optional).
	RESTPassword = "rest-password"
)

// ErrNotFound is returned by Get and Delete when no secret is stored under the
// given name. Callers can test for it with errors.Is.
var ErrNotFound = errors.New("credential not found")

// CredentialStore abstracts storage of named secrets so callers don't care
// whether the OS keychain or the file fallback is backing them.
type CredentialStore interface {
	// Get returns the secret stored under name, or ErrNotFound if absent.
	Get(name string) (string, error)
	// Set stores value under name, overwriting any existing value.
	Set(name, value string) error
	// Delete removes the secret stored under name. Deleting a missing secret
	// returns ErrNotFound.
	Delete(name string) error
	// Backend reports which backend is in use ("keychain" or "file") so
	// commands can tell the user where their secrets live.
	Backend() string
}

// Backend names for the credential store and the config credentials.backend
// field.
const (
	BackendAuto     = "auto"
	BackendKeychain = "keychain"
	BackendFile     = "file"
	// BackendMemory is the in-memory store used during setup to verify a
	// repository connection before any secret is persisted. It is never a valid
	// config credentials.backend selection.
	BackendMemory = "memory"
)

// keyringProvider abstracts the OS secret service so the keychain backend can be
// tested without touching the real keychain. It mirrors the subset of
// github.com/zalando/go-keyring used here.
type keyringProvider interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// Open selects and returns a CredentialStore according to the requested backend.
//
//   - "" or "auto": use the keychain when the secret service is available,
//     otherwise fall back to the file backend.
//   - "keychain": force the keychain backend (errors if it is unavailable).
//   - "file": force the file backend.
//
// fileDir is the directory the file backend writes its password files into
// (typically the XDG config dir).
func Open(backend, fileDir string) (CredentialStore, error) {
	return open(backend, fileDir, systemKeyring{})
}

// open is the testable core of Open, taking an injectable keyring.
func open(backend, fileDir string, kr keyringProvider) (CredentialStore, error) {
	switch backend {
	case "", BackendAuto:
		if keychainAvailable(kr) {
			return newKeychainStore(kr), nil
		}
		return newFileStore(fileDir), nil
	case BackendKeychain:
		if !keychainAvailable(kr) {
			return nil, errors.New("credentials: keychain backend requested but the OS secret service is unavailable")
		}
		return newKeychainStore(kr), nil
	case BackendFile:
		return newFileStore(fileDir), nil
	default:
		return nil, fmt.Errorf("credentials: unknown backend %q (use auto, keychain, or file)", backend)
	}
}
