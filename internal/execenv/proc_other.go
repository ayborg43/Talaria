//go:build !unix

package execenv

import (
	"os/exec"
	"time"
)

func setupKill(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }

// processAlive cannot tell on this platform, so nothing is ever treated as abandoned.
func processAlive(pid int) bool { return true }
