package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File permissions for credential files and their containing directory. These
// hold plaintext secrets, so they are owner-only.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// secretFileNames maps secret names to the basenames the file backend uses. The
// repository password lives in repo.password so it can be handed to restic via
// RESTIC_PASSWORD_FILE (see AC: secrets reach restic via a file, never argv).
var secretFileNames = map[string]string{
	RepoPassword: "repo.password",
	RESTUsername: "rest.username",
	RESTPassword: "rest.password",
}

// fileStore stores each secret in its own 0600 file under dir.
type fileStore struct {
	dir string
}

func newFileStore(dir string) *fileStore {
	return &fileStore{dir: dir}
}

// Backend reports the file backend in use.
func (*fileStore) Backend() string { return BackendFile }

// path returns the on-disk path for a named secret, erroring on an unknown name.
func (s *fileStore) path(name string) (string, error) {
	base, ok := secretFileNames[name]
	if !ok {
		return "", fmt.Errorf("credentials: unknown secret name %q", name)
	}
	return filepath.Join(s.dir, base), nil
}

// Get reads the secret stored under name, returning ErrNotFound if its file does
// not exist.
func (s *fileStore) Get(name string) (string, error) {
	path, err := s.path(name)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path) //nolint:gosec // path derived from XDG config dir, not arbitrary input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("credentials: read %s: %w", path, err)
	}
	return string(data), nil
}

// Set writes value to the secret's file, creating the directory (0700) and the
// file (0600) with restrictive permissions.
func (s *fileStore) Set(name, value string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("credentials: create dir %s: %w", s.dir, err)
	}

	if err := os.WriteFile(path, []byte(value), fileMode); err != nil {
		return fmt.Errorf("credentials: write %s: %w", path, err)
	}

	// WriteFile honors the mode only on creation; enforce perms on a
	// pre-existing (possibly looser) file too.
	if err := os.Chmod(path, fileMode); err != nil {
		return fmt.Errorf("credentials: chmod %s: %w", path, err)
	}

	return nil
}

// Delete removes the secret's file, returning ErrNotFound if it is absent.
func (s *fileStore) Delete(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("credentials: delete %s: %w", path, err)
	}
	return nil
}

// PasswordFilePath returns the path the file backend uses for the repository
// password. It is the path icebeam hands to restic via RESTIC_PASSWORD_FILE, so
// the password is never passed as a command-line argument.
func (s *fileStore) PasswordFilePath() string {
	return filepath.Join(s.dir, secretFileNames[RepoPassword])
}
