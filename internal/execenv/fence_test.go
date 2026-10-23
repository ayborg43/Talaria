package execenv

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	var r *RemoteFence
	if r.Check("/home/u/.ssh/id_rsa", "") != nil {
		t.Fatal("nil remote fence")
	}
	if (&RemoteFence{}).Check("/home/u/.ssh/id_rsa", "") != nil {
		t.Fatal("remote fence without a home")
	}
}

func TestRemoteFenceKeepsFileToolsOutOfCredentialPlaces(t *testing.T) {
	f := &RemoteFence{Home: "/home/u"}
	for p, ww := range map[string][2]string{
		"~/.ssh/id_rsa":          {"", "fenced"},
		"/home/u/.ssh":           {"", "fenced"},
		"/home/u/.aws/config":    {"", "fenced"},
		"../.ssh/id_rsa":         {"/home/u/app", "fenced"},
		"../../../home/u/.aws/x": {"/srv/app/deep", "fenced"},
		"keys/x":                 {"~/.ssh", "fenced"},
		"/home/u/work/file":      {"", "ok"},
		"~/work/file":            {"", "ok"},
		"file":                   {"/srv/app", "ok"},
		"~/.sshstuff/x":          {"", "ok"},
		"/home/u/.ssh-backup/x":  {"", "ok"},
		"~":                      {"", "ok"},
		"/etc/passwd":            {"", "ok"},
	} {
		workdir, want := ww[0], ww[1]
		err := f.Check(p, workdir)
		if (err != nil) != (want == "fenced") {
			t.Errorf("%q (workdir %q): err=%v, want %s", p, workdir, err, want)
		} else if err != nil && !errors.Is(err, ErrFenced) {
			t.Errorf("%q: %v is not ErrFenced", p, err)
		}
	}
}

// The fence runs before anything is sent to the far machine: a transport that
// would fail the test if used proves it.
func TestRemoteFileOpsAreFencedBeforeTheyRun(t *testing.T) {
	e := &posixEnv{t: failTransport{}, workdir: "/srv/app", remote: &RemoteFence{Home: "/home/u"}}
	ctx := context.Background()
	if _, _, err := e.ReadFile(ctx, "~/.ssh/id_rsa", 100); !errors.Is(err, ErrFenced) {
		t.Fatalf("read: %v", err)
	}
	if err := e.WriteFile(ctx, "/home/u/.aws/config", []byte("x")); !errors.Is(err, ErrFenced) {
		t.Fatalf("write: %v", err)
	}
	if _, err := e.ListDir(ctx, "~/.gnupg"); !errors.Is(err, ErrFenced) {
		t.Fatalf("list: %v", err)
	}
}

type failTransport struct{}

func (failTransport) command(ctx context.Context, script string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", "exit 99")
}

// A link on the far machine that points into a credential place does not get
// through, while an innocent link still works.
func TestRemoteSymlinkIntoACredentialPlaceDoesNotGetThrough(t *testing.T) {
	f := installFakes(t)
	sshDir := filepath.Join(f.sshHome, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sshHome, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sshDir, filepath.Join(f.sshHome, "keys")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".ssh", filepath.Join(f.sshHome, "more-keys")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("notes.txt", filepath.Join(f.sshHome, "alias.txt")); err != nil {
		t.Fatal(err)
	}

	env, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ctx := context.Background()
	for _, p := range []string{"~/keys/id_rsa", "~/more-keys/id_rsa", "~/keys/../keys/id_rsa"} {
		if _, _, err := env.ReadFile(ctx, p, 100); !errors.Is(err, ErrFenced) {
			t.Errorf("read %s: %v, want ErrFenced", p, err)
		}
	}
	if err := env.WriteFile(ctx, "~/keys/evil", []byte("x")); !errors.Is(err, ErrFenced) {
		t.Errorf("write: %v, want ErrFenced", err)
	}
	if _, err := env.ListDir(ctx, "~/keys"); !errors.Is(err, ErrFenced) {
		t.Errorf("list: %v, want ErrFenced", err)
	}
	if data, _, err := env.ReadFile(ctx, "~/alias.txt", 100); err != nil || string(data) != "hello" {
		t.Errorf("an innocent link should work: %q %v", data, err)
	}
}

// A credential place that is itself behind a link is still fenced, by the
// resolved form learned when the environment opened.
func TestRemoteCredentialPlaceBehindASymlinkIsStillFenced(t *testing.T) {
	f := installFakes(t)
	real := filepath.Join(f.sshHome, "realkeys")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "id_rsa"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(f.sshHome, ".ssh")); err != nil {
		t.Fatal(err)
	}

	env, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	fence := env.(*sshEnv).remote
	if DefaultProtected[0] != ".ssh" || fence.Resolved[0] == "" {
		t.Fatalf("the setup should resolve .ssh: %+v", fence)
	}
	ctx := context.Background()
	if _, _, err := env.ReadFile(ctx, "~/realkeys/id_rsa", 100); !errors.Is(err, ErrFenced) {
		t.Errorf("by the real path: %v, want ErrFenced", err)
	}
	if _, _, err := env.ReadFile(ctx, "~/.ssh/id_rsa", 100); !errors.Is(err, ErrFenced) {
		t.Errorf("through the link: %v, want ErrFenced", err)
	}
}

// When the far side cannot be asked, file operations are refused rather than
// run unchecked.
func TestRemoteResolveFailureFailsClosed(t *testing.T) {
	e := &posixEnv{t: failTransport{}, workdir: "/srv/app", remote: &RemoteFence{Home: "/home/u"}}
	ctx := context.Background()
	if _, _, err := e.ReadFile(ctx, "file", 100); !errors.Is(err, ErrFenced) {
		t.Errorf("read: %v, want ErrFenced", err)
	}
	if err := e.WriteFile(ctx, "file", []byte("x")); !errors.Is(err, ErrFenced) {
		t.Errorf("write: %v, want ErrFenced", err)
	}
	if _, err := e.ListDir(ctx, "dir"); !errors.Is(err, ErrFenced) {
		t.Errorf("list: %v, want ErrFenced", err)
	}
}

func TestRemoteFoldMatchesCaseInsensitively(t *testing.T) {
	folded := &RemoteFence{Home: "/home/u", Fold: true}
	for _, p := range []string{"~/.SSH/id_rsa", "/home/u/.Ssh", "~/.AWS/config"} {
		if err := folded.Check(p, ""); !errors.Is(err, ErrFenced) {
			t.Errorf("%s: %v, want ErrFenced", p, err)
		}
	}
	if err := folded.CheckAbs("/home/u/.SSH/id_rsa", "~/.SSH/id_rsa"); !errors.Is(err, ErrFenced) {
		t.Errorf("CheckAbs: %v, want ErrFenced", err)
	}
	if err := (&RemoteFence{Home: "/home/u"}).Check("~/.SSH/id_rsa", ""); err != nil {
		t.Errorf("without Fold matching is exact: %v", err)
	}
}

func TestParseSetup(t *testing.T) {
	answers := func(home, uname string, first string) string {
		lines := []string{home, uname}
		for i := range DefaultProtected {
			if i == 0 {
				lines = append(lines, first)
			} else {
				lines = append(lines, "MISSING")
			}
		}
		return strings.Join(lines, "\n") + "\n"
	}
	fence, err := parseSetup("Welcome to the machine!\n" + answers("/home/u", "Linux", "/home/u/.ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if fence.Home != "/home/u" || fence.Fold || len(fence.Resolved) != len(DefaultProtected) || fence.Resolved[0] != "/home/u/.ssh" {
		t.Fatalf("%+v", fence)
	}
	for _, r := range fence.Resolved[1:] {
		if r != "" {
			t.Fatalf("absent places resolve to nothing: %+v", fence)
		}
	}
	if fence, err := parseSetup(answers("/Users/u", "Darwin", "MISSING")); err != nil || !fence.Fold {
		t.Fatalf("a Mac folds case: %+v %v", fence, err)
	}
	for name, out := range map[string]string{
		"short":        "only\ntwo\n",
		"relative":     answers("home/u", "Linux", "MISSING"),
		"system":       answers("/home/u", "a b", "MISSING"),
		"resolved":     answers("/home/u", "Linux", "nope"),
		"control char": answers("/home/u", "Linux\x01", "MISSING"),
	} {
		if _, err := parseSetup(out); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The resolver follows absolute and relative links, .. after a link, and
// links to files, and refuses a loop.
func TestResolveScriptFollowsLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a POSIX shell")
	}
	base := t.TempDir()
	phys, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "sub", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"abs":      real,
		"rel":      "real",
		"dangling": "nowhere/x",
		"loopa":    "loopb",
		"loopb":    "loopa",
	} {
		if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("sub/f", filepath.Join(real, "inner")); err != nil {
		t.Fatal(err)
	}
	run := func(workdir, p string) (string, int) {
		cmd := exec.Command("/bin/sh", "-c", resolveScript, "sh", workdir, p)
		cmd.Env = append(os.Environ(), "HOME="+base)
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return string(out), ee.ExitCode()
			}
			t.Fatal(err)
		}
		return string(out), 0
	}
	for _, tc := range []struct{ workdir, in, want string }{
		{base, "real/sub/f", filepath.Join(phys, "real", "sub", "f")},
		{base, "abs/sub/f", filepath.Join(phys, "real", "sub", "f")},
		{base, "rel/sub/f", filepath.Join(phys, "real", "sub", "f")},
		{base, "real/inner", filepath.Join(phys, "real", "sub", "f")},
		{base, "abs/../rel/sub/f", filepath.Join(phys, "real", "sub", "f")},
		{base, "./rel//sub/f", filepath.Join(phys, "real", "sub", "f")},
		{base, "~/rel/sub/f", filepath.Join(phys, "real", "sub", "f")},
		{base, "dangling", filepath.Join(phys, "nowhere", "x")},
		{real, "sub/f", filepath.Join(phys, "real", "sub", "f")},
	} {
		got, code := run(tc.workdir, tc.in)
		if code != 0 || got != tc.want+"\n" {
			t.Errorf("%s: %q (code %d), want %q", tc.in, got, code, tc.want)
		}
	}
	if _, code := run(base, "loopa/x"); code != 5 {
		t.Errorf("a link loop: code %d, want 5", code)
	}
}

// Without readlink the path is printed as written, for the lexical check.
func TestResolveScriptWithoutReadlinkPrintsThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a POSIX shell")
	}
	base := t.TempDir()
	run := func(p string) string {
		cmd := exec.Command("/bin/sh", "-c", resolveScript, "sh", base, p)
		cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + base}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run("~/x"); got != filepath.Join(base, "x")+"\n" {
		t.Errorf("%q", got)
	}
	if got := run("sub/f"); got != filepath.Join(base, "sub", "f")+"\n" {
		t.Errorf("%q", got)
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
