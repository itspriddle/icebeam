package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// browseRunner is the subset of *restic.Runner the snapshots/ls/find commands
// drive. It is an interface so tests can inject a stub without a real restic.
type browseRunner interface {
	Snapshots(ctx context.Context, args ...string) ([]restic.Snapshot, error)
	LS(ctx context.Context, args ...string) (*restic.LSResult, error)
	Find(ctx context.Context, args ...string) ([]restic.FindResult, error)
}

// newBrowseRunner builds the restic runner the browse commands use. It is a
// package variable so tests can swap in a stub.
var newBrowseRunner = func(cfg *config.Config, store credentials.CredentialStore, logger *logging.Logger) (browseRunner, error) {
	return restic.New(cfg, store, logger)
}

// browseFilters collects the tag/host filters shared by the browse commands. Only
// the filters restic supports for a given subcommand are wired into its flags.
type browseFilters struct {
	tags []string
	host string
}

// args returns the restic --tag/--host flags for the configured filters.
func (f *browseFilters) args() []string {
	var args []string
	for _, tag := range f.tags {
		args = append(args, "--tag", tag)
	}
	if f.host != "" {
		args = append(args, "--host", f.host)
	}
	return args
}

// newSnapshotsCommand builds `icebeam snapshots` (alias `list`): lists the
// repository's snapshots in a table, or as JSON with --json.
func newSnapshotsCommand() *cobra.Command {
	var (
		jsonOut bool
		filters browseFilters
	)

	cmd := &cobra.Command{
		Use:     "snapshots",
		Aliases: []string{"list"},
		Short:   "List snapshots in the repository",
		Long: "snapshots lists the snapshots in your repository with their id, time, " +
			"host, tags, and paths. Pass --json for machine-readable output, or " +
			"filter with --tag/--host.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshots(cmd, &filters, jsonOut)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	flags.StringArrayVar(&filters.tags, "tag", nil, "only show snapshots with this tag (repeatable)")
	flags.StringVar(&filters.host, "host", "", "only show snapshots from this host")

	return cmd
}

// newLSCommand builds `icebeam ls <snapshotID> [path]`: lists the contents of a
// snapshot, optionally under a path. The selector may be `latest`.
func newLSCommand() *cobra.Command {
	var (
		jsonOut bool
		filters browseFilters
	)

	cmd := &cobra.Command{
		Use:   "ls <snapshotID> [path]",
		Short: "List the contents of a snapshot",
		Long: "ls lists the files and directories in a snapshot, optionally restricted " +
			"to a path. Use `latest` as the snapshot id to list the most recent " +
			"snapshot. Pass --json for machine-readable output.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLS(cmd, args, &filters, jsonOut)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	flags.StringArrayVar(&filters.tags, "tag", nil, "restrict `latest` to snapshots with this tag (repeatable)")
	flags.StringVar(&filters.host, "host", "", "restrict `latest` to snapshots from this host")

	return cmd
}

// newFindCommand builds `icebeam find <pattern>`: searches for files matching a
// pattern across snapshots and reports which snapshots contain them.
func newFindCommand() *cobra.Command {
	var (
		jsonOut bool
		filters browseFilters
	)

	cmd := &cobra.Command{
		Use:   "find <pattern>",
		Short: "Search for files across snapshots",
		Long: "find searches every snapshot for files matching a pattern and reports " +
			"which snapshots contain them. Pass --json for machine-readable output, " +
			"or filter with --tag/--host.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFind(cmd, args[0], &filters, jsonOut)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	flags.StringArrayVar(&filters.tags, "tag", nil, "only search snapshots with this tag (repeatable)")
	flags.StringVar(&filters.host, "host", "", "only search snapshots from this host")

	return cmd
}

// runSnapshots loads config, lists snapshots, and renders them as a table or
// JSON.
func runSnapshots(cmd *cobra.Command, filters *browseFilters, jsonOut bool) error {
	runner, cleanup, err := openBrowse(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	snapshots, err := runner.Snapshots(cmd.Context(), filters.args()...)
	if err != nil {
		return mapResticExit(err)
	}

	out := cmd.OutOrStdout()
	if jsonOut {
		return writeJSON(out, snapshots)
	}
	renderSnapshots(out, snapshots)
	return nil
}

// runLS loads config, lists a snapshot's contents, and renders them as a table or
// JSON. args[0] is the snapshot selector; args[1], if present, is a path.
func runLS(cmd *cobra.Command, args []string, filters *browseFilters, jsonOut bool) error {
	runner, cleanup, err := openBrowse(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := runner.LS(cmd.Context(), append(args, filters.args()...)...)
	if err != nil {
		return mapResticExit(err)
	}

	out := cmd.OutOrStdout()
	if jsonOut {
		return writeJSON(out, result.Nodes)
	}
	renderLS(out, result)
	return nil
}

// runFind loads config, searches for a pattern, and renders the matching
// snapshots as a table or JSON.
func runFind(cmd *cobra.Command, pattern string, filters *browseFilters, jsonOut bool) error {
	runner, cleanup, err := openBrowse(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	results, err := runner.Find(cmd.Context(), append([]string{pattern}, filters.args()...)...)
	if err != nil {
		return mapResticExit(err)
	}

	out := cmd.OutOrStdout()
	if jsonOut {
		return writeJSON(out, results)
	}
	renderFind(out, results)
	return nil
}

// openBrowse is the shared setup for the browse commands: it loads config and
// builds the logger, credential store, and restic runner. The returned cleanup
// closes the logger and must be deferred by the caller.
func openBrowse(cmd *cobra.Command) (browseRunner, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}

	logger, err := buildLogger(cfg, cmd.ErrOrStderr())
	if err != nil {
		return nil, nil, err
	}

	store, err := credentials.Open(cfg.Credentials.Backend, mustConfigDir())
	if err != nil {
		_ = logger.Close()
		return nil, nil, err
	}

	runner, err := newBrowseRunner(cfg, store, logger)
	if err != nil {
		_ = logger.Close()
		return nil, nil, err
	}

	return runner, func() { _ = logger.Close() }, nil
}

// renderSnapshots writes the snapshots table to out. An empty list prints a clear
// "no snapshots" line rather than an error.
func renderSnapshots(out io.Writer, snapshots []restic.Snapshot) {
	if len(snapshots) == 0 {
		writeLine(out, "No snapshots found.")
		return
	}

	tw := newTabWriter(out)
	writeLine(tw, "ID\tTIME\tHOST\tTAGS\tPATHS")
	for _, s := range snapshots {
		writeLine(tw, strings.Join([]string{
			snapshotID(s),
			s.Time.Local().Format("2006-01-02 15:04:05"),
			s.Hostname,
			joinOrDash(s.Tags),
			joinOrDash(s.Paths),
		}, "\t"))
	}
	flushTabWriter(tw)
}

// renderLS writes a snapshot's contents to out. An empty listing prints a clear
// "empty" line rather than an error.
func renderLS(out io.Writer, result *restic.LSResult) {
	if len(result.Nodes) == 0 {
		writeLine(out, "Snapshot is empty (no matching entries).")
		return
	}

	tw := newTabWriter(out)
	writeLine(tw, "MODE\tSIZE\tMTIME\tPATH")
	for _, n := range result.Nodes {
		writeLine(tw, strings.Join([]string{
			n.Mode,
			humanBytes(n.Size),
			n.MTime.Local().Format("2006-01-02 15:04:05"),
			n.Path,
		}, "\t"))
	}
	flushTabWriter(tw)
}

// renderFind writes the find results to out. No matches prints a clear "nothing
// found" line rather than an error.
func renderFind(out io.Writer, results []restic.FindResult) {
	matches := 0
	for _, r := range results {
		matches += len(r.Matches)
	}
	if matches == 0 {
		writeLine(out, "Nothing found.")
		return
	}

	tw := newTabWriter(out)
	writeLine(tw, "SNAPSHOT\tSIZE\tMTIME\tPATH")
	for _, r := range results {
		for _, m := range r.Matches {
			writeLine(tw, strings.Join([]string{
				shortID(r.Snapshot),
				humanBytes(m.Size),
				m.MTime.Local().Format("2006-01-02 15:04:05"),
				m.Path,
			}, "\t"))
		}
	}
	flushTabWriter(tw)
}

// snapshotID returns the snapshot's short id, falling back to the full id.
func snapshotID(s restic.Snapshot) string {
	if s.ShortID != "" {
		return s.ShortID
	}
	return shortID(s.ID)
}

// shortID abbreviates a full snapshot id to restic's customary 8-char short form.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// joinOrDash joins values with commas, returning "-" for an empty slice so a
// table cell is never blank.
func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

// writeJSON encodes v as indented JSON to out.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// writeLine writes a line to out, ignoring the (non-actionable) write error so
// errcheck stays satisfied for writes to a non-literal writer.
func writeLine(out io.Writer, line string) {
	_, _ = fmt.Fprintln(out, line)
}

// newTabWriter builds a tabwriter for column-aligned table output.
func newTabWriter(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
}

// flushTabWriter flushes the tabwriter, ignoring the (non-actionable) error.
func flushTabWriter(tw *tabwriter.Writer) {
	_ = tw.Flush()
}
