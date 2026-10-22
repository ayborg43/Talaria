package execenv

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fastGrace(t *testing.T) {
	old := clientGrace
	clientGrace = 300 * time.Millisecond
	t.Cleanup(func() { clientGrace = old })
}

func TestLocalConformance(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs sh")
	}
	conformance(t, Local{}, t.TempDir())
}

func TestSSHConformanceAndArguments(t *testing.T) {
	fastGrace(t)
	f := installFakes(t)
	key := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(key, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), "box", Spec{
		Type: "ssh", Host: "build.example.com", User: "ci", Port: 2222, IdentityFile: key,
		SSHArgs: []string{"-J", "bastion.example.com"}, Workdir: f.sshHome,
	}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()

	if !env.Remote() {
		t.Error("an ssh environment is remote")
	}
	describeAll(t, env, "ci@build.example.com", f.sshHome)
	conformance(t, env, f.sshHome+"/work")

	// What ssh was asked to do.
	all := calls(t, f.sshLog)
	if len(all) == 0 {
		t.Fatal("ssh was never run")
	}
	first := all[0]
	for _, want := range [][]string{
		{"-J", "bastion.example.com"}, {"-T"}, {"-o", "BatchMode=yes"}, {"-o", "ConnectTimeout=15"},
		{"-o", "ControlMaster=auto"}, {"-p", "2222"}, {"-i", key}, {"-l", "ci"},
	} {
		if !hasArgs(first, want...) {
			t.Errorf("ssh should have been given %v: %q", want, first)
		}
	}
	if !hasArgs(first, "--", "build.example.com") {
		t.Errorf("the host must follow -- so it can never be read as an option: %q", first)
	}
	// The user's own options come before ours, so they take precedence.
	if idx(first, "-J") > idx(first, "BatchMode=yes") {
		t.Errorf("ssh_args must come first: %q", first)
	}
	// The remote side is given one string that starts the shell with the script.
	remote := first[len(first)-1]
	if !strings.HasPrefix(remote, "sh -c '") || !strings.Contains(remote, " sh '") {
		t.Errorf("the remote command: %q", remote)
	}
}

func idx(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

func TestSSHClosesTheSharedConnection(t *testing.T) {
	f := installFakes(t)
	env, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	env.Close()
	var exited bool
	for _, c := range calls(t, f.sshLog) {
		if hasArgs(c, "-O", "exit") && hasArgs(c, "--", "h") {
			exited = true
		}
	}
	if !exited {
		t.Fatalf("closing should tell the connection master to exit: %q", calls(t, f.sshLog))
	}
}

func TestSSHStartupFailuresExplainThemselves(t *testing.T) {
	installFakes(t)
	for message, want := range map[string]string{
		"Host key verification failed.":                   "Connect once yourself with `ssh h`",
		"ci@h: Permission denied (publickey).":            "ssh-add",
		"ssh: Could not resolve hostname h: no such host": "~/.ssh/config",
		"Connection refused":                              "cannot run commands on h over ssh",
	} {
		t.Setenv("FAKE_SSH_FAIL", message)
		_, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h"}, os.Getenv)
		if err == nil || !strings.Contains(err.Error(), message) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want it to include the message and %q", message, err, want)
		}
	}

	// A working directory that does not exist is caught up front too.
	t.Setenv("FAKE_SSH_FAIL", "")
	_, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h", Workdir: "/talaria/not/a/dir"}, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "exit status 125") {
		t.Fatalf("got %v", err)
	}
}

func TestSSHWorkdirAndTilde(t *testing.T) {
	f := installFakes(t)
	if err := os.MkdirAll(filepath.Join(f.sshHome, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h", Workdir: "~/proj"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	ctx := context.Background()

	if res, _ := env.Shell(ctx, "pwd", 10*time.Second); strings.TrimSpace(res.Output) != filepath.Join(f.sshHome, "proj") {
		t.Fatalf("~ in the workdir means the remote home: %q", res.Output)
	}
	// Relative paths start in the workdir; ~/ means home.
	env.WriteFile(ctx, "rel.txt", []byte("relative"))
	env.WriteFile(ctx, "~/home.txt", []byte("home"))
	for path, want := range map[string]string{filepath.Join(f.sshHome, "proj", "rel.txt"): "relative", filepath.Join(f.sshHome, "home.txt"): "home"} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", path, b, err)
		}
	}
	if got, _, err := env.ReadFile(ctx, "~/home.txt", 100); err != nil || string(got) != "home" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := env.ListDir(ctx, "~"); err != nil || !strings.Contains(strings.Join(got, ","), "proj/") {
		t.Fatalf("%q %v", got, err)
	}
}

// The quoting that carries every command across ssh must hand the far shell back
// exactly the bytes that went in, whatever they contain.
func TestShellQuoteRoundTripsThroughARealShell(t *testing.T) {
	installFakes(t) // skips where there is no sh
	for _, s := range []string{
		"", "plain", "with space", "it's", "''", "'a'b'", `"double"`, "$HOME", "`date`", `a\b`, `\`, "\\'", "new\nline",
		"tab\there", "unicode ✓ é", "*?[]{}", "~", "; rm -rf /", "$(id)", "-n", "--", "!history", "a&b|c>d<e",
	} {
		out, err := execShell("printf %s " + shellQuote(s))
		if err != nil || out != s {
			t.Errorf("shellQuote(%q) came back as %q (%v)", s, out, err)
		}
	}
}

func execShell(script string) (string, error) {
	res, err := Local{}.Shell(context.Background(), script, 10*time.Second)
	return res.Output, err
}

func TestDockerManagedConformanceAndRunArguments(t *testing.T) {
	fastGrace(t)
	f := installFakes(t)
	host := t.TempDir()
	t.Setenv("MY_TOKEN", "s3cret")
	work := t.TempDir() // "/work" inside the fake container is just a directory here
	env, err := Open(context.Background(), "sandbox", Spec{
		Type: "docker", Image: "python:3.12-slim", Workdir: work, Network: "none", Memory: "512m", CPUs: 1.5,
		User: "1000", Mounts: []string{host + ":/data:ro", "cache:/cache"},
		Env: map[string]string{"B": "2", "A": "1"}, PassEnv: []string{"MY_TOKEN"}, DockerArgs: []string{"--cap-drop", "ALL"},
	}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	name := env.(*dockerEnv).container
	if !strings.HasPrefix(name, "talaria-") {
		t.Fatalf("container name %q", name)
	}

	runs := f.dockerCalls(t, "run")
	if len(runs) != 1 {
		t.Fatalf("one container should be started, saw %d docker run calls", len(runs))
	}
	run := runs[0]
	for _, want := range [][]string{
		{"run", "-d", "--init"}, {"--name", name}, {"--label", "talaria.managed=true"}, {"--label", "talaria.pid=" + strconv.Itoa(os.Getpid())},
		{"--security-opt", "no-new-privileges"}, {"--pids-limit", "1024"}, {"--entrypoint", ""},
		{"--network", "none"}, {"--memory", "512m"}, {"--cpus", "1.5"}, {"--user", "1000"}, {"--workdir", work},
		{"-e", "A=1", "-e", "B=2"}, // sorted, so the command line is stable
		{"-e", "MY_TOKEN"},         // by name: the value is never put on a command line
		{"-v", host + ":/data:ro", "-v", "cache:/cache"}, {"--cap-drop", "ALL"},
		{"python:3.12-slim", "tail", "-f", "/dev/null"},
	} {
		if !hasArgs(run, want...) {
			t.Errorf("docker run should include %v:\n%q", want, run)
		}
	}
	if strings.Contains(strings.Join(run, " "), "s3cret") {
		t.Error("a secret must never appear in a docker command line")
	}
	if idx(run, "--cap-drop") > idx(run, "python:3.12-slim") {
		t.Errorf("docker_args are options for docker run, so they go before the image: %q", run)
	}

	describeAll(t, env, "python:3.12-slim", host, "/data", "read-only", "no network access", work)
	conformance(t, env, work+"/conf")

	// Commands go to this container, one exec each.
	for _, c := range f.dockerCalls(t, "exec") {
		if !hasArgs(c, "exec", "-i", name, "sh", "-c") {
			t.Fatalf("an exec call: %q", c)
		}
	}

	env.Close()
	env.Close() // closing twice is harmless
	removed := f.dockerCalls(t, "rm")
	if len(removed) != 1 || !hasArgs(removed[0], "rm", "-f", name) {
		t.Fatalf("the container Talaria started must be removed exactly once: %q", removed)
	}
	if _, err := os.Stat(filepath.Join(f.state, name)); err == nil {
		t.Fatal("the container still exists")
	}
}

func TestDockerAttachLeavesTheContainerAlone(t *testing.T) {
	fastGrace(t)
	f := installFakes(t)
	t.Setenv("FAKE_DOCKER_ATTACH", "dev")
	work := t.TempDir()
	env, err := Open(context.Background(), "dev", Spec{Type: "docker", Container: "dev", Workdir: work, User: "bob"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, env, work+"/conf")
	describeAll(t, env, `"dev"`, work)

	if len(f.dockerCalls(t, "run")) != 0 {
		t.Fatal("attaching must not start anything")
	}
	exec := f.dockerCalls(t, "exec")[0]
	if !hasArgs(exec, "-w", work) || !hasArgs(exec, "-u", "bob") || !hasArgs(exec, "dev", "sh", "-c") {
		t.Fatalf("an attached container gets the workdir and user per exec: %q", exec)
	}
	env.Close()
	if len(f.dockerCalls(t, "rm")) != 0 {
		t.Fatal("closing must not remove a container Talaria did not start")
	}
}

func TestDockerFailures(t *testing.T) {
	f := installFakes(t)
	ctx := context.Background()
	managed := Spec{Type: "docker", Image: "img:1"}

	t.Setenv("FAKE_DOCKER_DOWN", "1")
	if _, err := Open(ctx, "x", managed, os.Getenv); err == nil || !strings.Contains(err.Error(), "cannot reach the Docker daemon") || !strings.Contains(err.Error(), "Is the docker daemon running?") {
		t.Fatalf("got %v", err)
	}
	t.Setenv("FAKE_DOCKER_DOWN", "")

	t.Setenv("FAKE_DOCKER_RUN_FAIL", "docker: Error response from daemon: pull access denied for img, repository does not exist")
	_, err := Open(ctx, "x", managed, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "cannot start a container from img:1") || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("got %v", err)
	}
	if len(f.dockerCalls(t, "rm")) != 1 {
		t.Fatal("a container that failed to start may still hold its name, so it should be removed")
	}
	t.Setenv("FAKE_DOCKER_RUN_FAIL", "")

	if _, err := Open(ctx, "x", Spec{Type: "docker", Container: "ghost"}, os.Getenv); err == nil || !strings.Contains(err.Error(), "cannot find the container") {
		t.Fatalf("got %v", err)
	}
	t.Setenv("FAKE_DOCKER_ATTACH", "ghost")
	t.Setenv("FAKE_DOCKER_STOPPED", "ghost")
	if _, err := Open(ctx, "x", Spec{Type: "docker", Container: "ghost"}, os.Getenv); err == nil || !strings.Contains(err.Error(), "is not running") || !strings.Contains(err.Error(), "docker start ghost") {
		t.Fatalf("got %v", err)
	}
	if _, err := Open(ctx, "x", Spec{Type: "docker", Image: "img", PassEnv: []string{"TALARIA_SURELY_UNSET"}}, os.Getenv); err == nil || !strings.Contains(err.Error(), "TALARIA_SURELY_UNSET") {
		t.Fatalf("a pass_env variable that is not set is a mistake worth catching: %v", err)
	}
	if _, err := Open(ctx, "x", Spec{Type: "docker"}, os.Getenv); err == nil {
		t.Fatal("an invalid spec is rejected before docker is touched")
	}
}

func TestMissingProgramsAreExplained(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // neither docker nor ssh can be found
	if _, err := Open(context.Background(), "d", Spec{Type: "docker", Image: "x"}, os.Getenv); err == nil || !strings.Contains(err.Error(), "docker is not installed") {
		t.Fatalf("got %v", err)
	}
	if _, err := Open(context.Background(), "s", Spec{Type: "ssh", Host: "h"}, os.Getenv); err == nil || !strings.Contains(err.Error(), "ssh is not installed") {
		t.Fatalf("got %v", err)
	}
}

func TestAbandonedContainersAreReaped(t *testing.T) {
	f := installFakes(t)
	// A container left by a Talaria process that has since died, one belonging to a live process
	// (this test), and one that is not Talaria's at all.
	dead := exitedPID(t)
	for name, pid := range map[string]int{"talaria-dead1": dead, "talaria-alive": os.Getppid(), "talaria-self": os.Getpid()} {
		os.WriteFile(filepath.Join(f.state, name), []byte(strconv.Itoa(pid)+"\n"), 0o644)
	}

	env, err := Open(context.Background(), "x", Spec{Type: "docker", Image: "img"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()

	if _, err := os.Stat(filepath.Join(f.state, "talaria-dead1")); err == nil {
		t.Error("a container whose Talaria process is gone should be removed")
	}
	for _, keep := range []string{"talaria-alive", "talaria-self"} {
		if _, err := os.Stat(filepath.Join(f.state, keep)); err != nil {
			t.Errorf("%s belongs to a live process and must be left alone", keep)
		}
	}
}

// exitedPID returns the pid of a process that has finished.
func exitedPID(t *testing.T) int {
	cmd, err := Local{}.Shell(context.Background(), "echo $$", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(cmd.Output))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// A command's helpers and background jobs must die with it. Killing only the
// shell would leave them running, and holding the output open.
func TestTimeoutKillsBackgroundJobsToo(t *testing.T) {
	fastGrace(t)
	f := installFakes(t)
	t.Setenv("FAKE_DOCKER_ATTACH", "box")
	work := t.TempDir()

	dock, err := Open(context.Background(), "d", Spec{Type: "docker", Container: "box", Workdir: work}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer dock.Close()
	ssh, err := Open(context.Background(), "s", Spec{Type: "ssh", Host: "h", Workdir: f.sshHome}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer ssh.Close()

	for name, env := range map[string]Env{"local": Local{}, "docker": dock, "ssh": ssh} {
		start := time.Now()
		res, err := env.Shell(context.Background(), "sleep 30 & echo $!; wait", 500*time.Millisecond)
		if err != nil || !res.TimedOut {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		if took := time.Since(start); took > 1800*time.Millisecond {
			t.Errorf("%s: returned after %v: the job still held the output open", name, took)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(res.Output))
		if err != nil {
			t.Fatalf("%s: expected the background job's pid, got %q", name, res.Output)
		}
		deadline := time.Now().Add(3 * time.Second)
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if processAlive(pid) {
			syscallKill(pid)
			t.Errorf("%s: the background job (pid %d) outlived its command", name, pid)
		}
	}
}

// The real ssh program, asked only to print the configuration it would use
// (-G connects to nothing), must accept every option we generate and read
// each one as intended, and let the user's own ssh_args take precedence.
func TestTheRealSSHClientReadsOurOptions(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	effective := func(s Spec) map[string]string {
		t.Helper()
		e := &sshEnv{spec: s}
		args := append(append([]string{"-G"}, e.options()...), "--", s.Host)
		out, err := exec.Command("ssh", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh rejected our options: %v\n%s\n%q", err, out, args)
		}
		cfg := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(line, " "); ok {
				if k == "identityfile" { // listed once per candidate key: ours and the defaults
					v = cfg[k] + " " + v
				}
				cfg[k] = v
			}
		}
		return cfg
	}

	key := filepath.Join(t.TempDir(), "id_test") // ssh ignores a key file that does not exist
	if err := os.WriteFile(key, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := effective(Spec{Type: "ssh", Host: "build.example.com", User: "ci", Port: 2222, IdentityFile: key, SSHArgs: []string{"-J", "bastion.example.com"}})
	for key, want := range map[string]string{
		"hostname": "build.example.com", "user": "ci", "port": "2222", "batchmode": "yes", "connecttimeout": "15",
		"serveraliveinterval": "15", "serveralivecountmax": "3", "loglevel": "ERROR", "proxyjump": "bastion.example.com", "requesttty": "false",
	} {
		if cfg[key] != want {
			t.Errorf("ssh reads %s as %q, want %q", key, cfg[key], want)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(cfg["identityfile"]), key) {
		t.Errorf("our key should be the first one tried: %q", cfg["identityfile"])
	}
	if runtime.GOOS != "windows" && (cfg["controlmaster"] != "auto" || !strings.HasPrefix(cfg["controlpath"], "/tmp/talaria-ssh-") || cfg["controlpersist"] != "120") {
		t.Errorf("connection sharing: master=%q path=%q persist=%q", cfg["controlmaster"], cfg["controlpath"], cfg["controlpersist"])
	}

	// Options the user gives come first, so ssh uses theirs.
	cfg = effective(Spec{Type: "ssh", Host: "h", SSHArgs: []string{"-o", "ServerAliveInterval=99", "-o", "ConnectTimeout=3"}})
	if cfg["serveraliveinterval"] != "99" || cfg["connecttimeout"] != "3" || cfg["batchmode"] != "yes" {
		t.Errorf("the user's ssh_args should win over our defaults while the rest stay: %v", cfg)
	}
}

func TestSSHIdentityFileMustExist(t *testing.T) {
	installFakes(t)
	_, err := Open(context.Background(), "box", Spec{Type: "ssh", Host: "h", IdentityFile: "/talaria/no/such/key"}, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "identity_file /talaria/no/such/key cannot be read") {
		t.Fatalf("a mistyped key path is an error, not a silent fallback to other keys: %v", err)
	}
}

// A process that explains itself on standard error and exits: asked for the explanation the moment its
// output ends, the caller must get it every time, not only when the copy happens to have finished.
func TestStderrIsCompleteRightAfterTheOutputEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	// The output ends at once (stdout is closed), and the explanation comes a moment later, then the process exits.
	// That is the worst case of what happens by chance when a program says why it is stopping.
	p, err := Local{}.Start(context.Background(), `exec 1>&-; sleep 0.3; echo "the browser was not found" >&2; exit 127`)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	io.Copy(io.Discard, p.Out)
	if got := p.Stderr(); got != "the browser was not found" {
		t.Fatalf("the reason the process gave was lost: %q", got)
	}
	// And the ordinary case, many times over.
	for i := 0; i < 100; i++ {
		q, err := Local{}.Start(context.Background(), `echo "gone" >&2; exit 1`)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, q.Out)
		if got := q.Stderr(); got != "gone" {
			t.Fatalf("round %d: %q", i, got)
		}
		q.Close()
	}
	// One that is still running is answered at once with what it has said so far.
	r, err := Local{}.Start(context.Background(), `echo early >&2; sleep 30`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if got := r.Stderr(); got != "early" || time.Since(start) > 3*time.Second {
		t.Fatalf("%q after %s", got, time.Since(start))
	}
}
