package web

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/evals"
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
)

type replies map[string]string

func (r replies) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	return provider.Message{Content: r[req.Messages[len(req.Messages)-1].Content]}, nil
}

func evalsRig(t *testing.T) *rig {
	r := newRig(t)
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{"lite": {Model: "m1"}, "main": {Model: "m2"}}}
	r.srv.Evals = &evals.Store{Path: filepath.Join(t.TempDir(), "evals.json")}
	answers := map[string]provider.Provider{
		"lite": replies{"capital of France": "Paris", "2+2": "five"},
		"main": replies{"capital of France": "Paris", "2+2": "4"},
	}
	r.srv.Engine.Backends = &chat.Backends{Default: "main", Names: []string{"lite", "main"},
		Open: func(n string) (chat.Backend, error) { return chat.Backend{Model: "m-" + n, Provider: answers[n]}, nil },
		OpenRaw: func(n string) (chat.Backend, error) {
			if p, ok := answers[n]; ok {
				return chat.Backend{Model: "m-" + n, Provider: p}, nil
			}
			return chat.Backend{}, context.DeadlineExceeded
		},
	}
	r.login()
	return r
}

func readEvals(t *testing.T, r *rig) evalsView {
	t.Helper()
	_, b := r.do("GET", "/api/evals", nil)
	var v evalsView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func waitIdle(t *testing.T, r *rig) evalsView {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if v := readEvals(t, r); v.Running == "" {
			return v
		}
	}
	t.Fatal("the run never finished")
	return evalsView{}
}

func TestTestQuestionsCanBeWrittenRunOnAModelAndScored(t *testing.T) {
	r := evalsRig(t)
	for _, c := range []evals.Case{{Prompt: "capital of France", Expect: []string{"Paris"}}, {Prompt: "2+2", Expect: []string{"4"}}} {
		if resp, _ := r.do("POST", "/api/evals", map[string]any{"add": c}); resp.StatusCode != 200 {
			t.Fatalf("add: %d", resp.StatusCode)
		}
	}
	if resp, _ := r.do("POST", "/api/evals", map[string]any{"add": evals.Case{Prompt: " "}}); resp.StatusCode != 400 {
		t.Errorf("an empty question: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/evals", map[string]any{"run": "lite"}); resp.StatusCode != 200 {
		t.Fatalf("run: %d", resp.StatusCode)
	}
	v := waitIdle(t, r)
	if len(v.Runs) != 1 || v.Runs[0].Profile != "lite" || v.Runs[0].Passed != 1 || v.Runs[0].Counted != 2 {
		t.Fatalf("%+v", v.Runs)
	}
	r.do("POST", "/api/evals", map[string]any{"run": "main"})
	if v = waitIdle(t, r); v.Runs[0].Profile != "main" || v.Runs[0].Passed != 2 {
		t.Errorf("the stronger model: %+v", v.Runs[0])
	}
	// the score shows against the profile in Settings
	_, body := r.do("GET", "/api/settings", nil)
	if !strings.Contains(string(body), "1 of 2 on your test questions") || !strings.Contains(string(body), "2 of 2 on your test questions") {
		t.Errorf("settings do not show the scores: %s", body)
	}
}

func TestARunNeedsQuestionsAWorkingModelAndOnlyOneAtATime(t *testing.T) {
	r := evalsRig(t)
	if resp, _ := r.do("POST", "/api/evals", map[string]any{"run": "lite"}); resp.StatusCode != 400 {
		t.Errorf("no questions: %d", resp.StatusCode)
	}
	r.srv.Evals.Add(evals.Case{Prompt: "2+2", Expect: []string{"4"}})
	if resp, _ := r.do("POST", "/api/evals", map[string]any{"run": "ghost"}); resp.StatusCode != 400 {
		t.Errorf("a model that cannot be used: %d", resp.StatusCode)
	}
	r.srv.evalRun.mu.Lock()
	r.srv.evalRun.running = "lite"
	r.srv.evalRun.mu.Unlock()
	if resp, _ := r.do("POST", "/api/evals", map[string]any{"run": "main"}); resp.StatusCode != 409 {
		t.Errorf("a second run: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/evals", map[string]any{"delete": "nope"}); resp.StatusCode != 404 {
		t.Errorf("deleting a missing question: %d", resp.StatusCode)
	}
}
