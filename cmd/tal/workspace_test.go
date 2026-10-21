package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/execenv"
	"talaria/internal/workspace"
)

// dockerModel is just enough of Docker for the workspace commands.
type dockerModel struct {
	image, volume bool
	container     string // "", "running", "stopped"
	calls         []string
}

func (d *dockerModel) run(_ context.Context, _ io.Reader, args ...string) (string, error) {
	d.calls = append(d.calls, strings.Join(args, " "))
	switch args[0] {
	case "version":
		return "99", nil
	case "image":
		if d.image {
			return "id", nil
		}
		return "", errors.New("no such image")
	case "volume":
		if args[1] == "rm" {
			d.volume = false
			return "", nil
		}
		if d.volume {
			return "v", nil
		}
		return "", errors.New("no such volume")
	case "inspect":
		if d.container == "" {
			return "", errors.New("no such container")
		}
		return map[bool]string{true: "true", false: "false"}[d.container == "running"] + " " + workspace.Image, nil
	case "build":
		d.image = true
	case "run":
		d.container, d.volume = "running", true
	case "start":
		d.container = "running"
	case "stop":
		d.container = "stopped"
	case "rm":
		d.container = ""
	case "exec":
		return "/usr/bin/chromium", nil
	}
	return "", nil
}

func TestWorkspaceCommands(t *testing.T) {
	d := &dockerModel{}
	m := &workspace.Manager{Run: d.run}
	cfg := &config.Config{Workspace: config.Workspace{Network: "none", Memory: "3g", CPUs: 2}}
	do := func(args ...string) (string, error) {
		return capture(t, func() error { return runWorkspaceWith(context.Background(), cfg, args, m) })
	}

	out, err := do()
	if err != nil || !strings.Contains(out, "not created") || !strings.Contains(out, "tal workspace up") {
		t.Fatalf("status is the default: %q %v", out, err)
	}
	out, err = do("up")
	if err != nil || d.container != "running" || !strings.Contains(out, "ready") || !strings.Contains(out, "tal -env workspace") {
		t.Fatalf("%q %v", out, err)
	}
	var run string
	for _, c := range d.calls {
		if strings.HasPrefix(c, "run ") {
			run = c
		}
	}
	for _, want := range []string{"--network none", "--memory 3g", "--cpus 2"} {
		if !strings.Contains(run, want) {
			t.Errorf("the config's settings reach the container: %q lacks %q", run, want)
		}
	}
	if out, _ := do("status"); !strings.Contains(out, ": running") || !strings.Contains(out, "tal -env workspace") {
		t.Fatalf("%q", out)
	}
	if out, err := do("down"); err != nil || d.container != "stopped" || !strings.Contains(out, "files are kept") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := do("remove"); err != nil || d.container != "" || !d.volume || !strings.Contains(out, "kept") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := do("remove", "wipe"); err != nil || d.volume || !strings.Contains(out, "gone") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := do("remove", "everything"); err == nil {
		t.Fatal("an unknown argument is an error, not a wipe")
	}
	if _, err := do("explode"); err == nil || !strings.Contains(err.Error(), "unknown workspace command") {
		t.Fatalf("%v", err)
	}
}

func TestEnvListingShowsTheBuiltInWorkspace(t *testing.T) {
	cfg, _ := config.Load(t.TempDir() + "/none.json")
	out, err := capture(t, func() error { return runEnv(context.Background(), cfg, "cfg.json", "", "", "") })
	if err != nil || !strings.Contains(out, "workspace") || !strings.Contains(out, "tal workspace up") || !strings.Contains(out, "no other environments are defined") {
		t.Fatalf("%q %v", out, err)
	}
	// A definition in the file replaces it, and is then an ordinary environment.
	cfg.Environments[execenv.WorkspaceName] = execenv.Spec{Type: "ssh", Host: "myhost"}
	out, _ = capture(t, func() error { return runEnv(context.Background(), cfg, "cfg.json", "", "", "") })
	if strings.Contains(out, "tal workspace up") || strings.Contains(out, "no other environments") {
		t.Fatalf("%q", out)
	}
}

func TestBrowserConfigFromTheFile(t *testing.T) {
	off := false
	got := browserConfig(&config.Config{Browser: config.Browser{
		Enabled: &off, AllowPrivate: true, IdleMinutes: 4, Binary: "/b", ProfileDir: "/p", NoSandbox: &off,
	}})
	if got.Enabled == nil || *got.Enabled || got.NoSandbox == nil || *got.NoSandbox ||
		!got.Options.AllowPrivate || got.Options.PageIdle != 4*time.Minute || got.Options.Binary != "/b" || got.Options.ProfileDir != "/p" {
		t.Fatalf("%+v", got)
	}
	if def := browserConfig(&config.Config{}); def.Enabled != nil || def.NoSandbox != nil || def.Options.PageIdle != 0 {
		t.Fatalf("unset means the defaults, which are decided later: %+v", def)
	}
}
