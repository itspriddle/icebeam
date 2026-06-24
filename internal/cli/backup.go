package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// backupRunner is the subset of *restic.Runner the backup/run commands drive. It
// is an interface so tests can inject a stub without a real restic binary.
type backupRunner interface {
	Backup(ctx context.Context, args ...string) (*restic.BackupSummary, error)
}

// newBackupRunner builds the restic runner the backup/run commands use. It is a
// package variable so tests can swap in a stub.
var newBackupRunner = func(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (backupRunner, error) {
	return restic.New(cfg, store, logger)
}

// newBackupCommand builds the `icebeam backup [set...]` command: backs up the
// named sets, or all configured sets when none are named.
func newBackupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "backup [set...]",
		Short: "Back up one or more configured sets",
		Long: "backup backs up the named backup set(s). With no arguments it backs " +
			"up every configured set. Each set is backed up with its paths, the " +
			"merged global and per-set excludes, the exclude_caches/one_file_system " +
			"options, and its tags. Every set is attempted even if an earlier one " +
			"fails; the exit code distinguishes success, partial failure, and total " +
			"failure.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, args)
		},
	}
}

// newRunCommand builds the `icebeam run` command: the scheduler entrypoint that
// backs up every configured set.
func newRunCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Scheduler entrypoint: back up all configured sets",
		Long: "run is the entrypoint a scheduler (launchd/systemd) invokes. It backs " +
			"up every configured set, attempts every set even if one fails, and exits " +
			"non-zero if any set failed so the scheduler can act on it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackup(cmd, nil)
		},
	}
}

// runBackup loads config, resolves the requested sets (all when none are named),
// and backs up each one, continuing past failures. It returns an exit-coded
// error reflecting partial vs. total failure.
func runBackup(cmd *cobra.Command, names []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	sets, err := resolveSets(cfg, names)
	if err != nil {
		return err
	}

	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer func() { _ = logger.Close() }()

	store, err := credentials.Open(cfg.Credentials.Backend, mustConfigDir())
	if err != nil {
		return err
	}

	runner, err := newBackupRunner(cfg, store, logger)
	if err != nil {
		return err
	}

	return backupSets(cmd, cfg, runner, logger, sets)
}

// stderrIsTerminal reports whether stderr is attached to a terminal. It governs
// whether the concise human summary prints; under a scheduler (non-TTY) the log
// file is the system of record. It is a package var so tests can force it on.
var stderrIsTerminal = func() bool { return logging.IsTerminal(os.Stderr) }

// backupSets backs up each set in order, logging and reporting per-set outcomes
// and continuing past failures. The returned error carries an exit code that
// distinguishes total failure (every set failed) from partial failure.
func backupSets(cmd *cobra.Command, cfg *config.Config, runner backupRunner, logger *logging.Logger, sets []config.Set) error {
	out := cmd.OutOrStdout()
	tty := stderrIsTerminal()

	var failures []string
	for _, set := range sets {
		if err := backupOneSet(cmd.Context(), cfg, runner, logger, set, out, tty); err != nil {
			failures = append(failures, set.Name)
			printlnTo(out, tty, fmt.Sprintf("set %q failed: %v", set.Name, err))
		}
	}

	if len(failures) == 0 {
		return nil
	}

	msg := fmt.Errorf("backup failed for %d of %d set(s): %s",
		len(failures), len(sets), strings.Join(failures, ", "))

	if len(failures) == len(sets) {
		return newExitError(exitTotalFailure, msg)
	}
	return newExitError(exitPartialFailure, msg)
}

// backupOneSet backs up a single set: logs the run start/end, builds restic's
// argument vector, invokes the runner, and prints a concise human summary on a
// TTY. An incomplete backup (restic exit code 3) is treated as a failure for the
// set so the scheduler is alerted, but the summary is still reported.
func backupOneSet(ctx context.Context, cfg *config.Config, runner backupRunner, logger *logging.Logger, set config.Set, out io.Writer, tty bool) error {
	args := backupArgs(cfg, set)
	logger.LogStart("backup", append([]string{set.Name}, args...))

	start := time.Now()
	summary, err := runner.Backup(ctx, args...)
	elapsed := time.Since(start)

	logger.LogEnd("backup:"+set.Name, elapsed, err)

	if summary != nil {
		logger.Info("backup summary",
			"set", set.Name,
			"snapshot", summary.SnapshotID,
			"files_processed", summary.TotalFilesProcessed,
			"bytes_processed", summary.TotalBytesProcessed,
			"data_added", summary.DataAdded,
		)
		if err == nil {
			printlnTo(out, tty, fmt.Sprintf(
				"set %q: %d files, %s processed, %s added (%s)",
				set.Name,
				summary.TotalFilesProcessed,
				humanBytes(summary.TotalBytesProcessed),
				humanBytes(summary.DataAdded),
				elapsed.Round(time.Millisecond),
			))
		}
	}

	return err
}

// backupArgs builds restic's backup argument vector for a set: its paths,
// followed by the merged global+set excludes, the global exclude_caches /
// one_file_system options, and the set's tags as --tag flags. Secrets never
// appear here; they reach restic via the environment.
func backupArgs(cfg *config.Config, set config.Set) []string {
	args := make([]string, 0, len(set.Paths)+8)
	args = append(args, set.Paths...)

	for _, ex := range cfg.Backup.Exclude {
		args = append(args, "--exclude", ex)
	}
	for _, ex := range set.Exclude {
		args = append(args, "--exclude", ex)
	}

	if cfg.Backup.ExcludeCaches {
		args = append(args, "--exclude-caches")
	}
	if cfg.Backup.OneFileSystem {
		args = append(args, "--one-file-system")
	}

	for _, tag := range set.Tags {
		args = append(args, "--tag", tag)
	}

	return args
}

// resolveSets returns the sets to back up. With no names it returns all
// configured sets; otherwise it returns the named sets, erroring on an unknown
// name and listing the available set names.
func resolveSets(cfg *config.Config, names []string) ([]config.Set, error) {
	if len(names) == 0 {
		if len(cfg.Sets) == 0 {
			return nil, errors.New("no backup sets are configured; add a [[set]] to your config or run `icebeam init`")
		}
		return cfg.Sets, nil
	}

	byName := make(map[string]config.Set, len(cfg.Sets))
	for _, s := range cfg.Sets {
		byName[s.Name] = s
	}

	sets := make([]config.Set, 0, len(names))
	for _, name := range names {
		set, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown set %q; available sets: %s", name, availableSetNames(cfg))
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// availableSetNames returns the configured set names, sorted, for error messages.
func availableSetNames(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Sets))
	for _, s := range cfg.Sets {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// buildLogger constructs the persistent logger, mirroring human output to errOut
// when stderr is a terminal. errOut is the command's error stream so tests can
// capture the mirror instead of polluting the process stderr.
func buildLogger(cfg *config.Config, errOut io.Writer) (*logging.Logger, error) {
	return logging.New(cfg, logging.Options{TTY: stderrIsTerminal(), Stderr: errOut})
}

// mustConfigDir resolves the XDG config dir for the credential file backend,
// falling back to an empty string when it cannot be resolved (Open will then
// surface the error path).
func mustConfigDir() string {
	dir, err := config.ConfigDir()
	if err != nil {
		return ""
	}
	return dir
}

// printlnTo writes a status line to out, but only when on a TTY: under a
// scheduler (non-TTY) the log file is the system of record and stdout stays
// quiet.
func printlnTo(out io.Writer, tty bool, line string) {
	if !tty {
		return
	}
	_, _ = fmt.Fprintln(out, line)
}

// humanBytes renders a byte count in a compact human-readable form (e.g. 1.5 MiB).
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
