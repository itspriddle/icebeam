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
	"github.com/spf13/pflag"
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
	keepDaily         int
	keepWeekly        int
	keepMonthly       int
	keepYearly        int
	passwordStdin     bool
	restPasswordStdin bool
	generatePassword  bool
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
			"exclusive.\n\n" +
			"init is re-runnable: running it on an already-configured machine pre-fills " +
			"every prompt with the current value and offers to keep stored secrets, so " +
			"a single setting can be changed without re-entering everything or wiping " +
			"the existing config.",
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
	registerRetentionFlags(flags, opts)
	flags.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the repository password from stdin (no echo); mutually exclusive with --rest-password-stdin")
	flags.BoolVar(&opts.restPasswordStdin, "rest-password-stdin", false, "read the REST-server password from stdin (no echo); mutually exclusive with --password-stdin")
	flags.BoolVar(&opts.generatePassword, "generate-password", false, "generate a strong repository password instead of prompting (shown once, cannot be recovered)")

	return cmd
}

// registerRetentionFlags registers the snapshot-retention flags shared by init
// and reconfigure. Their defaults match config.Default* so a fully-scripted run
// that omits them still establishes a sensible policy. A negative value is
// rejected later by config.Validate with a field-named error.
func registerRetentionFlags(flags *pflag.FlagSet, opts *initOptions) {
	flags.IntVar(&opts.keepDaily, "keep-daily", config.DefaultKeepDaily, "snapshots to keep, one per day")
	flags.IntVar(&opts.keepWeekly, "keep-weekly", config.DefaultKeepWeekly, "snapshots to keep, one per week")
	flags.IntVar(&opts.keepMonthly, "keep-monthly", config.DefaultKeepMonthly, "snapshots to keep, one per month")
	flags.IntVar(&opts.keepYearly, "keep-yearly", config.DefaultKeepYearly, "snapshots to keep, one per year")
}

// runInit executes the init flow. It is re-runnable: when a config already
// exists it is loaded and each value pre-fills the corresponding prompt (and
// stored secrets offer a "keep existing" default), so a single setting can be
// changed without re-entering everything or wiping the config. The collected
// inputs then drive the validate-first setup engine, which probes the repository
// *before* it writes config.toml or any secret.
func runInit(cmd *cobra.Command, opts *initOptions) error {
	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}

	// Load the existing config (if any) to pre-fill prompts. A missing config is
	// the first-run case (existing stays nil); a malformed config is a real error.
	existing, err := loadExistingConfig(configPath)
	if err != nil {
		return err
	}

	return runSetupFlow(cmd, opts, existing, configPath)
}

// runSetupFlow is the shared body driven by both `init` and `reconfigure`. It
// collects inputs (pre-filled from existing when supplied), loads any stored
// secrets, then hands the result to the validate-first setup engine, which
// probes the repository before persisting anything. The two callers differ only
// in how existing is obtained: init treats a missing config as a first run
// (existing nil), while reconfigure requires one.
func runSetupFlow(cmd *cobra.Command, opts *initOptions, existing *config.Config, configPath string) error {
	if opts.passwordStdin && opts.restPasswordStdin {
		return errors.New("only one secret can be read from stdin per run: pass --password-stdin or --rest-password-stdin, not both")
	}

	if opts.generatePassword && opts.passwordStdin {
		return errors.New("--generate-password and --password-stdin are mutually exclusive: generate a password or supply one, not both")
	}

	p := newPrompter(cmd.InOrStdin(), cmd.OutOrStdout())

	if existing != nil {
		p.printf("Existing config found at %s; press enter at any prompt to keep the current value.\n\n", configPath)
	}

	if err := collectInputs(p, opts, existing); err != nil {
		return err
	}

	// Strip any HTTP credentials embedded in a rest:https://user:pass@host/... URL
	// before anything is persisted: the normalized (userinfo-stripped) URL goes to
	// config.toml, and the embedded credentials are folded into the REST username/
	// password so they seed the probe and are stored in the credential store like
	// prompted ones. A secret-bearing URL must never reach config, logs, or stdout.
	if err := normalizeRepoURL(cmd, p, opts); err != nil {
		return err
	}

	if err := collectRetention(p, cmd, opts, existing); err != nil {
		return err
	}

	// Load any stored secrets from the file store so they can offer "keep
	// existing" defaults on a re-run.
	stored, err := loadStoredSecrets()
	if err != nil {
		return err
	}

	if err := collectRESTCredentials(p, opts, stored); err != nil {
		return err
	}

	password, passwordChanged, err := collectPassword(p, opts, stored)
	if err != nil {
		return err
	}

	cfg := buildConfig(opts, existing)

	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer func() { _ = logger.Close() }()

	params := &setupParams{
		cfg:          cfg,
		configPath:   configPath,
		password:     password,
		restUsername: opts.restUsername,
		restPassword: opts.restPassword,
		// The repository password is only re-verified by the probe when it (or the
		// repository URL) changed; an unchanged re-run skips the probe and rewrites
		// an equivalent config, leaving stored secrets intact.
		skipProbe: existing != nil && !passwordChanged && repoURLUnchanged(opts.repo, existing),
	}

	if _, err := runSetup(cmd.Context(), p, logger, params); err != nil {
		return err
	}

	printSummary(p, cfg, configPath)
	return nil
}

// loadExistingConfig loads the current config to pre-fill prompts on a re-run. A
// missing config (ErrNotConfigured) is the first-run case and returns nil
// without error; a malformed config is surfaced so the user can fix it.
func loadExistingConfig(path string) (*config.Config, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		if errors.Is(err, config.ErrNotConfigured) {
			return nil, nil //nolint:nilnil // nil config + nil error means "first run, no defaults"
		}
		return nil, err
	}
	return cfg, nil
}

// storedSecrets holds the secrets already in the credential store, used to offer
// "keep existing" defaults on a re-run. Each has*-flag is true only when that
// secret is actually stored.
type storedSecrets struct {
	repoPassword string
	hasRepo      bool
	restUsername string
	hasRESTUser  bool
	restPassword string
	hasRESTPass  bool
}

// loadStoredSecrets reads any already-stored secrets so re-running setup can
// offer to keep them. A missing secret is not an error (it simply has no "keep
// existing" default).
func loadStoredSecrets() (*storedSecrets, error) {
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	store, err := credentials.Open(configDir)
	if err != nil {
		return nil, err
	}

	s := &storedSecrets{}
	s.repoPassword, s.hasRepo, err = getStoredSecret(store, credentials.RepoPassword)
	if err != nil {
		return nil, err
	}
	s.restUsername, s.hasRESTUser, err = getStoredSecret(store, credentials.RESTUsername)
	if err != nil {
		return nil, err
	}
	s.restPassword, s.hasRESTPass, err = getStoredSecret(store, credentials.RESTPassword)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// getStoredSecret reads one secret, treating ErrNotFound as "not stored" (ok
// false) rather than an error.
func getStoredSecret(store credentials.CredentialStore, name string) (value string, ok bool, err error) {
	value, err = store.Get(name)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return value, true, nil
}

// repoURLUnchanged reports whether the supplied repository URL matches the one in
// the existing config (after the same trimming buildConfig applies).
func repoURLUnchanged(repo string, existing *config.Config) bool {
	return existing != nil && strings.TrimSpace(repo) == existing.Repository.URL
}

// collectInputs fills in any missing options interactively. Flag-supplied values
// are left untouched so a fully-flagged invocation never prompts. When an
// existing config is supplied, its values pre-fill each prompt so a re-run can
// accept the current value with an empty answer.
func collectInputs(p *prompter, opts *initOptions, existing *config.Config) error {
	existingSet := firstSet(existing)

	if opts.repo == "" {
		if existing != nil {
			repo, err := p.askDefault("Repository URL (e.g. rest:https://nas.local:8000/icebeam)", existing.Repository.URL)
			if err != nil {
				return err
			}
			opts.repo = repo
		} else {
			repo, err := p.ask("Repository URL (e.g. rest:https://nas.local:8000/icebeam)")
			if err != nil {
				return err
			}
			opts.repo = repo
		}
	}

	if opts.setName == "" {
		def := "home"
		if existingSet != nil && existingSet.Name != "" {
			def = existingSet.Name
		}
		name, err := p.askDefault("First backup set name", def)
		if err != nil {
			return err
		}
		opts.setName = name
	}

	if len(opts.paths) == 0 {
		if existingSet != nil && len(existingSet.Paths) > 0 {
			paths, err := p.askListDefault("Paths to back up (comma-separated)", existingSet.Paths)
			if err != nil {
				return err
			}
			opts.paths = paths
		} else {
			paths, err := p.askList("Paths to back up (comma-separated)")
			if err != nil {
				return err
			}
			opts.paths = paths
		}
	}

	return nil
}

// normalizeRepoURL parses the collected repository URL, replaces opts.repo with
// the normalized form (any embedded HTTP userinfo stripped), and folds embedded
// REST credentials into opts so they seed the probe and are persisted like
// prompted ones. It runs after the repo URL is collected and before REST
// credentials, retention, the probe, and any persistence — so a credential
// embedded in the URL participates in the connection test and never reaches
// config.toml, logs, or stdout.
//
// An explicitly supplied REST credential takes precedence over the URL-embedded
// one: a --rest-username flag (even empty) is left untouched, and a
// --rest-password-stdin value (read later) wins because it overwrites
// opts.restPassword after this point. When credentials are moved out of the URL
// a one-line, secret-free warning is printed.
func normalizeRepoURL(cmd *cobra.Command, p *prompter, opts *initOptions) error {
	parsed, err := config.ParseRepoURL(opts.repo)
	if err != nil {
		return err
	}

	// Store only the userinfo-stripped URL; a credential-bearing URL must never
	// reach config.toml or the summary.
	opts.repo = parsed.URL

	if !parsed.HasRESTCredentials() {
		return nil
	}

	// Fold the embedded username into opts unless --rest-username was explicitly
	// supplied (an explicit flag, even empty, wins over the URL).
	if !cmd.Flags().Changed("rest-username") && opts.restUsername == "" {
		opts.restUsername = parsed.RESTUsername
	}

	// The embedded password seeds opts.restPassword. A later --rest-password-stdin
	// read overwrites it, so a piped secret still takes precedence; otherwise this
	// value suppresses the interactive REST-password prompt and is used as-is.
	if !opts.restPasswordStdin && opts.restPassword == "" {
		opts.restPassword = parsed.RESTPassword
	}

	// The warning carries no secret value — only that credentials were relocated.
	p.println("Note: HTTP credentials were found in the repository URL; they have been moved to the credential store and stripped from the saved URL.")
	return nil
}

// collectRetention prompts for the snapshot-retention policy (keep
// daily/weekly/monthly/yearly) so a freshly set-up machine has a meaningful
// `icebeam forget` policy without hand-editing config.toml. Each prompt
// pre-fills with the default policy on a first run, or the existing value on a
// re-run. A flag-supplied value suppresses its prompt, preserving the scriptable
// path; negative values are rejected later by config.Validate with a field-named
// error.
func collectRetention(p *prompter, cmd *cobra.Command, opts *initOptions, existing *config.Config) error {
	flags := cmd.Flags()

	// When a secret is piped (--password-stdin or --rest-password-stdin), stdin is
	// reserved for that one secret and the invocation is scripted, so the
	// retention prompts are suppressed — a flag-supplied value wins, otherwise the
	// existing policy (re-run) or the default policy is used unchanged.
	scripted := opts.passwordStdin || opts.restPasswordStdin

	for _, r := range []struct {
		flag  string
		value *int
		label string
	}{
		{"keep-daily", &opts.keepDaily, "Keep daily snapshots"},
		{"keep-weekly", &opts.keepWeekly, "Keep weekly snapshots"},
		{"keep-monthly", &opts.keepMonthly, "Keep monthly snapshots"},
		{"keep-yearly", &opts.keepYearly, "Keep yearly snapshots"},
	} {
		// The flag default already populated *r.value with the policy default; on a
		// re-run, fall back to the existing value when the flag was not supplied.
		def := *r.value
		if !flags.Changed(r.flag) && existing != nil {
			def = retentionField(existing, r.flag)
		}
		if flags.Changed(r.flag) || scripted {
			*r.value = def // flag-supplied value or scripted default; no prompt.
			continue
		}
		n, err := p.askIntDefault(r.label, def)
		if err != nil {
			return err
		}
		*r.value = n
	}
	return nil
}

// retentionField returns the existing config's value for the named keep-* flag,
// used to pre-fill the retention prompts on a re-run.
func retentionField(existing *config.Config, flag string) int {
	switch flag {
	case "keep-daily":
		return existing.Retention.KeepDaily
	case "keep-weekly":
		return existing.Retention.KeepWeekly
	case "keep-monthly":
		return existing.Retention.KeepMonthly
	case "keep-yearly":
		return existing.Retention.KeepYearly
	default:
		return 0
	}
}

// firstSet returns the first backup set of the existing config, or nil when
// there is no existing config or it has no sets.
func firstSet(existing *config.Config) *config.Set {
	if existing == nil || len(existing.Sets) == 0 {
		return nil
	}
	return &existing.Sets[0]
}

// collectRESTCredentials prompts for the REST-server HTTP username and password
// when the repository is a REST endpoint, so a server behind HTTP basic auth can
// be reached without leaking the password on argv. Both are optional (a REST
// server may have no HTTP auth). For a non-REST repository it is a no-op so those
// prompts never appear.
//
// The username is non-secret (visible prompt); the password is hidden, or read
// from stdin when --rest-password-stdin is set. Flag-supplied values suppress
// their prompt, preserving the scriptable path. On a re-run, the username
// pre-fills from the stored value and a stored password offers a "keep existing"
// default. The collected secret reaches restic only via the environment
// (RESTIC_REST_USERNAME/PASSWORD), never argv.
func collectRESTCredentials(p *prompter, opts *initOptions, stored *storedSecrets) error {
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
		if stored.hasRESTUser {
			username, err := p.askDefault("REST-server username", stored.restUsername)
			if err != nil {
				return err
			}
			opts.restUsername = username
		} else {
			username, err := p.askOptional("REST-server username")
			if err != nil {
				return err
			}
			opts.restUsername = username
		}
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
		if stored.hasRESTPass {
			password, kept, err := p.askSecretKeep("REST-server password")
			if err != nil {
				return err
			}
			if kept {
				opts.restPassword = stored.restPassword
			} else {
				opts.restPassword = password
			}
		} else {
			password, err := p.askSecretOptional("REST-server password")
			if err != nil {
				return err
			}
			opts.restPassword = password
		}
	}

	return nil
}

// collectPassword resolves the repository password and reports whether it
// changed from the stored value. It reads from stdin when --password-stdin is
// set, generates one when --generate-password is set (or when the interactive
// prompt is answered blank on a fresh setup), and otherwise reads from a hidden
// interactive prompt. On a re-run with a stored password the prompt offers a
// "keep existing" default; keeping it returns the stored value with changed=false
// so the setup engine can skip re-verification. The stdin read goes through the
// prompter's shared reader so a preceding piped secret does not strand buffered
// input.
func collectPassword(p *prompter, opts *initOptions, stored *storedSecrets) (password string, changed bool, err error) {
	if opts.passwordStdin {
		password, err = p.readSecretLine("read password from stdin")
		if err != nil {
			return "", false, err
		}
		if password == "" {
			return "", false, errors.New("no password provided on stdin")
		}
		return password, password != stored.repoPassword, nil
	}

	// --generate-password selects generation non-interactively, regardless of
	// whether a password is already stored (it replaces it).
	if opts.generatePassword {
		generated, genErr := generateAndShowPassword(p)
		if genErr != nil {
			return "", false, genErr
		}
		return generated, generated != stored.repoPassword, nil
	}

	if stored.hasRepo {
		entered, kept, keepErr := p.askSecretKeep("Repository password")
		if keepErr != nil {
			return "", false, keepErr
		}
		if kept {
			return stored.repoPassword, false, nil
		}
		return entered, entered != stored.repoPassword, nil
	}

	// Fresh setup: offer generation when the prompt is answered blank, so a user
	// who does not want to invent a strong password can have icebeam create one.
	entered, generate, err := p.askSecretOrGenerate()
	if err != nil {
		return "", false, err
	}
	if generate {
		generated, genErr := generateAndShowPassword(p)
		if genErr != nil {
			return "", false, genErr
		}
		return generated, true, nil
	}
	return entered, true, nil
}

// generateAndShowPassword generates a strong repository password and displays it
// to the user exactly once with a prominent unrecoverable-loss warning. The value
// is written only to the prompter's output (stdout) for the user to save — it is
// never routed through the logger, so the redaction in internal/logging is not
// bypassed (nothing is logged to bypass). It is still stored in the credential
// store by the caller like any entered password.
func generateAndShowPassword(p *prompter) (string, error) {
	generated, err := generatePassword()
	if err != nil {
		return "", err
	}
	p.println("\n!! A strong repository password was generated. SAVE IT NOW.")
	p.println("!! It is NOT recoverable: if you lose it, your backups cannot be decrypted.")
	p.printf("\n    %s\n\n", generated)
	return generated, nil
}

// buildConfig assembles a Config from the collected options. On a re-run it
// starts from the existing config so values that have neither a flag nor a prompt
// (the rest of the config) are carried forward rather than reset; flag-supplied
// excludes/tags override the loaded set. The retention policy is always set from
// the collected (prompted or flagged) values, which pre-fill from the existing
// policy on a re-run. On a first run it starts from the defaults so min_version
// and log level are populated.
func buildConfig(opts *initOptions, existing *config.Config) *config.Config {
	var cfg config.Config
	if existing != nil {
		cfg = *existing
	} else {
		cfg = config.Default()
	}

	cfg.Repository.URL = strings.TrimSpace(opts.repo)
	cfg.Retention = config.Retention{
		KeepDaily:   opts.keepDaily,
		KeepWeekly:  opts.keepWeekly,
		KeepMonthly: opts.keepMonthly,
		KeepYearly:  opts.keepYearly,
	}

	set := config.Set{
		Name:  strings.TrimSpace(opts.setName),
		Paths: opts.paths,
	}
	// Carry forward the existing set's excludes/tags unless a flag overrides them,
	// so a re-run that changes only the paths does not silently drop them.
	existingSet := firstSet(existing)
	if opts.excludes != nil {
		set.Exclude = opts.excludes
	} else if existingSet != nil {
		set.Exclude = existingSet.Exclude
	}
	if opts.tags != nil {
		set.Tags = opts.tags
	} else if existingSet != nil {
		set.Tags = existingSet.Tags
	}

	cfg.Sets = []config.Set{set}
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

// printSummary reports the configured repository, secret-storage location, sets,
// log location, and the suggested next step. Secrets are stored as 0600 files in
// the config dir, so the credential-backend line is gone; the path tells the user
// where their secrets live (the file-ownership security boundary).
func printSummary(p *prompter, cfg *config.Config, configPath string) {
	p.println("\nicebeam is configured.")
	p.printf("  Repository:  %s\n", cfg.Repository.URL)
	if dir, err := config.ConfigDir(); err == nil {
		p.printf("  Credentials: %s (0600 files)\n", dir)
	}
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
