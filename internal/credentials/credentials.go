// Package credentials stores icebeam's secrets — the restic repository password
// and optional REST-server HTTP credentials — outside the plaintext config. It
// mirrors restic's own model: each secret is a 0600 file owned by the run-as
// user, which works everywhere icebeam runs (servers, Synology, a logged-out
// Mac) without depending on an OS keyring that may be locked or absent.
package credentials

import (
	"errors"
)

// ErrNotFound is returned by Get and Delete when no secret is stored under the
// given name. Callers can test for it with errors.Is.
var ErrNotFound = errors.New("credential not found")

// Secret names addressable through a CredentialStore. They double as the keys
// used by the file backend.
const (
	// RepoPassword is the restic repository password.
	RepoPassword = "repo-password"
	// RESTUsername is the REST-server HTTP username (optional).
	RESTUsername = "rest-username"
	// RESTPassword is the REST-server HTTP password (optional).
	RESTPassword = "rest-password"
)

// CredentialStore abstracts storage of named secrets so callers don't care
// whether the persistent file store or the in-memory probe store is backing
// them.
type CredentialStore interface {
	// Get returns the secret stored under name, or ErrNotFound if absent.
	Get(name string) (string, error)
	// Set stores value under name, overwriting any existing value.
	Set(name, value string) error
	// Delete removes the secret stored under name. Deleting a missing secret
	// returns ErrNotFound.
	Delete(name string) error
	// Backend reports which backend is in use ("file" or "memory") so commands
	// can tell the user where their secrets live.
	Backend() string
}

// Backend names for the credential store.
const (
	// BackendFile is the sole persistent backend: 0600 files owned by the
	// run-as user, mirroring restic's RESTIC_PASSWORD_FILE model.
	BackendFile = "file"
	// BackendMemory is the in-memory store used during setup to verify a
	// repository connection before any secret is persisted. It is never
	// persisted.
	BackendMemory = "memory"
)

// Open returns the persistent CredentialStore. Secrets are stored as 0600 files
// owned by the run-as user, the same trust model restic, SSH keys, and .pgpass
// rely on — it works under launchd/systemd and on systems with no OS keyring.
//
// fileDir is the directory the store writes its password files into (typically
// the XDG config dir).
func Open(fileDir string) (CredentialStore, error) {
	return newFileStore(fileDir), nil
}
