package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// restoreRunner is the subset of *restic.Runner the restore/dump commands drive.
// It is an interface so tests can inject a stub without a real restic binary.
type restoreRunner interface {
	Restore(ctx context.Context, args ...string) error
	Dump(ctx context.Context, w io.Writer, args ...string) error
}

// newRestoreRunner builds the restic runner the restore/dump commands use. It is
// a package variable so tests can swap in a stub.
var newRestoreRunner = func(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (restoreRunner, error) {
	return restic.New(cfg, store, logger)
}

// restoreOptions collects the flags that drive `icebeam restore`.
type restoreOptions struct {
	target  string
	include []string
	exclude []string
	force   bool
}

// newRestoreCommand builds the `icebeam restore <snapshotID>` command: restores a
// snapshot (or a filtered subset of it) into a target directory.
func newRestoreCommand() *cobra.Command {
	opts := &restoreOptions{}

	cmd := &cobra.Command{
		Use:   "restore <snapshotID>",
		Short: "Restore a snapshot to a target directory",
		Long: "restore writes the contents of a snapshot into a target directory. Use " +
			"`latest` as the snapshot id to restore the most recent snapshot, and " +
			"--include/--exclude to restore only matching paths. restore refuses to " +
			"write into a non-empty target unless --force is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd, args[0], opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.target, "target", "", "directory to restore into (required)")
	flags.StringArrayVar(&opts.include, "include", nil, "only restore paths matching this pattern (repeatable)")
	flags.StringArrayVar(&opts.exclude, "exclude", nil, "skip paths matching this pattern (repeatable)")
	flags.BoolVar(&opts.force, "force", false, "allow restoring into a non-empty target directory")
	_ = cmd.MarkFlagRequired("target")

	return cmd
}

// newDumpCommand builds the `icebeam dump <snapshotID> <path>` command: writes a
// single file's contents from a snapshot to stdout.
func newDumpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dump <snapshotID> <path>",
		Short: "Write a single file from a snapshot to stdout",
		Long: "dump writes the contents of a single file from a snapshot to stdout, " +
			"suitable for piping. Use `latest` as the snapshot id to read from the " +
			"most recent snapshot. dump errors clearly if the path is a directory or " +
			"does not exist.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDump(cmd, args[0], args[1])
		},
	}
}

// runRestore loads config, guards against clobbering a non-empty target, reports
// the target it will write to, and restores the snapshot.
func runRestore(cmd *cobra.Command, snapshot string, opts *restoreOptions) error {
	if err := guardRestoreTarget(opts.target, opts.force); err != nil {
		return err
	}

	runner, logger, cleanup, err := openRestore(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	args := restoreArgs(snapshot, opts)

	// Report the destination before writing so the user knows where data lands.
	writeLine(cmd.OutOrStdout(), fmt.Sprintf("Restoring snapshot %q to %s", snapshot, opts.target))

	logger.LogStart("restore", args)
	start := time.Now()
	runErr := runner.Restore(cmd.Context(), args...)
	logger.LogEnd("restore", time.Since(start), runErr)

	if runErr != nil {
		return mapResticExit(runErr)
	}
	writeLine(cmd.OutOrStdout(), "Restore complete.")
	return nil
}

// runDump loads config and streams a single file from a snapshot to stdout.
func runDump(cmd *cobra.Command, snapshot, path string) error {
	runner, logger, cleanup, err := openRestore(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	args := []string{snapshot, path}

	logger.LogStart("dump", args)
	start := time.Now()
	runErr := runner.Dump(cmd.Context(), cmd.OutOrStdout(), args...)
	logger.LogEnd("dump", time.Since(start), runErr)

	if runErr != nil {
		return mapResticExit(runErr)
	}
	return nil
}

// restoreArgs builds restic's restore argument vector: the snapshot selector,
// the required --target, and any --include/--exclude path filters.
func restoreArgs(snapshot string, opts *restoreOptions) []string {
	args := []string{snapshot, "--target", opts.target}
	for _, inc := range opts.include {
		args = append(args, "--include", inc)
	}
	for _, exc := range opts.exclude {
		args = append(args, "--exclude", exc)
	}
	return args
}

// guardRestoreTarget refuses to restore into a non-empty target directory unless
// force is set. A missing target is fine (restic creates it); an empty directory
// is fine. An existing file (not a directory) at the path is always an error.
func guardRestoreTarget(target string, force bool) error {
	if force {
		return nil
	}

	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect target %q: %w", target, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("target %q exists and is not a directory", target)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("read target %q: %w", target, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("target %q is not empty; pass --force to restore into it anyway", target)
	}
	return nil
}

// openRestore is the shared setup for the restore/dump commands: it loads config
// and builds the logger, credential store, and restic runner. The returned
// cleanup closes the logger and must be deferred by the caller.
func openRestore(cmd *cobra.Command) (restoreRunner, *logging.Logger, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, err
	}

	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return nil, nil, nil, err
	}

	store, err := credentials.Open(cfg.Credentials.Backend, mustConfigDir())
	if err != nil {
		_ = logger.Close()
		return nil, nil, nil, err
	}

	runner, err := newRestoreRunner(cfg, store, logger)
	if err != nil {
		_ = logger.Close()
		return nil, nil, nil, err
	}

	return runner, logger, func() { _ = logger.Close() }, nil
}
