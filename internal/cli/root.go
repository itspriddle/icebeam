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
	root.AddCommand(newRunCommand())
	root.AddCommand(newBackupCommand())
	root.AddCommand(newForgetCommand())
	root.AddCommand(newPruneCommand())
	root.AddCommand(newCheckCommand())
	root.AddCommand(newSnapshotsCommand())
	root.AddCommand(newLSCommand())
	root.AddCommand(newFindCommand())
	root.AddCommand(newRestoreCommand())
	root.AddCommand(newDumpCommand())

	stubs := []struct {
		use   string
		short string
	}{
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
}
