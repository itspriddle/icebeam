package cli

import (
	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/config"
)

// newReconfigureCommand builds the `icebeam reconfigure` command: a guided edit
// of an existing setup. It drives the same validate-first setup engine as
// `icebeam init`, but requires a config to already exist so the intent ("change
// my settings") is distinct from first-time setup.
func newReconfigureCommand() *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "reconfigure",
		Short: "Edit an existing setup: config, credentials, and repository",
		Long: "reconfigure edits an already-configured machine. It pre-fills every " +
			"prompt with the current value and offers to keep stored secrets, then " +
			"runs the same validate-first flow as init: the repository is probed " +
			"before config.toml or any secret is rewritten. Use it to change a single " +
			"setting (repository URL, paths, REST-server credentials, ...) without " +
			"re-entering everything. It requires an existing config; on a fresh " +
			"machine run `icebeam init` instead. All prompts have flag equivalents so " +
			"reconfigure can be scripted. Secrets are never accepted on argv: a single " +
			"secret may be read from stdin per run via --password-stdin (repository " +
			"password) or --rest-password-stdin (REST-server password); the two are " +
			"mutually exclusive.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReconfigure(cmd, opts)
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
	registerRetentionFlags(flags, opts)
	flags.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the repository password from stdin (no echo); mutually exclusive with --rest-password-stdin")
	flags.BoolVar(&opts.restPasswordStdin, "rest-password-stdin", false, "read the REST-server password from stdin (no echo); mutually exclusive with --password-stdin")

	return cmd
}

// runReconfigure edits an existing setup. Unlike init it requires a config to
// already exist: a missing config returns config.ErrNotConfigured (the standard
// "run `icebeam init`" message) rather than starting a fresh setup. The loaded
// config pre-fills every prompt and the same validate-first engine probes the
// repository before persisting anything.
func runReconfigure(cmd *cobra.Command, opts *initOptions) error {
	configPath, err := config.ConfigPath()
	if err != nil {
		return err
	}

	existing, err := loadExistingConfig(configPath)
	if err != nil {
		return err
	}
	if existing == nil {
		return config.ErrNotConfigured
	}

	return runSetupFlow(cmd, opts, existing, configPath)
}
