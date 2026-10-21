//go:build !unix

package vault

// lockFile does nothing where there is no flock: changes from two processes at
// the same instant can then overwrite each other, as before.
func (v *Vault) lockFile() (func(), error) { return func() {}, nil }
