package execenv

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// exchange sends a line to a process and reads one back.
func exchange(t *testing.T, p *Process, line string) string {
	t.Helper()
	if _, err := p.In.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(p.Out).ReadString('\n')
		got <- strings.TrimRight(s, "\n")
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no answer from the process")
		return ""
	}
}

func TestLocalStartTalksToAProcess(t *testing.T) {
	p, err := Local{}.Start(context.Background(), `while read l; do echo "echo: $l"; done`)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := exchange(t, p, "hello"); got != "echo: hello" {
		t.Fatalf("%q", got)
	}
	if got := exchange(t, p, "again"); got != "echo: again" {
		t.Fatalf("%q", got)
	}
}

func TestStartPassesArgumentsAndKeepsStderrApart(t *testing.T) {
	p, err := Local{}.Start(context.Background(), `echo "args: $1 $2"; echo "oops" >&2`, "a b", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	line, _ := bufio.NewReader(p.Out).ReadString('\n')
	<-p.Done()
	if strings.TrimSpace(line) != "args: a b c" {
		t.Fatalf("%q", line)
	}
	if p.Stderr() != "oops" {
		t.Fatalf("stderr %q", p.Stderr())
	}
	if p.Err() != nil {
		t.Fatal(p.Err())
	}
}

// Closing stdin is how a program that reads to the end is asked to stop.
func TestCloseAsksFirstThenKills(t *testing.T) {
	polite, _ := Local{}.Start(context.Background(), `cat >/dev/null`)
	start := time.Now()
	polite.Close()
	if time.Since(start) > time.Second {
		t.Fatalf("a program that stops when its input ends should stop at once: %s", time.Since(start))
	}
	select {
	case <-polite.Done():
	default:
		t.Fatal("not stopped")
	}

	stubborn, _ := Local{}.Start(context.Background(), `trap '' HUP; while true; do sleep 1; done`)
	start = time.Now()
	stubborn.Close()
	if d := time.Since(start); d < time.Second || d > 6*time.Second {
		t.Fatalf("a program that ignores it is killed after a short wait: %s", d)
	}
	select {
	case <-stubborn.Done():
	default:
		t.Fatal("not stopped")
	}
	stubborn.Close() // harmless again
}

func TestStartEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Local{}.Start(ctx, `sleep 60 & wait`)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process outlived its context")
	}
	p.Close()
}

func TestStartRefusesNULArguments(t *testing.T) {
	f := installFakes(t)
	_ = f
	t.Setenv("FAKE_DOCKER_ATTACH", "dev")
	env, err := Open(context.Background(), "dev", Spec{Type: "docker", Container: "dev", Workdir: t.TempDir()}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.(Starter).Start(context.Background(), "true", "a\x00b"); err == nil {
		t.Fatal("a NUL cannot cross a command line")
	}
}

// A Docker environment starts processes with docker exec, input attached, in the container.
func TestDockerStartRunsInTheContainerWithInputAttached(t *testing.T) {
	f := installFakes(t)
	t.Setenv("FAKE_DOCKER_ATTACH", "dev")
	env, err := Open(context.Background(), "dev", Spec{Type: "docker", Container: "dev"}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	starter, ok := env.(Starter)
	if !ok {
		t.Fatal("a docker environment can start processes")
	}
	p, err := starter.Start(context.Background(), `read l; echo "got $l"`)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := exchange(t, p, "ping"); got != "got ping" {
		t.Fatalf("%q", got)
	}
	exec := f.dockerCalls(t, "exec")
	if len(exec) == 0 || !hasArgs(exec[len(exec)-1], "-i", "dev", "sh", "-c") {
		t.Fatalf("%q", exec)
	}
	for _, e := range []Env{Local{}, env} {
		if _, ok := e.(Starter); !ok {
			t.Fatalf("%T cannot start processes", e)
		}
	}
}

func TestAttachingToTheWorkspaceExplainsHowToSetItUp(t *testing.T) {
	installFakes(t)
	_, err := Open(context.Background(), "workspace", WorkspaceSpec(), os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "tal workspace up") {
		t.Fatalf("%v", err)
	}
	// Other containers keep the general message.
	_, err = Open(context.Background(), "dev", Spec{Type: "docker", Container: "dev"}, os.Getenv)
	if err == nil || strings.Contains(err.Error(), "tal workspace") {
		t.Fatalf("%v", err)
	}
}

func TestDockerEnvNamesItsContainer(t *testing.T) {
	installFakes(t)
	t.Setenv("FAKE_DOCKER_ATTACH", WorkspaceContainer)
	spec := WorkspaceSpec()
	spec.Workdir = t.TempDir() // the fake container is this machine, which has no /workspace
	env, err := Open(context.Background(), "workspace", spec, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if n := env.(interface{ ContainerName() string }).ContainerName(); n != WorkspaceContainer {
		t.Fatal(n)
	}
}
