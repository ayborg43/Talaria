package execenv

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Process is a long-running command in an environment, with its input and
// output connected to Talaria. A browser is one: it is started once and talked
// to for as long as it is needed.
type Process struct {
	In  io.WriteCloser // the command's standard input
	Out io.ReadCloser  // its standard output

	cmd    *exec.Cmd
	stderr *capBuffer
	cancel context.CancelFunc

	once sync.Once
	done chan struct{}
	err  error
}

// Starter is an environment that can start a Process. This machine, a Docker
// container and an SSH host all can.
type Starter interface {
	// Start runs script as `sh -c script sh args...`. The process ends when ctx
	// does, when it exits, or when Close is called; closing In is how a program
	// that reads its input to the end is told to stop.
	Start(ctx context.Context, script string, args ...string) (*Process, error)
}

func startProcess(parent context.Context, cmd *exec.Cmd, cancel context.CancelFunc) (*Process, error) {
	setupKill(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &capBuffer{max: maxStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p := &Process{In: in, Out: out, cmd: cmd, stderr: stderr, cancel: cancel, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Done is closed when the process has ended.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err says how it ended; it is meaningful once Done is closed.
func (p *Process) Err() error { return p.err }

// Stderr is what the process wrote to its standard error, up to a few kilobytes.
func (p *Process) Stderr() string { return strings.TrimSpace(string(p.stderr.bytes())) }

// Close stops the process: it closes its input, which most programs take as a
// request to finish, gives it a moment, then kills it. Closing twice is harmless.
func (p *Process) Close() {
	p.once.Do(func() {
		p.In.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
		p.cancel()
		<-p.done
	})
}

// Start runs script on this machine.
func (Local) Start(ctx context.Context, script string, args ...string) (*Process, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "sh", append([]string{"-c", script, "sh"}, args...)...)
	return startProcess(ctx, cmd, cancel)
}

// Start runs script in the environment's machine, through its transport.
func (e *posixEnv) Start(ctx context.Context, script string, args ...string) (*Process, error) {
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("a value contains a NUL byte, which cannot be passed to a command")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	return startProcess(ctx, e.t.command(ctx, script, args), cancel)
}
