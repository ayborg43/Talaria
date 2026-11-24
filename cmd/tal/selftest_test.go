package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/goal"
	"talaria/internal/kanban"
	"talaria/internal/provider"
	"talaria/internal/watch"
)

func TestCheckToolsCatchesWhatTheAPIsRefuse(t *testing.T) {
	good := provider.ToolSpec{Name: "read_file", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
	if r := checkTools([]provider.ToolSpec{good, {Name: "no_args"}}); r.Status != stOK {
		t.Fatalf("%+v", r)
	}
	cases := map[string]provider.ToolSpec{
		"a name with a space": {Name: "read file", Parameters: good.Parameters},
		"a name too long":     {Name: "mcp_" + strings.Repeat("x", 70), Parameters: good.Parameters},
		"invalid JSON":        {Name: "bad", Parameters: json.RawMessage(`{"type":"object","properties":{"a":"x" "b":1}}`)},
		"not an object":       {Name: "arr", Parameters: json.RawMessage(`{"type":"array"}`)},
		"an empty name":       {Name: "", Parameters: good.Parameters},
	}
	for name, spec := range cases {
		if r := checkTools([]provider.ToolSpec{good, spec}); r.Status != stFail {
			t.Errorf("%s was accepted: %+v", name, r)
		}
	}
	if r := checkTools([]provider.ToolSpec{good, good}); r.Status != stFail || !strings.Contains(r.Detail, "twice") {
		t.Errorf("a repeated name: %+v", r)
	}
	many := make([]provider.ToolSpec, maxTools+1)
	for i := range many {
		many[i] = provider.ToolSpec{Name: fmt.Sprintf("t%d", i)}
	}
	if r := checkTools(many); r.Status != stWarn {
		t.Errorf("too many tools should warn: %+v", r)
	}
}

func TestReportPassesUnlessSomethingFailed(t *testing.T) {
	r := &selftestReport{}
	r.add(checkResult{Status: stOK})
	r.add(checkResult{Status: stWarn})
	r.add(checkResult{Status: stSkip})
	if r.finish(); !r.OK {
		t.Fatal("a warning or a skip failed the run")
	}
	r.add(checkResult{Status: stFail})
	if r.finish(); r.OK {
		t.Fatal("a failure passed")
	}
}

// fakeChat answers like a model: with a tool call when asked to use one, with its result afterwards, otherwise "ok"; a key
// it does not know gets 401, and one that rejects stream_options gets 400.
func fakeChat(t *testing.T, rejectStreamOptions bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			http.Error(w, `{"error":{"message":"bad key"}}`, 401)
			return
		}
		var body struct {
			Messages []struct{ Role, Content string }
			Tools    []json.RawMessage
			Stream   bool
			Options  *json.RawMessage `json:"stream_options"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "bad json: "+err.Error(), 400)
			return
		}
		if rejectStreamOptions && body.Options != nil {
			http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		last := body.Messages[len(body.Messages)-1]
		switch {
		case last.Role == "tool":
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"2026"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
		case strings.Contains(last.Content, "Call the current_time tool"):
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"current_time","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
		default:
			fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"ok (%d tools)"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`, len(body.Tools))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func selftestCLI(t *testing.T, profiles map[string]config.Profile, def string) *cli {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{Profiles: profiles, DefaultProfile: def}
	res, err := cfg.Resolve(config.Overrides{Profile: def}, envFn())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := provider.New(res.Spec())
	rt := app.Open(app.Config{Home: home, Provider: p, Model: res.Model,
		Goals: &goal.Store{Path: filepath.Join(home, "s.db")}, Watches: &watch.Store{Path: filepath.Join(home, "s.db")}, Cards: &kanban.Store{Path: filepath.Join(home, "s.db")}})
	t.Cleanup(rt.Close)
	return &cli{ctx: context.Background(), home: home, cfg: cfg, getenv: envFn(), res: res, p: p, health: provider.NewHealth(), rt: rt,
		log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestSelftestAsksEachProfileItsOwnModelWithEveryToolAndCallsOne(t *testing.T) {
	good := fakeChat(t, false)
	badKey := fakeChat(t, false)
	c := selftestCLI(t, map[string]config.Profile{
		"main":  {Type: "openai", BaseURL: good.URL, APIKey: "good-key", Model: "m-main"},
		"oops":  {Type: "openai", BaseURL: badKey.URL, APIKey: "wrong", Model: "m-oops"},
		"nokey": {Type: "openai", APIKeyEnv: "NO_SUCH_KEY_VAR", Model: "m-nokey"}, // the hosted address needs a key
	}, "main")
	report := &selftestReport{}
	for _, name := range c.profilesToTest() {
		c.selftestProfile(context.Background(), name, false, report, func(checkResult) {})
	}
	report.finish()
	got := map[string]checkResult{}
	for _, r := range report.Checks {
		got[r.Name] = r
	}
	if r := got["main (m-main): request"]; r.Status != stOK || !strings.Contains(r.Detail, "tools attached") {
		t.Errorf("main request: %+v", r)
	}
	if r := got["main (m-main): tool use"]; r.Status != stOK || !strings.Contains(r.Detail, "current_time") {
		t.Errorf("main tool use: %+v", r)
	}
	if r := got["oops (m-oops): request"]; r.Status != stFail || !strings.Contains(r.Detail, "refused") {
		t.Errorf("a refused key should fail and say so: %+v", r)
	}
	if r := got["nokey: request"]; r.Status != stSkip {
		t.Errorf("a profile with no key that is not in use is skipped: %+v", r)
	}
	if report.OK {
		t.Error("a refused key must fail the run")
	}
}

func TestSelftestSaysWhenTheServiceRejectsTheTokenCountOption(t *testing.T) {
	strict := fakeChat(t, true)
	c := selftestCLI(t, map[string]config.Profile{"strict": {Type: "openai", BaseURL: strict.URL, APIKey: "good-key", Model: "m"}}, "strict")
	report := &selftestReport{}
	c.selftestProfile(context.Background(), "strict", true, report, func(checkResult) {})
	if len(report.Checks) != 1 || report.Checks[0].Status != stFail || !strings.Contains(report.Checks[0].Detail, "stream_options") {
		t.Fatalf("%+v", report.Checks)
	}
}

func TestSelftestToolsIncludeWhatOnlyAChatGets(t *testing.T) {
	good := fakeChat(t, false)
	c := selftestCLI(t, map[string]config.Profile{"main": {Type: "openai", BaseURL: good.URL, APIKey: "good-key", Model: "m"}}, "main")
	specs := c.chatAgent(func(string, string) bool { return false }).Tools.Specs()
	have := map[string]bool{}
	for _, s := range specs {
		have[s.Name] = true
	}
	for _, want := range []string{"current_time", "delegate", "kanban_add", "kanban_move", "model_list", "while_away", "agent_create"} {
		if !have[want] {
			t.Errorf("%s is not among the tools a selftest checks", want)
		}
	}
	if r := checkTools(specs); r.Status != stOK {
		t.Fatalf("%+v", r)
	}
}
