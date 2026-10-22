package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/calendar"
	"talaria/internal/execenv"
	"talaria/internal/memory"
	"talaria/internal/provider"
	"talaria/internal/subagent"
)

type chatFunc func(ctx context.Context, req provider.Request) (provider.Message, error)

func (f chatFunc) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	return f(ctx, req)
}

func open(t *testing.T, p provider.Provider, limits subagent.Limits) (*Runtime, string) {
	home := t.TempDir()
	rt := Open(Config{Home: home, Provider: p, Model: "m", Subagents: limits}, func(string, ...any) {})
	t.Cleanup(rt.Close)
	return rt, home
}

func idle() provider.Provider {
	return chatFunc(func(context.Context, provider.Request) (provider.Message, error) {
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
	})
}

func hasTool(req provider.Request, name string) bool {
	for _, t := range req.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func TestDelegateIsOfferedUnlessDisabled(t *testing.T) {
	for _, tc := range []struct {
		limits subagent.Limits
		want   bool
	}{{subagent.Limits{}, true}, {subagent.Limits{Disabled: true}, false}} {
		var seen provider.Request
		p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
			seen = req
			return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
		})
		rt, _ := open(t, p, tc.limits)
		if _, err := rt.NewAgent(AgentOptions{}).Run(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		if hasTool(seen, "delegate") != tc.want {
			t.Errorf("limits %+v: delegate offered = %v, want %v", tc.limits, !tc.want, tc.want)
		}
	}
}

func TestSubagentInheritsTheUsersNotesAndModel(t *testing.T) {
	var subSystem, subModel string
	var subHadDelegate bool
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		switch {
		case strings.Contains(req.Messages[0].Content, "You are a subagent"):
			subSystem, subModel, subHadDelegate = req.Messages[0].Content, req.Model, hasTool(req, "delegate")
			return provider.Message{Role: provider.RoleAssistant, Content: "the subagent says hi"}, nil
		case last.Role == provider.RoleTool:
			return provider.Message{Role: provider.RoleAssistant, Content: "relayed: " + last.Content}, nil
		}
		args, _ := json.Marshal(map[string]string{"task": "say hi"})
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "delegate", Arguments: string(args)}}}, nil
	})
	rt, home := open(t, p, subagent.Limits{})
	if err := (&memory.Notes{Dir: filepath.Join(home, "memories")}).Add(memory.User, "likes brevity"); err != nil {
		t.Fatal(err)
	}

	got, err := rt.NewAgent(AgentOptions{}).Run(context.Background(), "go")
	if err != nil || got != "relayed: the subagent says hi" {
		t.Fatalf("%q %v", got, err)
	}
	if !strings.Contains(subSystem, "likes brevity") {
		t.Fatalf("a subagent knows what the main agent knows about the user: %q", subSystem)
	}
	if subModel != "m" || subHadDelegate {
		t.Fatalf("model %q, subagent has delegate: %v", subModel, subHadDelegate)
	}
}

// remoteEnv is an environment somewhere else, as far as the app can tell.
type remoteEnv struct{ execenv.Local }

func (remoteEnv) Describe() string { return "the build server (ci@build.example.com)" }
func (remoteEnv) Remote() bool     { return true }

func TestTheModelIsToldWhereItsToolsRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  execenv.Env
		want bool
	}{{"remote", remoteEnv{}, true}, {"this machine", nil, false}} {
		var system string
		var shellDesc string
		p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
			system = req.Messages[0].Content
			for _, tl := range req.Tools {
				if tl.Name == "shell" {
					shellDesc = tl.Description
				}
			}
			return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
		})
		home := t.TempDir()
		rt := Open(Config{Home: home, Provider: p, Model: "m", Env: tc.env}, func(string, ...any) {})
		if _, err := rt.NewAgent(AgentOptions{}).Run(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		rt.Close()

		told := strings.Contains(system, "Your shell and file tools run in the build server (ci@build.example.com).\nThat is not the user's computer")
		if told != tc.want {
			t.Errorf("%s: system prompt mentions the environment = %v, want %v\n%s", tc.name, told, tc.want, system)
		}
		if tc.want && !strings.Contains(shellDesc, "the build server") {
			t.Errorf("%s: the shell tool itself should name the environment: %q", tc.name, shellDesc)
		}
		if !tc.want && strings.Contains(system, "not the user's computer") {
			t.Errorf("%s: nothing to say when running locally", tc.name)
		}
	}
}

func TestSubagentsAreToldToo(t *testing.T) {
	var subSystem string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		switch {
		case strings.Contains(req.Messages[0].Content, "You are a subagent"):
			subSystem = req.Messages[0].Content
			return provider.Message{Role: provider.RoleAssistant, Content: "done"}, nil
		case last.Role == provider.RoleTool:
			return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
		}
		args, _ := json.Marshal(map[string]string{"task": "look around"})
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "delegate", Arguments: string(args)}}}, nil
	})
	rt := Open(Config{Home: t.TempDir(), Provider: p, Model: "m", Env: remoteEnv{}}, func(string, ...any) {})
	defer rt.Close()
	if _, err := rt.NewAgent(AgentOptions{}).Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subSystem, "the build server") {
		t.Fatalf("a subagent uses the same tools, so it must know where they run: %q", subSystem)
	}
}

// The tools really do use the environment the app was given.
func TestToolsRunInTheConfiguredEnvironment(t *testing.T) {
	var result string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == provider.RoleTool {
			result = last.Content
			return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
		}
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "list_dir", Arguments: `{"path":"/anywhere"}`}}}, nil
	})
	rt := Open(Config{Home: t.TempDir(), Provider: p, Model: "m", Env: listingEnv{}}, func(string, ...any) {})
	defer rt.Close()
	if _, err := rt.NewAgent(AgentOptions{}).Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if result != "from-the-environment.txt\n" {
		t.Fatalf("list_dir should have asked the environment, got %q", result)
	}
}

type listingEnv struct{ remoteEnv }

func (listingEnv) ListDir(context.Context, string) ([]string, error) {
	return []string{"from-the-environment.txt"}, nil
}

// namedEnv is an environment that says which one it is wherever it is used.
type namedEnv struct {
	execenv.Local
	name   string
	remote bool
}

func (e namedEnv) Describe() string { return "the " + e.name + " host" }
func (e namedEnv) Remote() bool     { return e.remote }
func (e namedEnv) ListDir(context.Context, string) ([]string, error) {
	return []string{"in-" + e.name}, nil
}

// Moving a live agent to another environment must change everything that
// mentions or uses it, and leave the conversation alone.
func TestUseEnvironmentMovesALiveAgent(t *testing.T) {
	type seen struct{ system, shellDesc, listing string }
	var requests []seen
	var subSystem string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		if strings.Contains(req.Messages[0].Content, "You are a subagent") {
			subSystem = req.Messages[0].Content
			return provider.Message{Role: provider.RoleAssistant, Content: "sub done"}, nil
		}
		var s seen
		s.system = req.Messages[0].Content
		for _, tl := range req.Tools {
			if tl.Name == "shell" {
				s.shellDesc = tl.Description
			}
		}
		if last.Role == provider.RoleTool {
			s.listing = last.Content
			requests = append(requests, s)
			return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
		}
		requests = append(requests, s)
		switch last.Content {
		case "delegate":
			return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "delegate", Arguments: `{"task":"look"}`}}}, nil
		}
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "list_dir", Arguments: `{"path":"/"}`}}}, nil
	})
	rt := Open(Config{Home: t.TempDir(), Provider: p, Model: "m", Env: namedEnv{name: "first", remote: true}}, func(string, ...any) {})
	defer rt.Close()
	ag := rt.NewAgent(AgentOptions{})

	run := func(text string) seen {
		t.Helper()
		if _, err := ag.Run(context.Background(), text); err != nil {
			t.Fatal(err)
		}
		return requests[len(requests)-1]
	}

	first := run("list")
	if first.listing != "in-first\n" || !strings.Contains(first.system, "run in the first host.") || !strings.Contains(first.shellDesc, "the first host") {
		t.Fatalf("before: %+v", first)
	}

	rt.UseEnvironment(ag, namedEnv{name: "second", remote: true})
	second := run("list")
	if second.listing != "in-second\n" {
		t.Fatalf("the tools must act in the new environment: %q", second.listing)
	}
	if strings.Contains(second.system, "first host") || !strings.Contains(second.system, "run in the second host.") {
		t.Fatalf("the system prompt must say the new one and never the old: %q", second.system)
	}
	if !strings.Contains(second.shellDesc, "the second host") || strings.Contains(second.shellDesc, "first") {
		t.Fatalf("the tool descriptions too: %q", second.shellDesc)
	}

	// Subagents started after the move work in, and are told about, the new one.
	run("delegate")
	if !strings.Contains(subSystem, "the second host") || strings.Contains(subSystem, "first") {
		t.Fatalf("subagent background: %q", subSystem)
	}

	// Moving to this machine removes the note altogether.
	rt.UseEnvironment(ag, namedEnv{name: "here", remote: false})
	third := run("list")
	if third.listing != "in-here\n" || strings.Contains(third.system, "not the user's computer") || strings.Contains(third.shellDesc, "not the user's computer") {
		t.Fatalf("a local environment needs no note: %+v", third)
	}
}

func TestUseEnvironmentKeepsTheOtherTools(t *testing.T) {
	var names []string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		names = nil
		for _, tl := range req.Tools {
			names = append(names, tl.Name)
		}
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
	})
	rt := Open(Config{Home: t.TempDir(), Provider: p, Model: "m"}, func(string, ...any) {})
	defer rt.Close()
	ag := rt.NewAgent(AgentOptions{})
	ag.Run(context.Background(), "x")
	before := append([]string(nil), names...)

	rt.UseEnvironment(ag, namedEnv{name: "other", remote: true})
	ag.Run(context.Background(), "y")
	if strings.Join(before, ",") != strings.Join(names, ",") {
		t.Fatalf("switching replaces the four tools in place and adds or drops nothing:\nbefore %v\nafter  %v", before, names)
	}
}

// Everything Talaria keeps is private to the user: the history is every conversation.
func TestStateIsKeptOwnerOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	os.MkdirAll(home, 0o755) // made by an older version, open to others
	rt := Open(Config{Home: home, Provider: nil}, func(string, ...any) {})
	defer rt.Close()
	rt.notes.Add("memory", "likes tea")
	if fi, err := os.Stat(home); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the home directory: %v %v", fi, err)
	}
	err := filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		fi, _ := d.Info()
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by others: %v", p, fi.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions.db")); err != nil {
		t.Fatal("the history was created")
	}
}

func TestTheCalendarToolIsOfferedOnlyWhenACalendarIsSetUp(t *testing.T) {
	home := t.TempDir()
	with := Open(Config{Home: home, Provider: nil, Calendars: &calendar.Service{Sources: []calendar.Source{{Name: "p", URL: "https://example.com/a.ics"}}}}, func(string, ...any) {})
	defer with.Close()
	if _, ok := with.NewAgent(AgentOptions{}).Tools.Get("calendar_events"); !ok || !with.HasCalendars() {
		t.Fatal("the tool is missing though a calendar is set up")
	}
	without := Open(Config{Home: t.TempDir(), Provider: nil}, func(string, ...any) {})
	defer without.Close()
	if _, ok := without.NewAgent(AgentOptions{}).Tools.Get("calendar_events"); ok || without.HasCalendars() {
		t.Fatal("a tool for something that is not there")
	}
}
