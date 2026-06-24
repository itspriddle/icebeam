package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// setupParams carries the resolved inputs that drive the validate-first setup
// engine. Everything here is collected (from flags and/or prompts) but not yet
// persisted: the engine probes the repository first and only writes config.toml
// and the credential store once the connection is verified.
type setupParams struct {
	// cfg is the assembled config, built in memory and persisted only after a
	// successful probe.
	cfg *config.Config
	// configPath is where cfg is written once the probe succeeds.
	configPath string
	// password is the repository password. The engine re-prompts it in place on
	// a wrong-password probe outcome.
	password string
	// restUsername and restPassword are the optional REST-server HTTP
	// credentials. They reach restic only via the environment, never argv.
	restUsername string
	restPassword string
	// skipProbe is set on a re-run where neither the repository URL nor the
	// repository password changed: the connection was verified on a prior run, so
	// the engine rewrites an equivalent config and re-persists the unchanged
	// secrets without probing or re-initializing the repository.
	skipProbe bool
}

// setupResult reports what runSetup did so the caller can render a summary.
type setupResult struct {
	// store is the real (persisted) credential store the secrets were copied
	// into.
	store credentials.CredentialStore
	// created reports whether the repository was freshly initialized (true) or
	// an existing repository was verified (false).
	created bool
}

// runSetup is the validate-first setup engine shared by `icebeam init` (and,
// later, `reconfigure`). It seeds the collected secrets into an in-memory store,
// probes the repository with `restic cat config` *before* writing anything, and
// only persists config.toml and the credential store once the connection is
// confirmed. A wrong repository password is retried in place without re-asking
// the other inputs; any other failure offers a re-enter-or-abort choice, and an
// abort leaves nothing on disk.
func runSetup(ctx context.Context, p *prompter, logger *logging.Logger, params *setupParams) (*setupResult, error) {
	var created bool
	if params.skipProbe {
		// Re-run with an unchanged repository URL and password: the connection was
		// already verified, so persist the (possibly otherwise-edited) config and
		// re-store the unchanged secrets without probing or re-initializing.
		p.println("Repository URL and password unchanged; skipping re-verification.")
	} else {
		var err error
		created, err = probeRepository(ctx, p, logger, params)
		if err != nil {
			return nil, err
		}
	}

	// The probe succeeded (or confirmed a new repo). Now — and only now —
	// persist config and copy the verified secrets into the real backend.
	if err := params.cfg.SaveFile(params.configPath); err != nil {
		return nil, err
	}

	store, err := persistSecrets(params)
	if err != nil {
		return nil, err
	}

	if created {
		if err := initRepository(ctx, p, logger, params.cfg, store); err != nil {
			return nil, err
		}
	}

	return &setupResult{store: store, created: created}, nil
}

// probeRepository runs `restic cat config` against the target repository using
// an in-memory credential store seeded with the collected secrets, so a wrong
// password is caught before anything is written. It returns created=true when
// the repository does not exist yet (the caller must run `restic init` after
// persisting), created=false when an existing repository was verified.
//
// Outcomes:
//   - exit 0          → existing repo, password verified; created=false.
//   - repo-not-exist  → new repo; created=true.
//   - wrong-password  → re-prompt the repository password and retry the probe.
//   - any other error → surface restic's message; offer to re-enter credentials
//     and retry, or abort (nothing persisted).
//
// The probe is wrapped in the persistent logger's LogStart/LogEnd so a
// first-time setup attempt is recorded even when it fails.
func probeRepository(ctx context.Context, p *prompter, logger *logging.Logger, params *setupParams) (created bool, err error) {
	probeArgs := []string{"cat", "config"}

	for {
		memStore := credentials.NewMemoryStore(map[string]string{
			credentials.RepoPassword: params.password,
			credentials.RESTUsername: params.restUsername,
			credentials.RESTPassword: params.restPassword,
		})

		runner, runnerErr := newResticRunner(params.cfg, memStore, logger)
		if runnerErr != nil {
			return false, runnerErr
		}

		logger.LogStart("init", probeArgs)
		start := time.Now()
		probeErr := runner.Run(ctx, probeArgs...)
		logger.LogEnd("init", time.Since(start), probeErr)

		if probeErr == nil {
			p.println("Repository already initialized; verified access.")
			return false, nil
		}

		var exitErr *restic.ExitError
		switch {
		case errors.As(probeErr, &exitErr) && exitErr.IsRepoNotExist():
			p.println("Repository not found; it will be initialized.")
			return true, nil

		case errors.As(probeErr, &exitErr) && exitErr.IsWrongPassword():
			// Wrong password: re-prompt only the password and retry, leaving the
			// other collected inputs untouched.
			p.println("Repository password rejected; please try again.")
			pw, askErr := p.askSecret("Repository password")
			if askErr != nil {
				return false, askErr
			}
			params.password = pw

		default:
			// Any other failure (unreachable host, REST auth rejected, repo
			// locked): surface restic's message and let the user re-enter
			// credentials and retry, or abort. Nothing has been persisted, so an
			// abort is clean.
			p.printf("Could not reach repository: %v\n", probeErr)
			retry, askErr := p.askYesNo("Re-enter the repository password and try again?", false)
			if askErr != nil {
				return false, askErr
			}
			if !retry {
				return false, fmt.Errorf("setup aborted; nothing was written: %w", probeErr)
			}
			pw, askErr := p.askSecret("Repository password")
			if askErr != nil {
				return false, askErr
			}
			params.password = pw
		}
	}
}

// persistSecrets opens the persistent file store and writes the verified
// repository password plus any REST-server credentials into it. It is the persist
// step run only after a successful probe: the secrets move from an in-memory
// store into the file store via credentials.CopyInto, never touching argv,
// config, or logs.
func persistSecrets(params *setupParams) (credentials.CredentialStore, error) {
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}

	store, err := credentials.Open(configDir)
	if err != nil {
		return nil, err
	}

	mem := credentials.NewMemoryStore(map[string]string{
		credentials.RepoPassword: params.password,
	})
	if params.restUsername != "" {
		if err := mem.Set(credentials.RESTUsername, params.restUsername); err != nil {
			return nil, err
		}
	}
	if params.restPassword != "" {
		if err := mem.Set(credentials.RESTPassword, params.restPassword); err != nil {
			return nil, err
		}
	}
	if err := credentials.CopyInto(mem, store); err != nil {
		return nil, err
	}

	return store, nil
}
