package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/guard"
	"talaria/internal/memory"
	"talaria/internal/provider"
	"talaria/internal/subagent"
)

func openGuarded(t *testing.T, p provider.Provider, rules map[string]string) (*Runtime, string, *guard.Guard) {
	t.Helper()
	home := t.TempDir()
	pol, err := guard.ParsePolicy(rules, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &guard.Guard{
		Policy: pol,
		Grants: &guard.Grants{Path: filepath.Join(home, "grants.json")},
		Audit:  &guard.Log{Path: filepath.Join(home, "audit.log")},
		Redact: guard.NewRedactor([]string{"hunter2hunter2"}),
	}
	rt := Open(Config{Home: home, Provider: p, Model: "m", Guard: g}, func(string, ...any) {})
	t.Cleanup(rt.Close)
	return rt, home, g
}

func toolCall(name, args string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: name, Arguments: args}}}
}

func replies(msgs ...provider.Message) provider.Provider {
	i := 0
	return chatFunc(func(context.Context, provider.Request) (provider.Message, error) {
		m := msgs[min(i, len(msgs)-1)]
		i++
		return m, nil
	})
}

// What a user types, and what the model says, is kept in searchable history
// that outlives the conversation: a secret must not be in it.
func TestHistoryDoesNotKeepSecrets(t *testing.T) {
	rt, home, _ := openGuarded(t, replies(provider.Message{Role: provider.RoleAssistant, Content: "I will use hunter2hunter2 for that"}), nil)
	a := rt.NewAgent(AgentOptions{})
	if _, err := a.Run(context.Background(), "my password is hunter2hunter2, remember uniqueword"); err != nil {
		t.Fatal(err)
	}
	rt.Close()
	s, err := memory.OpenSessions(filepath.Join(home, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hits, err := s.Search("uniqueword", 5, "")
	if err != nil || len(hits) != 1 {
		t.Fatalf("%v %v", hits, err)
	}
	if strings.Contains(hits[0].Snippet, "hunter2") {
		t.Fatalf("a secret reached the history: %q", hits[0].Snippet)
	}
	hits, _ = s.Search("hunter2hunter2", 5, "")
	for _, h := range hits {
		if strings.Contains(h.Snippet, "hunter2hunter2") {
			t.Fatalf("%+v", h)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(home, "sessions.db"))
	wal, _ := os.ReadFile(filepath.Join(home, "sessions.db-wal"))
	if strings.Contains(string(raw)+string(wal), "hunter2hunter2") {
		t.Fatal("the secret is in the database file")
	}
}

func TestToolOutputIsScrubbedAndLogged(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("PASSWORD=hunter2hunter2\n"), 0o600)
	var seen string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		if last := req.Messages[len(req.Messages)-1]; last.Role == provider.RoleTool {
			seen = last.Content
			return provider.Message{Role: provider.RoleAssistant, Content: "done"}, nil
		}
		return toolCall("read_file", `{"path":"`+path+`"}`), nil
	})
	rt, home, g := openGuarded(t, p, nil)
	a := rt.NewAgent(AgentOptions{ChatID: 5, Gateway: "discord"})
	if _, err := a.Run(context.Background(), "read it"); err != nil {
		t.Fatal(err)
	}
	if seen != "PASSWORD="+guard.Mask+"\n" {
		t.Fatalf("the model saw %q", seen)
	}
	recs, _ := g.Audit.Recent(guard.Filter{Gateway: "discord", Chat: 5}, 5)
	if len(recs) != 1 || recs[0].Tool != "read_file" || recs[0].Origin != "chat" || recs[0].Status != "ok" {
		t.Fatalf("%+v", recs)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "audit.log"))
	if strings.Contains(string(raw), "hunter2hunter2") {
		t.Fatalf("a secret is in the log: %s", raw)
	}
}

func TestOriginsAndGatewaysAreRecorded(t *testing.T) {
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		if req.Messages[len(req.Messages)-1].Role == provider.RoleTool {
			return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
		}
		return toolCall("list_dir", `{"path":"."}`), nil
	})
	rt, _, g := openGuarded(t, p, nil)
	for _, o := range []AgentOptions{
		{},          // the command line
		{ChatID: 3}, // a chat with no gateway named is Telegram's
		{ChatID: 4, Gateway: "discord"},
		{Origin: "job:abc", ChatID: 5, Gateway: "telegram", NoCron: true},
	} {
		a := rt.NewAgent(o)
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
	}
	recs, _ := g.Audit.Recent(guard.Filter{}, 10)
	type key struct {
		origin, gateway string
		chat            int64
	}
	var got []key
	for _, r := range recs {
		got = append(got, key{r.Origin, r.Gateway, r.Chat})
	}
	want := []key{{"cli", "", 0}, {"chat", "telegram", 3}, {"chat", "discord", 4}, {"job:abc", "telegram", 5}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d: %v, want %v", i, got[i], want[i])
		}
	}
}

// A scheduled job has the chat it reports to, but no way to schedule more jobs.
func TestNoCronKeepsJobsFromSchedulingJobs(t *testing.T) {
	var offered bool
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		offered = hasTool(req, "cron_add")
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
	})
	rt, _, _ := openGuarded(t, p, nil)
	rt.NewAgent(AgentOptions{ChatID: 1, NoCron: true}).Run(context.Background(), "x")
	if offered {
		t.Fatal("a job was offered cron_add")
	}
	rt.NewAgent(AgentOptions{ChatID: 1}).Run(context.Background(), "x")
	if !offered {
		t.Fatal("a chat is offered cron_add")
	}
}

// Delegating is no way round a rule: a subagent is bound by the same rules.
func TestRulesBindSubagentsToo(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
	delegate, _ := json.Marshal(map[string]string{"task": "write the file", "name": "writer"})
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		sub := strings.Contains(req.Messages[0].Content, "You are a subagent")
		last := req.Messages[len(req.Messages)-1]
		switch {
		case sub && last.Role != provider.RoleTool:
			return toolCall("write_file", string(args)), nil
		case sub, last.Role == provider.RoleTool:
			return provider.Message{Role: provider.RoleAssistant, Content: last.Content}, nil
		}
		return toolCall("delegate", string(delegate)), nil
	})
	home := t.TempDir()
	pol, _ := guard.ParsePolicy(map[string]string{"write_file": "deny"}, nil)
	g := &guard.Guard{Policy: pol, Audit: &guard.Log{Path: filepath.Join(home, "audit.log")}}
	rt := Open(Config{Home: home, Provider: p, Model: "m", Guard: g, Subagents: subagent.Limits{}}, func(string, ...any) {})
	defer rt.Close()
	a := rt.NewAgent(AgentOptions{Approve: func(string, string) bool { t.Error("asked about a denied tool"); return true }})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the subagent wrote a file the rules forbid")
	}
	denied, _ := g.Audit.Recent(guard.Filter{Denied: true}, 5)
	if len(denied) != 1 || denied[0].Label != "writer" {
		t.Fatalf("%+v", denied)
	}
}

func TestNoGuardChangesNothing(t *testing.T) {
	rt, _ := open(t, replies(provider.Message{Role: provider.RoleAssistant, Content: "ok"}), subagent.Limits{})
	a := rt.NewAgent(AgentOptions{})
	if a.Gate != nil {
		t.Fatal("no guard configured, so no gate")
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
}

// A watch polls every few minutes: its runs are not conversations to keep.
func TestNoHistoryKeepsARunOutOfTheSavedHistory(t *testing.T) {
	rt, home, _ := openGuarded(t, replies(provider.Message{Role: provider.RoleAssistant, Content: "nothing for you"}), nil)
	if _, err := rt.NewAgent(AgentOptions{NoHistory: true}).Run(context.Background(), "polled-marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.NewAgent(AgentOptions{}).Run(context.Background(), "kept-marker"); err != nil {
		t.Fatal(err)
	}
	rt.Close()
	s, err := memory.OpenSessions(filepath.Join(home, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if hits, _ := s.Search("polledmarker OR polled", 5, ""); len(hits) != 0 {
		t.Fatalf("kept: %+v", hits)
	}
	if hits, _ := s.Search("kept", 5, ""); len(hits) != 1 {
		t.Fatalf("the ordinary run is kept: %+v", hits)
	}
}
