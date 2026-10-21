package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestAChatMaySendABurstThenOnlyAtTheRate(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := &limiter{now: c.now}
	for i := 0; i < 10; i++ {
		if ok, _ := l.allowMessage(1, 10); !ok {
			t.Fatalf("message %d of a burst of 10", i+1)
		}
	}
	ok, warn := l.allowMessage(1, 10)
	if ok || !warn {
		t.Fatalf("the 11th is turned away, and the chat is told once: %v %v", ok, warn)
	}
	if ok, warn := l.allowMessage(1, 10); ok || warn {
		t.Fatalf("not told again straight away: %v %v", ok, warn)
	}
	if ok, _ := l.allowMessage(2, 10); !ok {
		t.Fatal("another chat has its own allowance")
	}
	c.add(6 * time.Second) // 10 a minute is one every six seconds
	if ok, _ := l.allowMessage(1, 10); !ok {
		t.Fatal("it refills")
	}
	if ok, _ := l.allowMessage(1, 10); ok {
		t.Fatal("but only at the rate")
	}
	c.add(time.Hour)
	for i := 0; i < 10; i++ {
		if ok, _ := l.allowMessage(1, 10); !ok {
			t.Fatal("a long rest refills to the burst, no further")
		}
	}
	if ok, _ := l.allowMessage(1, 10); ok {
		t.Fatal("never more than the burst")
	}
	if ok, _ := (&limiter{}).allowMessage(1, 0); !ok {
		t.Fatal("0 means no limit")
	}
}

func TestStrangersAreAnsweredRarely(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := &limiter{now: c.now}
	if !l.allowStrangerReply(7) || l.allowStrangerReply(7) {
		t.Fatal("one person is answered once")
	}
	c.add(strangerEvery + time.Second)
	if !l.allowStrangerReply(7) {
		t.Fatal("and again after a while")
	}
	answered := 0
	for u := int64(100); u < 200; u++ {
		if l.allowStrangerReply(u) {
			answered++
		}
	}
	if answered > strangerMax {
		t.Fatalf("a hundred strangers got %d answers", answered)
	}
	c.add(2 * time.Minute)
	if !l.allowStrangerReply(999) {
		t.Fatal("the window moves on")
	}
}

func TestTheTrackedStrangersAreBounded(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := &limiter{now: c.now}
	for u := int64(1); u <= maxTracked+500; u++ {
		l.allowStrangerReply(u)
		c.add(time.Second / 2)
	}
	if len(l.known) > maxTracked+1 {
		t.Fatalf("%d remembered", len(l.known))
	}
}

type okModel struct {
	mu    sync.Mutex
	calls int
}

func (m *okModel) Chat(context.Context, provider.Request) (provider.Message, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
}

func newLimitedEngine(t *testing.T, perMinute int) (*Engine, *fakeService, *okModel) {
	t.Helper()
	svc := newFakeService(4000, false)
	model := &okModel{}
	e := &Engine{T: svc, Allowed: map[int64]bool{1: true}, MessagesPerMinute: perMinute, Logf: func(string, ...any) {},
		NewAgent: func(int64, func(string, string) bool) *agent.Agent {
			return &agent.Agent{Provider: model, Model: "m", Tools: tool.NewRegistry()}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); e.Wait(2 * time.Second) })
	e.Start(ctx)
	return e, svc, model
}

func TestAStrangerWhoFloodsTheBotGetsOneAnswer(t *testing.T) {
	e, svc, model := newLimitedEngine(t, 0)
	for i := 0; i < 50; i++ {
		e.Incoming(666, 666, "/start")
		e.Incoming(666, 666, "/whoami")
		e.Incoming(666, 666, "hello")
	}
	n := 0
	for _, ev := range svc.log() {
		if strings.Contains(ev.Text, "Not authorized") {
			n++
		}
	}
	if n != 1 || model.calls != 0 {
		t.Fatalf("%d answers, %d model calls", n, model.calls)
	}
}

func TestAChatThatSendsTooFastIsSlowedDownButCanStillStop(t *testing.T) {
	e, svc, _ := newLimitedEngine(t, 3)
	for i := 0; i < 30; i++ {
		e.Incoming(1, 1, "message")
	}
	slow := 0
	for _, ev := range svc.log() {
		if strings.Contains(ev.Text, "Slow down") {
			slow++
		}
	}
	if slow != 1 {
		t.Fatalf("told once, not thirty times: %d", slow)
	}
	e.Incoming(1, 1, "/stop")
	waitFor(t, "the stop to be answered", func() bool {
		for _, ev := range svc.log() {
			if strings.Contains(ev.Text, "Stopped") || strings.Contains(ev.Text, "Nothing is running") {
				return true
			}
		}
		return false
	})
}

type longModel struct{}

func (longModel) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	if req.OnText != nil {
		for i := 0; i < 400; i++ {
			req.OnText(strings.Repeat("word ", 200))
		}
	}
	return provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("word ", 200*400)}, nil
}

func TestAHugeReplyIsCutRatherThanBecomingThousandsOfMessages(t *testing.T) {
	svc := newFakeService(500, false)
	// Not streamed: delivered in pieces.
	Deliver(context.Background(), svc, 1, strings.Repeat("word ", 20000), nil)
	if n := len(svc.log()); n != maxReplyParts {
		t.Fatalf("%d messages, want %d", n, maxReplyParts)
	}
	last := svc.log()[maxReplyParts-1].Text
	if !strings.Contains(last, "left out") {
		t.Fatalf("the user is told: %q", last)
	}
	// A short one is untouched.
	svc2 := newFakeService(500, false)
	Deliver(context.Background(), svc2, 1, "hello", nil)
	if len(svc2.log()) != 1 {
		t.Fatal("one message")
	}

	// Streamed.
	svc3 := newFakeService(500, false)
	l := newLiveReply(context.Background(), svc3, 1, time.Millisecond, nil)
	for i := 0; i < 400; i++ {
		l.onText(strings.Repeat("word ", 100))
	}
	l.finish("")
	sent := map[int64]bool{}
	for _, ev := range svc3.log() {
		sent[ev.Msg] = true
	}
	if len(sent) > maxReplyParts+1 {
		t.Fatalf("%d messages from a streamed reply", len(sent))
	}
	all := ""
	for _, txt := range svc3.texts {
		all += txt
	}
	if !strings.Contains(all, "left out") {
		t.Fatal("the cut is announced")
	}
}
