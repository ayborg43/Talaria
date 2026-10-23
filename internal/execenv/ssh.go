package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sshEnv runs commands on a remote host by running the system ssh program, so
// ~/.ssh/config, keys, the agent and jump hosts all work as they do for you.
// Connections are shared (ControlMaster) so each command does not pay for a
// new handshake. Everything is non-interactive: a password or passphrase
// prompt would hang an agent, so authentication must work without one.
type sshEnv struct {
	posixEnv
	spec    Spec
	base    []string // ssh options common to every command
	control string   // ControlPath, "" when connection sharing is off
	once    sync.Once
}

func openSSH(ctx context.Context, name string, s Spec) (Env, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, fmt.Errorf("environment %q: ssh is not installed or not on PATH", name)
	}
	// ssh quietly ignores a missing key file and tries your default keys instead,
	// which would authenticate as the wrong identity or fail for a confusing reason.
	if s.IdentityFile != "" {
		if _, err := os.Stat(expandHome(s.IdentityFile)); err != nil {
			return nil, fmt.Errorf("environment %q: identity_file %s cannot be read: %v", name, s.IdentityFile, errors.Unwrap(err))
		}
	}
	e := &sshEnv{spec: s}
	e.base = e.options()
	e.posixEnv = posixEnv{t: e, workdir: s.Workdir, desc: e.describe(), closer: e.stop}

	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := e.Shell(pctx, "true", 20*time.Second)
	if err == nil && (res.ExitCode != 0 || res.TimedOut) {
		err = fmt.Errorf("the check command failed (exit status %d): %s", res.ExitCode, strings.TrimSpace(res.Output))
		if res.TimedOut {
			err = fmt.Errorf("the check command timed out: %s", strings.TrimSpace(res.Output))
		}
	}
	if err != nil {
		e.stop()
		return nil, fmt.Errorf("environment %q: cannot run commands on %s over ssh: %w%s", name, s.Host, err, sshHint(res.Output, s.Host))
	}
	fence, err := remoteSetup(pctx, e)
	if err != nil {
		e.stop()
		return nil, fmt.Errorf("environment %q: cannot learn the remote home directory, so the file tools cannot be fenced: %w", name, err)
	}
	e.remote = fence
	return e, nil
}

// setupScript learns what the file fence needs in one round trip: the remote
// home resolved, the remote system, and each usual credential place resolved
// (or MISSING when it is not there). Its output is one line per answer, in
// that order.
var setupScript = resolveFunc + `resolve_one "$HOME"
uname -s
for d in ` + strings.Join(DefaultProtected, " ") + `; do
  if [ -e "$HOME/$d" ] || [ -L "$HOME/$d" ]; then resolve_one "$HOME/$d"; else printf 'MISSING\n'; fi
done
`

// setupLines is how many answers the setup script prints.
var setupLines = 2 + len(DefaultProtected)

// remoteSetup asks the far machine what the file fence needs. Anything
// unexpected refuses the environment rather than running it unfenced.
func remoteSetup(ctx context.Context, e *sshEnv) (*RemoteFence, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := e.run(ctx, setupScript, nil, nil, false, MaxOutput)
	if err != nil {
		return nil, err
	}
	if r.code != 0 || r.truncated {
		return nil, fmt.Errorf("the setup query failed: %s", strings.TrimSpace(string(r.stdout)+string(r.stderr)))
	}
	return parseSetup(string(r.stdout))
}

// parseSetup reads the setup script's answers. A login banner can precede them
// on the stream, so the last lines win; anything that does not look right
// refuses the environment rather than running it unfenced.
func parseSetup(output string) (*RemoteFence, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < setupLines {
		return nil, fmt.Errorf("the setup query returned %d lines, want %d", len(lines), setupLines)
	}
	lines = lines[len(lines)-setupLines:]
	home := strings.TrimSuffix(lines[0], "/")
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, "\x00\r\n\t") {
		return nil, fmt.Errorf("the answer was not a path: %q", lines[0])
	}
	if !validUname(lines[1]) {
		return nil, fmt.Errorf("the answer was not a system name: %q", lines[1])
	}
	resolved := make([]string, len(DefaultProtected))
	for i := range DefaultProtected {
		if line := lines[2+i]; line != "MISSING" {
			if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, "\x00\r\n\t") {
				return nil, fmt.Errorf("the answer was not a path: %q", line)
			}
			resolved[i] = path.Clean(line)
		}
	}
	return &RemoteFence{Home: home, Fold: lines[1] == "Darwin", Resolved: resolved}, nil
}

func validUname(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// options builds the ssh flags. The user's own ssh_args come first, because ssh
// uses the first value it sees for an option, so they can override our defaults.
func (e *sshEnv) options() []string {
	s := e.spec
	opts := slices.Clone(s.SSHArgs)
	opts = append(opts,
		"-T", // no terminal: output must not be mangled and nothing should wait on one
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "LogLevel=ERROR",
	)
	if _, err := os.Stat("/tmp"); err == nil && runtime.GOOS != "windows" {
		// A short path: unix sockets are limited to about 100 characters. %C is a hash of the connection.
		e.control = fmt.Sprintf("/tmp/talaria-ssh-%d-%%C", os.Getpid())
		opts = append(opts, "-o", "ControlMaster=auto", "-o", "ControlPath="+e.control, "-o", "ControlPersist=120")
	}
	if s.Port != 0 {
		opts = append(opts, "-p", strconv.Itoa(s.Port))
	}
	if s.IdentityFile != "" {
		opts = append(opts, "-i", expandHome(s.IdentityFile))
	}
	if s.User != "" {
		opts = append(opts, "-l", s.User)
	}
	return opts
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func (e *sshEnv) command(ctx context.Context, script string, args []string) *exec.Cmd {
	// ssh joins its arguments into one string for the remote login shell, so
	// quote every word: that shell sees exactly `sh -c SCRIPT sh ARG...`.
	remote := "sh -c " + shellQuote(script) + " sh"
	for _, a := range args {
		remote += " " + shellQuote(a)
	}
	argv := append(slices.Clone(e.base), "--", e.spec.Host, remote)
	return exec.CommandContext(ctx, "ssh", argv...)
}

// stop closes the shared connection. Closing twice is harmless.
func (e *sshEnv) stop() {
	if e.control == "" {
		return
	}
	e.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exec.CommandContext(ctx, "ssh", append(slices.Clone(e.base), "-O", "exit", "--", e.spec.Host)...).Run()
	})
}

func (e *sshEnv) describe() string {
	s := e.spec
	target := s.Host
	if s.User != "" {
		target = s.User + "@" + s.Host
	}
	d := "the remote host " + target
	if s.Workdir != "" {
		d += ", working in " + s.Workdir
	}
	return d
}

// sshHint turns ssh's usual failure messages into what to do about them.
func sshHint(output, host string) string {
	switch {
	case strings.Contains(output, "Host key verification failed"):
		return fmt.Sprintf("\nhint: ssh does not know this host yet. Connect once yourself with `ssh %s` and accept its key.", host)
	case strings.Contains(output, "Permission denied"):
		return "\nhint: Talaria cannot answer a password or passphrase prompt. Use a key that is loaded in your ssh agent (ssh-add) or has no passphrase, and set identity_file if it is not a default one."
	case strings.Contains(output, "Could not resolve hostname"):
		return "\nhint: check the host name, or use an alias from ~/.ssh/config."
	}
	return ""
}
