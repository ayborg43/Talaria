package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// fakeModel is an OpenAI-compatible endpoint that answers every request with one status.
func fakeModel(t *testing.T, status int, reply string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != 200 {
			http.Error(w, `{"error":{"message":"nope"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestAnyProfilesTaskMovesToTheNextModelWhenItsOwnIsNotWorking(t *testing.T) {
	down, downHits := fakeModel(t, 503, "")
	badKey, _ := fakeModel(t, 401, "")
	good, goodHits := fakeModel(t, 200, "from the good one")
	cfg := &config.Config{
		FallbackProfiles: []string{"badkey", "good"},
		Profiles: map[string]config.Profile{
			"main":   {Type: "openai", BaseURL: down.URL, APIKey: "k1", Model: "m1"},
			"badkey": {Type: "openai", BaseURL: badKey.URL, APIKey: "k2", Model: "m2"},
			"good":   {Type: "openai", BaseURL: good.URL, APIKey: "k3", Model: "m3"},
		},
	}
	health := provider.NewHealth()
	def, _ := cfg.Resolve(config.Overrides{Profile: "main"}, envFn())
	b := gatewayBackends(cfg, def, provider.NewOpenAI(def.BaseURL, def.APIKey), envFn(), health, nil)

	// "main" is down and "badkey" is refused: the answer comes from "good", whichever profile the task asked for.
	for _, name := range []string{"badkey", "good"} {
		be, err := b.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		_ = be
	}
	be, err := b.Open("main")
	if err != nil {
		t.Fatal(err)
	}
	// the default keeps the provider built at startup; build the chain the way startup does
	chain := withFallbacks(cfg, envFn(), health, nil, "main", "m1", be.Provider)
	msg, err := chain.Chat(t.Context(), provider.Request{Model: "m1", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil || msg.Content != "from the good one" {
		t.Fatalf("%q %v", msg.Content, err)
	}
	if !health.Get("main").Working && downHits.Load() == 0 {
		t.Error("the failing model was never asked")
	}
	if st := health.Get("badkey"); st.Working || !st.Hard || !strings.Contains(st.Reason, "key was refused") {
		t.Errorf("badkey: %+v", st)
	}
	// the next task does not go back to the models that just failed
	before := downHits.Load()
	chain.Chat(t.Context(), provider.Request{Model: "m1", Messages: []provider.Message{{Role: provider.RoleUser, Content: "again"}}})
	if downHits.Load() != before || goodHits.Load() != 2 {
		t.Errorf("down asked again: %d -> %d, good answered %d times", before, downHits.Load(), goodHits.Load())
	}
}

func TestAProfileThatListsItsOwnFallbacksUsesThose(t *testing.T) {
	cfg := &config.Config{
		FallbackProfile:  "x",
		FallbackProfiles: []string{"y"},
		Profiles: map[string]config.Profile{
			"a": {Model: "a"}, "b": {Model: "b", Fallbacks: []string{"a", "b", "ghost", "a"}},
			"x": {Model: "x"}, "y": {Model: "y"},
		},
	}
	if got := strings.Join(cfg.FallbackChain("a"), ","); got != "x,y" {
		t.Errorf("shared chain: %s", got)
	}
	if got := strings.Join(cfg.FallbackChain("b"), ","); got != "a" {
		t.Errorf("its own, without itself, repeats or a name that is not defined: %s", got)
	}
	if got := strings.Join(cfg.FallbackChain("x"), ","); got != "y" {
		t.Errorf("a fallback does not fall back to itself: %s", got)
	}
}

func TestModelListTellsTheAgentWhatEachModelIsForAndWhatIsFailing(t *testing.T) {
	down, _ := fakeModel(t, 503, "")
	good, _ := fakeModel(t, 200, "ok")
	cfg := &config.Config{
		FallbackProfiles: []string{"good"},
		Profiles: map[string]config.Profile{
			"main":  {Type: "openai", BaseURL: down.URL, APIKey: "k", Model: "m1", Notes: "my usual one"},
			"good":  {Type: "openai", BaseURL: good.URL, APIKey: "k", Model: "m3", BestFor: []string{"cheap", "fast"}, Notes: "free tier, fine for summaries"},
			"nokey": {Type: "openai", APIKeyEnv: "NOPE_KEY", Model: "m4"},
		},
	}
	health := provider.NewHealth()
	def, _ := cfg.Resolve(config.Overrides{Profile: "main"}, envFn())
	b := gatewayBackends(cfg, def, provider.NewOpenAI(def.BaseURL, def.APIKey), envFn(), health, nil)
	chain := withFallbacks(cfg, envFn(), health, nil, "main", "m1", provider.NewOpenAI(down.URL, "k"))
	chain.Chat(t.Context(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})

	rt := &app.Runtime{}
	rt.SetModels(modelList(cfg, b, health))
	reg := tool.NewRegistry()
	app.RegisterModelToolForTest(reg, rt)
	list, _ := reg.Get("model_list")
	out, err := list.Run(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main: m1", "used by default", "my usual one", "NOT WORKING NOW: the service is down or overloaded (503)", "good: m3", "cheap, fast", "free tier, fine for summaries", "If it fails: good", "nokey", "NOT WORKING NOW"} {
		if !strings.Contains(out, want) {
			t.Errorf("model_list lacks %q:\n%s", want, out)
		}
	}
	if note := modelNote(cfg, health, "main"); !strings.Contains(note, "my usual one") || !strings.Contains(note, "NOT WORKING NOW") {
		t.Errorf("the note a delegated task's model choice carries: %q", note)
	}
	var _ = chat.Backends{}
}
