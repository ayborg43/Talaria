package web

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/gateway/chat"
)

type note struct{ title, body, tag, path string }
type notes struct {
	mu  sync.Mutex
	got []note
}

func (n *notes) Notify(title, body, tag, path string) {
	n.mu.Lock()
	n.got = append(n.got, note{title, body, tag, path})
	n.mu.Unlock()
}
func (n *notes) list() []note { n.mu.Lock(); defer n.mu.Unlock(); return append([]note(nil), n.got...) }

func drain(ch <-chan Event, n int, within time.Duration) []Event {
	var out []Event
	deadline := time.After(within)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
	return out
}

func TestAReplyIsStreamedByEditingOneMessageAndLosesTheEnginesCursor(t *testing.T) {
	h := &Hub{}
	ctx := context.Background()
	_, _, epoch, _ := h.Snapshot()
	ch, cancel := h.Subscribe(epoch, 0)
	defer cancel()
	id, _ := h.Send(ctx, ChatID, "Hel"+chat.StreamCursor)
	h.Edit(ctx, ChatID, id, "Hello wor"+chat.StreamCursor)
	h.Edit(ctx, ChatID, id, "Hello world")
	msgs, _, _, _ := h.Snapshot()
	if len(msgs) != 1 || msgs[0].Text != "Hello world" || msgs[0].Role != "assistant" {
		t.Fatalf("%+v", msgs)
	}
	var texts []string
	for _, ev := range drain(ch, 4, time.Second) {
		if ev.Type == "msg" {
			texts = append(texts, ev.Msg.Text)
		}
	}
	if strings.Join(texts, "|") != "Hel|Hello wor|Hello world" {
		t.Fatalf("the page saw %q", texts)
	}
	if err := h.Edit(ctx, ChatID, 999, "x"); h.Classify(err) != chat.EditGone {
		t.Fatalf("editing a message that is not there: %v", err)
	}
}

func TestAPageThatMissedSomeChangesIsCaughtUpOrToldToStartAgain(t *testing.T) {
	h := &Hub{}
	ctx := context.Background()
	a, _ := h.Send(ctx, ChatID, "one")
	_, seq1, epoch, _ := h.Snapshot()
	h.Send(ctx, ChatID, "two")
	h.Edit(ctx, ChatID, a, "one, edited")

	ch, cancel := h.Subscribe(epoch, seq1)
	got := drain(ch, 3, 300*time.Millisecond)
	cancel()
	var texts []string
	for _, ev := range got {
		if ev.Type == "msg" {
			texts = append(texts, ev.Msg.Text)
		}
	}
	// What changed after seq1, each message once and as it is now.
	if strings.Join(texts, "|") != "two|one, edited" {
		t.Fatalf("caught up with %q", texts)
	}

	for name, args := range map[string][2]int64{"another run of Talaria": {epoch + 1, seq1}, "ahead of the server": {epoch, 9999}} {
		ch, cancel := h.Subscribe(args[0], args[1])
		ev := drain(ch, 1, time.Second)
		cancel()
		if len(ev) != 1 || ev[0].Type != "reset" {
			t.Fatalf("%s: %+v", name, ev)
		}
	}
}

func TestAPageThatCannotKeepUpIsDroppedNotWaitedFor(t *testing.T) {
	h := &Hub{}
	_, _, epoch, _ := h.Snapshot()
	ch, _ := h.Subscribe(epoch, 0)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ { // nobody reads ch
			h.Send(context.Background(), ChatID, "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a slow page held up the conversation")
	}
	n := 0
	for range ch {
		n++
	}
	if n == 0 || n >= 2000 {
		t.Fatalf("%d events", n)
	}
}

func TestToolCallsShowHowTheyWentAndHowLong(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	h := &Hub{Now: func() time.Time { return now }}
	h.ToolStart(ChatID, "calendar_events")
	h.ToolStart(ChatID, "email_list")
	now = now.Add(1200 * time.Millisecond)
	h.ToolEnd(ChatID, "calendar_events", "Calendar from Thu 22 Oct")
	h.ToolEnd(ChatID, "email_list", "error: the mail server refused the sign-in")
	msgs, _, _, _ := h.Snapshot()
	if len(msgs) != 2 || msgs[0].State != "ok" || msgs[0].Ms != 1200 || msgs[1].State != "error" {
		t.Fatalf("%+v %+v", msgs[0], msgs[1])
	}
	if got := h.Activity(1); got[0] != 2 {
		t.Fatalf("activity %v", got)
	}
}

func TestAnApprovalHasATokenUntilItIsAnsweredAndThenNoLonger(t *testing.T) {
	h := &Hub{}
	id, _ := h.SendApproval(context.Background(), ChatID, "Allow shell?\n```\nls\n```", "tok123", true)
	p := h.Pending()
	if len(p) != 1 || p[0].Token != "tok123" || !p[0].Always || p[0].State != "pending" {
		t.Fatalf("%+v", p)
	}
	h.Resolve("nope", chat.ChoiceAllow)
	if len(h.Pending()) != 1 {
		t.Fatal("a token that matches nothing resolved something")
	}
	h.Resolve("tok123", chat.ChoiceAlways)
	msgs, _, _, _ := h.Snapshot()
	if msgs[0].State != "always" || msgs[0].Token != "" || len(h.Pending()) != 0 {
		t.Fatalf("%+v", msgs[0])
	}
	// A question the engine ends (timeout, /stop) stops being answerable.
	id2, _ := h.SendApproval(context.Background(), ChatID, "Allow shell?", "tok456", false)
	h.Edit(context.Background(), ChatID, id2, "Allow shell?\n\nTimed out, denied.")
	if m := h.byID[id2]; m.State != "expired" || m.Token != "" {
		t.Fatalf("%+v", m)
	}
	_ = id
}

func TestTheConversationSurvivesARestartButNothingStaysHalfDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.json")
	h := &Hub{Path: path}
	ctx := context.Background()
	h.AddUser("hello")
	h.Send(ctx, ChatID, "hi there")
	h.ToolStart(ChatID, "shell")
	h.SendApproval(ctx, ChatID, "Allow shell?", "tok", false)
	h.Close()

	g := &Hub{Path: path}
	msgs, _, _, _ := g.Snapshot()
	if len(msgs) != 4 || msgs[0].Text != "hello" || msgs[1].Text != "hi there" {
		t.Fatalf("%+v", msgs)
	}
	if msgs[2].State != "error" {
		t.Fatalf("a tool that was running when Talaria stopped is still shown running: %+v", msgs[2])
	}
	if msgs[3].State != "expired" || msgs[3].Token != "" || len(g.Pending()) != 0 {
		t.Fatalf("a question from before the restart can still be answered: %+v", msgs[3])
	}
	// New messages carry on from the old numbers.
	if id, _ := g.Send(ctx, ChatID, "again"); id <= msgs[3].ID {
		t.Fatalf("id %d reused", id)
	}
}

func TestOnlyTheLatestMessagesAreKept(t *testing.T) {
	h := &Hub{}
	for i := 0; i < maxMessages+50; i++ {
		h.Send(context.Background(), ChatID, "m")
	}
	msgs, _, _, _ := h.Snapshot()
	if len(msgs) != maxMessages {
		t.Fatalf("%d", len(msgs))
	}
	if err := h.Edit(context.Background(), ChatID, 1, "x"); err == nil {
		t.Fatal("a message that was dropped can still be edited")
	}
	long := strings.Repeat("é", maxText+10)
	id, _ := h.Send(context.Background(), ChatID, long)
	if m := h.byID[id]; len([]rune(m.Text)) != maxText {
		t.Fatalf("%d characters", len([]rune(m.Text)))
	}
}

func TestYouAreNotNotifiedAboutWhatYouAreLookingAt(t *testing.T) {
	n := &notes{}
	h := &Hub{Notifier: n, QuietAfter: 30 * time.Millisecond}
	ctx := context.Background()

	h.Seen() // the page is open and in view
	h.SendApproval(ctx, ChatID, "Allow shell?", "t1", false)
	id, _ := h.Send(ctx, ChatID, "hello")
	time.Sleep(150 * time.Millisecond)
	if len(n.list()) != 0 {
		t.Fatalf("notified while the page was in view: %+v", n.list())
	}

	h.mu.Lock()
	h.seen = time.Now().Add(-time.Minute) // the page has not been seen for a minute
	h.mu.Unlock()
	h.SendApproval(ctx, ChatID, "Allow shell?\n```\nrm x\n```", "t2", false)
	for i := 0; i < 5; i++ { // a reply being written: one notification, about the finished text
		h.Edit(ctx, ChatID, id, strings.Repeat("word ", i+1))
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	got := n.list()
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	var ask, reply note
	for _, g := range got {
		if g.tag == "approval" {
			ask = g
		} else {
			reply = g
		}
	}
	if ask.title != "Needs your OK" || ask.body != "Allow shell?" || ask.path != "/#needs" {
		t.Fatalf("%+v", ask)
	}
	if reply.title != "Talaria" || reply.body != strings.TrimSpace(strings.Repeat("word ", 5)) {
		t.Fatalf("the notification was about %q, not the finished reply", reply.body)
	}
	h.Close()
}

func TestTheAgentsRunIsAnnouncedAndNewPagesAreToldWhereItStands(t *testing.T) {
	h := &Hub{}
	_, _, epoch, _ := h.Snapshot()
	ch, cancel := h.Subscribe(epoch, 0)
	defer cancel()
	if ev := drain(ch, 1, time.Second); len(ev) != 1 || ev[0].Type != "run" || ev[0].On {
		t.Fatalf("%+v", ev)
	}
	h.RunStart(ChatID)
	if ev := drain(ch, 1, time.Second); len(ev) != 1 || ev[0].Type != "run" || !ev[0].On {
		t.Fatalf("%+v", ev)
	}
	if _, _, _, running := h.Snapshot(); !running {
		t.Fatal("a page loading now would not know it is running")
	}
	ch2, cancel2 := h.Subscribe(epoch, 0)
	defer cancel2()
	if ev := drain(ch2, 1, time.Second); len(ev) != 1 || !ev[0].On {
		t.Fatalf("a new page is not told the agent is working: %+v", ev)
	}
	h.RunEnd(ChatID)
}
