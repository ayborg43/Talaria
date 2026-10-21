//go:build unix

package execenv

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// setupKill makes cmd run in its own process group and, when its context ends,
// kills the whole group, so a command's helpers and background jobs go with
// it. WaitDelay stops Wait blocking on a pipe that a straggler still holds.
func setupKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

// processAlive reports whether a process with this pid exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
