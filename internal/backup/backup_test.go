package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/memory"
)

func put(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sampleHome(t *testing.T) string {
	home := filepath.Join(t.TempDir(), "home")
	put(t, home, "config.json", `{"a":1}`, 0o644)
	put(t, home, "vault.enc", "ENCRYPTED", 0o600)
	put(t, home, "vault.key", "THE-KEY", 0o600)
	put(t, home, "memories/user.md", "likes tea", 0o644)
	put(t, home, "cron/jobs.json", "{}", 0o600)
	put(t, home, "browser/Default/Cookies", "COOKIES", 0o600)
	put(t, home, "leftover.tmp", "x", 0o600)
	put(t, home, "vault.enc.lock", "", 0o600)
	return home
}

func entries(t *testing.T, archive []byte) map[string]string {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			return out
		}
		var b bytes.Buffer
		b.ReadFrom(tr)
		out[h.Name] = b.String()
	}
}

func TestBackupKeepsStateAndLeavesOutKeysAndBrowserProfile(t *testing.T) {
	home := sampleHome(t)
	var buf bytes.Buffer
	rep, err := Create(home, &buf, Options{Exclude: []string{filepath.Join(home, "vault.key")}})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, buf.Bytes())
	for _, want := range []string{"config.json", "vault.enc", "memories/user.md", "cron/jobs.json", "BACKUP.json"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	for name, content := range got {
		if strings.Contains(name, "vault.key") || strings.Contains(content, "THE-KEY") {
			t.Errorf("the key file is in the backup: %s", name)
		}
		if strings.HasPrefix(name, "browser") || strings.Contains(content, "COOKIES") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".lock") {
			t.Errorf("%s should be left out", name)
		}
	}
	if rep.Files != 4 || len(rep.Skipped) == 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestRestoreRoundTripsAndKeepsEverythingPrivate(t *testing.T) {
	home := sampleHome(t)
	var buf bytes.Buffer
	if _, err := Create(home, &buf, Options{Exclude: []string{filepath.Join(home, "vault.key")}}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "new", ".talaria")
	moved, n, err := Restore(bytes.NewReader(buf.Bytes()), dst, false, nil)
	if err != nil || moved != "" || n != 4 {
		t.Fatalf("%q %d %v", moved, n, err)
	}
	if read(t, filepath.Join(dst, "memories/user.md")) != "likes tea" || read(t, filepath.Join(dst, "vault.enc")) != "ENCRYPTED" {
		t.Fatal("content differs")
	}
	filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		fi, _ := d.Info()
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by others: %v", p, fi.Mode())
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(dst, "BACKUP.json")); err == nil {
		t.Error("the manifest is not state")
	}
}

func TestRestoreNeverOverwritesWithoutBeingTold(t *testing.T) {
	home := sampleHome(t)
	var buf bytes.Buffer
	Create(home, &buf, Options{})
	other := filepath.Join(t.TempDir(), "h")
	put(t, other, "precious.txt", "keep me", 0o600)

	if _, _, err := Restore(bytes.NewReader(buf.Bytes()), other, false, nil); err == nil || !strings.Contains(err.Error(), "-replace") {
		t.Fatalf("%v", err)
	}
	if read(t, filepath.Join(other, "precious.txt")) != "keep me" {
		t.Fatal("touched")
	}
	fixed := func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	moved, _, err := Restore(bytes.NewReader(buf.Bytes()), other, true, fixed)
	if err != nil || !strings.HasSuffix(moved, ".before-restore-20261021-120000") {
		t.Fatalf("%q %v", moved, err)
	}
	if read(t, filepath.Join(moved, "precious.txt")) != "keep me" || read(t, filepath.Join(other, "config.json")) != `{"a":1}` {
		t.Fatal("the old state is kept aside and the new one is in place")
	}
	if _, err := os.Stat(filepath.Join(other, "precious.txt")); err == nil {
		t.Fatal("mixed")
	}
}

func hostile(t *testing.T, build func(tw *tar.Writer)) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	build(tw)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func file(tw *tar.Writer, name, content string) {
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write([]byte(content))
}

func TestRestoreRefusesHostileArchives(t *testing.T) {
	manifest := func(tw *tar.Writer) { file(tw, manifestName, "{}") }
	cases := map[string]func(*tar.Writer){
		"parent traversal": func(tw *tar.Writer) { manifest(tw); file(tw, "../escape.txt", "x") },
		"nested traversal": func(tw *tar.Writer) { manifest(tw); file(tw, "a/../../escape.txt", "x") },
		"absolute":         func(tw *tar.Writer) { manifest(tw); file(tw, "/tmp/escape.txt", "x") },
		"symlink": func(tw *tar.Writer) {
			manifest(tw)
			tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc", Typeflag: tar.TypeSymlink})
		},
		"hard link": func(tw *tar.Writer) {
			manifest(tw)
			tw.WriteHeader(&tar.Header{Name: "link", Linkname: "config.json", Typeflag: tar.TypeLink})
		},
		"device": func(tw *tar.Writer) { manifest(tw); tw.WriteHeader(&tar.Header{Name: "dev", Typeflag: tar.TypeChar}) },
		"duplicate": func(tw *tar.Writer) {
			manifest(tw)
			file(tw, "config.json", "a")
			file(tw, "config.json", "b")
		},
		"no manifest": func(tw *tar.Writer) { file(tw, "config.json", "a") },
	}
	for name, build := range cases {
		parent := t.TempDir()
		home := filepath.Join(parent, "home")
		_, _, err := Restore(bytes.NewReader(hostile(t, build)), home, false, nil)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(home); err == nil {
			t.Errorf("%s: left a home behind", name)
		}
		if _, err := os.Stat(filepath.Join(parent, "escape.txt")); err == nil {
			t.Errorf("%s: escaped", name)
		}
		if left, _ := filepath.Glob(filepath.Join(parent, ".talaria-restore-*")); len(left) != 0 {
			t.Errorf("%s: staging left behind", name)
		}
	}
	if _, _, err := Restore(strings.NewReader("not a gzip"), filepath.Join(t.TempDir(), "h"), false, nil); err == nil || !strings.Contains(err.Error(), "not a Talaria backup") {
		t.Fatalf("%v", err)
	}
}

func TestATruncatedArchiveChangesNothing(t *testing.T) {
	home := sampleHome(t)
	var buf bytes.Buffer
	Create(home, &buf, Options{})
	other := filepath.Join(t.TempDir(), "h")
	put(t, other, "x.txt", "mine", 0o600)
	if _, _, err := Restore(bytes.NewReader(buf.Bytes()[:buf.Len()/2]), other, true, nil); err == nil {
		t.Fatal("accepted")
	}
	if read(t, filepath.Join(other, "x.txt")) != "mine" {
		t.Fatal("damaged")
	}
}

// The history may be in use while it is saved.
func TestHistoryIsSavedConsistentlyWhileBeingWritten(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	os.MkdirAll(home, 0o755)
	s, err := memory.OpenSessions(filepath.Join(home, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Start()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.Add(id, "user", "needle number "+string(rune('a'+i%26)))
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	var buf bytes.Buffer
	if _, err := Create(home, &buf, Options{}); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	s.Close()

	dst := filepath.Join(t.TempDir(), "r")
	if _, _, err := Restore(bytes.NewReader(buf.Bytes()), dst, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sessions.db-wal")); err == nil {
		t.Fatal("a snapshot is one file")
	}
	db, err := sql.Open("sqlite", filepath.Join(dst, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("%q %v", ok, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM messages").Scan(&n); err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestBackupOfAMissingHomeFails(t *testing.T) {
	if _, err := Create(filepath.Join(t.TempDir(), "nope"), &bytes.Buffer{}, Options{}); err == nil {
		t.Fatal("no")
	}
}

// Rows still in the write-ahead log, with the database open, must be in the backup:
// copying sessions.db by itself would lose them.
func TestHistoryInTheWriteAheadLogIsNotLost(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	os.MkdirAll(home, 0o755)
	s, err := memory.OpenSessions(filepath.Join(home, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := s.Start()
	for i := 0; i < 20; i++ {
		s.Add(id, "user", "remember the walrus")
	}
	var buf bytes.Buffer
	if _, err := Create(home, &buf, Options{}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "r")
	if _, _, err := Restore(bytes.NewReader(buf.Bytes()), dst, false, nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dst, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM messages WHERE content LIKE '%walrus%'").Scan(&n); err != nil || n != 20 {
		t.Fatalf("%d rows, want 20: %v", n, err)
	}
}
