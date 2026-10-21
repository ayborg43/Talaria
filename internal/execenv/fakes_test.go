package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The fake docker and ssh programs are shell scripts put first on PATH. They
// do not talk to a daemon or a network: "the container" and "the remote host"
// are this machine, with a home directory of their own. What matters is that
// they treat their arguments the way the real programs do: fake ssh gives the
// command string to `sh -c`, exactly as a remote login shell would, so quoting
// mistakes show up here as they would for real.

const fakeDocker = `#!/bin/sh
printf '%s\037' "$@" >> "$FAKE_DOCKER_LOG"; printf '\036' >> "$FAKE_DOCKER_LOG"
state="$FAKE_DOCKER_STATE"
cmd=$1; shift
case $cmd in
version)
  if [ -n "$FAKE_DOCKER_DOWN" ]; then
    echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2; exit 1
  fi
  echo 99.0.0 ;;
run)
  if [ -n "$FAKE_DOCKER_RUN_FAIL" ]; then echo "$FAKE_DOCKER_RUN_FAIL" >&2; exit 125; fi
  name=; pid=
  while [ $# -gt 0 ]; do
    case $1 in
      --name) name=$2; shift 2 ;;
      --label) case $2 in talaria.pid=*) pid=${2#talaria.pid=};; esac; shift 2 ;;
      *) shift ;;
    esac
  done
  printf '%s\n' "$pid" > "$state/$name"
  echo "0123456789abcdef" ;;
exec)
  dir=
  while [ $# -gt 0 ]; do
    case $1 in
      -i) shift ;;
      -w) dir=$2; shift 2 ;;
      -u|-e) shift 2 ;;
      *) break ;;
    esac
  done
  name=$1; shift
  if [ ! -e "$state/$name" ] && ! printf '%s' ",$FAKE_DOCKER_ATTACH," | grep -q ",$name,"; then
    echo "Error response from daemon: No such container: $name" >&2; exit 1
  fi
  if [ -n "$dir" ]; then cd "$dir" || { echo "OCI runtime exec failed: chdir to $dir failed" >&2; exit 126; }; fi
  exec "$@" ;;
rm)
  [ "$1" = -f ] && shift
  rm -f "$state/$1" ;;
ps)
  for f in "$state"/*; do
    [ -e "$f" ] || continue
    printf '%s\t%s\n' "$(basename "$f")" "$(cat "$f")"
  done ;;
inspect)
  name=$3
  if printf '%s' ",$FAKE_DOCKER_STOPPED," | grep -q ",$name,"; then echo false
  elif printf '%s' ",$FAKE_DOCKER_ATTACH," | grep -q ",$name,"; then echo true
  else echo "Error: No such object: $name" >&2; exit 1; fi ;;
*) echo "fake docker: unsupported command $cmd" >&2; exit 2 ;;
esac
`

const fakeSSH = `#!/bin/sh
printf '%s\037' "$@" >> "$FAKE_SSH_LOG"; printf '\036' >> "$FAKE_SSH_LOG"
op=
while [ $# -gt 0 ]; do
  case $1 in
    --) shift; break ;;
    -o|-p|-i|-l|-J|-F|-L|-R|-D|-b|-c|-E|-e|-m|-S|-w|-W) shift 2 ;;
    -O) op=$2; shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
host=$1; shift
[ "$op" = exit ] && exit 0
if [ -n "$FAKE_SSH_FAIL" ]; then echo "$FAKE_SSH_FAIL" >&2; exit 255; fi
cd "$FAKE_SSH_HOME" || exit 1
# ssh joins what is left into one string and hands it to the remote shell
HOME="$FAKE_SSH_HOME" exec sh -c "$*"
`

type fakes struct {
	dockerLog, sshLog string
	state, sshHome    string
}

// installFakes puts fake docker and ssh programs first on PATH.
func installFakes(t *testing.T) *fakes {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker and ssh programs are shell scripts")
	}
	bin, work := t.TempDir(), t.TempDir()
	f := &fakes{
		dockerLog: filepath.Join(work, "docker.log"),
		sshLog:    filepath.Join(work, "ssh.log"),
		state:     filepath.Join(work, "containers"),
		sshHome:   filepath.Join(work, "remote-home"),
	}
	for _, d := range []string{f.state, f.sshHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"docker": fakeDocker, "ssh": fakeSSH} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_LOG", f.dockerLog)
	t.Setenv("FAKE_DOCKER_STATE", f.state)
	t.Setenv("FAKE_SSH_LOG", f.sshLog)
	t.Setenv("FAKE_SSH_HOME", f.sshHome)
	for _, v := range []string{"FAKE_DOCKER_DOWN", "FAKE_DOCKER_RUN_FAIL", "FAKE_DOCKER_ATTACH", "FAKE_DOCKER_STOPPED", "FAKE_SSH_FAIL"} {
		t.Setenv(v, "")
	}
	return f
}

// calls returns each logged invocation as its list of arguments. Calls end with
// a record separator and arguments with a unit separator, so an argument may
// contain anything, newlines included.
func calls(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, record := range strings.Split(string(raw), "\x1e") {
		if record == "" {
			continue
		}
		out = append(out, strings.Split(strings.TrimSuffix(record, "\x1f"), "\x1f"))
	}
	return out
}

func hasArgs(call []string, want ...string) bool {
	for i := 0; i+len(want) <= len(call); i++ {
		if strings.Join(call[i:i+len(want)], "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func (f *fakes) dockerCalls(t *testing.T, sub string) [][]string {
	var out [][]string
	for _, c := range calls(t, f.dockerLog) {
		if len(c) > 0 && c[0] == sub {
			out = append(out, c)
		}
	}
	return out
}
