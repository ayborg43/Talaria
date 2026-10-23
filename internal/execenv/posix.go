package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The Docker and SSH environments reach their machine differently, but both
// then run the same small POSIX shell scripts there, so the behaviour is
// identical and written once. Each script is run as `sh -c SCRIPT sh ARGS...`
// and takes the working directory as its first argument.

const prelude = `wd=$1; shift
if [ -n "$wd" ]; then
  case $wd in "~") wd=$HOME;; "~/"*) wd=$HOME/${wd#"~/"};; esac
  cd -- "$wd" || exit 125
fi
`

const tilde = `case $p in "~") p=$HOME;; "~/"*) p=$HOME/${p#"~/"};; esac
`

var (
	// args: command, seconds. A kill-after timeout on the far side stops a
	// command there even if the connection to it is gone; without a timeout
	// program the command still runs, bounded by the client-side limit.
	//
	// Standard error is merged into standard output here, on the far side:
	// docker exec and ssh carry the two as separate streams and lose their
	// relative order, but one stream keeps it.
	shellScript = prelude + `if command -v timeout >/dev/null 2>&1; then exec timeout -s KILL "$2" sh -c "$1" 2>&1; fi
exec sh -c "$1" 2>&1`
	// args: path, byte limit.
	readScript = prelude + `p=$1
` + tilde + `exec head -c "$2" -- "$p"`
	// args: path. The file's bytes arrive on standard input.
	writeScript = prelude + `p=$1
` + tilde + `mkdir -p -- "$(dirname -- "$p")" && cat > "$p"`
	// args: path.
	listScript = prelude + `p=$1
` + tilde + `if [ ! -e "$p" ]; then echo "$p: No such file or directory" >&2; exit 2; fi
if [ ! -d "$p" ]; then echo "$p: Not a directory" >&2; exit 20; fi
cd -- "$p" && LC_ALL=C ls -1Ap`
)

// clientGrace is how long past a command's own timeout the client waits
// before killing the connection. The far side's timeout should end the command
// first and report it. A variable so tests can shrink it.
var clientGrace = 5 * time.Second

// transport knows how to start a process in the environment.
type transport interface {
	// command builds the process that runs script, as `sh -c script sh args...`, there.
	command(ctx context.Context, script string, args []string) *exec.Cmd
}

type posixEnv struct {
	t       transport
	workdir string // given to every script, which changes into it; "" if the transport already starts there
	desc    string
	closer  func()
	// remote, if set, keeps the file tools out of the far machine's
	// credential places. Docker leaves it unset: a fresh container holds no
	// credentials of its own, and the container is the sandbox.
	remote *RemoteFence
}

func (e *posixEnv) Describe() string { return e.desc }
func (e *posixEnv) Remote() bool     { return true }

func (e *posixEnv) Close() {
	if e.closer != nil {
		e.closer()
	}
}

type runOut struct {
	stdout, stderr []byte
	truncated      bool
	code           int
}

// run executes a script. combined merges standard error into the output.
// A command that ends with a failing exit status is reported in code, not as an error.
func (e *posixEnv) run(ctx context.Context, script string, args []string, stdin []byte, combined bool, outMax int) (runOut, error) {
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return runOut{}, errors.New("a value contains a NUL byte, which cannot be passed to a command")
		}
	}
	cmd := e.t.command(ctx, script, append([]string{e.workdir}, args...))
	setupKill(cmd)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := &capBuffer{max: outMax}
	var errBuf *capBuffer
	if combined {
		cmd.Stdout, cmd.Stderr = out, out
	} else {
		errBuf = &capBuffer{max: maxStderr}
		cmd.Stdout, cmd.Stderr = out, errBuf
	}

	err := cmd.Run()
	r := runOut{stdout: out.bytes(), truncated: out.wasTruncated()}
	if errBuf != nil {
		r.stderr = errBuf.bytes()
	}
	if err == nil {
		return r, nil
	}
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.code = ee.ExitCode()
		return r, nil
	}
	return r, err
}

func (e *posixEnv) Shell(ctx context.Context, command string, timeout time.Duration) (ShellResult, error) {
	secs := max(int(math.Ceil(timeout.Seconds())), 1)
	cctx, cancel := context.WithTimeout(ctx, timeout+clientGrace)
	defer cancel()

	start := time.Now()
	r, err := e.run(cctx, shellScript, []string{command, strconv.Itoa(secs)}, nil, true, MaxOutput)
	res := ShellResult{Output: string(r.stdout), Truncated: r.truncated, ExitCode: r.code}
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case cctx.Err() != nil: // the far side's own timeout never fired (or there is none)
			res.TimedOut = true
			return res, nil
		}
		return res, err
	}
	if r.code == 137 && time.Since(start) >= timeout-time.Second { // killed by the far side's timeout
		res.TimedOut = true
	}
	return res, nil
}

// failure turns a failed file operation into an error carrying what the far side said.
func failure(what string, r runOut) error {
	msg := strings.Join(strings.Fields(string(r.stderr)), " ")
	if msg == "" {
		msg = fmt.Sprintf("%s failed (exit status %d)", what, r.code)
	}
	return errors.New(msg)
}

func (e *posixEnv) ReadFile(ctx context.Context, path string, limit int) ([]byte, bool, error) {
	if err := e.remote.Check(path, e.workdir); err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, fileOp)
	defer cancel()
	r, err := e.run(ctx, readScript, []string{path, strconv.Itoa(limit + 1)}, nil, false, limit+1)
	if err != nil {
		return nil, false, err
	}
	if r.code != 0 {
		return nil, false, failure("reading "+path, r)
	}
	if len(r.stdout) > limit {
		return r.stdout[:limit], true, nil
	}
	return r.stdout, false, nil
}

func (e *posixEnv) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := e.remote.Check(path, e.workdir); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, fileOp)
	defer cancel()
	if data == nil {
		data = []byte{}
	}
	r, err := e.run(ctx, writeScript, []string{path}, data, false, 0)
	if err != nil {
		return err
	}
	if r.code != 0 {
		return failure("writing "+path, r)
	}
	return nil
}

func (e *posixEnv) ListDir(ctx context.Context, path string) ([]string, error) {
	if err := e.remote.Check(path, e.workdir); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fileOp)
	defer cancel()
	r, err := e.run(ctx, listScript, []string{path}, nil, false, MaxOutput*4)
	if err != nil {
		return nil, err
	}
	if r.code != 0 {
		return nil, failure("listing "+path, r)
	}
	text := strings.TrimRight(string(r.stdout), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// shellQuote makes s one word for a POSIX shell: single quotes, with each
// embedded single quote written as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
