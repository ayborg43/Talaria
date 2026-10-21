//go:build !unix

package execenv

func syscallKill(pid int) {}
