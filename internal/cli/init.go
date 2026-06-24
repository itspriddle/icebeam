package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// resticRunner is the subset of *restic.Runner the init command drives. It is an
// interface so tests can inject a stub without a real restic binary.
type resticRunner interface {
	Run(ctx context.Context, args ...string) error
}

// newResticRunner builds the restic runner the init command probes the
// repository with. It threads the persistent logger so restic's output during a
// first-time setup is recorded. It is a package variable so tests can swap in a
// stub.
var newResticRunner = func(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (resticRunner, error) {
	return restic.New(cfg, store, logger)
}

// initOptions collects the values that drive `icebeam init`, populated from
// flags and/or interactive prompts.
type initOptions struct {
	repo              string
	setName           string
	paths             []string
	excludes          []string
	tags              []string
	restUsername      string
	restPassword      string
	backend           string
	passwordStdin     bool
	restPasswordStdin bool
	force             bool
}

// newInitCommand builds the `icebeam init` command: a guided setup that writes
// config, stores secrets, and initializes (or verifies access to) the repository.
func newInitCommand() *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Guided setup: config, credentials, and repository initialization",
		Long: "init walks a fresh machine from nothing to a working, initialized " +
			"repository. It prompts for the repository URL, password, optional " +
			"REST-server credentials, and a first backup set, then writes config.toml, " +
			"stores secrets in the credential store, and runs `restic init` if the " +
			"repository does not yet exist. All prompts have flag equivalents so init " +
			"can be scripted. Secrets are never accepted on argv: a single secret may " +
			"be read from stdin per run via --password-stdin (repository password) or " +
			"--rest-password-stdin (REST-server password); the two are mutually " +
			"exclusive.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.repo, "repo", "", "repository URL (e.g. rest:https://nas.local:8000/icebeam)")
	flags.StringVar(&opts.setName, "set", "", "name of the first backup set")
	flags.StringArrayVar(&opts.paths, "path", nil, "path to back up in the set (repeatable)")
	flags.StringArrayVar(&opts.excludes, "exclude", nil, "exclude pattern for the set (repeatable)")
	flags.StringArrayVar(&opts.tags, "tag", nil, "tag to apply to the set (repeatable)")
	flags.StringVar(&opts.restUsername, "rest-username", "", "REST-server HTTP username (optional)")
	flags.StringVar(&opts.backend, "backend", "", "credential backend: auto, keychain, or file")
	flags.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the repository password from stdin (no echo); mutually exclusive with --rest-password-stdin")
	flags.BoolVar(&opts.restPasswordStdin, "rest-password-stdin", false, "read the REST-server password from stdin (no echo); mutually exclusive with --password-stdin")
	flags.BoolVar(&opts.force, "force", false, "overwrite an existing config")

	return cmd
}

// runInit executes the init flow: resolve inputs, guard against clobbering an
// existing config, then drive the validate-first setup engine, which probes the
// repository *before* it writes config.toml or any secret.
func runInit(cmd *cobra.Command, opts *initOptions) error {
	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}

	if err := guardExistingConfig(configPath, opts.force); err != nil {
		return err
	}

	if opts.passwordStdin && opts.restPasswordStdin {
		return errors.New("only one secret can be read from stdin per run: pass --password-stdin or --rest-password-stdin, not both")
	}

	p := newPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	if err := collectInputs(p, opts); err != nil {
		return err
	}

	if err := collectRESTCredentials(p, opts); err != nil {
		return err
	}

	password, err := collectPassword(p, opts)
	if err != nil {
		return err
	}

	cfg := buildConfig(opts)

	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer func() { _ = logger.Close() }()

	params := &setupParams{
		cfg:          cfg,
		configPath:   configPath,
		backend:      opts.backend,
		password:     password,
		restUsername: opts.restUsername,
		restPassword: opts.restPassword,
	}

	result, err := runSetup(cmd.Context(), p, logger, params)
	if err != nil {
		return err
	}

	printSummary(p, cfg, result.store, configPath)
	return nil
}

// guardExistingConfig refuses to proceed when a config already exists unless
// force is set, pointing the user at the existing file.
func guardExistingConfig(path string, force bool) error {
	if force {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists at %s; pass --force to overwrite it", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config %s: %w", path, err)
	}
	return nil
}

// collectInputs fills in any missing options interactively. Flag-supplied values
// are left untouched so a fully-flagged invocation never prompts.
func collectInputs(p *prompter, opts *initOptions) error {
	if opts.repo == "" {
		repo, err := p.ask("Repository URL (e.g. rest:https://nas.local:8000/icebeam)")
		if err != nil {
			return err
		}
		opts.repo = repo
	}

	if opts.setName == "" {
		name, err := p.askDefault("First backup set name", "home")
		if err != nil {
			return err
		}
		opts.setName = name
	}

	if len(opts.paths) == 0 {
		paths, err := p.askList("Paths to back up (comma-separated)")
		if err != nil {
			return err
		}
		opts.paths = paths
	}

	return nil
}

// collectRESTCredentials prompts for the REST-server HTTP username and password
// when the repository is a REST endpoint, so a server behind HTTP basic auth can
// be reached without leaking the password on argv. Both are optional (a REST
// server may have no HTTP auth). For a non-REST repository it is a no-op so those
// prompts never appear.
//
// The username is non-secret (visible prompt); the password is hidden, or read
// from stdin when --rest-password-stdin is set. Flag-supplied values suppress
// their prompt, preserving the scriptable path. The collected secret reaches
// restic only via the environment (RESTIC_REST_USERNAME/PASSWORD), never argv.
func collectRESTCredentials(p *prompter, opts *initOptions) error {
	repoURL, err := config.ParseRepoURL(opts.repo)
	if err != nil {
		return err
	}
	if !repoURL.IsRESTEndpoint() {
		return nil
	}

	// When a secret is being piped (--password-stdin or --rest-password-stdin),
	// stdin is reserved for that one secret and the invocation is scripted, so the
	// interactive REST prompts are suppressed — REST credentials then come solely
	// from --rest-username and --rest-password-stdin.
	scripted := opts.passwordStdin || opts.restPasswordStdin

	if opts.restUsername == "" && !scripted {
		username, err := p.askOptional("REST-server username")
		if err != nil {
			return err
		}
		opts.restUsername = username
	}

	if opts.restPasswordStdin {
		// REST password may be empty (server with no HTTP auth), so an empty stdin
		// line is accepted here.
		password, err := p.readSecretLine("read REST password from stdin")
		if err != nil {
			return err
		}
		opts.restPassword = password
		return nil
	}

	if opts.restPassword == "" && !scripted {
		password, err := p.askSecretOptional("REST-server password")
		if err != nil {
			return err
		}
		opts.restPassword = password
	}

	return nil
}

// collectPassword resolves the repository password: from stdin when
// --password-stdin is set, otherwise via a hidden interactive prompt. The stdin
// read goes through the prompter's shared reader so a preceding piped secret does
// not strand buffered input.
func collectPassword(p *prompter, opts *initOptions) (string, error) {
	if opts.passwordStdin {
		password, err := p.readSecretLine("read password from stdin")
		if err != nil {
			return "", err
		}
		if password == "" {
			return "", errors.New("no password provided on stdin")
		}
		return password, nil
	}
	return p.askSecret("Repository password")
}

// buildConfig assembles a Config from the collected options, starting from the
// defaults so min_version and log level are populated.
func buildConfig(opts *initOptions) *config.Config {
	cfg := config.Default()
	cfg.Repository.URL = strings.TrimSpace(opts.repo)
	cfg.Credentials.Backend = opts.backend
	cfg.Sets = []config.Set{
		{
			Name:    strings.TrimSpace(opts.setName),
			Paths:   opts.paths,
			Exclude: opts.excludes,
			Tags:    opts.tags,
		},
	}
	return &cfg
}

// initRepository creates the repository with `restic init`. It is called only
// after the setup engine's probe has confirmed the repository does not yet
// exist and config + secrets have been persisted, so it never probes itself. The
// init is wrapped in LogStart/LogEnd so a first-time setup is recorded in the
// persistent log.
func initRepository(ctx context.Context, p *prompter, logger *logging.Logger, cfg *config.Config, store credentials.CredentialStore) error {
	runner, err := newResticRunner(cfg, store, logger)
	if err != nil {
		return err
	}

	p.println("Initializing repository...")
	initArgs := []string{"init"}
	logger.LogStart("init", initArgs)
	start := time.Now()
	initErr := runner.Run(ctx, initArgs...)
	logger.LogEnd("init", time.Since(start), initErr)
	if initErr != nil {
		return fmt.Errorf("initialize repository: %w", initErr)
	}
	p.println("Repository initialized.")
	return nil
}

// printSummary reports the configured repository, credential backend, sets, log
// location, and the suggested next step.
func printSummary(p *prompter, cfg *config.Config, store credentials.CredentialStore, configPath string) {
	p.println("\nicebeam is configured.")
	p.printf("  Repository:  %s\n", cfg.Repository.URL)
	p.printf("  Credentials: %s backend\n", store.Backend())
	p.printf("  Config:      %s\n", configPath)
	for _, s := range cfg.Sets {
		p.printf("  Set %q:    %s\n", s.Name, strings.Join(s.Paths, ", "))
	}
	if logPath, err := resolveLogPath(cfg); err == nil {
		p.printf("  Log:         %s\n", logPath)
	}
	p.println("\nNext: run `icebeam run` to back up now, or `icebeam schedule install` to back up on a schedule.")
}

// isTerminal reports whether the reader is an interactive terminal, used to
// decide between hidden ReadPassword and plain line input. Indirected for tests.
var isTerminal = func(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// readHiddenPassword reads a password from a terminal without echoing it.
var readHiddenPassword = func(in io.Reader) (string, error) {
	f, ok := in.(*os.File)
	if !ok {
		return "", errors.New("cannot read hidden password: input is not a terminal")
	}
	b, err := term.ReadPassword(int(f.Fd()))
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}
