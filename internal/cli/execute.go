package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Execute builds the root command and runs it against the process arguments,
// returning the exit code the caller should pass to os.Exit. An error carrying a
// specific exit code (see exitCoder) maps to that code so a scheduler can tell a
// partial backup failure from a total one; any other error maps to exitError.
func Execute(ctx context.Context) int {
	root := NewRootCommand()

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "icebeam:", err)

		var coder exitCoder
		if errors.As(err, &coder) {
			return coder.ExitCode()
		}
		return exitError
	}

	return exitOK
}
