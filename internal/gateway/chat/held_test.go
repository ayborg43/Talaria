package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// heldModel answers from a script and keeps what it was asked.
type heldModel struct {
	mu      sync.Mutex
	replies []provider.Message
	seen    [][]provider.Message
}

func (m *heldModel) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, append([]provider.Message(nil), req.Messages...))
	if len(m.replies) == 0 {
		return provider.Message{Role: provider.RoleAssistant, Content: "(nothing more)"}, nil
	}
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

func (m *heldModel) lastUser() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.seen) - 1; i >= 0; i-- {
		msgs := m.seen[i]
		for j := len(msgs) - 1; j >= 0; j-- {
			if msgs[j].Role == provider.RoleUser {
				return msgs[j].Content
			}
		}
	}
	return ""
}

func (m *heldModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}

type wipeLog struct {
	mu   sync.Mutex
	ran  []string
	runs int
}

func (w *wipeLog) get() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.ran...)
}

func heldEngine(t *testing.T, path string, model *heldModel, wl *wipeLog) (*Engine, *fakeService, context.CancelFunc) {
	t.Helper()
	f := newFakeService(4000, false)
	e := &Engine{
		T: f, Allowed: map[int64]bool{1: true}, Stream: false, Destructive: true, HeldPath: path,
		NewAgent: func(_ int64, approve func(string, string) bool) *agent.Agent {
			reg := tool.NewRegistry()
			reg.Register(tool.Tool{Name: "wipe", Risk: tool.RiskDeletes, Run: func(_ context.Context, args json.RawMessage) (string, error) {
				wl.mu.Lock()
				defer wl.mu.Unlock()
				wl.ran = append(wl.ran, string(args))
				return "wiped " + string(args), nil
			}})
			return &agent.Agent{Provider: model, Model: "m", Tools: reg, Ask: agent.AskDestructive, Approve: approve}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	return e, f, cancel
}

func approvalToken(t *testing.T, f *fakeService) string {
	t.Helper()
	var token string
	waitFor(t, "an approval prompt", func() bool {
		for _, ev := range f.log() {
			if ev.Kind == "approval" {
				token = ev.Text[strings.LastIndex(ev.Text, "|")+1:]
				return true
			}
		}
		return false
	})
	return token
}

func wipeCall() provider.Message {
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "wipe", Arguments: `{"dir":"/tmp/old"}`}}}
}

func said(f *fakeService, want string) bool {
	for _, ev := range f.log() {
		if ev.Kind == "send" && strings.Contains(ev.Text, want) {
			return true
		}
	}
	return false
}

// A request that needs permission does not hold the work up: the agent is told it is waiting and finishes what it
// can, and when the user allows it the call runs and the work is taken up again with its result and the original ask.
func TestAHeldRequestIsRunAndTheWorkResumesWhenAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.json")
	model := &heldModel{replies: []provider.Message{
		wipeCall(),
		{Role: provider.RoleAssistant, Content: "Waiting for your OK to clear the folder."},
		{Role: provider.RoleAssistant, Content: "All cleared."},
	}}
	wl := &wipeLog{}
	e, f, cancel := heldEngine(t, path, model, wl)
	defer cancel()

	e.Incoming(1, 1, "clean out the old folder")
	token := approvalToken(t, f)
	waitFor(t, "the agent to carry on", func() bool { return said(f, "Waiting for your OK") })
	if len(wl.get()) != 0 {
		t.Fatal("the call ran before it was allowed")
	}
	if last := model.seen[1][len(model.seen[1])-1]; last.Role != provider.RoleTool || !strings.Contains(last.Content, "needs the user's permission") || !strings.Contains(last.Content, "deletes something") {
		t.Fatalf("the agent was not told it is waiting, and why: %+v", last)
	}
	if got := e.Held.List(); len(got) != 1 || got[0].Task != "clean out the old folder" || got[0].Tool != "wipe" {
		t.Fatalf("the request was not kept with what was asked: %+v", got)
	}

	if d, _ := e.Decide(token, 1, 1, ChoiceAllow); d != DecisionRecorded {
		t.Fatalf("decision %v", d)
	}
	waitFor(t, "the work to resume", func() bool { return said(f, "All cleared.") })
	if got := wl.get(); len(got) != 1 || got[0] != `{"dir":"/tmp/old"}` {
		t.Fatalf("the saved call was not run exactly once as written: %v", got)
	}
	prompt := model.lastUser()
	for _, want := range []string{"allowed the request", "wiped", "clean out the old folder"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the resumed work lacks %q:\n%s", want, prompt)
		}
	}
	if len(e.Held.List()) != 0 {
		t.Fatal("an answered request is still held")
	}
	if d, _ := e.Decide(token, 1, 1, ChoiceAllow); d != DecisionAnswered {
		t.Fatalf("a second press: %v", d)
	}
	if d, _ := e.Decide("nonsense", 1, 1, ChoiceAllow); d != DecisionExpired {
		t.Fatalf("an unknown token: %v", d)
	}
}

// The request waits through a restart: a fresh engine on the same file can still be answered, and the work resumes.
func TestAHeldRequestSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.json")
	model := &heldModel{replies: []provider.Message{wipeCall(), {Role: provider.RoleAssistant, Content: "Waiting."}}}
	wl := &wipeLog{}
	e, f, cancel := heldEngine(t, path, model, wl)
	e.Incoming(1, 1, "tidy up")
	token := approvalToken(t, f)
	waitFor(t, "the agent to carry on", func() bool { return said(f, "Waiting.") })
	cancel()
	e.Wait(time.Second)

	model2 := &heldModel{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "Done after the restart."}}}
	e2, f2, cancel2 := heldEngine(t, path, model2, wl)
	defer cancel2()
	if d, _ := e2.Decide(token, 2, 1, ChoiceAllow); d != DecisionForbidden {
		t.Fatalf("somebody else answered: %v", d)
	}
	if d, _ := e2.Decide(token, 1, 99, ChoiceAllow); d != DecisionForbidden {
		t.Fatalf("answered from another chat: %v", d)
	}
	if d, _ := e2.Decide(token, 1, 1, ChoiceAllow); d != DecisionRecorded {
		t.Fatalf("after a restart: %v", d)
	}
	waitFor(t, "the resumed work", func() bool { return said(f2, "Done after the restart.") })
	if got := wl.get(); len(got) != 1 {
		t.Fatalf("ran %d times: %v", len(got), got)
	}
	if !strings.Contains(model2.lastUser(), "tidy up") {
		t.Fatalf("the original ask was lost across the restart:\n%s", model2.lastUser())
	}
}

// Saying no drops the call, and the agent is told not to find another way.
func TestADeniedHeldRequestIsNeverRun(t *testing.T) {
	model := &heldModel{replies: []provider.Message{wipeCall(), {Role: provider.RoleAssistant, Content: "Waiting."}, {Role: provider.RoleAssistant, Content: "Skipped it."}}}
	wl := &wipeLog{}
	e, f, cancel := heldEngine(t, filepath.Join(t.TempDir(), "held.json"), model, wl)
	defer cancel()
	e.Incoming(1, 1, "remove the old files")
	token := approvalToken(t, f)
	waitFor(t, "the agent to carry on", func() bool { return said(f, "Waiting.") })
	e.Decide(token, 1, 1, ChoiceDeny)
	waitFor(t, "the work to resume", func() bool { return said(f, "Skipped it.") })
	if len(wl.get()) != 0 {
		t.Fatal("a denied call ran")
	}
	if p := model.lastUser(); !strings.Contains(p, "did not allow it") || !strings.Contains(p, "another way") {
		t.Fatalf("the agent was not told:\n%s", p)
	}
}

// "Always" is never offered when only deleting and posting ask: a yes covers one call.
func TestDestructiveRequestsNeverOfferAlways(t *testing.T) {
	model := &heldModel{replies: []provider.Message{wipeCall(), {Role: provider.RoleAssistant, Content: "Waiting."}}}
	wl := &wipeLog{}
	e, f, cancel := heldEngine(t, filepath.Join(t.TempDir(), "held.json"), model, wl)
	defer cancel()
	e.Permissions = alwaysEverything{}
	e.Incoming(1, 1, "delete things")
	token := approvalToken(t, f)
	if _, got := e.Decide(token, 1, 1, ChoiceAlways); got != ChoiceAllow {
		t.Fatalf("always was honoured: %v", got)
	}
}

type alwaysEverything struct{}

func (alwaysEverything) Rememberable(string) bool     { return true }
func (alwaysEverything) Remember(int64, string) error { return nil }
func (alwaysEverything) Describe(int64) string        { return "" }
func (alwaysEverything) Reset(int64) (int, error)     { return 0, nil }
func (alwaysEverything) Activity(int64, int) string   { return "" }

// The same call asked for twice waits once.
func TestTheSameHeldCallIsNotAskedTwice(t *testing.T) {
	model := &heldModel{replies: []provider.Message{wipeCall(), wipeCall(), {Role: provider.RoleAssistant, Content: "Waiting."}}}
	wl := &wipeLog{}
	e, f, cancel := heldEngine(t, filepath.Join(t.TempDir(), "held.json"), model, wl)
	defer cancel()
	e.Incoming(1, 1, "wipe it")
	waitFor(t, "the agent to carry on", func() bool { return said(f, "Waiting.") })
	if n := len(e.Held.List()); n != 1 {
		t.Fatalf("%d requests held for one call", n)
	}
	prompts := 0
	for _, ev := range f.log() {
		if ev.Kind == "approval" {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("the user was asked %d times", prompts)
	}
}

// A request nobody answers lapses after a week rather than waiting for ever.
func TestAHeldRequestLapses(t *testing.T) {
	now := time.Now()
	s := &HeldStore{Path: filepath.Join(t.TempDir(), "h.json"), Now: func() time.Time { return now }}
	if err := s.Add(Held{Token: "a", Chat: 1, Tool: "wipe", Args: "{}"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(HeldTTL - time.Hour)
	if !s.Has("a") {
		t.Fatal("lapsed early")
	}
	now = now.Add(2 * time.Hour)
	if s.Has("a") {
		t.Fatal("still waiting after the time is up")
	}
	s2 := &HeldStore{Path: s.Path, Now: func() time.Time { return now }}
	if len(s2.List()) != 0 {
		t.Fatal("a lapsed request came back from the file")
	}
}

// When the request cannot be written down, the user is asked the usual way instead of the question being lost.
func TestAHeldRequestFallsBackToAskingWhenItCannotBeKept(t *testing.T) {
	model := &heldModel{replies: []provider.Message{wipeCall(), {Role: provider.RoleAssistant, Content: "ok"}}}
	wl := &wipeLog{}
	dir := t.TempDir()
	e, f, cancel := heldEngine(t, filepath.Join(dir, "held.json"), model, wl)
	defer cancel()
	e.Held.Path = filepath.Join(dir, "held.json", "no", "way") // a file stands where a folder would have to be
	writeBlocker(t, filepath.Join(dir, "held.json"))
	e.Incoming(1, 1, "wipe")
	token := approvalToken(t, f)
	if d, _ := e.Decide(token, 1, 1, ChoiceAllow); d != DecisionRecorded {
		t.Fatalf("the usual approval did not work: %v", d)
	}
	waitFor(t, "the call to run", func() bool { return len(wl.get()) == 1 })
}

func writeBlocker(t *testing.T, path string) {
	t.Helper()
	if err := osWriteFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func osWriteFile(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }

// tallyService counts the tool starts and ends a service is told of.
type tallyService struct {
	*fakeService
	mu          sync.Mutex
	starts, end int
}

func (s *tallyService) ToolStart(int64, string) { s.mu.Lock(); s.starts++; s.mu.Unlock() }
func (s *tallyService) ToolEnd(int64, string, string) {
	s.mu.Lock()
	s.end++
	s.mu.Unlock()
}

// A call carried out after the user's yes is shown as one tool call, begun once and ended once: two starts would leave
// one spinning on the page.
func TestAnAllowedHeldCallIsReportedAsOneToolCall(t *testing.T) {
	model := &heldModel{replies: []provider.Message{wipeCall(), {Role: provider.RoleAssistant, Content: "Waiting."}, {Role: provider.RoleAssistant, Content: "Done."}}}
	wl := &wipeLog{}
	f := newFakeService(4000, false)
	ts := &tallyService{fakeService: f}
	e, _, cancel := heldEngine(t, filepath.Join(t.TempDir(), "held.json"), model, wl)
	defer cancel()
	e.T = ts
	e.Incoming(1, 1, "wipe the old things")
	token := approvalToken(t, f)
	waitFor(t, "the agent to carry on", func() bool { return said(f, "Waiting.") })
	ts.mu.Lock()
	baseStarts, baseEnds := ts.starts, ts.end
	ts.mu.Unlock()
	e.Decide(token, 1, 1, ChoiceAllow)
	waitFor(t, "the work to resume", func() bool { return said(f, "Done.") })
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.starts-baseStarts != 1 || ts.end-baseEnds != 1 {
		t.Fatalf("the carried-out call was reported with %d starts and %d ends", ts.starts-baseStarts, ts.end-baseEnds)
	}
}
