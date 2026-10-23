package execenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dockerEnv runs commands with `docker exec`. Given an image it starts one
// long-lived container, shared by every conversation until Talaria stops, then
// removes it; given a container name it attaches to that one and leaves it alone.
type dockerEnv struct {
	posixEnv
	spec      Spec
	container string
	managed   bool
	once      sync.Once
}

// Containers Talaria starts carry these labels, so ones left behind by a
// Talaria that was killed can be found and removed.
const (
	labelManaged = "talaria.managed=true"
	labelPID     = "talaria.pid"
)

// dockerOut runs a docker subcommand and returns its output. A failure's error
// carries what docker said.
func dockerOut(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	setupKill(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.Join(strings.Fields(errb.String()), " ")
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func openDocker(ctx context.Context, name string, s Spec, getenv func(string) string) (Env, error) {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("environment %q: "+format, append([]any{name}, a...)...)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fail("docker is not installed or not on PATH")
	}
	if _, err := dockerOut(ctx, 20*time.Second, "version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, fail("cannot reach the Docker daemon (is Docker running?): %v", err)
	}
	for _, v := range s.PassEnv {
		if getenv(v) == "" {
			return nil, fail("pass_env lists %s, but it is not set", v)
		}
	}

	d := &dockerEnv{spec: s, managed: s.Image != ""}
	d.posixEnv = posixEnv{t: d, desc: d.describe(), closer: d.stop} // the container's own working directory applies

	if d.managed {
		reapAbandoned(ctx)
		d.container = "talaria-" + randomHex(4)
		if _, err := dockerOut(ctx, 10*time.Minute, d.runArgs()...); err != nil { // the wait allows for pulling the image
			d.stop() // a container that was created but did not start still holds its name
			return nil, fail("cannot start a container from %s: %v", s.Image, err)
		}
	} else {
		d.container = s.Container
		running, err := dockerOut(ctx, 20*time.Second, "inspect", "--format", "{{.State.Running}}", s.Container)
		if s.Container == WorkspaceContainer { // the one Talaria makes: say how to make it
			if err != nil || running != "true" {
				return nil, fail("the workspace is not running: set it up with `tal workspace up`")
			}
		}
		if err != nil {
			return nil, fail("cannot find the container: %v", err)
		}
		if running != "true" {
			return nil, fail("container %q is not running (start it with: docker start %s)", s.Container, s.Container)
		}
	}

	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := d.Shell(pctx, "true", 20*time.Second)
	if err == nil && (res.ExitCode != 0 || res.TimedOut) {
		err = fmt.Errorf("the check command failed (exit status %d): %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	if err != nil {
		d.stop()
		return nil, fail("the container does not run commands (it needs a POSIX sh): %v", err)
	}
	return d, nil
}

// runArgs builds the `docker run` for a managed container.
func (d *dockerEnv) runArgs() []string {
	s := d.spec
	args := []string{
		"run", "-d", "--init", "--name", d.container,
		"--label", labelManaged, "--label", labelPID + "=" + strconv.Itoa(os.Getpid()),
		"--security-opt", "no-new-privileges", "--pids-limit", "1024",
		"--entrypoint", "", // whatever the image runs by default, it is only kept alive here
	}
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	if s.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(s.CPUs, 'f', -1, 64))
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.Workdir != "" {
		args = append(args, "--workdir", s.Workdir) // docker creates it if the image lacks it
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+s.Env[k])
	}
	for _, k := range s.PassEnv {
		args = append(args, "-e", k) // docker copies the value from our environment; it never appears in a command line
	}
	for _, m := range s.Mounts {
		args = append(args, "-v", m)
	}
	args = append(args, s.DockerArgs...)
	return append(args, s.Image, "tail", "-f", "/dev/null")
}

func (d *dockerEnv) command(ctx context.Context, script string, args []string) *exec.Cmd {
	argv := []string{"exec", "-i"}
	if !d.managed { // a container we started already has its workdir and user
		if d.spec.Workdir != "" {
			argv = append(argv, "-w", d.spec.Workdir)
		}
		if d.spec.User != "" {
			argv = append(argv, "-u", d.spec.User)
		}
	}
	argv = append(argv, d.container, "sh", "-c", script, "sh")
	return exec.CommandContext(ctx, "docker", append(argv, args...)...)
}

// stop removes a container we started. One we merely attached to is left running.
// Cleanup runs detached: it must go out even when the request that owned the
// environment is already done, or containers leak.
func (d *dockerEnv) stop() {
	if !d.managed {
		return
	}
	d.once.Do(func() {
		dockerOut(context.Background(), 20*time.Second, "rm", "-f", d.container)
	})
}

// ContainerName is the container commands run in.
func (d *dockerEnv) ContainerName() string { return d.container }

func (d *dockerEnv) describe() string {
	s := d.spec
	if !d.managed {
		desc := fmt.Sprintf("the running Docker container %q", s.Container)
		if s.Workdir != "" {
			desc += ", working in " + s.Workdir
		}
		return desc
	}
	desc := "a Docker container started from the image " + s.Image
	for _, m := range s.Mounts {
		mt, err := parseMount(m)
		if err != nil {
			continue
		}
		mode := "read-write"
		if mt.mode == "ro" {
			mode = "read-only"
		}
		desc += fmt.Sprintf(", with %s shared at %s (%s)", mt.host, mt.container, mode)
	}
	if s.Workdir != "" {
		desc += ", working in " + s.Workdir
	}
	if s.Network == "none" {
		desc += ", with no network access"
	}
	if len(s.Mounts) > 0 {
		return desc + "; files outside the shared directories are lost when Talaria stops"
	}
	return desc + "; its files are lost when Talaria stops"
}

// reapAbandoned removes containers that an earlier Talaria started and never
// cleaned up (because it was killed). Only containers whose recorded process
// is gone are touched. Best effort: any failure is ignored.
func reapAbandoned(ctx context.Context) {
	out, err := dockerOut(ctx, 20*time.Second, "ps", "-a", "--filter", "label="+labelManaged,
		"--format", "{{.Names}}\t{{.Label \""+labelPID+"\"}}")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		name, pidText, ok := strings.Cut(strings.TrimSpace(line), "\t")
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if !ok || name == "" || err != nil || pid <= 0 || pid == os.Getpid() || processAlive(pid) {
			continue
		}
		dockerOut(ctx, 20*time.Second, "rm", "-f", name)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
