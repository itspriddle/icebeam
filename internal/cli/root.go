// Package cli wires up the icebeam command tree built on cobra.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/version"
)

// NewRootCommand builds the icebeam root command with all subcommands attached.
// Subcommands are stubs at this stage and are fleshed out by later stories.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "icebeam",
		Short: "Manage restic backups on personal machines and Linux servers",
		Long: "icebeam is a single-binary wrapper around restic that owns the parts " +
			"restic leaves to the user: a declarative config of what to back up, secure " +
			"credential storage with a portable fallback, a persistent log, an init setup " +
			"wizard, and OS-native scheduling so a single `icebeam run` can be driven by " +
			"launchd or systemd.",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetVersionTemplate("icebeam {{.Version}}\n")

	addCommandGroups(root)

	return root
}

// addCommandGroups attaches the stubbed command surface described in the PRD.
// Each command is replaced with a real implementation in a later story.
func addCommandGroups(root *cobra.Command) {
	root.AddCommand(newInitCommand())

	stubs := []struct {
		use   string
		short string
	}{
		{"run", "Scheduler entrypoint: back up all configured sets"},
		{"backup", "Back up one or more configured sets"},
		{"forget", "Apply the configured retention policy"},
		{"prune", "Reclaim space from removed snapshots"},
		{"check", "Verify repository integrity"},
		{"snapshots", "List snapshots in the repository"},
		{"ls", "List the contents of a snapshot"},
		{"find", "Search for files across snapshots"},
		{"restore", "Restore a snapshot to a target directory"},
		{"dump", "Write a single file from a snapshot to stdout"},
		{"schedule", "Install, uninstall, or inspect the OS scheduler unit"},
	}

	for _, s := range stubs {
		cmd := &cobra.Command{
			Use:   s.use,
			Short: s.short,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return errNotImplemented(cmd.Name())
			},
		}
		root.AddCommand(cmd)
	}

	// "list" is an alias-style command for "snapshots" per the PRD.
	if snapshots, _, err := root.Find([]string{"snapshots"}); err == nil {
		snapshots.Aliases = append(snapshots.Aliases, "list")
	}
}
