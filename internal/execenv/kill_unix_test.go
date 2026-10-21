//go:build unix

package execenv

import "syscall"

func syscallKill(pid int) { syscall.Kill(pid, syscall.SIGKILL) }
