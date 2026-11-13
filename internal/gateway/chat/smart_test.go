package chat

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/routing"
)

// labeller is a model that answers every message with one word, the way the classifier asks.
type labeller struct {
	word  string
	err   error
	delay time.Duration
	calls atomic.Int32
	seen  atomic.Value
}

func (l *labeller) Chat(ctx context.Context, r provider.Request) (provider.Message, error) {
	l.calls.Add(1)
	l.seen.Store(r.Messages[len(r.Messages)-1].Content)
	select {
	case <-time.After(l.delay):
	case <-ctx.Done():
		return provider.Message{}, ctx.Err()
	}
	return provider.Message{Content: l.word}, l.err
}

func smartEngine(t *testing.T, lab provider.Provider, settings RouterSettings, store *routing.Store) *Engine {
	t.Helper()
	open := func(name string) (Backend, error) {
		switch name {
		case "main":
			return Backend{Model: "m-main", Provider: lab}, nil
		case "coder":
			return Backend{Model: "m-code", Reasoning: true, Best: []string{"code"}}, nil
		case "lite":
			return Backend{Model: "m-lite", Best: []string{"cheap"}, Provider: lab}, nil
		case "pen":
			return Backend{Model: "m-pen", Best: []string{"writing"}}, nil
		}
		return Backend{}, errors.New("no profile named " + name)
	}
	return &Engine{
		Backends:       &Backends{Default: "main", Names: []string{"coder", "lite", "main", "pen"}, Open: open},
		RouterSettings: func() RouterSettings { return settings },
		Learn:          store,
	}
}

func TestASmallModelCanSayWhatAMessageIsWhenTheKeywordsCannot(t *testing.T) {
	lab := &labeller{word: "writing"}
	e := smartEngine(t, lab, RouterSettings{Smart: true}, nil)
	// no keyword in it, and long enough not to be "simple": the rules would leave it on the default
	text := "Could you tidy up the paragraph below so it sounds friendlier and a little less stiff for the newsletter, thanks"
	d := e.decide(nil, text, "")
	if d.name != "pen" || d.kind != routing.Writing || !strings.Contains(d.why, "judged as writing") {
		t.Fatalf("%+v", d)
	}
	// the same message again is answered from memory, with no second call
	e.decide(nil, text, "")
	if lab.calls.Load() != 1 {
		t.Errorf("the labelling model was asked %d times", lab.calls.Load())
	}
	// the message the model saw is bounded
	e.decide(nil, strings.Repeat("x ", 900), "")
	if got := lab.seen.Load().(string); len([]rune(got)) > 600 {
		t.Errorf("the model was sent %d characters", len([]rune(got)))
	}
}

func TestSmartRoutingNeverSlowsOrBreaksAReply(t *testing.T) {
	text := "Could you tidy up the paragraph below so it sounds friendlier and a little less stiff for the newsletter, thanks"
	for name, lab := range map[string]*labeller{
		"too slow":       {word: "writing", delay: 5 * time.Second},
		"fails":          {err: errors.New("503")},
		"talks nonsense": {word: "banana"},
	} {
		e := smartEngine(t, lab, RouterSettings{Smart: true}, nil)
		began := time.Now()
		d := e.decide(nil, text, "")
		if d.name != "" || d.kind != routing.General {
			t.Errorf("%s: fell back to %+v", name, d)
		}
		if took := time.Since(began); took > 3500*time.Millisecond {
			t.Errorf("%s: waited %v", name, took)
		}
	}
}

func TestSmartRoutingIsOffUnlessAskedForAndSkippedWhenChosen(t *testing.T) {
	lab := &labeller{word: "writing"}
	text := "Could you tidy up the paragraph below so it sounds friendlier and a little less stiff for the newsletter, thanks"
	smartEngine(t, lab, RouterSettings{}, nil).decide(nil, text, "")
	smartEngine(t, lab, RouterSettings{Smart: true}, nil).decide(nil, text, "coder")       // the person chose
	smartEngine(t, lab, RouterSettings{Smart: true}, nil).decide(nil, "```go\nx\n```", "") // already certain
	if lab.calls.Load() != 0 {
		t.Errorf("the labelling model was asked %d times when it should not have been", lab.calls.Load())
	}
}

func TestHowRepliesWereReceivedCanOverruleTheRule(t *testing.T) {
	store := &routing.Store{}
	e := smartEngine(t, &labeller{}, RouterSettings{Learn: true}, store)
	d := e.decide(nil, "hi", "")
	if d.name != "lite" || d.kind != routing.Simple {
		t.Fatalf("before learning: %+v", d)
	}
	for range 7 { // the cheap model keeps getting sent back, the default keeps being accepted
		store.Record(routing.Simple, "lite", routing.Retry)
		store.Record(routing.Simple, "main", routing.Up)
	}
	d = e.decide(nil, "hi", "")
	if d.name != "" || !strings.Contains(d.why, "learned") {
		t.Fatalf("after learning the default should answer (it is shown as \"\"): %+v", d)
	}
	// and it does not apply when learning is off
	off := smartEngine(t, &labeller{}, RouterSettings{Learn: false}, store)
	if d := off.decide(nil, "hi", ""); d.name != "lite" {
		t.Errorf("learning was off: %+v", d)
	}
}

func TestAModelThatJustFailedIsNotChosenByTheRouter(t *testing.T) {
	e := smartEngine(t, &labeller{}, RouterSettings{}, nil)
	e.Backends.Resting = func(n string) bool { return n == "lite" }
	if d := e.decide(nil, "hi", ""); d.name != "" {
		t.Errorf("the cheap model is resting: %+v", d)
	}
}

func TestEscalateChoosesSomethingStrongerThanWhatAnswered(t *testing.T) {
	e := smartEngine(t, &labeller{}, RouterSettings{}, nil)
	if got := e.Escalate("lite"); got != "coder" {
		t.Errorf("from the cheap model: %q", got)
	}
	if got := e.Escalate("coder"); got != "main" {
		t.Errorf("from the strongest, the default: %q", got)
	}
	if got := e.Escalate("main"); got != "coder" {
		t.Errorf("from the default: %q", got)
	}
	if (&Engine{}).Escalate("x") != "" {
		t.Error("with no backends there is nothing stronger")
	}
}

func TestARetryForgetsTheAnswerItReplacesAndPinsOneProfileForOneTask(t *testing.T) {
	e := smartEngine(t, &labeller{}, RouterSettings{}, nil)
	c := &chat{id: 1}
	c.agent = &agent.Agent{System: "s"}
	c.agent.Restore([]provider.Message{
		{Role: provider.RoleUser, Content: "first question"}, {Role: provider.RoleAssistant, Content: "first answer"},
		{Role: provider.RoleUser, Content: "second question"}, {Role: provider.RoleAssistant, Content: "a poor answer"},
	})
	e.prepareRetry(c, "coder", routing.Writing)
	tr := c.agent.Transcript()
	if len(tr) != 2 || tr[1].Content != "first answer" {
		t.Errorf("the replaced exchange stayed in memory: %+v", tr)
	}
	if c.once != "coder" || c.onceKind != routing.Writing {
		t.Errorf("one-off profile: %q %q", c.once, c.onceKind)
	}
	// with the agent not built yet, the stored conversation is trimmed the same way
	c2 := &chat{id: 2, pending: []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}}
	e.prepareRetry(c2, "", "")
	if len(c2.pending) != 0 {
		t.Errorf("stored conversation: %+v", c2.pending)
	}
	if err := (&Engine{}).Retry(1, 1, "x", "", ""); err == nil {
		t.Error("a retry with one model should say so")
	}
}
