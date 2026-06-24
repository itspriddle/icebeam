package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// maintenanceRunner is the subset of *restic.Runner the forget/prune/check
// commands drive. It is an interface so tests can inject a stub without a real
// restic binary.
type maintenanceRunner interface {
	Run(ctx context.Context, args ...string) error
}

// newMaintenanceRunner builds the restic runner the maintenance commands use. It
// is a package variable so tests can swap in a stub.
var newMaintenanceRunner = func(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (maintenanceRunner, error) {
	return restic.New(cfg, store, logger)
}

// forgetOptions collects the flags that drive `icebeam forget`.
type forgetOptions struct {
	prune   bool
	noPrune bool
	dryRun  bool
}

// pruneEnabled reports whether pruning should run, reconciling the --prune
// (default on) and --no-prune opt-out flags. --no-prune wins when set.
func (o *forgetOptions) pruneEnabled() bool {
	return o.prune && !o.noPrune
}

// checkOptions collects the flags that drive `icebeam check`.
type checkOptions struct {
	readDataSubset string
}

// newForgetCommand builds the `icebeam forget` command: applies the configured
// retention policy and optionally prunes in the same invocation.
func newForgetCommand() *cobra.Command {
	opts := &forgetOptions{}

	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Apply the configured retention policy",
		Long: "forget applies the retention policy from your config " +
			"(keep_daily/weekly/monthly/yearly) as restic --keep-* flags, grouped " +
			"by host and tag so each set's snapshots are pruned independently. By " +
			"default it also prunes the now-unreferenced data in the same run; pass " +
			"--no-prune to skip pruning, or --dry-run to see what would be removed " +
			"without removing anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runForget(cmd, opts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.prune, "prune", true, "prune unreferenced data after forgetting")
	flags.BoolVar(&opts.noPrune, "no-prune", false, "do not prune after forgetting (opt out of the default --prune)")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "show what would be removed without removing it")
	cmd.MarkFlagsMutuallyExclusive("prune", "no-prune")

	return cmd
}

// newPruneCommand builds the `icebeam prune` command: runs restic prune
// standalone.
func newPruneCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Reclaim space from removed snapshots",
		Long: "prune runs restic prune standalone, reclaiming the space held by data " +
			"no longer referenced by any snapshot (e.g. after a `forget --no-prune`).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(cmd)
		},
	}
}

// newCheckCommand builds the `icebeam check` command: verifies repository
// integrity, optionally reading back a subset of the actual data.
func newCheckCommand() *cobra.Command {
	opts := &checkOptions{}

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Verify repository integrity",
		Long: "check verifies the repository's structure and metadata. By default it " +
			"checks metadata only; pass --read-data-subset to also read back and " +
			"verify a subset of the actual pack data (e.g. \"10%\" or \"1/50\"), " +
			"which catches storage corruption restic's metadata check cannot.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.readDataSubset, "read-data-subset", "",
		"also read and verify a subset of the data (e.g. \"10%\" or \"1/50\"); default is metadata-only")

	return cmd
}

// runForget loads config and runs restic forget with the retention flags derived
// from config plus the --prune/--dry-run wiring.
func runForget(cmd *cobra.Command, opts *forgetOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return runMaintenance(cmd, cfg, "forget", forgetArgs(cfg, opts))
}

// runPrune loads config and runs restic prune standalone.
func runPrune(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return runMaintenance(cmd, cfg, "prune", []string{"prune"})
}

// runCheck loads config and runs restic check with the configured verification
// flags.
func runCheck(cmd *cobra.Command, opts *checkOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return runMaintenance(cmd, cfg, "check", checkArgs(opts))
}

// runMaintenance is the shared driver for the maintenance commands: it builds the
// logger and credential store, opens the runner, logs the invocation start/end,
// runs restic, and maps a restic failure to an icebeam exit code. logName is the
// label used for the log lines.
func runMaintenance(cmd *cobra.Command, cfg *config.Config, logName string, args []string) error {
	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer func() { _ = logger.Close() }()

	store, err := credentials.Open(mustConfigDir())
	if err != nil {
		return err
	}

	runner, err := newMaintenanceRunner(cfg, store, logger)
	if err != nil {
		return err
	}

	logger.LogStart(logName, args)

	start := time.Now()
	runErr := runner.Run(cmd.Context(), args...)
	elapsed := time.Since(start)

	logger.LogEnd(logName, elapsed, runErr)

	if runErr != nil {
		return mapResticExit(runErr)
	}
	return nil
}

// forgetArgs builds restic's forget argument vector. The retention policy from
// config becomes --keep-* flags; --group-by host,tags keeps each set's snapshots
// pruned independently. Pruning runs inline unless disabled; --dry-run reports
// without removing.
func forgetArgs(cfg *config.Config, opts *forgetOptions) []string {
	args := []string{"forget", "--group-by", "host,tags"}

	if cfg.Retention.KeepDaily > 0 {
		args = append(args, "--keep-daily", strconv.Itoa(cfg.Retention.KeepDaily))
	}
	if cfg.Retention.KeepWeekly > 0 {
		args = append(args, "--keep-weekly", strconv.Itoa(cfg.Retention.KeepWeekly))
	}
	if cfg.Retention.KeepMonthly > 0 {
		args = append(args, "--keep-monthly", strconv.Itoa(cfg.Retention.KeepMonthly))
	}
	if cfg.Retention.KeepYearly > 0 {
		args = append(args, "--keep-yearly", strconv.Itoa(cfg.Retention.KeepYearly))
	}

	// A dry run never modifies the repository, so it must not request a prune.
	if opts.pruneEnabled() && !opts.dryRun {
		args = append(args, "--prune")
	}
	if opts.dryRun {
		args = append(args, "--dry-run")
	}

	return args
}

// checkArgs builds restic's check argument vector. With no subset it checks
// metadata only; --read-data-subset adds back-reading of a fraction of the data.
func checkArgs(opts *checkOptions) []string {
	args := []string{"check"}
	if opts.readDataSubset != "" {
		args = append(args, "--read-data-subset", opts.readDataSubset)
	}
	return args
}

// mapResticExit translates a restic failure into an icebeam exit-coded error.
// A restic ExitError carries restic's own exit code through to the process exit
// status so a scheduler can act on it; any other failure is a generic error.
func mapResticExit(err error) error {
	var exitErr *restic.ExitError
	if errors.As(err, &exitErr) {
		return newExitError(exitErr.Code, fmt.Errorf("restic exited with status %d: %w", exitErr.Code, err))
	}
	return err
}
