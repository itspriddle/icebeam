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
	// A cancellable context lets restic invocations (added in later stories) be
	// terminated cleanly on SIGINT/SIGTERM rather than orphaning the child.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx))
}
