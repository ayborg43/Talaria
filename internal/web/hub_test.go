package web

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/activity"
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
	for _, ev := range drain(ch, 5, time.Second) { // the run state and who is working come first, then the three versions
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

func TestANewChatForgetsTheConversationAndTellsEveryPage(t *testing.T) {
	h := &Hub{}
	ctx := context.Background()
	h.AddUser("hello")
	h.Send(ctx, ChatID, "hi there")
	_, seq, epoch, _ := h.Snapshot()
	ch, cancel := h.Subscribe(epoch, seq)
	defer cancel()
	drain(ch, 2, time.Second) // the run state and who is working
	h.Reset()
	if msgs, _, epoch2, _ := h.Snapshot(); len(msgs) != 0 {
		t.Fatalf("the old conversation is still there: %+v", msgs)
	} else if epoch2 == epoch {
		t.Fatal("the epoch did not move, so a reconnecting page would not start again")
	}
	if ev := drain(ch, 1, time.Second); len(ev) != 1 || ev[0].Type != "reset" {
		t.Fatalf("an open page was told %+v", ev)
	}
	// What is said afterwards starts a new conversation, and a page from before it is told to start again.
	h.Send(ctx, ChatID, "Started a new conversation.")
	if msgs, _, _, _ := h.Snapshot(); len(msgs) != 1 || msgs[0].Text != "Started a new conversation." {
		t.Fatalf("%+v", msgs)
	}
	ch2, cancel2 := h.Subscribe(epoch, 0)
	defer cancel2()
	if ev := drain(ch2, 1, time.Second); len(ev) != 1 || ev[0].Type != "reset" {
		t.Fatalf("a page from before the new chat was told %+v", ev)
	}
}

func TestReplaceShowsAnotherConversationAndTellsEveryPage(t *testing.T) {
	h := &Hub{}
	ctx := context.Background()
	h.AddUser("hello")
	h.Send(ctx, ChatID, "hi there")
	_, seq, epoch, _ := h.Snapshot()
	ch, cancel := h.Subscribe(epoch, seq)
	defer cancel()
	drain(ch, 2, time.Second) // the run state and who is working
	h.Replace([]*Message{{ID: 7, Role: "user", Text: "an old question"}, {ID: 9, Role: "tool", Tool: "shell", State: "running"}})
	msgs, _, epoch2, running := h.Snapshot()
	if len(msgs) != 2 || msgs[0].Text != "an old question" {
		t.Fatalf("the other conversation is showing: %+v", msgs)
	}
	if msgs[1].State != "error" {
		t.Fatalf("a tool left running stops with its conversation: %+v", msgs[1])
	}
	if epoch2 == epoch || running {
		t.Fatal("pages must fetch again, and nothing runs on")
	}
	if ev := drain(ch, 1, time.Second); len(ev) != 1 || ev[0].Type != "reset" {
		t.Fatalf("an open page was told %+v", ev)
	}
	// What is said afterwards continues the shown conversation with fresh ids.
	h.AddUser("and then")
	if msgs, _, _, _ := h.Snapshot(); len(msgs) != 3 || msgs[2].ID != 10 || msgs[2].Text != "and then" {
		t.Fatalf("ids carry on in the shown conversation: %+v", msgs)
	}
}

func TestWritingTheConversationDownTellsWhoKeepsConversations(t *testing.T) {
	old := saveDelay
	saveDelay = 20 * time.Millisecond
	defer func() { saveDelay = old }()
	h := &Hub{Path: filepath.Join(t.TempDir(), "log.json")}
	called := make(chan struct{}, 1)
	h.OnChange = func() { called <- struct{}{} }
	h.AddUser("hello")
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("the keeper was not told")
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
	if ask.title != "Needs your OK" || ask.body != "Allow shell?" || ask.path != "/#agents" {
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
	if ev := drain(ch, 2, time.Second); len(ev) != 2 || ev[0].Type != "run" || ev[0].On || ev[1].Type != "agents" || len(ev[1].Agents) != 0 {
		t.Fatalf("%+v", ev)
	}
	h.RunStart(ChatID)
	if ev := drain(ch, 2, time.Second); len(ev) != 2 || ev[0].Type != "run" || !ev[0].On || ev[1].Type != "agents" || len(ev[1].Agents) != 1 || ev[1].Agents[0].Kind != "main" {
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

func kinds(ws []activity.Worker) string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Kind+":"+w.Label+":"+w.Doing)
	}
	return strings.Join(out, ", ")
}

func TestWhoIsWorkingFollowsTheRunTheToolsAndTheHelpers(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	h := &Hub{Now: func() time.Time { return now }}
	if len(h.Workers()) != 0 {
		t.Fatal("somebody is working before anything began")
	}
	h.RunStart(ChatID)
	if got := kinds(h.Workers()); got != "main:Talaria:" {
		t.Fatalf("%q", got)
	}
	h.ToolStart(ChatID, "email_list")
	h.ToolStart(ChatID, "calendar_events")
	if got := kinds(h.Workers()); got != "main:Talaria:calendar_events, email_list" {
		t.Fatalf("%q", got)
	}
	h.ToolEnd(ChatID, "calendar_events", "ok")
	if got := kinds(h.Workers()); got != "main:Talaria:email_list" {
		t.Fatalf("%q", got)
	}
	sub := h.Start(activity.Sub, "compare prices")
	h.Doing(sub, "web_fetch")
	now = now.Add(90 * time.Second)
	job := h.Start(activity.Job, "inbox check")
	ws := h.Workers()
	if got := kinds(ws); got != "main:Talaria:email_list, sub:compare prices:web_fetch, job:inbox check:" {
		t.Fatalf("%q", got)
	}
	if ws[1].AgeMs != 90000 || ws[2].AgeMs != 0 {
		t.Fatalf("ages %d and %d", ws[1].AgeMs, ws[2].AgeMs)
	}
	h.Stop(job)
	h.Stop(job) // ending it twice is harmless
	h.Doing(12345, "x")
	if got := kinds(h.Workers()); !strings.Contains(got, "sub:compare prices") || strings.Contains(got, "job:") {
		t.Fatalf("%q", got)
	}
	h.RunEnd(ChatID)
	if got := kinds(h.Workers()); got != "" {
		t.Fatalf("the run is over, and still shown working: %q", got)
	}
}

func TestOtherWorkSurvivesTheMainRunEndingButHelpersDoNot(t *testing.T) {
	h := &Hub{}
	task := h.Start(activity.Task, "write report") // started by the scheduler, not by a chat message
	h.RunStart(ChatID)
	h.Start(activity.Sub, "helper")
	h.RunEnd(ChatID)
	if got := kinds(h.Workers()); got != "task:write report:" {
		t.Fatalf("%q", got)
	}
	h.Stop(task)
}

func TestAHelpersProgressLineIsNotAChipThatSpinsForever(t *testing.T) {
	h := &Hub{}
	h.RunStart(ChatID)
	h.ToolStart(ChatID, "delegate")
	h.ToolStart(ChatID, "  ↳ [prices] web_fetch") // how a subagent's progress reaches its parent
	h.ToolStart(ChatID, "  ↳ [prices] probe")
	msgs, _, _, _ := h.Snapshot()
	if len(msgs) != 1 || msgs[0].Tool != "delegate" {
		t.Fatalf("a helper's progress became chips: %+v", msgs)
	}
	if got := kinds(h.Workers()); got != "main:Talaria:delegate" {
		t.Fatalf("%q", got)
	}
}

func TestWhateverIsStillSpinningWhenTheRunEndsIsMarkedAsCutShort(t *testing.T) {
	h := &Hub{}
	h.RunStart(ChatID)
	h.ToolStart(ChatID, "shell")
	h.ToolStart(ChatID, "web_fetch")
	h.ToolEnd(ChatID, "web_fetch", "fine")
	h.RunEnd(ChatID) // /stop, or a timeout: the shell never reported back
	msgs, _, _, _ := h.Snapshot()
	if msgs[0].State != "error" || msgs[1].State != "ok" {
		t.Fatalf("%+v %+v", msgs[0], msgs[1])
	}
	if got := kinds(h.Workers()); got != "" {
		t.Fatal(got)
	}
	h.RunStart(ChatID) // and the next run starts clean, with no tool carried over
	if got := kinds(h.Workers()); got != "main:Talaria:" {
		t.Fatalf("%q", got)
	}
}

func TestThePageIsToldOnlyWhatChangedAndAWorkerNobodyEndedFadesAway(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	h := &Hub{Now: func() time.Time { return now }}
	_, _, epoch, _ := h.Snapshot()
	ch, cancel := h.Subscribe(epoch, 0)
	defer cancel()
	drain(ch, 2, time.Second) // the run state and the empty roster
	id := h.Start(activity.Sub, "helper")
	h.Doing(id, "probe")
	h.Doing(id, "probe") // the same again: nothing new to say
	h.Doing(id, "web_fetch")
	events := drain(ch, 5, 300*time.Millisecond)
	if len(events) != 3 {
		t.Fatalf("%d events for a start and two changes: %+v", len(events), events)
	}
	for _, ev := range events {
		if ev.Type != "agents" || len(ev.Agents) != 1 {
			t.Fatalf("%+v", ev)
		}
	}
	if events[2].Agents[0].Doing != "web_fetch" {
		t.Fatalf("%+v", events[2].Agents[0])
	}
	now = now.Add(pruneAfter + time.Minute) // its end was never reported
	if got := kinds(h.Workers()); got != "" {
		t.Fatalf("a worker that began hours ago is still shown: %q", got)
	}
}

func TestANewPageIsToldWhoIsWorkingAtOnce(t *testing.T) {
	h := &Hub{}
	h.RunStart(ChatID)
	h.Start(activity.Sub, "helper")
	_, _, epoch, _ := h.Snapshot()
	ch, cancel := h.Subscribe(epoch, 0)
	defer cancel()
	evs := drain(ch, 2, time.Second)
	if len(evs) != 2 || evs[1].Type != "agents" || len(evs[1].Agents) != 2 {
		t.Fatalf("%+v", evs)
	}
}

func TestWhatFinishesIsRememberedNewestFirstAndSurvivesARestart(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "log.json")
	h := &Hub{Path: path, Now: func() time.Time { return now }}

	a := h.Start(activity.Sub, "first helper")
	now = now.Add(14 * time.Second)
	h.Stop(a)
	b := h.Start(activity.Job, "inbox check")
	now = now.Add(90 * time.Second)
	h.Stop(b)
	h.RunStart(ChatID) // the main agent is not listed: it ends with every answer
	h.RunEnd(ChatID)

	rec := h.Recent()
	if len(rec) != 2 || rec[0].Label != "inbox check" || rec[0].Seconds != 90 || rec[1].Label != "first helper" || rec[1].Seconds != 14 || rec[0].Stopped {
		t.Fatalf("%+v", rec)
	}
	h.Close()
	g := &Hub{Path: path, Now: func() time.Time { return now }}
	if got := g.Recent(); len(got) != 2 || got[0].Label != "inbox check" {
		t.Fatalf("after a restart: %+v", got)
	}
}

func TestAHelperCutShortByStoppingIsListedAsStopped(t *testing.T) {
	h := &Hub{}
	h.RunStart(ChatID)
	h.Start(activity.Sub, "slow helper")
	h.RunEnd(ChatID) // stopped while the helper was still working
	rec := h.Recent()
	if len(rec) != 1 || !rec[0].Stopped || rec[0].Label != "slow helper" {
		t.Fatalf("%+v", rec)
	}
}

func TestOnlyTheLatestFinishedAreKept(t *testing.T) {
	h := &Hub{}
	for i := 0; i < maxRecent+10; i++ {
		id := h.Start(activity.Job, "job")
		h.Stop(id)
	}
	if n := len(h.Recent()); n != maxRecent {
		t.Fatalf("%d", n)
	}
}

// Who is working says which model each is using, and what finished remembers it.
func TestWorkersShowTheModelTheyUse(t *testing.T) {
	h := &Hub{}
	h.RunStart(1)
	h.NoteModel(1, "deepseek-v4-pro")
	id := h.Start(activity.Sub, "lookup")
	h.SetModel(id, "flash-1")
	got := h.Workers()
	if len(got) != 2 || got[0].Kind != activity.Main || got[0].Model != "deepseek-v4-pro" || got[1].Model != "flash-1" {
		t.Fatalf("%+v", got)
	}
	h.Stop(id)
	if r := h.Recent(); len(r) != 1 || r[0].Model != "flash-1" {
		t.Fatalf("%+v", r)
	}
	h.RunEnd(1)
	h.RunStart(1) // the next task starts out under the model last used, until it says otherwise
	if w := h.Workers(); len(w) != 1 || w[0].Model != "deepseek-v4-pro" {
		t.Fatalf("%+v", w)
	}
}

// A question still waiting after a restart stays a question; one nobody can answer any more expires.
func TestAQuestionKeptAcrossARestartStaysPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.json")
	h := &Hub{Path: path}
	h.SendApproval(context.Background(), 1, "Allow a?", "keep", true)
	h.SendApproval(context.Background(), 1, "Allow b?", "gone", true)
	h.Close()
	h2 := &Hub{Path: path, Held: func(token string) bool { return token == "keep" }}
	states := map[string]string{}
	msgs, _, _, _ := h2.Snapshot()
	for _, m := range msgs {
		states[m.Text] = m.State
	}
	if states["Allow a?"] != "pending" || states["Allow b?"] != "expired" {
		t.Fatalf("%v", states)
	}
}
