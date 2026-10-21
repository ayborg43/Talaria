package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fenced(t *testing.T) (Local, string, string) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, ".talaria")
	os.MkdirAll(filepath.Join(home, "memories"), 0o755)
	os.WriteFile(filepath.Join(home, "vault.key"), []byte("PASSPHRASE-IN-A-FILE"), 0o600)
	os.WriteFile(filepath.Join(home, "grants.json"), []byte("{}"), 0o600)
	work := filepath.Join(base, "work")
	os.MkdirAll(work, 0o755)
	os.WriteFile(filepath.Join(work, "ok.txt"), []byte("fine"), 0o644)
	return Local{Fence: &Fence{Paths: []string{home, filepath.Join(base, "elsewhere", "key")}}}, home, work
}

func TestFencedPathsCannotBeReadListedOrWritten(t *testing.T) {
	l, home, work := fenced(t)
	ctx := context.Background()
	for _, p := range []string{
		filepath.Join(home, "vault.key"),
		filepath.Join(home, "grants.json"),
		home,
		filepath.Join(home, "memories", "..", "grants.json"),
		filepath.Join(home, "not", "yet", "there"),
		filepath.Join(work, "..", ".talaria", "vault.key"),
	} {
		if _, _, err := l.ReadFile(ctx, p, 100); !errors.Is(err, ErrFenced) {
			t.Errorf("read %s: %v", p, err)
		}
		if err := l.WriteFile(ctx, p, []byte("x")); !errors.Is(err, ErrFenced) {
			t.Errorf("write %s: %v", p, err)
		}
		if _, err := l.ListDir(ctx, p); !errors.Is(err, ErrFenced) {
			t.Errorf("list %s: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, "grants.json")); string(b) != "{}" {
		t.Fatal("a fenced file was changed")
	}
	if _, err := os.Stat(filepath.Join(home, "not")); err == nil {
		t.Fatal("a fenced directory was created")
	}
	// Everything else works as before.
	if b, _, err := l.ReadFile(ctx, filepath.Join(work, "ok.txt"), 100); err != nil || string(b) != "fine" {
		t.Fatalf("%q %v", b, err)
	}
	if err := l.WriteFile(ctx, filepath.Join(work, "new", "f.txt"), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if names, err := l.ListDir(ctx, work); err != nil || len(names) != 2 {
		t.Fatalf("%v %v", names, err)
	}
	if !strings.Contains(l.Fence.Check(filepath.Join(home, "vault.key")).Error(), "terminal") {
		t.Fatal("the refusal says what to do instead")
	}
}

func TestASymlinkIntoAFencedPlaceDoesNotGetThrough(t *testing.T) {
	l, home, work := fenced(t)
	link := filepath.Join(work, "innocent")
	if err := os.Symlink(home, link); err != nil {
		t.Skip("no symlinks here")
	}
	fileLink := filepath.Join(work, "keylink")
	os.Symlink(filepath.Join(home, "vault.key"), fileLink)
	for _, p := range []string{filepath.Join(link, "vault.key"), fileLink, link} {
		if _, _, err := l.ReadFile(context.Background(), p, 10); !errors.Is(err, ErrFenced) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := l.WriteFile(context.Background(), filepath.Join(link, "grants.json"), []byte(`{"x":1}`)); !errors.Is(err, ErrFenced) {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "grants.json")); string(b) != "{}" {
		t.Fatal("written through the link")
	}
}

func TestACaseChangeDoesNotGetThroughOnCaseInsensitiveSystems(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case matters here")
	}
	l, home, _ := fenced(t)
	upper := filepath.Join(filepath.Dir(home), ".TALARIA", "VAULT.KEY")
	if _, _, err := l.ReadFile(context.Background(), upper, 10); !errors.Is(err, ErrFenced) {
		t.Fatalf("%v", err)
	}
}

func TestAPathThatOnlyStartsLikeAFencedOneIsNotFenced(t *testing.T) {
	l, home, _ := fenced(t)
	sibling := home + "-notes"
	os.MkdirAll(sibling, 0o755)
	os.WriteFile(filepath.Join(sibling, "a.txt"), []byte("a"), 0o644)
	if _, _, err := l.ReadFile(context.Background(), filepath.Join(sibling, "a.txt"), 10); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestNoFenceChangesNothing(t *testing.T) {
	var f *Fence
	if f.Check("/etc/passwd") != nil {
		t.Fatal("nil fence")
	}
	if (&Fence{}).Check("/etc/passwd") != nil {
		t.Fatal("empty fence")
	}
	if len(f.Environ()) != len(os.Environ()) {
		t.Fatal("environment")
	}
}

func TestShellIsNotGivenTalariasSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	t.Setenv("TALARIA_VAULT_PASSPHRASE", "open-sesame-phrase")
	t.Setenv("TALARIA_HOME", "/somewhere")
	t.Setenv("MY_PROFILE_KEY", "sk-from-profile")
	t.Setenv("UNRELATED_NOTE", "kept")
	l := Local{Fence: &Fence{EnvDeny: []string{"MY_PROFILE_KEY"}}}
	res, err := l.Shell(context.Background(), `echo "[$TALARIA_VAULT_PASSPHRASE][$MY_PROFILE_KEY][$TALARIA_HOME][$UNRELATED_NOTE]"`, 10*time.Second)
	if err != nil || strings.TrimSpace(res.Output) != "[][][/somewhere][kept]" {
		t.Fatalf("%q %v", res.Output, err)
	}
	// Without a fence nothing changes: this is how the shell behaved before.
	res, _ = Local{}.Shell(context.Background(), `echo "[$MY_PROFILE_KEY]"`, 10*time.Second)
	if strings.TrimSpace(res.Output) != "[sk-from-profile]" {
		t.Fatalf("%q", res.Output)
	}
}

func TestProtectedPathsCoverTalariaAndTheUsualCredentialPlaces(t *testing.T) {
	got := ProtectedPaths("/h/.talaria", "/h/cfg.json", "/k/key", "/Users/me", []string{"~/Documents/taxes", "/abs/secret", ""})
	for _, want := range []string{"/h/.talaria", "/h/cfg.json", "/k/key", "/Users/me/.ssh", "/Users/me/.aws", "/Users/me/.config/gh", "/Users/me/Documents/taxes", "/abs/secret"} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	for _, g := range got {
		if g == "" {
			t.Fatal("empty path")
		}
	}
}
