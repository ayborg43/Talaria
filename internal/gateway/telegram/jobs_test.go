package telegram

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/agent"
	"talaria/internal/cron"
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// A job scheduled from a chat starts out with the model and environment the chat
// has chosen at that moment, read from the gateway's own state.
func TestJobsTakeTheChatsCurrentModelAndEnvironment(t *testing.T) {
	f := newFake(t)
	k := newEnvKit(t)
	store := &cron.Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	var g *Gateway

	// The model asks to schedule a job whenever the user says "schedule it".
	scheduler := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		last := req.Messages[len(req.Messages)-1]
		switch {
		case last.Role == provider.RoleTool:
			return provider.Message{Role: provider.RoleAssistant, Content: "done: " + last.Content}, nil
		case strings.HasSuffix(last.Content, "schedule it"): // a note about a switch may come first
			return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "cron_add", Arguments: `{"schedule":"@daily","prompt":"report"}`}}}, nil
		}
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
	})
	// The gateway points each chat's agent at the backend the chat chose, so both must schedule.
	backends := &chat.Backends{Default: "a", Names: []string{"a", "b"}, Open: func(name string) (chat.Backend, error) {
		return chat.Backend{Provider: scheduler, Model: "model-" + name, Desc: name}, nil
	}}
	factory := func(chatID int64, approve func(string, string) bool) *agent.Agent {
		reg := tool.NewRegistry()
		tool.RegisterBuiltinsIn(reg, k.def)
		cron.RegisterTools(reg, store, "", chatID, cron.Targets{
			Profiles: []string{"a", "b"}, Environments: []string{"local", "sandbox"},
			DefaultProfile: "a", DefaultEnvironment: "local",
			Current: func() (string, string) { return g.Selection(chatID) },
		})
		return &agent.Agent{Provider: scheduler, Model: "m", Tools: reg, Approve: func(string, string) bool { return true }}
	}
	start(t, f, []int64{1, 2}, factory,
		func(gw *Gateway) { g = gw },
		withBackends(backends, nil),
		k.with([]string{"local", "sandbox"}, nil))

	// Nothing chosen yet: the defaults, and a job that follows them.
	if p, e := g.Selection(404); p != "" || e != "" {
		t.Fatalf("a chat that has not spoken has made no choices: %q %q", p, e)
	}
	say(f, 1, 1, 1, "schedule it")
	f.waitFor("the first job", func() bool { return f.count("scheduled job") == 1 })

	say(f, 2, 1, 1, "/model b")
	f.waitFor("the model switch", func() bool { return f.sawText("Switched to b") })
	say(f, 3, 1, 1, "/env sandbox")
	f.waitFor("the environment switch", func() bool { return f.sawText("Switched to sandbox") })
	say(f, 4, 1, 1, "schedule it")
	f.waitFor("the second job", func() bool { return f.count("scheduled job") == 2 })

	// Another chat is unaffected by the first chat's choices.
	say(f, 5, 2, 2, "schedule it")
	f.waitFor("the third job", func() bool { return f.count("scheduled job") == 3 })

	jobs, err := store.List()
	if err != nil || len(jobs) != 3 {
		t.Fatalf("%+v %v", jobs, err)
	}
	for i, want := range []struct {
		chat    int64
		profile string
		env     string
	}{{1, "", ""}, {1, "b", "sandbox"}, {2, "", ""}} {
		if jobs[i].ChatID != want.chat || jobs[i].Profile != want.profile || jobs[i].Environment != want.env {
			t.Errorf("job %d: chat %d, model %q, environment %q; want %d, %q, %q", i, jobs[i].ChatID, jobs[i].Profile, jobs[i].Environment, want.chat, want.profile, want.env)
		}
	}
	if !f.sawText("it runs with the b model profile and the sandbox environment") || !f.sawText("the a (default) model profile and the local (default) environment") {
		t.Fatalf("the user is told where each job will run: %q", f.texts())
	}
}
