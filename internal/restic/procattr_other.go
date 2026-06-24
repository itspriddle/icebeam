//go:build !unix

package restic

import (
	"os/exec"
	"time"
)

// waitDelay bounds how long Wait blocks after the context is cancelled before
// the child's I/O pipes are force-closed, so the output reader unblocks.
const waitDelay = 2 * time.Second

// setProcessGroup is a no-op on platforms without process-group semantics
// (icebeam targets only macOS and Linux, both unix). WaitDelay still ensures the
// output reader unblocks after cancellation so the runner does not hang.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = waitDelay
}
