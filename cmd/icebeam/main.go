// Command icebeam is a single-binary wrapper around restic for managing backups
// on personal machines and Linux servers.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/itspriddle/icebeam/internal/cli"
)

func main() {
	os.Exit(run())
}

// run wraps the command execution so the deferred signal stop runs before the
// process exits (os.Exit would otherwise skip deferred calls).
func run() int {
	// A cancellable context lets restic invocations be terminated cleanly on
	// SIGINT/SIGTERM rather than orphaning the child.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Execute(ctx)
}
