package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/gateway/chat"
	"talaria/internal/guard"
	"talaria/internal/provider"
)

// jobModel asks to list a directory and reports "<its name>:<what the tool returned>",
// so a job's result shows which model ran it and which environment its tool acted in.
type jobModel struct {
	name string

	mu     sync.Mutex
	calls  int
	models []string
}

func (m *jobModel) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	m.mu.Lock()
	m.calls++
	m.models = append(m.models, req.Model)
	m.mu.Unlock()
	last := req.Messages[len(req.Messages)-1]
	if last.Role == provider.RoleTool {
		return provider.Message{Role: provider.RoleAssistant, Content: m.name + ":" + strings.TrimSpace(last.Content)}, nil
	}
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "list_dir", Arguments: `{"path":"/"}`}}}, nil
}

func (m *jobModel) called() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// listEnv is an environment that names itself in its listings.
type listEnv struct {
	execenv.Local
	name string
}

func (e listEnv) Describe() string { return "the " + e.name + " box" }
func (e listEnv) Remote() bool     { return e.name != "local" }
func (e listEnv) ListDir(context.Context, string) ([]string, error) {
	return []string{"in-" + e.name}, nil
}

type jobKit struct {
	runner *jobRunner
	models map[string]*jobModel
	opened []string // environments the runner asked for
	mu     sync.Mutex
}

// newJobKit offers profiles openai (the default), claude and broken, and environments
// local (the default), sandbox and flaky, which cannot start.
func newJobKit(t *testing.T) *jobKit {
	k := &jobKit{models: map[string]*jobModel{"openai": {name: "openai"}, "claude": {name: "claude"}}}
	rt := app.Open(app.Config{Home: t.TempDir(), Provider: k.models["openai"], Model: "model-openai", Env: listEnv{name: "local"}})
	t.Cleanup(rt.Close)

	backends := &chat.Backends{
		Default: "openai",
		Names:   []string{"broken", "claude", "openai"},
		Open: func(name string) (chat.Backend, error) {
			if name == "broken" {
				return chat.Backend{}, errors.New("needs an API key")
			}
			return chat.Backend{Provider: k.models[name], Model: "model-" + name, Desc: name}, nil
		},
	}
	envs := &chat.Environments{
		Default: "local",
		Names:   []string{"flaky", "local", "sandbox"},
		Get: func(_ context.Context, name string) (execenv.Env, error) {
			k.mu.Lock()
			k.opened = append(k.opened, name)
			k.mu.Unlock()
			if name == "flaky" {
				return nil, errors.New("cannot reach the Docker daemon")
			}
			return listEnv{name: name}, nil
		},
	}
	k.runner = &jobRunner{rt: rt, backends: backends, envs: envs, allowed: map[int64]bool{1: true}}
	return k
}

func job(profile, env string) cron.Job {
	return cron.Job{ID: "j1", Name: "report", Prompt: "do it", ChatID: 1, Profile: profile, Environment: env}
}

func TestJobRunsOnTheDefaultsWhenItAsksForNothing(t *testing.T) {
	k := newJobKit(t)
	out, err := k.runner.run(context.Background(), job("", ""))
	if err != nil || out != "openai:in-local" {
		t.Fatalf("%q %v", out, err)
	}
	if len(k.opened) != 0 {
		t.Fatalf("the default environment is already in place; nothing needs starting: %v", k.opened)
	}
	if k.models["openai"].models[0] != "model-openai" {
		t.Fatalf("%v", k.models["openai"].models)
	}
}

func TestJobRunsOnItsOwnModelAndEnvironment(t *testing.T) {
	k := newJobKit(t)
	out, err := k.runner.run(context.Background(), job("claude", "sandbox"))
	if err != nil || out != "claude:in-sandbox" {
		t.Fatalf("the job's model ran it, and its tool acted in its environment: %q %v", out, err)
	}
	if got := k.models["claude"].models; len(got) != 2 || got[0] != "model-claude" {
		t.Fatalf("the profile's model name is used: %v", got)
	}
	if k.models["openai"].called() != 0 {
		t.Fatal("the default model must not have been involved")
	}

	// One setting at a time works too.
	if out, _ := k.runner.run(context.Background(), job("claude", "")); out != "claude:in-local" {
		t.Fatalf("%q", out)
	}
	if out, _ := k.runner.run(context.Background(), job("", "sandbox")); out != "openai:in-sandbox" {
		t.Fatalf("%q", out)
	}
}

// A job that asks for something it cannot have must fail, not run on the default.
func TestJobThatCannotHaveWhatItAsksForFailsInsteadOfFallingBack(t *testing.T) {
	for name, tc := range map[string]struct {
		job  cron.Job
		want string
	}{
		"profile not offered any more":      {job("retired", ""), `the "retired" model profile, which cannot be used: the gateway does not offer it any more`},
		"profile that cannot be used":       {job("broken", ""), `the "broken" model profile, which cannot be used: needs an API key`},
		"environment not offered":           {job("", "prod"), `the "prod" environment, which cannot be used: the gateway does not offer it any more`},
		"environment that cannot start":     {job("", "flaky"), `the "flaky" environment, which cannot be used: cannot reach the Docker daemon`},
		"a good profile, a bad environment": {job("claude", "flaky"), `the "flaky" environment`},
	} {
		k := newJobKit(t)
		out, err := k.runner.run(context.Background(), tc.job)
		if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
			t.Errorf("%s: got %q, %v; want an error containing %q", name, out, err, tc.want)
		}
		if k.models["openai"].called()+k.models["claude"].called() != 0 {
			t.Errorf("%s: no model may be called when the job cannot run as asked", name)
		}
	}
}

func TestJobsWhenThereIsNothingToChooseFrom(t *testing.T) {
	k := newJobKit(t)
	k.runner.backends, k.runner.envs = nil, nil
	for _, j := range []cron.Job{job("claude", ""), job("", "sandbox")} {
		if _, err := k.runner.run(context.Background(), j); err == nil || !strings.Contains(err.Error(), "does not offer it any more") {
			t.Errorf("%+v: %v", j, err)
		}
	}
	if out, err := k.runner.run(context.Background(), job("", "")); err != nil || out != "openai:in-local" {
		t.Fatalf("jobs on the defaults still run: %q %v", out, err)
	}
}

func TestJobForAChatThatIsNoLongerAllowedIsDropped(t *testing.T) {
	k := newJobKit(t)
	k.runner.allowed = map[int64]bool{}
	if _, err := k.runner.run(context.Background(), job("claude", "sandbox")); !errors.Is(err, cron.ErrDrop) {
		t.Fatalf("%v", err)
	}
	if len(k.opened) != 0 || k.models["claude"].called() != 0 {
		t.Fatal("nothing is started for a job that will not run")
	}
}

// What a job may do unattended is unchanged by where it runs.
func TestJobApprovalsAreUnchanged(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	writer := &writerModel{path: target}
	rt := app.Open(app.Config{Home: t.TempDir(), Provider: writer, Model: "m", Env: listEnv{name: "local"}})
	defer rt.Close()

	for _, tc := range []struct {
		risky bool
		want  string
	}{{false, "denied"}, {true, "wrote"}} {
		os.Remove(target)
		r := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}}
		out, err := r.run(context.Background(), cron.Job{Prompt: "write", ChatID: 1, AllowRisky: tc.risky})
		if err != nil || !strings.Contains(out, tc.want) {
			t.Fatalf("allow_risky=%v: %q %v", tc.risky, out, err)
		}
		if _, statErr := os.Stat(target); (statErr == nil) != tc.risky {
			t.Fatalf("allow_risky=%v but the file exists: %v", tc.risky, statErr == nil)
		}
	}
	yolo := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}, yolo: true}
	os.Remove(target)
	if out, _ := yolo.run(context.Background(), cron.Job{Prompt: "write", ChatID: 1}); !strings.Contains(out, "wrote") {
		t.Fatalf("-yolo allows everything: %q", out)
	}
}

type writerModel struct{ path string }

func (m *writerModel) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	if last := req.Messages[len(req.Messages)-1]; last.Role == provider.RoleTool {
		return provider.Message{Role: provider.RoleAssistant, Content: last.Content}, nil
	}
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: `{"path":"` + m.path + `","content":"x"}`}}}, nil
}

func TestGatewayJobTargets(t *testing.T) {
	cur := func() (string, string) { return "claude", "sandbox" }
	two := &chat.Backends{Default: "openai", Names: []string{"claude", "openai"}, Open: func(n string) (chat.Backend, error) {
		if n == "claude" {
			return chat.Backend{}, errors.New("needs an API key")
		}
		return chat.Backend{}, nil
	}}
	envs := &chat.Environments{Default: "local", Names: []string{"local", "sandbox"}}

	tg := gatewayJobTargets(two, envs, cur)
	if strings.Join(tg.Profiles, ",") != "claude,openai" || tg.DefaultProfile != "openai" ||
		strings.Join(tg.Environments, ",") != "local,sandbox" || tg.DefaultEnvironment != "local" {
		t.Fatalf("%+v", tg)
	}
	if p, e := tg.Current(); p != "claude" || e != "sandbox" {
		t.Fatalf("the chat's own choices are passed through: %q %q", p, e)
	}
	if err := tg.CheckProfile("claude"); err == nil || err.Error() != "needs an API key" {
		t.Fatalf("a job learns at creation that its model cannot be used: %v", err)
	}
	if tg.CheckProfile("openai") != nil {
		t.Fatal("a usable profile passes")
	}

	// One of each is nothing to choose between, and nil means switching is off.
	one := &chat.Backends{Default: "openai", Names: []string{"openai"}}
	if tg := gatewayJobTargets(one, &chat.Environments{Default: "local", Names: []string{"local"}}, cur); len(tg.Profiles) != 0 || len(tg.Environments) != 0 {
		t.Fatalf("%+v", tg)
	}
	if tg := gatewayJobTargets(nil, nil, cur); len(tg.Profiles) != 0 || len(tg.Environments) != 0 || tg.CheckProfile != nil {
		t.Fatalf("%+v", tg)
	}
}

// A scheduled job answers to the same rules as a chat does, and its calls are
// logged as the job's, against the chat it reports to. allow_risky and -yolo
// cannot override a rule that denies a tool; an unattended run is where a
// forbidden tool matters most.
func TestJobsObeyPermissionRulesAndAreLogged(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	home := t.TempDir()
	pol, _ := guard.ParsePolicy(map[string]string{"write_file": "deny"}, nil)
	g := &guard.Guard{Policy: pol, Audit: &guard.Log{Path: filepath.Join(home, "audit.log")}}
	rt := app.Open(app.Config{Home: home, Provider: &writerModel{path: target}, Model: "m", Env: listEnv{name: "local"}, Guard: g})
	defer rt.Close()

	for _, r := range []*jobRunner{
		{rt: rt, allowed: map[int64]bool{7: true}},
		{rt: rt, allowed: map[int64]bool{7: true}, yolo: true},
	} {
		out, err := r.run(context.Background(), cron.Job{ID: "abc", Gateway: "discord", Prompt: "write", ChatID: 7, AllowRisky: true})
		if err != nil || !strings.Contains(out, "permission rules") {
			t.Fatalf("yolo=%v: %q %v", r.yolo, out, err)
		}
		if _, statErr := os.Stat(target); statErr == nil {
			t.Fatalf("yolo=%v: a denied tool ran in a job", r.yolo)
		}
	}
	recs, _ := g.Audit.Recent(guard.Filter{}, 10)
	if len(recs) != 2 || recs[0].Origin != "job:abc" || recs[0].Gateway != "discord" || recs[0].Chat != 7 || recs[0].Allowed {
		t.Fatalf("%+v", recs)
	}
	// And it shows up in that chat's own log.
	chatView, _ := g.Audit.Recent(guard.Filter{Gateway: "discord", Chat: 7}, 10)
	if len(chatView) != 2 {
		t.Fatalf("the chat sees its jobs' activity: %+v", chatView)
	}
}

// A grant the user made in a chat does not extend to that chat's scheduled jobs:
// nobody is there to see what they do.
func TestChatGrantsDoNotCoverJobs(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	home := t.TempDir()
	pol, _ := guard.ParsePolicy(nil, nil)
	grants := &guard.Grants{Path: filepath.Join(home, "grants.json")}
	grants.Add("telegram:1", "write_file")
	g := &guard.Guard{Policy: pol, Grants: grants}
	rt := app.Open(app.Config{Home: home, Provider: &writerModel{path: target}, Model: "m", Env: listEnv{name: "local"}, Guard: g})
	defer rt.Close()
	r := &jobRunner{rt: rt, allowed: map[int64]bool{1: true}}
	out, _ := r.run(context.Background(), cron.Job{ID: "j", Prompt: "write", ChatID: 1}) // not allow_risky
	if !strings.Contains(out, "denied") {
		t.Fatalf("%q", out)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a chat's grant let an unattended job write")
	}
}
