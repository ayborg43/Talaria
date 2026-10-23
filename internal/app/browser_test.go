package app

import (
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/browser"
	"talaria/internal/execenv"
)

// wsEnv stands in for the workspace container: an environment that can start
// processes, is not this machine, and names its container.
type wsEnv struct {
	execenv.Local
	container string
}

func (wsEnv) Remote() bool            { return true }
func (w wsEnv) ContainerName() string { return w.container }

// plainEnv can start processes but is not the workspace.
type plainEnv struct{ execenv.Local }

// noStart cannot start processes at all.
type noStart struct{ execenv.Env }

func (noStart) Describe() string { return "nowhere" }

func browserTools(rt *Runtime, env execenv.Env) []string {
	a := rt.NewAgent(AgentOptions{})
	if env != nil {
		rt.UseEnvironment(a, env)
	}
	var names []string
	for _, s := range a.Tools.Specs() {
		if strings.HasPrefix(s.Name, browser.Prefix) {
			names = append(names, s.Name)
		}
	}
	return names
}

func openWith(t *testing.T, cfg Config) *Runtime {
	t.Helper()
	cfg.Home, cfg.Provider, cfg.Model = t.TempDir(), idle(), "m"
	rt := Open(cfg)
	t.Cleanup(rt.Close)
	return rt
}

func TestBrowserToolsComeWithTheWorkspaceOnly(t *testing.T) {
	ws := wsEnv{container: execenv.WorkspaceContainer}
	if got := browserTools(openWith(t, Config{Env: ws}), nil); len(got) != len(browser.ToolNames) {
		t.Fatalf("the workspace has a browser: %v", got)
	}
	for name, env := range map[string]execenv.Env{
		"this machine":      execenv.Local{},
		"another container": wsEnv{container: "some-other"},
		"an environment that cannot start processes": noStart{execenv.Local{}},
	} {
		if got := browserTools(openWith(t, Config{Env: env}), nil); len(got) != 0 {
			t.Errorf("%s: the browser should not be offered unasked: %v", name, got)
		}
	}
}

func TestBrowserCanBeTurnedOnOrOff(t *testing.T) {
	on, off := true, false
	if got := browserTools(openWith(t, Config{Env: execenv.Local{}, Browser: BrowserConfig{Enabled: &on}}), nil); len(got) != len(browser.ToolNames) {
		t.Fatalf("enabled: %v", got)
	}
	if got := browserTools(openWith(t, Config{Env: wsEnv{container: execenv.WorkspaceContainer}, Browser: BrowserConfig{Enabled: &off}}), nil); len(got) != 0 {
		t.Fatalf("disabled: %v", got)
	}
	// It cannot be turned on where nothing can run it.
	if got := browserTools(openWith(t, Config{Env: noStart{execenv.Local{}}, Browser: BrowserConfig{Enabled: &on}}), nil); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestMovingAnAgentMovesItsBrowser(t *testing.T) {
	rt := openWith(t, Config{Env: execenv.Local{}})
	if got := browserTools(rt, wsEnv{container: execenv.WorkspaceContainer}); len(got) != len(browser.ToolNames) {
		t.Fatalf("moving into the workspace brings the browser: %v", got)
	}
	a := rt.NewAgent(AgentOptions{})
	rt.UseEnvironment(a, wsEnv{container: execenv.WorkspaceContainer})
	rt.UseEnvironment(a, execenv.Local{})
	for _, s := range a.Tools.Specs() {
		if strings.HasPrefix(s.Name, browser.Prefix) {
			t.Fatalf("moving out of the workspace takes the browser away, or it would still act there: %s", s.Name)
		}
	}
	rt.UseEnvironment(a, wsEnv{container: execenv.WorkspaceContainer})
	n := 0
	for _, s := range a.Tools.Specs() {
		if strings.HasPrefix(s.Name, browser.Prefix) {
			n++
		}
	}
	if n != len(browser.ToolNames) {
		t.Fatalf("and moving back restores it, once: %d", n)
	}
}

func TestOneBrowserPerEnvironmentWithTheRightSettings(t *testing.T) {
	ws := wsEnv{container: execenv.WorkspaceContainer}
	rt := openWith(t, Config{Env: ws, Browser: BrowserConfig{Options: browser.Options{AllowPrivate: true, Binary: "/opt/chrome"}}})
	rt.NewAgent(AgentOptions{})
	rt.NewAgent(AgentOptions{})
	if len(rt.browsers) != 1 {
		t.Fatalf("two conversations share a browser: %d", len(rt.browsers))
	}
	m := rt.browsers[envKey(ws)]
	if m.Opts.ProfileDir != "/workspace/.browser-profile" || !m.Opts.NoSandbox || !m.Opts.AllowPrivate || m.Opts.Binary != "/opt/chrome" {
		t.Fatalf("%+v", m.Opts)
	}

	// On this machine the profile lives under the Talaria home, and the sandbox stays on.
	on := true
	rt2 := openWith(t, Config{Env: execenv.Local{}, Browser: BrowserConfig{Enabled: &on}})
	rt2.NewAgent(AgentOptions{})
	m2 := rt2.browsers[envKey(execenv.Local{})]
	if m2.Opts.ProfileDir != filepath.Join(rt2.cfg.Home, "browser") || m2.Opts.NoSandbox {
		t.Fatalf("%+v", m2.Opts)
	}

	// Settings given win over what is worked out.
	no := false
	rt3 := openWith(t, Config{Env: ws, Browser: BrowserConfig{Options: browser.Options{ProfileDir: "/mine"}, NoSandbox: &no}})
	rt3.NewAgent(AgentOptions{})
	if o := rt3.browsers[envKey(ws)].Opts; o.ProfileDir != "/mine" || o.NoSandbox {
		t.Fatalf("%+v", o)
	}
}

func TestEnvKeyHandlesEnvironmentsThatCannotBeCompared(t *testing.T) {
	type sliceEnv struct {
		execenv.Local
		tags []string
	}
	// Using one as a map key would panic; the key must be something that can be.
	m := map[any]bool{envKey(sliceEnv{tags: []string{"x"}}): true}
	if !m[envKey(sliceEnv{tags: []string{"x"}})] {
		t.Fatal("the same environment must give the same key")
	}
}
