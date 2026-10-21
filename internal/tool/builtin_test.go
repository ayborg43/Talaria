package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/execenv"
)

// fakeEnv records what the tools ask of the environment and answers from fields.
type fakeEnv struct {
	remote bool

	mu      sync.Mutex
	calls   []string
	timeout time.Duration
	written map[string][]byte

	shell     execenv.ShellResult
	shellErr  error
	readData  []byte
	readTrunc bool
	readErr   error
	listing   []string
	writeErr  error
}

func (f *fakeEnv) Describe() string { return "the test box" }
func (f *fakeEnv) Remote() bool     { return f.remote }
func (f *fakeEnv) Close()           {}

func (f *fakeEnv) log(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeEnv) Shell(_ context.Context, cmd string, timeout time.Duration) (execenv.ShellResult, error) {
	f.log("shell:" + cmd)
	f.timeout = timeout
	return f.shell, f.shellErr
}

func (f *fakeEnv) ReadFile(_ context.Context, path string, limit int) ([]byte, bool, error) {
	f.log("read:" + path)
	if limit != 256<<10 {
		return nil, false, errors.New("unexpected limit")
	}
	return f.readData, f.readTrunc, f.readErr
}

func (f *fakeEnv) WriteFile(_ context.Context, path string, data []byte) error {
	f.log("write:" + path)
	if f.written == nil {
		f.written = map[string][]byte{}
	}
	f.written[path] = data
	return f.writeErr
}

func (f *fakeEnv) ListDir(_ context.Context, path string) ([]string, error) {
	f.log("list:" + path)
	return f.listing, nil
}

func run(t *testing.T, r *Registry, name, args string) (string, error) {
	t.Helper()
	tl, ok := r.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tl.Run(context.Background(), json.RawMessage(args))
}

func TestToolsActInTheEnvironment(t *testing.T) {
	env := &fakeEnv{readData: []byte("file body"), listing: []string{"a.txt", "sub/"}}
	r := NewRegistry()
	RegisterBuiltinsIn(r, env)

	if out, err := run(t, r, "read_file", `{"path":"/etc/hosts"}`); err != nil || out != "file body" {
		t.Fatalf("%q %v", out, err)
	}
	env.readTrunc = true
	if out, _ := run(t, r, "read_file", `{"path":"/big"}`); out != "file body\n[truncated]" {
		t.Fatalf("a cut-off read says so: %q", out)
	}
	if out, err := run(t, r, "list_dir", `{"path":"/srv"}`); err != nil || out != "a.txt\nsub/\n" {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := run(t, r, "write_file", `{"path":"/srv/x.txt","content":"héllo"}`); err != nil || out != "wrote 6 bytes to /srv/x.txt" || string(env.written["/srv/x.txt"]) != "héllo" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := run(t, r, "shell", `{"command":"ls"}`); err != nil {
		t.Fatal(err)
	}
	if env.timeout != 60*time.Second {
		t.Fatalf("commands get a 60 second limit, got %v", env.timeout)
	}
	if strings.Join(env.calls, ",") != "read:/etc/hosts,read:/big,list:/srv,write:/srv/x.txt,shell:ls" {
		t.Fatalf("calls: %v", env.calls)
	}

	env.readErr, env.writeErr = errors.New("permission denied"), errors.New("disk full")
	if _, err := run(t, r, "read_file", `{"path":"/x"}`); err == nil || err.Error() != "permission denied" {
		t.Fatalf("%v", err)
	}
	if _, err := run(t, r, "write_file", `{"path":"/x","content":"y"}`); err == nil || err.Error() != "disk full" {
		t.Fatalf("%v", err)
	}
	if _, err := run(t, r, "read_file", `not json`); err == nil {
		t.Fatal("bad arguments are an error")
	}
}

func TestShellResultFormatting(t *testing.T) {
	for name, tc := range map[string]struct {
		res  execenv.ShellResult
		want string
	}{
		"clean":     {execenv.ShellResult{Output: "ok\n"}, "ok\n"},
		"exit code": {execenv.ShellResult{Output: "partial\n", ExitCode: 3}, "partial\n\n[exit: exit status 3]"},
		"signal":    {execenv.ShellResult{Output: "x", ExitCode: -1}, "x\n[exit: killed by a signal]"},
		"timeout":   {execenv.ShellResult{Output: "started\n", TimedOut: true, ExitCode: 137}, "started\n\n[timed out after 1m0s]"},
		"truncated": {execenv.ShellResult{Output: "lots", Truncated: true}, "lots\n[truncated]"},
		"both":      {execenv.ShellResult{Output: "lots", Truncated: true, ExitCode: 1}, "lots\n[truncated]\n[exit: exit status 1]"},
	} {
		r := NewRegistry()
		RegisterBuiltinsIn(r, &fakeEnv{shell: tc.res})
		if got, err := run(t, r, "shell", `{"command":"x"}`); err != nil || got != tc.want {
			t.Errorf("%s: got %q (%v), want %q", name, got, err, tc.want)
		}
	}
	r := NewRegistry()
	RegisterBuiltinsIn(r, &fakeEnv{shellErr: errors.New("could not start")})
	if _, err := run(t, r, "shell", `{"command":"x"}`); err == nil {
		t.Fatal("an environment failure is an error the model can see")
	}
}

func TestRemoteEnvironmentsAreNamedInTheToolDescriptions(t *testing.T) {
	local, remote := NewRegistry(), NewRegistry()
	RegisterBuiltinsIn(local, &fakeEnv{})
	RegisterBuiltinsIn(remote, &fakeEnv{remote: true})

	for _, name := range []string{"read_file", "write_file", "list_dir", "shell"} {
		l, _ := local.Get(name)
		r, _ := remote.Get(name)
		if strings.Contains(l.Description, "not the user's computer") {
			t.Errorf("%s: a local environment needs no note: %q", name, l.Description)
		}
		if !strings.Contains(r.Description, "This runs in the test box. That is not the user's computer.") {
			t.Errorf("%s: a remote environment must be named: %q", name, r.Description)
		}
	}
	if ct, _ := remote.Get("current_time"); strings.Contains(ct.Description, "test box") {
		t.Error("current_time is the local clock whatever the environment")
	}
	for name, want := range map[string]bool{"read_file": false, "list_dir": false, "write_file": true, "shell": true, "current_time": false} {
		if tl, _ := remote.Get(name); tl.NeedsApproval != want {
			t.Errorf("%s: NeedsApproval = %v, want %v", name, tl.NeedsApproval, want)
		}
	}
}

// With no environment named, the tools work on this machine, exactly as before.
func TestDefaultRegistrationWorksLocally(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry()
	RegisterBuiltins(r)

	file := filepath.Join(dir, "sub", "note.txt")
	if out, err := run(t, r, "write_file", `{"path":"`+file+`","content":"hello"}`); err != nil || !strings.Contains(out, "wrote 5 bytes") {
		t.Fatalf("%q %v", out, err)
	}
	if b, _ := os.ReadFile(file); string(b) != "hello" {
		t.Fatalf("%q", b)
	}
	if out, err := run(t, r, "read_file", `{"path":"`+file+`"}`); err != nil || out != "hello" {
		t.Fatalf("%q %v", out, err)
	}
	if out, _ := run(t, r, "list_dir", `{"path":"`+dir+`"}`); out != "sub/\n" {
		t.Fatalf("%q", out)
	}
	if out, _ := run(t, r, "shell", `{"command":"echo out; echo err >&2; exit 3"}`); out != "out\nerr\n\n[exit: exit status 3]" {
		t.Fatalf("%q", out)
	}
	if _, err := run(t, r, "read_file", `{"path":"`+filepath.Join(dir, "missing")+`"}`); err == nil {
		t.Fatal("a missing file is an error")
	}
}
