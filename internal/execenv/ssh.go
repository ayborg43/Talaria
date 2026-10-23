package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	home, err := remoteHome(pctx, e)
	if err != nil {
		e.stop()
		return nil, fmt.Errorf("environment %q: cannot learn the remote home directory, so the file tools cannot be fenced: %w", name, err)
	}
	e.remote = &RemoteFence{Home: home}
	return e, nil
}

// remoteHome asks the far machine where its user lives, for the file fence. A
// login banner can precede the answer on the shared stream, so the last line
// wins; anything that does not look like an absolute path refuses the
// environment rather than running it unfenced.
func remoteHome(ctx context.Context, e *sshEnv) (string, error) {
	res, err := e.Shell(ctx, "printf '%s' \"$HOME\"", 20*time.Second)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 || res.TimedOut {
		return "", fmt.Errorf("the home query failed: %s", strings.TrimSpace(res.Output))
	}
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	home := strings.TrimSuffix(lines[len(lines)-1], "/")
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, "\x00\r\n\t") {
		return "", fmt.Errorf("the answer was not a path: %q", res.Output)
	}
	return home, nil
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
