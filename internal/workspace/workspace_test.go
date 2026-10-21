package workspace

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeDocker is a tiny model of the Docker daemon: it knows whether the image,
// volume and container exist, and records every command.
type fakeDocker struct {
	mu        sync.Mutex
	calls     [][]string
	stdin     map[string]string // the first argument of a call -> what it was given on stdin
	image     bool
	volume    bool
	container string // "", "running" or "stopped"
	madeFrom  string
	noBrowser bool
	down      bool
}

func (f *fakeDocker) run(_ context.Context, stdin io.Reader, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		if f.stdin == nil {
			f.stdin = map[string]string{}
		}
		f.stdin[args[0]] = string(b)
	}
	switch args[0] {
	case "version":
		if f.down {
			return "", errors.New("Cannot connect to the Docker daemon")
		}
		return "99", nil
	case "image":
		if f.image {
			return "sha256:abc", nil
		}
		return "", errors.New("No such image")
	case "volume":
		if args[1] == "rm" {
			f.volume = false
			return "", nil
		}
		if f.volume {
			return Volume, nil
		}
		return "", errors.New("No such volume")
	case "inspect":
		if f.container == "" {
			return "", errors.New("No such object")
		}
		image := f.madeFrom
		if image == "" {
			image = Image
		}
		return map[bool]string{true: "true", false: "false"}[f.container == "running"] + " " + image, nil
	case "build":
		f.image = true
		return "", nil
	case "run":
		f.container, f.volume = "running", true
		return "id", nil
	case "start":
		f.container = "running"
		return "", nil
	case "stop":
		f.container = "stopped"
		return "", nil
	case "rm":
		f.container = ""
		return "", nil
	case "exec":
		if f.noBrowser {
			return "", errors.New("exit status 1")
		}
		return "/usr/bin/chromium", nil
	}
	return "", errors.New("unsupported: " + args[0])
}

func (f *fakeDocker) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c[0] == "version" || c[0] == "inspect" || c[0] == "image" || (c[0] == "volume" && c[1] == "inspect") || (c[0] == "exec") {
			continue
		}
		out = append(out, c[0])
	}
	return out
}

func (f *fakeDocker) call(verb string) []string {
	for _, c := range f.calls {
		if c[0] == verb {
			return c
		}
	}
	return nil
}

func manager(f *fakeDocker) *Manager { return &Manager{Run: f.run} }

func TestUpFromNothingBuildsCreatesAndChecks(t *testing.T) {
	f := &fakeDocker{}
	var log []string
	m := &Manager{Run: f.run, Log: func(s string) { log = append(log, s) }}
	if err := m.Up(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.verbs(), ","); got != "build,run" {
		t.Fatalf("%s", got)
	}
	if f.stdin["build"] != dockerfile {
		t.Fatal("the image is built from the embedded Dockerfile, given on stdin")
	}
	if !slices.Contains(f.call("build"), Image) {
		t.Fatalf("%v", f.call("build"))
	}
	if len(log) == 0 {
		t.Fatal("progress is reported")
	}
}

func TestRunArgumentsAreContained(t *testing.T) {
	args := strings.Join(runArgs(Options{Network: "isolated", Memory: "4g", CPUs: 1.5}), " ")
	for _, want := range []string{
		"--name " + Container, "--restart unless-stopped", "-v " + Volume + ":" + Dir, "--workdir " + Dir, "--init",
		"--cap-drop ALL", "--security-opt no-new-privileges", "--shm-size 1g", "--memory 4g", "--pids-limit 2048",
		"--network isolated", "--cpus 1.5", "--label talaria.workspace=" + Version,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	for _, never := range []string{"docker.sock", "--privileged", " -p ", "--publish", "--network host", "--pid host", "--pid=", "--cap-add"} {
		if strings.Contains(args, never) {
			t.Errorf("the workspace must not have %q: %s", never, args)
		}
	}
	if !strings.HasSuffix(args, Image) {
		t.Fatalf("%s", args)
	}
	if def := strings.Join(runArgs(Options{}), " "); !strings.Contains(def, "--memory 2g") || strings.Contains(def, "--cpus") || strings.Contains(def, "--network") {
		t.Fatalf("defaults: %s", def)
	}
}

func TestUpIsIdempotent(t *testing.T) {
	f := &fakeDocker{image: true, volume: true, container: "running"}
	if err := manager(f).Up(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(f.verbs()) != 0 {
		t.Fatalf("a running workspace needs nothing done: %v", f.verbs())
	}
}

func TestUpStartsAStoppedContainerWithoutRecreatingIt(t *testing.T) {
	f := &fakeDocker{image: true, volume: true, container: "stopped"}
	if err := manager(f).Up(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.verbs(), ","); got != "start" {
		t.Fatalf("%s", got)
	}
}

func TestUpReusesTheImageAndTheVolume(t *testing.T) {
	f := &fakeDocker{image: true, volume: true}
	manager(f).Up(context.Background(), Options{})
	if got := strings.Join(f.verbs(), ","); got != "run" {
		t.Fatalf("an existing image is not rebuilt: %s", got)
	}
}

func TestUpRefusesAnOutOfDateContainerAndSaysHowToFixIt(t *testing.T) {
	f := &fakeDocker{image: true, volume: true, container: "running", madeFrom: "talaria-workspace:0"}
	err := manager(f).Up(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "talaria-workspace:0") || !strings.Contains(err.Error(), "tal workspace remove") || !strings.Contains(err.Error(), "files are kept") {
		t.Fatalf("%v", err)
	}
	if len(f.verbs()) != 0 {
		t.Fatalf("nothing is changed behind the user's back: %v", f.verbs())
	}
}

func TestUpNoticesAMissingBrowser(t *testing.T) {
	f := &fakeDocker{image: true, volume: true, container: "running", noBrowser: true}
	if err := manager(f).Up(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "no browser") {
		t.Fatalf("%v", err)
	}
}

func TestUpExplainsDockerProblems(t *testing.T) {
	if err := manager(&fakeDocker{down: true}).Up(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "Docker daemon") {
		t.Fatalf("%v", err)
	}
	failing := func(_ context.Context, _ io.Reader, args ...string) (string, error) {
		if args[0] == "build" {
			return "", errors.New("network is unreachable")
		}
		if args[0] == "version" {
			return "99", nil
		}
		return "", errors.New("missing")
	}
	if err := (&Manager{Run: failing}).Up(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "cannot build the image: network is unreachable") {
		t.Fatalf("%v", err)
	}
}

func TestDownKeepsEverything(t *testing.T) {
	f := &fakeDocker{image: true, volume: true, container: "running"}
	if err := manager(f).Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.verbs(), ","); got != "stop" || f.container != "stopped" || !f.volume {
		t.Fatalf("%s %s %v", got, f.container, f.volume)
	}
	f.calls = nil
	manager(f).Down(context.Background()) // already stopped: nothing to do
	if len(f.verbs()) != 0 {
		t.Fatalf("%v", f.verbs())
	}
}

func TestRemoveKeepsTheVolumeUnlessAskedToWipe(t *testing.T) {
	f := &fakeDocker{image: true, volume: true, container: "running"}
	if err := manager(f).Remove(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if f.container != "" || !f.volume {
		t.Fatalf("the files must survive removing the container: %q %v", f.container, f.volume)
	}
	f.container = "stopped"
	if err := manager(f).Remove(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.container != "" || f.volume {
		t.Fatalf("wipe removes the volume too: %q %v", f.container, f.volume)
	}
	if err := manager(f).Remove(context.Background(), true); err != nil {
		t.Fatalf("removing what is not there is fine: %v", err)
	}
}

func TestStatusDescribe(t *testing.T) {
	for _, tc := range []struct {
		f    *fakeDocker
		want []string
	}{
		{&fakeDocker{}, []string{"not created", "not built"}},
		{&fakeDocker{image: true, volume: true, container: "running"}, []string{": running", "built", "exists"}},
		{&fakeDocker{image: true, container: "stopped", madeFrom: "talaria-workspace:0"}, []string{"stopped", "made from talaria-workspace:0"}},
	} {
		s, err := manager(tc.f).Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		d := s.Describe()
		for _, w := range tc.want {
			if !strings.Contains(d, w) {
				t.Errorf("%q missing from:\n%s", w, d)
			}
		}
	}
}

func TestDockerfileMatchesWhatTheCodeExpects(t *testing.T) {
	for _, want := range []string{
		"chromium", "USER talaria", "WORKDIR " + Dir, `LABEL talaria.workspace="` + Version + `"`, "ca-certificates", "tail", "git", "python3",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(dockerfile, "USER root") || strings.Contains(dockerfile, "sudo") {
		t.Fatal("the workspace runs as an ordinary user")
	}
}
