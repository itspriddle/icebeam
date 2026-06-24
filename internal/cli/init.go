package cli

import (
	"bufio"
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
	repo          string
	setName       string
	paths         []string
	excludes      []string
	tags          []string
	restUsername  string
	restPassword  string
	backend       string
	passwordStdin bool
	force         bool
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
			"can be scripted; with --password-stdin the password is read from stdin.",
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
	flags.StringVar(&opts.restPassword, "rest-password", "", "REST-server HTTP password (optional)")
	flags.StringVar(&opts.backend, "backend", "", "credential backend: auto, keychain, or file")
	flags.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the repository password from stdin (no echo)")
	flags.BoolVar(&opts.force, "force", false, "overwrite an existing config")

	return cmd
}

// runInit executes the init flow: resolve inputs, guard against clobbering an
// existing config, write config + secrets, then probe/initialize the repository.
func runInit(cmd *cobra.Command, opts *initOptions) error {
	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}

	if err := guardExistingConfig(configPath, opts.force); err != nil {
		return err
	}

	p := newPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	if err := collectInputs(p, opts); err != nil {
		return err
	}

	password, err := collectPassword(cmd, p, opts)
	if err != nil {
		return err
	}

	cfg := buildConfig(opts)
	if err := cfg.SaveFile(configPath); err != nil {
		return err
	}

	store, err := storeSecrets(opts, password)
	if err != nil {
		return err
	}

	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer func() { _ = logger.Close() }()

	if err := initRepository(cmd.Context(), p, logger, cfg, store); err != nil {
		return err
	}

	printSummary(p, cfg, store, configPath)
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

// collectPassword resolves the repository password: from stdin when
// --password-stdin is set, otherwise via a hidden interactive prompt.
func collectPassword(cmd *cobra.Command, p *prompter, opts *initOptions) (string, error) {
	if opts.passwordStdin {
		return readPasswordStdin(cmd.InOrStdin())
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

// storeSecrets opens the credential store for the configured backend and stores
// the repository password plus any REST-server credentials.
func storeSecrets(opts *initOptions, password string) (credentials.CredentialStore, error) {
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}

	store, err := credentials.Open(opts.backend, configDir)
	if err != nil {
		return nil, err
	}

	if err := store.Set(credentials.RepoPassword, password); err != nil {
		return nil, err
	}

	if opts.restUsername != "" {
		if err := store.Set(credentials.RESTUsername, opts.restUsername); err != nil {
			return nil, err
		}
	}
	if opts.restPassword != "" {
		if err := store.Set(credentials.RESTPassword, opts.restPassword); err != nil {
			return nil, err
		}
	}

	return store, nil
}

// initRepository probes the repository and initializes it when absent. An
// already-initialized repository is verified for access and left untouched. The
// probe is wrapped in LogStart/LogEnd so a failed first-time setup is recorded
// in the persistent log.
func initRepository(ctx context.Context, p *prompter, logger *logging.Logger, cfg *config.Config, store credentials.CredentialStore) error {
	runner, err := newResticRunner(cfg, store, logger)
	if err != nil {
		return err
	}

	// `cat config` reads the repository's config blob: it succeeds on an
	// initialized, accessible repository and returns the repo-not-exist code
	// when the repository has not been created yet.
	probeArgs := []string{"cat", "config"}
	logger.LogStart("init", probeArgs)
	start := time.Now()
	err = runner.Run(ctx, probeArgs...)
	if err == nil {
		logger.LogEnd("init", time.Since(start), nil)
		p.println("Repository already initialized; verified access.")
		return nil
	}

	var exitErr *restic.ExitError
	if errors.As(err, &exitErr) && exitErr.IsRepoNotExist() {
		p.println("Repository not found; initializing...")
		initErr := runner.Run(ctx, "init")
		logger.LogEnd("init", time.Since(start), initErr)
		if initErr != nil {
			return fmt.Errorf("initialize repository: %w", initErr)
		}
		p.println("Repository initialized.")
		return nil
	}

	logger.LogEnd("init", time.Since(start), err)
	return fmt.Errorf("access repository: %w", err)
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

// readPasswordStdin reads a single password line from stdin without echoing
// (stdin is already not a terminal when piped). A trailing newline is trimmed.
func readPasswordStdin(in io.Reader) (string, error) {
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("no password provided on stdin")
	}
	return password, nil
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
