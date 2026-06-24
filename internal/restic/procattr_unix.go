//go:build unix

package restic

import (
	"os/exec"
	"syscall"
	"time"
)

// waitDelay bounds how long Wait blocks after the context is canceled before
// the child's I/O pipes are force-closed. It exists so the output reader
// (which reads to EOF before Wait) unblocks promptly even if a restic
// descendant keeps the pipe's write-end open after the direct child is killed.
const waitDelay = 2 * time.Second

// setProcessGroup configures cmd so that canceling its context terminates the
// restic process and every descendant it spawned, leaving no orphan.
//
// exec.CommandContext's default cancellation only SIGKILLs the direct child, so
// any restic descendant keeps running (and keeps the output pipe open, blocking
// the reader). To reap the whole tree, the child is started in its own process
// group (Setpgid) and Cancel signals the negative pid — the entire group — with
// SIGTERM. WaitDelay then SIGKILLs anything that ignores SIGTERM and force-closes
// the pipes so the reader cannot block indefinitely.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error {
		// Signal the whole process group (negative pid). The group id equals the
		// child's pid because Setpgid made it a group leader.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
