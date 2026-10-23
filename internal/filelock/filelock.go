// Package filelock serialises changes to one file across Talaria processes (a
// gateway and the CLI, say), so a change is read, made and saved without
// another process doing the same in between. Locks are only honoured by
// processes that also lock.
package filelock

// Lock takes an exclusive inter-process lock for path and returns its release
// function. It blocks until the lock is held; sections using it must be short.
// The lock lives in a path+".lock" sidecar, so path's directory must exist.
func Lock(path string) (release func(), err error) {
	return lock(path)
}
