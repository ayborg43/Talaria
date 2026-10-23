//go:build unix

package vault

import "talaria/internal/filelock"

// lockFile takes an exclusive lock that other Talaria processes (a gateway and
// `tal connect`, say) also honour, so a change is read, made and saved without
// another process doing the same in between. The returned function releases it.
func (v *Vault) lockFile() (func(), error) {
	return filelock.Lock(v.Path)
}
