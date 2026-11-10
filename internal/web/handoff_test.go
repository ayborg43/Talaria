package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"talaria/internal/browser"
)

type noticeLog struct{ got chan string }

func (n noticeLog) Notify(title, body, tag, path string) { n.got <- tag + ":" + title + ":" + body }

// waitPending returns the pending handoff message once the agent has asked.
func waitPending(t *testing.T, h *Hub) *Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _, _, _ := h.Snapshot()
		for _, m := range msgs {
			if m.Role == "handoff" && m.State == "pending" {
				return m
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the agent never asked")
	return nil
}

func askAsync(h *Hub, ctx context.Context) <-chan string {
	out := make(chan string, 1)
	go func() {
		o, err := h.Handoff(ctx, "Sign in to x.com", "https://x.com/i/flow/login")
		if err != nil {
			o = "error:" + err.Error()
		}
		out <- o
	}()
	return out
}

func TestTheAgentWaitsAtAHandoffUntilTheUserSaysTheyAreDone(t *testing.T) {
	h := &Hub{}
	res := askAsync(h, context.Background())
	m := waitPending(t, h)
	if m.Text != "Sign in to x.com" || m.URL != "https://x.com/i/flow/login" || m.Token == "" {
		t.Fatalf("%+v", m)
	}
	select {
	case o := <-res:
		t.Fatalf("the agent went on before the user answered: %s", o)
	case <-time.After(50 * time.Millisecond):
	}
	if h.ResolveHandoff("nope", "done") || h.ResolveHandoff(m.Token, "maybe") {
		t.Fatal("a wrong token or action resolved it")
	}
	if !h.ResolveHandoff(m.Token, "done") {
		t.Fatal("the user's answer was not taken")
	}
	if o := <-res; o != browser.HandoffDone {
		t.Fatalf("the agent heard %q", o)
	}
	msgs, _, _, _ := h.Snapshot()
	if last := msgs[len(msgs)-1]; last.State != "done" || last.Token != "" {
		t.Fatalf("the card is left as %+v", last)
	}
	if h.ResolveHandoff(m.Token, "done") {
		t.Fatal("an answered request was answered twice")
	}
}

func TestSkippingWritingInTheChatAndStoppingAllEndTheWait(t *testing.T) {
	h := &Hub{}
	res := askAsync(h, context.Background())
	h.ResolveHandoff(waitPending(t, h).Token, "skip")
	if o := <-res; o != browser.HandoffSkip {
		t.Fatalf("skip: %q", o)
	}

	res = askAsync(h, context.Background())
	waitPending(t, h)
	h.AddUser("I'm in, go on")
	if o := <-res; o != browser.HandoffReplied {
		t.Fatalf("a message in the chat: %q", o)
	}

	ctx, stop := context.WithCancel(context.Background())
	res = askAsync(h, ctx)
	waitPending(t, h)
	stop()
	if o := <-res; !strings.HasPrefix(o, "error:") {
		t.Fatalf("stopping the agent should end the wait with an error, got %q", o)
	}
	for _, m := range func() []*Message { ms, _, _, _ := h.Snapshot(); return ms }() {
		if m.Role == "handoff" && m.State == "pending" {
			t.Fatalf("a card is still waiting: %+v", m)
		}
	}
}

func TestAHandoffNotifiesWhenThePageIsClosedAndDoesNotSurviveARestart(t *testing.T) {
	path := t.TempDir() + "/log.json"
	n := noticeLog{got: make(chan string, 1)}
	h := &Hub{Path: path, Notifier: n}
	res := askAsync(h, context.Background())
	m := waitPending(t, h)
	select {
	case got := <-n.got:
		if !strings.HasPrefix(got, "approval:Needs you in the browser:Sign in to x.com") {
			t.Fatalf("notified %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("nobody was told, and the page is closed")
	}
	h.save()
	h.Close()
	_ = m
	_ = res

	again := &Hub{Path: path}
	msgs, _, _, _ := again.Snapshot()
	for _, m := range msgs {
		if m.Role == "handoff" && m.State != "expired" {
			t.Fatalf("a waiting card came back after a restart: %+v", m)
		}
	}
}

func TestTheHandoffEndpointOnlyAcceptsDoneOrSkip(t *testing.T) {
	r := newRig(t)
	r.login()
	res := askAsync(r.hub, context.Background())
	tok := waitPending(t, r.hub).Token
	if resp, _ := r.do("POST", "/api/handoff", map[string]any{"token": tok, "action": "delete"}); resp.StatusCode != 400 {
		t.Fatalf("a strange action answered %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/handoff", map[string]any{"token": "nope", "action": "done"}); resp.StatusCode != 404 {
		t.Fatalf("an unknown request answered %d", resp.StatusCode)
	}
	if resp, body := r.do("POST", "/api/handoff", map[string]any{"token": tok, "action": "done"}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if o := <-res; o != browser.HandoffDone {
		t.Fatalf("%q", o)
	}
}

// A handoff with no page to take over is an action the user's rules say they do themselves: it asks as "Needs you".
func TestAnActionHandoffHasNoPageAndNotifiesAsNeedsYou(t *testing.T) {
	n := noticeLog{got: make(chan string, 1)}
	h := &Hub{Notifier: n}
	go h.Handoff(context.Background(), "Do this yourself: Send a message to ana@example.com", "")
	m := waitPending(t, h)
	if m.URL != "" || !strings.HasPrefix(m.Text, "Do this yourself") {
		t.Fatalf("%+v", m)
	}
	select {
	case got := <-n.got:
		if !strings.HasPrefix(got, "approval:Needs you:Do this yourself") {
			t.Fatalf("notified %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("nobody was told")
	}
	h.ResolveHandoff(m.Token, "skip")
}
