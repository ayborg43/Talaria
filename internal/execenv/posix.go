package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path"
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

// resolveFunc defines resolve_one, which prints a path with its symlinks
// followed, absolute: component by component from the root, following each
// link with readlink and carrying on from its target. A .. pops a real
// directory, so a link can never smuggle one past the fence. At most 40 links
// are followed, like the kernel's own loop protection; past that, or when a
// link cannot be read, it returns 5 or 4. Without readlink at all the path is
// printed as written, for the lexical check alone.
const resolveFunc = `resolve_one() {
  p=$1
  case $p in "~") p=$HOME;; "~/"*) p=$HOME/${p#"~/"};; esac
  case $p in
  /*) ;;
  *) p=$PWD/$p ;;
  esac
  if ! command -v readlink >/dev/null 2>&1; then
    printf '%s\n' "$p"
    return 0
  fi
  todo=$p
  phys=/
  links=0
  while [ -n "$todo" ]; do
    case $todo in
    /*) todo=${todo#/}; continue ;; # extra slashes, including from // mid-path: phys already says where we are
    esac
    case $todo in
    */*) c=${todo%%/*}; todo=${todo#*/} ;;
    *) c=$todo; todo= ;;
    esac
    case $c in
    ""|.) ;;
    ..) phys=$(dirname "$phys") ;;
    *)
      case $phys in /) cand=/$c ;; *) cand=$phys/$c ;; esac
      if [ -L "$cand" ]; then
        links=$((links+1))
        if [ "$links" -gt 40 ]; then return 5; fi
        t=$(readlink "$cand") || return 4
        case $t in
        /*) phys=/; todo=${t#/}${todo:+/$todo} ;;
        *) todo=$t${todo:+/$todo} ;;
        esac
      else
        phys=$cand
      fi ;;
    esac
  done
  printf '%s\n' "$phys"
}
`

// resolveScript prints one path with its symlinks followed. It takes the
// working directory as its first argument, like every script.
var resolveScript = prelude + resolveFunc + `resolve_one "$1"
`

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
	if err := e.checkResolved(ctx, path); err != nil {
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
	if err := e.checkResolved(ctx, path); err != nil {
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
	if err := e.checkResolved(ctx, path); err != nil {
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

// checkResolved follows the far side's symlinks and checks the result, so a
// link into a credential place does not get through. The lexical check has
// already passed; anything odd here fails closed. A nil fence, or one without
// a home (Docker leaves it unset), skips the round trip: there is nothing to
// check against.
func (e *posixEnv) checkResolved(ctx context.Context, p string) error {
	if e.remote == nil || e.remote.Home == "" {
		return nil
	}
	resolved, err := e.resolveRemote(ctx, p)
	if err != nil {
		return err
	}
	return e.remote.CheckAbs(resolved, p)
}

func (e *posixEnv) resolveRemote(ctx context.Context, p string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fileOp)
	defer cancel()
	r, err := e.run(ctx, resolveScript, []string{p}, nil, false, MaxOutput)
	denied := func(why string) (string, error) {
		return "", fmt.Errorf("%w (cannot resolve %s to check it: %s)", ErrFenced, p, why)
	}
	if err != nil {
		return denied(err.Error())
	}
	if r.code != 0 {
		msg := strings.Join(strings.Fields(string(r.stderr)), " ")
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", r.code)
		}
		return denied(msg)
	}
	if r.truncated {
		return denied("an absurdly long answer")
	}
	lines := strings.Split(strings.TrimRight(string(r.stdout), "\n"), "\n")
	resolved := path.Clean(lines[len(lines)-1])
	if !strings.HasPrefix(resolved, "/") || strings.ContainsAny(resolved, "\x00\r\n\t") {
		return denied("the answer was not a path")
	}
	return resolved, nil
}

// shellQuote makes s one word for a POSIX shell: single quotes, with each
// embedded single quote written as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
