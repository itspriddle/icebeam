// Package surface produces a deterministic snapshot of a cobra command tree so
// tests can detect unintended changes to icebeam's CLI surface (commands,
// flags, and positional arguments).
package surface

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Generate walks a cobra command tree and produces a deterministic, sorted
// snapshot of all commands, flags, and positional arguments. Built-in commands
// (help, completion) and the --help flag are excluded.
func Generate(root *cobra.Command) string {
	var lines []string
	walk(root, &lines)
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func walk(cmd *cobra.Command, lines *[]string) {
	name := cmd.Name()

	// Skip Cobra built-in commands.
	if name == "help" || name == "completion" {
		return
	}

	path := fullPath(cmd)

	// CMD line.
	*lines = append(*lines, fmt.Sprintf("CMD %s", path))

	// ARG lines — extracted from the Use string, recording each positional's
	// name and kind (required/optional, variadic) so a contract change such as
	// making an argument optional is caught.
	for i, arg := range parseArgs(cmd.Use) {
		*lines = append(*lines, fmt.Sprintf("ARG %s %d %s %s", path, i, arg.name, arg.kind))
	}

	// FLAG lines.
	// For the root command, emit persistent flags (they apply globally).
	// For all commands, emit local non-persistent flags.
	if !cmd.HasParent() {
		cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			*lines = append(*lines, fmt.Sprintf("FLAG %s --%s type=%s", path, f.Name, f.Value.Type()))
		})
	}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" {
			return
		}
		// Skip persistent flags (already emitted on root).
		if cmd.PersistentFlags().Lookup(f.Name) != nil {
			return
		}
		*lines = append(*lines, fmt.Sprintf("FLAG %s --%s type=%s", path, f.Name, f.Value.Type()))
	})

	for _, child := range cmd.Commands() {
		walk(child, lines)
	}
}

// fullPath returns the full command path (e.g. "icebeam restore").
func fullPath(cmd *cobra.Command) string {
	parts := []string{}
	for c := cmd; c != nil; c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return strings.Join(parts, " ")
}

type cmdArg struct {
	name string
	kind string // required | optional | required-variadic | optional-variadic
}

// parseArgs extracts positional argument names and their kind from a cobra Use
// string. Required args use angle brackets, optional args use square brackets,
// and a trailing "..." marks a variadic argument:
//
//	"restore <snapshotID>"   -> [{snapshotID, required}]
//	"ls <snapshotID> [path]" -> [{snapshotID, required}, {path, optional}]
//	"backup [set...]"        -> [{set, optional-variadic}]
func parseArgs(use string) []cmdArg {
	var args []cmdArg
	for _, token := range strings.Fields(use) {
		var optional bool
		var inner string

		switch {
		case strings.HasPrefix(token, "<") && strings.HasSuffix(token, ">"):
			inner = token[1 : len(token)-1]
		case strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]"):
			inner = token[1 : len(token)-1]
			optional = true
		default:
			continue
		}

		variadic := strings.HasSuffix(inner, "...")
		inner = strings.TrimSuffix(inner, "...")
		if inner == "" {
			continue
		}

		kind := "required"
		if optional {
			kind = "optional"
		}
		if variadic {
			kind += "-variadic"
		}

		args = append(args, cmdArg{name: inner, kind: kind})
	}
	return args
}
