// Package workspace sets up the agent's persistent computer: a Docker container
// with a browser and a few tools, whose /workspace folder lives in a volume so
// that files, logins and browser profiles survive restarts of the container, of
// Talaria and of the machine.
package workspace

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"talaria/internal/execenv"
)

//go:embed Dockerfile
var dockerfile string

// Version identifies the image's contents. A container made from an older image
// is told apart by it, so `tal workspace up` can say it needs recreating.
const Version = "1"

const (
	Image     = "talaria-workspace:" + Version
	Container = execenv.WorkspaceContainer
	Volume    = "talaria-workspace"
	Dir       = execenv.WorkspaceDir

	labelKey = "talaria.workspace"
)

// Options are the container's resource limits and network.
type Options struct {
	Network string  // a Docker network name; "" is Docker's default bridge
	Memory  string  // e.g. "2g"; "" is 2g
	CPUs    float64 // 0 is no limit
}

// Runner runs a docker command, giving it stdin, and returns its output.
type Runner func(ctx context.Context, stdin io.Reader, args ...string) (string, error)

// Manager creates, starts, stops and removes the workspace.
type Manager struct {
	Run Runner       // nil runs the real docker
	Log func(string) // progress messages; may be nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.Log != nil {
		m.Log(fmt.Sprintf(format, args...))
	}
}

func (m *Manager) docker(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if m.Run != nil {
		return m.Run(ctx, stdin, args...)
	}
	return runDocker(ctx, stdin, args...)
}

func runDocker(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("docker is not installed or not on PATH")
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.Join(strings.Fields(errb.String()), " ")
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() != nil {
			msg = ctx.Err().Error()
		}
		return out.String(), errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Status is what exists.
type Status struct {
	DockerOK      bool
	ImagePresent  bool
	Exists        bool // the container
	Running       bool
	Image         string // what the container was made from
	VolumePresent bool
}

// Status looks at Docker.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	var s Status
	if _, err := m.docker(ctx, nil, "version", "--format", "{{.Server.Version}}"); err != nil {
		return s, fmt.Errorf("cannot reach the Docker daemon (is Docker running?): %w", err)
	}
	s.DockerOK = true
	if _, err := m.docker(ctx, nil, "image", "inspect", "--format", "{{.Id}}", Image); err == nil {
		s.ImagePresent = true
	}
	if _, err := m.docker(ctx, nil, "volume", "inspect", "--format", "{{.Name}}", Volume); err == nil {
		s.VolumePresent = true
	}
	out, err := m.docker(ctx, nil, "inspect", "--format", "{{.State.Running}} {{.Config.Image}}", Container)
	if err == nil {
		s.Exists = true
		running, image, _ := strings.Cut(out, " ")
		s.Running, s.Image = running == "true", image
	}
	return s, nil
}

// Up makes the workspace ready: builds the image if it is missing, creates the
// container if there is none, and starts it if it is stopped. Running it again
// changes nothing.
func (m *Manager) Up(ctx context.Context, o Options) error {
	s, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if !s.ImagePresent {
		m.logf("building the workspace image (the first time takes a few minutes)...")
		if _, err := m.docker(ctx, strings.NewReader(dockerfile), "build", "-t", Image, "-"); err != nil {
			return fmt.Errorf("cannot build the image: %w", err)
		}
	}
	if s.Exists && s.Image != Image {
		return fmt.Errorf("the workspace container was made from %s, but this Talaria wants %s: run `tal workspace remove`, then `tal workspace up`; your files are kept", s.Image, Image)
	}
	switch {
	case !s.Exists:
		m.logf("creating the workspace container...")
		if _, err := m.docker(ctx, nil, runArgs(o)...); err != nil {
			return fmt.Errorf("cannot create the container: %w", err)
		}
	case !s.Running:
		m.logf("starting the workspace container...")
		if _, err := m.docker(ctx, nil, "start", Container); err != nil {
			return fmt.Errorf("cannot start the container: %w", err)
		}
	default:
		m.logf("the workspace is already running")
	}
	// Prove it works, and that the browser is there.
	out, err := m.docker(ctx, nil, "exec", Container, "sh", "-c", "command -v chromium || command -v chromium-browser")
	if err != nil || strings.TrimSpace(out) == "" {
		return errors.New("the workspace is running but has no browser; run `tal workspace remove`, then `tal workspace up` to rebuild it")
	}
	return nil
}

func runArgs(o Options) []string {
	memory := o.Memory
	if memory == "" {
		memory = "2g"
	}
	args := []string{
		"run", "-d", "--init", "--name", Container, "--restart", "unless-stopped",
		"--label", labelKey + "=" + Version,
		"-v", Volume + ":" + Dir, "--workdir", Dir,
		// Chromium needs room in /dev/shm, and a bound on how much it can take from the machine.
		"--shm-size", "1g", "--memory", memory, "--pids-limit", "2048",
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL",
	}
	if o.Network != "" {
		args = append(args, "--network", o.Network)
	}
	if o.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(o.CPUs, 'f', -1, 64))
	}
	return append(args, Image)
}

// Down stops the container. Everything in it, and the volume, is kept.
func (m *Manager) Down(ctx context.Context) error {
	s, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if !s.Exists || !s.Running {
		return nil
	}
	_, err = m.docker(ctx, nil, "stop", "--time", "10", Container)
	return err
}

// Remove deletes the container. The volume, and so every file and login in the
// workspace, is kept, unless wipe is set.
func (m *Manager) Remove(ctx context.Context, wipe bool) error {
	s, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if s.Exists {
		if _, err := m.docker(ctx, nil, "rm", "-f", Container); err != nil {
			return err
		}
	}
	if wipe && s.VolumePresent {
		if _, err := m.docker(ctx, nil, "volume", "rm", Volume); err != nil {
			return err
		}
	}
	return nil
}

// Describe says in words what Status found.
func (s Status) Describe() string {
	var b strings.Builder
	state := "not created"
	switch {
	case s.Running:
		state = "running"
	case s.Exists:
		state = "stopped"
	}
	fmt.Fprintf(&b, "container %s: %s\n", Container, state)
	if s.Exists && s.Image != Image {
		fmt.Fprintf(&b, "  made from %s; this Talaria wants %s\n", s.Image, Image)
	}
	fmt.Fprintf(&b, "image %s: %s\n", Image, map[bool]string{true: "built", false: "not built"}[s.ImagePresent])
	fmt.Fprintf(&b, "volume %s (your files, mounted at %s): %s", Volume, Dir, map[bool]string{true: "exists", false: "not created"}[s.VolumePresent])
	return b.String()
}

// WaitReady waits until the container answers a command, which it does as soon
// as it is running; it is for the first start after `docker run`.
func (m *Manager) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := m.docker(ctx, nil, "exec", Container, "true"); err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("the workspace container does not answer")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
