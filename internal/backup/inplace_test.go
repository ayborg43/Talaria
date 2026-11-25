package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
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

func tarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := `{"format":1,"created":"2026-11-25T00:00:00Z","files":1}`
	tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifest)), Typeflag: tar.TypeReg})
	tw.Write([]byte(manifest))
	for name, body := range entries {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// A restored file of signed-in browsers would let whoever made the archive in, so it is never taken in, and never saved.
func TestSignInsAreNeverCarriedByABackup(t *testing.T) {
	hostile := tarGz(t, map[string]string{"config.json": "{}", SessionsName: `{"abc":"2099-01-01T00:00:00Z"}`})
	dst := filepath.Join(t.TempDir(), "data")
	put(t, dst, SessionsName, `{"mine":"2030-01-01T00:00:00Z"}`, 0o600)
	if _, _, err := RestoreInPlace(bytes.NewReader(hostile), dst, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, SessionsName)); !os.IsNotExist(err) {
		t.Fatal("the archive's sign-ins were put in place")
	}
	if _, err := os.Stat(filepath.Join(dst, "config.json")); err != nil {
		t.Fatal("the rest of the archive should still restore")
	}
	home := sampleHome(t)
	put(t, home, SessionsName, `{"mine":"2030-01-01T00:00:00Z"}`, 0o600)
	if _, ok := entries(t, makeArchive(t, home))[SessionsName]; ok {
		t.Fatal("a backup holds the sign-ins")
	}
}

func TestAnArchiveOfMillionsOfEmptyEntriesIsRefused(t *testing.T) {
	old := maxEntries
	maxEntries = 200
	defer func() { maxEntries = old }()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := `{"format":1}`
	tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifest)), Typeflag: tar.TypeReg})
	tw.Write([]byte(manifest))
	for i := 0; i <= maxEntries; i++ {
		tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("d/%d", i), Mode: 0o600, Typeflag: tar.TypeReg})
	}
	tw.Close()
	gz.Close()
	if _, _, err := Verify(bytes.NewReader(buf.Bytes())); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("%v", err)
	}
	if _, _, err := RestoreInPlace(bytes.NewReader(buf.Bytes()), filepath.Join(t.TempDir(), "data"), nil, nil); err == nil {
		t.Fatal("restore accepted it")
	}
}

func TestConfigNotesSayWhatASettingsFileStarts(t *testing.T) {
	arc := tarGz(t, map[string]string{"config.json": `{"mcp_servers":{"x":{"command":"/bin/sh"},"remote":{"url":"https://a.example"}},"policy":{"rules":{"shell":"allow"}},"environments":{"box":{"type":"ssh"}}}`})
	notes := strings.Join(ConfigNotes(bytes.NewReader(arc)), " | ")
	for _, want := range []string{`"x": /bin/sh`, "permission rules", `"box"`} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing %q in %q", want, notes)
		}
	}
	if strings.Contains(notes, "remote") {
		t.Errorf("a service reached by address starts no program: %q", notes)
	}
	if n := ConfigNotes(bytes.NewReader(tarGz(t, map[string]string{"config.json": `{"profiles":{}}`}))); len(n) != 0 {
		t.Errorf("an ordinary config should say nothing: %v", n)
	}
	if n := ConfigNotes(strings.NewReader("junk")); n != nil {
		t.Errorf("%v", n)
	}
}
