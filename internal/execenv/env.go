// Package execenv is where the shell and file tools run: on this machine, in a
// Docker container, or on a remote host over SSH.
package execenv

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Env is a place to run commands and touch files. Paths and commands refer to
// that place's filesystem; a relative path starts in its working directory.
type Env interface {
	// Describe says where this is, for the model and for the user.
	Describe() string
	// Remote reports whether this is somewhere other than the machine Talaria runs on.
	Remote() bool

	// Shell runs command with sh -c, stopping it after timeout.
	Shell(ctx context.Context, command string, timeout time.Duration) (ShellResult, error)
	// ReadFile reads at most limit bytes, reporting whether the file was longer.
	ReadFile(ctx context.Context, path string, limit int) (data []byte, truncated bool, err error)
	// WriteFile creates or replaces a file, making its parent directories.
	WriteFile(ctx context.Context, path string, data []byte) error
	// ListDir returns a directory's entries by name, directories with a trailing "/".
	ListDir(ctx context.Context, path string) ([]string, error)

	// Close releases what the environment holds: a container it started, an SSH connection.
	Close()
}

// ShellResult is the outcome of a command.
type ShellResult struct {
	Output    string // standard output and standard error together, in order
	ExitCode  int    // -1 if the command was killed by a signal
	TimedOut  bool
	Truncated bool // Output is cut at MaxOutput bytes
}

// Limits shared by every environment.
const (
	MaxOutput = 32 << 10 // bytes of command output kept
	maxStderr = 8 << 10  // bytes of standard error kept for an error message
	fileOp    = 2 * time.Minute
)

// capBuffer keeps the first max bytes written to it and drops the rest. It
// accepts every write, so a chatty command is never blocked or killed by the cap.
type capBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.max - len(c.buf)
	if room > 0 {
		n := min(room, len(p))
		c.buf = append(c.buf, p[:n]...)
		if n < len(p) {
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *capBuffer) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf
}

func (c *capBuffer) wasTruncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

// Open starts the environment a Spec describes. A container or connection that
// cannot be set up is an error: Talaria never falls back to running on the
// local machine, which is the thing the environment was chosen to avoid.
func Open(ctx context.Context, name string, s Spec, getenv func(string) string) (Env, error) {
	if err := s.Validate(name); err != nil {
		return nil, err
	}
	switch s.Type {
	case "docker":
		return openDocker(ctx, name, s, getenv)
	case "ssh":
		return openSSH(ctx, name, s)
	}
	return nil, fmt.Errorf("environment %q: unknown type %q", name, s.Type)
}
