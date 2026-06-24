// Package restic wraps the official restic binary. It owns binary discovery,
// minimum-version gating, environment construction (repository + credentials,
// never argv), output streaming to the logger, context cancellation, and
// translation of restic's documented exit codes into distinguishable Go errors.
//
// Every higher-level icebeam command drives restic through a Runner rather than
// invoking os/exec directly, so the repository, credentials, and cancellation
// semantics are constructed in exactly one place.
package restic

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
)

// defaultBinary is the binary name looked up on PATH when config.Restic.Binary
// is empty.
const defaultBinary = "restic"

// ErrBinaryNotFound is returned when the restic binary cannot be located. It
// carries an actionable message telling the user to install restic.
var ErrBinaryNotFound = errors.New(
	"restic binary not found; install restic (e.g. `brew install restic`) " +
		"or set restic.binary in your config",
)

// Runner invokes the restic binary with a consistent environment and surfaces
// its output and exit status. Build one with New.
type Runner struct {
	// binary is the resolved path to the restic executable.
	binary string

	// minVersion is the minimum acceptable restic version.
	minVersion string

	// repoURL is the restic repository URL (RESTIC_REPOSITORY).
	repoURL string

	// store backs the repository password (and, when present, REST-server
	// credentials) handed to restic via the environment.
	store credentials.CredentialStore

	// logger receives restic's combined output and run start/end lines. It may
	// be nil, in which case output is not logged.
	logger *logging.Logger

	// checkedVersion records that the binary's version has been verified against
	// minVersion, so the check runs at most once per Runner.
	checkedVersion bool
}

// New builds a Runner from config and a credential store. It resolves the restic
// binary (config.Restic.Binary when set, otherwise PATH) and returns
// ErrBinaryNotFound if it cannot be located. The logger may be nil.
func New(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (*Runner, error) {
	if cfg == nil {
		return nil, errors.New("restic: config is required")
	}

	binary, err := resolveBinary(cfg.Restic.Binary)
	if err != nil {
		return nil, err
	}

	return &Runner{
		binary:     binary,
		minVersion: cfg.Restic.MinVersion,
		repoURL:    cfg.Repository.URL,
		store:      store,
		logger:     logger,
	}, nil
}

// Binary returns the resolved path to the restic binary.
func (r *Runner) Binary() string { return r.binary }

// resolveBinary locates the restic binary. An explicit path from config is used
// as-is (and verified to exist); otherwise restic is looked up on PATH. A
// missing binary yields ErrBinaryNotFound.
func resolveBinary(configured string) (string, error) {
	if configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("%w (configured restic.binary=%q): %w", ErrBinaryNotFound, configured, err)
		}
		return path, nil
	}

	path, err := exec.LookPath(defaultBinary)
	if err != nil {
		return "", ErrBinaryNotFound
	}
	return path, nil
}

// env builds the environment restic runs with: RESTIC_REPOSITORY, the password
// variable(s) from the credential store, and optional REST-server credentials.
// Secrets are only ever placed in the environment, never in argv.
func (r *Runner) env(ctx context.Context) ([]string, error) {
	env := []string{"RESTIC_REPOSITORY=" + r.repoURL}

	if r.store != nil {
		passwordEnv, err := credentials.ResticPasswordEnv(r.store)
		if err != nil {
			return nil, err
		}
		env = append(env, passwordEnv...)

		restEnv, err := restServerEnv(ctx, r.store)
		if err != nil {
			return nil, err
		}
		env = append(env, restEnv...)
	}

	return env, nil
}

// restServerEnv returns the REST-server HTTP credentials as environment entries
// when both are present. restic reads RESTIC_REST_USERNAME / RESTIC_REST_PASSWORD
// so the credentials never need to be embedded in the repository URL or argv.
// Absent REST credentials are not an error: many repositories don't use them.
func restServerEnv(_ context.Context, store credentials.CredentialStore) ([]string, error) {
	user, err := store.Get(credentials.RESTUsername)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("restic: load REST username: %w", err)
	}

	password, err := store.Get(credentials.RESTPassword)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("restic: load REST password: %w", err)
	}

	return []string{
		"RESTIC_REST_USERNAME=" + user,
		"RESTIC_REST_PASSWORD=" + password,
	}, nil
}
