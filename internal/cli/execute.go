package cli

import (
	"context"
	"fmt"
	"os"
)

// Execute builds the root command and runs it against the process arguments,
// returning the exit code the caller should pass to os.Exit.
func Execute(ctx context.Context) int {
	root := NewRootCommand()

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "icebeam:", err)
		return 1
	}

	return 0
}
