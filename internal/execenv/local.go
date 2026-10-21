package execenv

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Local runs everything on the machine Talaria runs on. Fence, if set, keeps the
// file tools out of protected places and Talaria's secrets out of shell commands.
type Local struct{ Fence *Fence }

func (Local) Describe() string { return "this machine" }
func (Local) Remote() bool     { return false }
func (Local) Close()           {}

func (l Local) Shell(ctx context.Context, command string, timeout time.Duration) (ShellResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "sh", "-c", command)
	if l.Fence != nil {
		cmd.Env = l.Fence.Environ()
	}
	setupKill(cmd)
	out := &capBuffer{max: MaxOutput}
	cmd.Stdout, cmd.Stderr = out, out // one writer, so the two streams stay in order

	err := cmd.Run()
	res := ShellResult{Output: string(out.bytes()), Truncated: out.wasTruncated()}
	switch {
	case ctx.Err() != nil: // the caller gave up
		return res, ctx.Err()
	case cctx.Err() != nil:
		res.TimedOut = true
		return res, nil
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return res, err // sh itself could not be started
		}
		res.ExitCode = ee.ExitCode()
	}
	return res, nil
}

func (l Local) ReadFile(_ context.Context, path string, limit int) ([]byte, bool, error) {
	if err := l.Fence.Check(path); err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(buf) > limit {
		return buf[:limit], true, nil
	}
	return buf, false, nil
}

func (l Local) WriteFile(_ context.Context, path string, data []byte) error {
	if err := l.Fence.Check(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (l Local) ListDir(_ context.Context, path string) ([]string, error) {
	if err := l.Fence.Check(path); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return names, nil
}
