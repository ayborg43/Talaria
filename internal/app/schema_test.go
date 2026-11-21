package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"talaria/internal/cron"
	"talaria/internal/files"
	"talaria/internal/goal"
	"talaria/internal/kanban"
	"talaria/internal/phone"
	"talaria/internal/provider"
	"talaria/internal/watch"
)

type nop struct{}

func (nop) Chat(context.Context, provider.Request) (provider.Message, error) {
	return provider.Message{}, nil
}

// A tool whose schema is not valid JSON makes every request to the model fail, so every tool a chat gets must parse.
func TestEveryToolHasValidSchema(t *testing.T) {
	home := t.TempDir()
	on := true
	rt := Open(Config{Home: home, Provider: nop{}, Model: "m", Goals: &goal.Store{Path: filepath.Join(home, "s.db")},
		Watches: &watch.Store{Path: filepath.Join(home, "s.db")}, Cards: &kanban.Store{Path: filepath.Join(home, "s.db")}, Phone: phone.NewHub(), Browser: BrowserConfig{Enabled: &on}, Files: &files.Store{Dir: filepath.Join(home, "files")}})
	rt.SetUsage(func(int) string { return "" })
	rt.SetDigest(func(time.Duration) string { return "" })
	rt.SetKanban(&kanban.Dispatcher{Cards: rt.Cards()})
	a := rt.NewAgent(AgentOptions{ChatID: 1, Gateway: "web", Tasks: &cron.Scheduler{}})
	seen := map[string]bool{}
	for _, s := range a.Tools.Specs() {
		seen[s.Name] = true
		if !json.Valid(s.Parameters) {
			t.Errorf("tool %s has an invalid schema: %s", s.Name, s.Parameters)
		}
	}
	for _, want := range []string{"kanban_add", "kanban_move", "while_away", "usage_summary", "phone_do", "delegate"} {
		if !seen[want] {
			t.Errorf("%s is not among the tools checked", want)
		}
	}
}

// The same, through the real request: the whole tool list must marshal into what is sent to the model.
func TestWholeRequestMarshals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	home := t.TempDir()
	on := true
	rt := Open(Config{Home: home, Provider: nop{}, Model: "m", Goals: &goal.Store{Path: filepath.Join(home, "s.db")},
		Watches: &watch.Store{Path: filepath.Join(home, "s.db")}, Cards: &kanban.Store{Path: filepath.Join(home, "s.db")}, Phone: phone.NewHub(), Browser: BrowserConfig{Enabled: &on}, Files: &files.Store{Dir: filepath.Join(home, "files")}})
	rt.SetUsage(func(int) string { return "" })
	rt.SetDigest(func(time.Duration) string { return "" })
	rt.SetKanban(&kanban.Dispatcher{Cards: rt.Cards()})
	a := rt.NewAgent(AgentOptions{ChatID: 1, Gateway: "web", Tasks: &cron.Scheduler{}})
	a.Provider = provider.NewOpenAI(srv.URL, "k")
	a.Model = "m"
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
}
