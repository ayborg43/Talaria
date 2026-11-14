package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// stub answers with a fixed error or reply, and remembers what it was asked and how often.
type stub struct {
	err    error
	reply  string
	speak  string // text it streams before failing or answering
	mu     sync.Mutex
	calls  int
	models []string
}

func (s *stub) Chat(_ context.Context, r Request) (Message, error) {
	s.mu.Lock()
	s.calls++
	s.models = append(s.models, r.Model)
	s.mu.Unlock()
	if s.speak != "" && r.OnText != nil {
		r.OnText(s.speak)
	}
	if s.err != nil {
		return Message{}, s.err
	}
	return Message{Content: s.reply}, nil
}

func status(code int, msg string) error { return &StatusError{Status: code, Err: errors.New(msg)} }

func link(name string, s *stub) Link { return Link{Name: name, Model: name + "-model", Provider: s} }

func TestChainMovesOnWhenAModelIsDownAndAsksEachByItsOwnName(t *testing.T) {
	a, b := &stub{err: status(503, "overloaded")}, &stub{reply: "from b"}
	var switched []string
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth(),
		OnSwitch: func(from, to Link, err error) { switched = append(switched, from.Name+">"+to.Name) }}
	msg, err := c.Chat(context.Background(), Request{Model: "whatever-the-agent-had"})
	if err != nil || msg.Content != "from b" {
		t.Fatalf("%q %v", msg.Content, err)
	}
	if a.models[0] != "a-model" || b.models[0] != "b-model" {
		t.Errorf("models asked: %v %v", a.models, b.models)
	}
	if len(switched) != 1 || switched[0] != "a>b" {
		t.Errorf("switches: %v", switched)
	}
}

func TestAnOrdinaryFirstModelIsUsedAndNoOneElseIsTouched(t *testing.T) {
	a, b := &stub{reply: "a"}, &stub{reply: "b"}
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}
	if msg, _ := c.Chat(context.Background(), Request{}); msg.Content != "a" || b.calls != 0 {
		t.Fatalf("%q, b called %d times", msg.Content, b.calls)
	}
}

func TestARefusedKeyOrMissingModelMovesOnToo(t *testing.T) {
	for _, e := range []error{status(401, "bad key"), status(403, "no"), status(402, "pay"), status(404, "model not found"), status(400, "The model `x` does not exist")} {
		a, b := &stub{err: e}, &stub{reply: "b"}
		c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}
		if msg, err := c.Chat(context.Background(), Request{}); err != nil || msg.Content != "b" {
			t.Errorf("%v: %q %v", e, msg.Content, err)
		}
	}
}

func TestARequestProblemMovesOnToo_ButIsNotHeldAgainstTheModel(t *testing.T) {
	// too long for the first model, fine for the second: the person gets an answer, and the first is not rested
	a, b := &stub{err: status(400, "messages: too many tokens")}, &stub{reply: "b"}
	h := NewHealth()
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: h}
	msg, err := c.Chat(context.Background(), Request{})
	if err != nil || msg.Content != "b" {
		t.Fatalf("a bad request ended in an error: %v %q", err, msg.Content)
	}
	if !h.Get("a").Working {
		t.Error("a request problem was held against the model")
	}
	// an odd failure with no explanation moves on as well
	plain := &stub{err: errors.New("the reply could not be read")}
	c = Chain{Links: []Link{link("a", plain), link("b", &stub{reply: "ok"})}}
	if msg, err := c.Chat(context.Background(), Request{}); err != nil || msg.Content != "ok" {
		t.Fatalf("an ordinary error ended the request: %v", err)
	}
	// when every model meets it, the person hears what each said; a lone model's error is returned as it is
	bad := errors.New("the arguments were wrong")
	if _, err := (Chain{Links: []Link{link("a", &stub{err: bad})}}).Chat(context.Background(), Request{}); err != bad {
		t.Errorf("a lone model's error changed: %v", err)
	}
	_, err = Chain{Links: []Link{link("a", &stub{err: bad}), link("b", &stub{err: bad})}}.Chat(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "a: ") || !strings.Contains(err.Error(), "b: ") {
		t.Errorf("both models should be named: %v", err)
	}
}

func TestATimedOutModelMovesOn(t *testing.T) {
	timeout := fmt.Errorf("provider timed out (no data for 1m0s): %w", context.DeadlineExceeded)
	a, b := &stub{err: timeout}, &stub{reply: "b"}
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}
	if msg, err := c.Chat(context.Background(), Request{}); err != nil || msg.Content != "b" {
		t.Fatalf("%v %q", err, msg.Content)
	}
}

func TestNetworkErrorsMoveOn(t *testing.T) {
	a, b := &stub{err: &url.Error{Op: "Post", URL: "https://x", Err: errors.New("connection refused")}}, &stub{reply: "b"}
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}
	if msg, err := c.Chat(context.Background(), Request{}); err != nil || msg.Content != "b" {
		t.Fatalf("%q %v", msg.Content, err)
	}
}

func TestAModelThatJustFailedIsLeftAloneForAWhile(t *testing.T) {
	a, b := &stub{err: status(429, "slow down")}, &stub{reply: "b"}
	h := NewHealth()
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: h}
	c.Chat(context.Background(), Request{})
	c.Chat(context.Background(), Request{})
	c.Chat(context.Background(), Request{})
	if a.calls != 1 {
		t.Errorf("the failing model was asked %d times", a.calls)
	}
	if st := h.Get("a"); st.Working || !strings.Contains(st.Reason, "rate limited") || st.Hard {
		t.Errorf("state: %+v", st)
	}
	// once the rest is over it is tried again, and a success clears it
	a.err, a.reply = nil, "a again"
	h.now = func() time.Time { return time.Now().Add(SoftCooldown + time.Second) }
	if msg, _ := c.Chat(context.Background(), Request{}); msg.Content != "a again" {
		t.Errorf("not tried again: %q", msg.Content)
	}
	if !h.Get("a").Working {
		t.Error("a success did not clear it")
	}
}

func TestARefusedKeyRestsLongerAndSaysSo(t *testing.T) {
	h := NewHealth()
	a, b := &stub{err: status(401, "nope")}, &stub{reply: "b"}
	Chain{Links: []Link{link("a", a), link("b", b)}, Health: h}.Chat(context.Background(), Request{})
	st := h.Get("a")
	if !st.Hard || !strings.Contains(st.Reason, "key was refused") || st.Until.Sub(time.Now()) < HardCooldown-time.Minute {
		t.Errorf("state: %+v", st)
	}
	h.Forget("a")
	if !h.Get("a").Working {
		t.Error("an edited profile keeps its old failure")
	}
}

func TestWhenEveryModelIsRestingTheyAreTriedAnyway(t *testing.T) {
	a, b := &stub{err: status(503, "x")}, &stub{err: status(503, "y")}
	h := NewHealth()
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: h}
	if _, err := c.Chat(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "no model in the chain worked") {
		t.Fatalf("both failing: %v", err)
	}
	a.err, a.reply = nil, "a is back"
	if msg, err := c.Chat(context.Background(), Request{}); err != nil || msg.Content != "a is back" {
		t.Fatalf("nothing was tried while all rested: %q %v", msg.Content, err)
	}
}

func TestNoSecondAnswerIsTackedOntoPartOfTheFirstUnlessTheFrontEndCanRestart(t *testing.T) {
	a, b := &stub{err: status(503, "dropped"), speak: "Half an ans"}, &stub{reply: "whole"}
	var shown strings.Builder
	c := Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}
	_, err := c.Chat(context.Background(), Request{OnText: func(d string) { shown.WriteString(d) }})
	if err == nil || b.calls != 0 || shown.String() != "Half an ans" {
		t.Errorf("err=%v b=%d shown=%q", err, b.calls, shown.String())
	}

	// with a way to close the unfinished text, the next model answers
	a, b = &stub{err: status(503, "dropped"), speak: "Half an ans"}, &stub{reply: "whole"}
	restarts := 0
	c = Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}
	msg, err := c.Chat(context.Background(), Request{OnText: func(string) {}, OnRestart: func() { restarts++ }})
	if err != nil || msg.Content != "whole" || restarts != 1 {
		t.Errorf("err=%v msg=%q restarts=%d", err, msg.Content, restarts)
	}
}

func TestACancelledRunDoesNotMoveOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, b := &stub{err: context.Canceled}, &stub{reply: "b"}
	Chain{Links: []Link{link("a", a), link("b", b)}, Health: NewHealth()}.Chat(ctx, Request{})
	if b.calls != 0 {
		t.Fatal("a cancelled run moved on")
	}
}

func TestShortSaysItInWords(t *testing.T) {
	for code, want := range map[int]string{401: "key was refused", 402: "no credit", 404: "does not have this model", 429: "rate limited", 503: "down or overloaded"} {
		if got := Short(status(code, "x")); !strings.Contains(got, want) {
			t.Errorf("%d: %q", code, got)
		}
	}
}
