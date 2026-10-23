//go:build unix

package filelock

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLockSerialisesTwoHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		second, err := Lock(path)
		if err != nil {
			t.Error(err)
			return
		}
		acquired <- second
	}()
	select {
	case <-acquired:
		t.Fatal("the second lock was granted while the first was held")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case second := <-acquired:
		second()
	case <-time.After(2 * time.Second):
		t.Fatal("the second lock was never granted")
	}
}
