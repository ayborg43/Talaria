package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeArchive(t *testing.T, home string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Create(home, &buf, Options{Exclude: []string{filepath.Join(home, "vault.key")}}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVerifyAcceptsABackupAndRefusesOthers(t *testing.T) {
	arc := makeArchive(t, sampleHome(t))
	n, created, err := Verify(bytes.NewReader(arc))
	if err != nil || n == 0 || created.IsZero() {
		t.Fatalf("%d %v %v", n, created, err)
	}
	if _, _, err := Verify(strings.NewReader("not gzip")); err == nil {
		t.Fatal("plain text accepted")
	}
	var evil bytes.Buffer
	gz := gzip.NewWriter(&evil)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if _, _, err := Verify(&evil); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("an escaping name was accepted: %v", err)
	}
	var noManifest bytes.Buffer
	gz = gzip.NewWriter(&noManifest)
	tw = tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if _, _, err := Verify(&noManifest); err == nil || !strings.Contains(err.Error(), "BACKUP.json") {
		t.Fatalf("an archive with no manifest was accepted: %v", err)
	}
}

func TestRestoreInPlaceKeepsTheOldStateAside(t *testing.T) {
	src := sampleHome(t)
	arc := makeArchive(t, src)

	dst := filepath.Join(t.TempDir(), "data") // stands for a mounted volume: it is never renamed
	put(t, dst, "config.json", `{"old":true}`, 0o600)
	put(t, dst, "memories/user.md", "old notes", 0o600)
	put(t, dst, PendingName, string(arc), 0o600)

	now := func() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) }
	moved, files, err := RestoreInPlace(bytes.NewReader(arc), dst, []string{PendingName}, now)
	if err != nil || files == 0 {
		t.Fatalf("%d %v", files, err)
	}
	if got := read(t, filepath.Join(dst, "config.json")); got != `{"a":1}` {
		t.Fatalf("config not restored: %s", got)
	}
	if got := read(t, filepath.Join(dst, "memories/user.md")); got != "likes tea" {
		t.Fatalf("notes not restored: %s", got)
	}
	if !strings.HasSuffix(moved, ".before-restore-20261121-090000") || read(t, filepath.Join(moved, "config.json")) != `{"old":true}` {
		t.Fatalf("old state not kept aside at %q", moved)
	}
	if _, err := os.Stat(filepath.Join(dst, PendingName)); err != nil {
		t.Fatal("the kept file should still be there")
	}
	if _, err := os.Stat(filepath.Join(dst, ".restore-staging")); !os.IsNotExist(err) {
		t.Fatal("staging was left behind")
	}
	// A backup taken now holds neither the pending archive nor the old state.
	again := entries(t, makeArchive(t, dst))
	for name := range again {
		if strings.HasPrefix(name, ".before-restore") || name == PendingName {
			t.Fatalf("%s was archived", name)
		}
	}
}

func TestRestoreInPlaceChangesNothingForABadArchive(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "data")
	put(t, dst, "config.json", "keep me", 0o600)
	if _, _, err := RestoreInPlace(strings.NewReader("junk"), dst, nil, nil); err == nil {
		t.Fatal("junk accepted")
	}
	if read(t, filepath.Join(dst, "config.json")) != "keep me" {
		t.Fatal("state changed")
	}
	if _, err := os.Stat(filepath.Join(dst, ".restore-staging")); !os.IsNotExist(err) {
		t.Fatal("staging was left behind")
	}
}
