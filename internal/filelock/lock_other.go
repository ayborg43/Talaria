//go:build !unix

package filelock

// lock does nothing where there is no flock: changes from two processes at
// the same instant can then overwrite each other, as before.
func lock(path string) (func(), error) { return func() {}, nil }
