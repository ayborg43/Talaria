package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeService is a chat service that records what the engine does to it.
type fakeService struct {
	mu       sync.Mutex
	limit    int
	markdown bool
	events   []event
	texts    map[int64]string // current text of each message
	next     int64
	editErr  error // returned by every Edit when set
	outcome  EditOutcome
}

type event struct {
	Kind string // "send", "edit", "approval"
	Msg  int64
	Text string
}

func newFakeService(limit int, markdown bool) *fakeService {
	return &fakeService{limit: limit, markdown: markdown, texts: map[int64]string{}}
}

func (f *fakeService) Service() string                     { return "Fake" }
func (f *fakeService) MaxMessage() int                     { return f.limit }
func (f *fakeService) EditInterval() time.Duration         { return 5 * time.Millisecond }
func (f *fakeService) Markdown() bool                      { return f.markdown }
func (f *fakeService) CommandPrefix() string               { return "/" }
func (f *fakeService) Classify(error) EditOutcome          { return f.outcome }
func (f *fakeService) Typing(context.Context, int64) error { return nil }

func (f *fakeService) Send(_ context.Context, _ int64, text string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.texts[f.next] = text
	f.events = append(f.events, event{"send", f.next, text})
	return f.next, nil
}

func (f *fakeService) Edit(_ context.Context, _, msg int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.texts[msg] = text
	f.events = append(f.events, event{"edit", msg, text})
	return nil
}

func (f *fakeService) SendApproval(_ context.Context, _ int64, text, token string, _ bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.texts[f.next] = text
	f.events = append(f.events, event{"approval", f.next, text + "|" + token})
	return f.next, nil
}

func (f *fakeService) log() []event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]event(nil), f.events...)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func words(n int) string {
	var w []string
	for i := 0; i < n; i++ {
		w = append(w, "word")
	}
	return strings.Join(w, " ")
}

func TestSplitMessage(t *testing.T) {
	if got := SplitMessage("  ", 10, false); len(got) != 1 || got[0] != "(no output)" {
		t.Fatalf("empty: %q", got)
	}
	if got := SplitMessage("short", 10, false); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short: %q", got)
	}
	text := strings.Repeat("line one\n", 5) + strings.Repeat("é", 25)
	parts := SplitMessage(text, 20, false)
	for _, p := range parts {
		if n := len([]rune(p)); n > 20 || n == 0 {
			t.Fatalf("part has %d runes: %q", n, p)
		}
	}
	if !strings.HasPrefix(parts[0], "line one") {
		t.Fatalf("unexpected split: %q", parts)
	}
	// No content is lost apart from trimmed whitespace.
	if strings.ReplaceAll(strings.Join(parts, ""), "\n", "") != strings.ReplaceAll(text, "\n", "") {
		t.Fatalf("content lost: %q", parts)
	}
}

// A code block cut by a split is closed at the end of one piece and reopened,
// with its language, at the start of the next, so each piece renders as code.
func TestSplitMessageKeepsCodeBlocksBalanced(t *testing.T) {
	var code []string
	for i := 0; i < 40; i++ {
		code = append(code, "x := compute(arg)")
	}
	text := "Intro text.\n```go\n" + strings.Join(code, "\n") + "\n```\nOutro."
	limit := 200
	parts := SplitMessage(text, limit, true)
	if len(parts) < 3 {
		t.Fatalf("expected the block to span several messages, got %d", len(parts))
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > limit {
			t.Fatalf("part %d has %d runes, over the limit of %d", i, n, limit)
		}
		if open, _ := fenceState(p, false, ""); open {
			t.Fatalf("part %d leaves a code block open: %q", i, p)
		}
		if i > 0 && strings.Contains(p, "compute") && !strings.HasPrefix(p, "```go\n") {
			t.Fatalf("part %d continues the block without reopening it: %q", i, p)
		}
	}
	// Every line of code survives, in order.
	if got := strings.Count(strings.Join(parts, "\n"), "x := compute(arg)"); got != 40 {
		t.Fatalf("code lines lost: %d of 40", got)
	}
	if !strings.HasSuffix(parts[len(parts)-1], "Outro.") {
		t.Fatalf("the tail is missing: %q", parts[len(parts)-1])
	}
}

// Without Markdown, text is left exactly as written: fences mean nothing.
func TestSplitMessageLeavesPlainTextAlone(t *testing.T) {
	text := "```\n" + strings.Repeat("line\n", 50)
	for _, p := range SplitMessage(text, 60, false) {
		if strings.HasSuffix(p, "```") {
			t.Fatalf("plain text must not get fences added: %q", p)
		}
	}
}

func TestCommand(t *testing.T) {
	for in, want := range map[string]string{"/new": "/new", "/NEW@talaria_bot now": "/new", "hello": "", "/": "/"} {
		if got := command(in); got != want {
			t.Errorf("command(%q) = %q, want %q", in, got, want)
		}
	}
}

func sentText(f *fakeService) string {
	var b strings.Builder
	for _, ev := range f.log() {
		b.WriteString(ev.Text + "\n")
	}
	return b.String()
}

func TestTheApprovalBodyIsNeverCutOffSilently(t *testing.T) {
	fake := newFakeService(20000, false)
	e := &Engine{T: fake, root: context.Background()}
	// Short: shown as it is, the target first.
	body, ok := e.approvalBody(1, `{"content":"hi","path":"/tmp/x"}`)
	if !ok || body != "path: /tmp/x\ncontent: hi" {
		t.Fatalf("%q %v", body, ok)
	}
	// Long: all of it is sent first, and the question points at it.
	long := strings.Repeat("x", 2000)
	body, ok = e.approvalBody(1, `{"path":"/home/me/.zshrc","content":"`+long+`"}`)
	sent := sentText(fake)
	if !ok || !strings.Contains(sent, long) || !strings.Contains(sent, "path: /home/me/.zshrc") || !strings.Contains(body, "the whole request is in the message above") {
		t.Fatalf("the full text must go out before the question:\nsent: %.200q\nbody: %.200q", sent, body)
	}
	if !strings.HasPrefix(body, "path: /home/me/.zshrc") {
		t.Fatalf("the question still starts with the target: %.100q", body)
	}
	// Too long to check: refused, and the user is told why.
	fake = newFakeService(20000, false)
	e = &Engine{T: fake, root: context.Background()}
	if _, ok := e.approvalBody(1, `{"command":"`+strings.Repeat("y", approvalArgsHard+1)+`"}`); ok {
		t.Fatal("a request nobody can read must not be put to them")
	}
	if !strings.Contains(sentText(fake), "did not run it") {
		t.Fatal("the user is told")
	}
}

func TestApprovalTokensAreUnguessableAndUnique(t *testing.T) {
	taken := map[string]*pendingApproval{}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		tok := newToken(taken)
		if len(tok) != 16 || seen[tok] || strings.ContainsAny(tok, ":") {
			t.Fatalf("%q", tok)
		}
		seen[tok] = true
		taken[tok] = &pendingApproval{}
	}
	if _, _, ok := ParseApproval(ApprovalData(newToken(nil), ChoiceAllow)); !ok {
		t.Fatal("a token fits in the button data")
	}
	if tok := newToken(nil); tok == "1" || tok == "2" {
		t.Fatal("not a counter, which restarts at 1 after the gateway does")
	}
}

func TestApprovalData(t *testing.T) {
	for _, c := range []Choice{ChoiceDeny, ChoiceAllow, ChoiceAlways} {
		tok, got, ok := ParseApproval(ApprovalData("42", c))
		if !ok || tok != "42" || got != c {
			t.Fatalf("round trip of %v gave %q %v %v", c, tok, got, ok)
		}
	}
	if ApprovalData("7", ChoiceAllow) != "ap:7:y" || ApprovalData("7", ChoiceDeny) != "ap:7:n" || ApprovalData("7", ChoiceAlways) != "ap:7:a" {
		t.Fatal("the wire form is part of the buttons already on screen")
	}
	for _, bad := range []string{"", "ap", "ap::y", "ap:1:maybe", "xx:1:y", "ap:1:y:z"} {
		if _, _, ok := ParseApproval(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestDeliverSplitsToTheServicesLimit(t *testing.T) {
	f := newFakeService(50, false)
	Deliver(context.Background(), f, 1, strings.Repeat("0123456789\n", 20), nil)
	ev := f.log()
	if len(ev) < 4 {
		t.Fatalf("expected several messages, got %d", len(ev))
	}
	for _, e := range ev {
		if n := len([]rune(e.Text)); n > 50 {
			t.Fatalf("message of %d runes exceeds the limit", n)
		}
	}
}

// A spurious wake-up (wakes coalesce, so one can outlive the text that caused
// it) must not turn into an edit that changes nothing: services answer those
// with an error, and each one spends a rate-limit slot.
func TestLiveReplyNeverRepeatsAnIdenticalEdit(t *testing.T) {
	f := newFakeService(2000, false)
	l := newLiveReply(context.Background(), f, 1, 20*time.Millisecond, t.Logf)

	l.onText(words(12))
	waitFor(t, "the first message", func() bool { return len(f.log()) == 1 })

	for i := 0; i < 3; i++ {
		l.wake <- struct{}{} // woken again, but there is nothing new to show
		time.Sleep(60 * time.Millisecond)
	}
	if n := len(f.log()); n != 1 {
		t.Fatalf("an unchanged message must be left alone, saw %d events: %+v", n, f.log())
	}

	if !l.finish("") {
		t.Fatal("the text was shown, so the reply is delivered")
	}
	ev := f.log()
	if len(ev) != 2 || ev[1].Kind != "edit" || ev[1].Text != words(12) {
		t.Fatalf("finishing should replace the cursor with the final text: %+v", ev)
	}
}

// A streamed reply that outgrows a message rolls over to a new one, and a code
// block it cuts through is closed and reopened, as in SplitMessage.
func TestLiveReplyRollsOverInsideACodeBlock(t *testing.T) {
	f := newFakeService(160, true)
	l := newLiveReply(context.Background(), f, 1, time.Millisecond, t.Logf)

	var code []string
	for i := 0; i < 30; i++ {
		code = append(code, "echo line number")
	}
	full := "```sh\n" + strings.Join(code, "\n") + "\n```"
	for _, line := range strings.SplitAfter(full, "\n") {
		l.onText(line)
		time.Sleep(2 * time.Millisecond)
	}
	if !l.finish("") {
		t.Fatal("the reply was shown")
	}

	f.mu.Lock()
	var ids []int64
	for id := range f.texts {
		ids = append(ids, id)
	}
	final := map[int64]string{}
	for id, txt := range f.texts {
		final[id] = txt
	}
	f.mu.Unlock()
	if len(ids) < 3 {
		t.Fatalf("expected the reply to span several messages, got %d", len(ids))
	}
	lines := 0
	for id := int64(1); id <= int64(len(ids)); id++ {
		txt := final[id]
		if n := len([]rune(txt)); n > 160 {
			t.Fatalf("message %d has %d runes, over the limit", id, n)
		}
		if open, _ := fenceState(txt, false, ""); open {
			t.Fatalf("message %d leaves a code block open: %q", id, txt)
		}
		if strings.HasSuffix(txt, StreamCursor) {
			t.Fatalf("message %d still shows the cursor", id)
		}
		if id > 1 && !strings.HasPrefix(txt, "```sh\n") {
			t.Fatalf("message %d does not reopen the block: %q", id, txt)
		}
		lines += strings.Count(txt, "echo line number")
	}
	if lines != 30 {
		t.Fatalf("code lines lost or repeated: %d of 30", lines)
	}
}

// When the user deletes the message being edited, the reply carries on in a new one.
func TestLiveReplyStartsAgainWhenItsMessageIsGone(t *testing.T) {
	f := newFakeService(2000, false)
	f.editErr, f.outcome = errors.New("gone"), EditGone
	l := newLiveReply(context.Background(), f, 1, time.Millisecond, t.Logf)

	l.onText(words(12))
	waitFor(t, "the first message", func() bool { return len(f.log()) == 1 })
	l.onText(" and more text")
	time.Sleep(30 * time.Millisecond)
	if !l.finish("") {
		t.Fatal("the reply should end up shown")
	}
	ev := f.log()
	last := ev[len(ev)-1]
	if last.Kind != "send" || last.Text != words(12)+" and more text" {
		t.Fatalf("the whole reply should be sent again as a fresh message: %+v", ev)
	}
}

// An edit the service says changed nothing is not an error and not a reason to resend.
func TestLiveReplyTreatsUnchangedAsSuccess(t *testing.T) {
	f := newFakeService(2000, false)
	f.editErr, f.outcome = errors.New("same"), EditUnchanged
	l := newLiveReply(context.Background(), f, 1, time.Millisecond, t.Logf)
	l.onText(words(12))
	waitFor(t, "the first message", func() bool { return len(f.log()) == 1 })
	l.onText(" more")
	time.Sleep(20 * time.Millisecond)
	l.finish("")
	if n := len(f.log()); n != 1 {
		t.Fatalf("no resend expected, saw %+v", f.log())
	}
}

// Cutting in the middle of a long line, with nowhere kind to break, must still
// leave room for the fence that closes the piece.
func TestLiveReplyStaysUnderTheLimitWhenCuttingMidLine(t *testing.T) {
	f := newFakeService(100, true)
	l := newLiveReply(context.Background(), f, 1, time.Millisecond, t.Logf)
	l.onText("```\n" + strings.Repeat("a", 400) + "\n```")
	if !l.finish("") {
		t.Fatal("the reply was shown")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) < 4 {
		t.Fatalf("expected several messages, got %d", len(f.texts))
	}
	for id, txt := range f.texts {
		if n := len([]rune(txt)); n > 100 {
			t.Fatalf("message %d has %d runes, over the limit of 100: %q", id, n, txt)
		}
	}
}
