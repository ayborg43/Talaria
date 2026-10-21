//go:build unix

package vault

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive lock that other Talaria processes (a gateway and
// `tal connect`, say) also honour, so a change is read, made and saved without
// another process doing the same in between. The returned function releases it.
func (v *Vault) lockFile() (func(), error) {
	f, err := os.OpenFile(v.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("locking the vault: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking the vault: %w", err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
