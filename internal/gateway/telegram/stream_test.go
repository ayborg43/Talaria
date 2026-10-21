package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
)

// step is one model call: it streams chunks through OnText, then returns.
type step struct {
	chunks []string
	pause  time.Duration // between chunks
	tools  []provider.ToolCall
	err    error
	hang   bool // after streaming, wait until the run is cancelled
}

// streamer is a model that streams its replies the way real providers do.
type streamer struct {
	mu    sync.Mutex
	steps []step
	asked []bool // for each call, whether streaming was requested
}

func (p *streamer) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	p.mu.Lock()
	p.asked = append(p.asked, req.OnText != nil)
	s := step{chunks: []string{"ok"}}
	if len(p.steps) > 0 {
		s, p.steps = p.steps[0], p.steps[1:]
	}
	p.mu.Unlock()

	var content strings.Builder
	for _, c := range s.chunks {
		if req.OnText != nil {
			req.OnText(c)
		}
		content.WriteString(c)
		time.Sleep(s.pause)
	}
	if s.hang {
		<-ctx.Done()
		return provider.Message{}, ctx.Err()
	}
	if s.err != nil {
		return provider.Message{}, s.err
	}
	return provider.Message{Role: provider.RoleAssistant, Content: content.String(), ToolCalls: s.tools}, nil
}

func streaming(g *Gateway) {
	g.Stream = true
	g.EditInterval = 30 * time.Millisecond
}

func (f *fakeTelegram) eventLog() []event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]event(nil), f.events...)
}

// finals returns, per message id in order of appearance, the text it ended with.
func (f *fakeTelegram) finals() (ids []int, text map[int]string) {
	text = map[int]string{}
	for _, e := range f.eventLog() {
		if _, seen := text[e.ID]; !seen {
			ids = append(ids, e.ID)
		}
		text[e.ID] = e.Text
	}
	return ids, text
}

func words(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "word%02d ", i)
	}
	return strings.TrimSpace(b.String())
}

func chunked(s string, size int) []string {
	r := []rune(s)
	var out []string
	for len(r) > 0 {
		n := min(size, len(r))
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}

func TestStreamedReplyIsEditedInPlace(t *testing.T) {
	f := newFake(t)
	full := words(40) // ~280 characters, arriving over ~0.6s
	p := &streamer{steps: []step{{chunks: chunked(full, 14), pause: 30 * time.Millisecond}}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "tell me")}
	f.waitFor("the finished reply", func() bool {
		ev := f.eventLog()
		return len(ev) > 0 && ev[len(ev)-1].Text == full
	})

	ev := f.eventLog()
	if ev[0].Kind != "send" {
		t.Fatalf("the first event should create the message: %+v", ev[0])
	}
	edits := 0
	for i, e := range ev {
		if e.ID != ev[0].ID {
			t.Fatalf("a streamed reply must stay in one message, event %d is message %d: %+v", i, e.ID, ev)
		}
		if e.Kind == "edit" {
			edits++
		}
		if i > 0 && e.Text == ev[i-1].Text {
			t.Fatalf("an edit that changes nothing wastes a rate-limit slot (and Telegram rejects it): %q", e.Text)
		}
		last := i == len(ev)-1
		if last == strings.HasSuffix(e.Text, chat.StreamCursor) {
			t.Fatalf("only unfinished text carries the cursor (event %d last=%v): %q", i, last, e.Text)
		}
	}
	if edits < 2 {
		t.Fatalf("expected the text to grow through several edits, saw %d", edits)
	}
	if n := len(f.texts()); n != 1 {
		t.Fatalf("the finished reply must not be sent again on top: %d messages sent", n)
	}
}

func TestStreamingOffSendsTheWholeReplyOnce(t *testing.T) {
	f := newFake(t)
	p := &streamer{steps: []step{{chunks: []string{"all", " at", " once"}}}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls)) // Stream is off by default

	f.updates <- []Update{userMsg(1, 1, 1, "private", "hi")}
	f.waitFor("the reply", func() bool { return f.sawText("all at once") })

	if p.asked[0] {
		t.Fatal("the model must not be asked to stream when streaming is off")
	}
	if len(f.eventLog()) != 1 || f.eventLog()[0].Kind != "send" {
		t.Fatalf("one plain message expected: %+v", f.eventLog())
	}
}

func TestProviderThatDoesNotStreamStillDelivers(t *testing.T) {
	f := newFake(t)
	var calls atomic.Int32
	// scripted never calls OnText, like a provider without streaming support.
	start(t, f, []int64{1}, echoAgent(&scripted{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "plain reply"}}}, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "hi")}
	f.waitFor("the reply", func() bool { return f.sawText("plain reply") })
	time.Sleep(150 * time.Millisecond)
	if got := f.texts(); len(got) != 1 || got[0] != "plain reply" {
		t.Fatalf("delivered exactly once: %q", got)
	}
}

func TestTextBeforeAToolCallIsItsOwnMessage(t *testing.T) {
	f := newFake(t)
	before := "Let me check the time on this machine for you."
	after := "It is late, so you should probably get some sleep."
	p := &streamer{steps: []step{
		{chunks: []string{before}, tools: []provider.ToolCall{{ID: "1", Name: "current_time", Arguments: "{}"}}},
		{chunks: []string{after}},
	}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "what time is it")}
	f.waitFor("the final answer", func() bool { return f.sawText(after) })
	time.Sleep(100 * time.Millisecond)

	ids, text := f.finals()
	if len(ids) != 2 || text[ids[0]] != before || text[ids[1]] != after {
		t.Fatalf("expected the text before the tool call and the answer as two messages: %v %v", ids, text)
	}
	// The first message is finished before the second begins.
	lastOfFirst, firstOfSecond := -1, -1
	for i, e := range f.eventLog() {
		if e.ID == ids[0] {
			lastOfFirst = i
		} else if firstOfSecond < 0 {
			firstOfSecond = i
		}
	}
	if lastOfFirst > firstOfSecond {
		t.Fatalf("messages interleaved: %+v", f.eventLog())
	}
}

func TestApprovalPromptFollowsTheStreamedText(t *testing.T) {
	f := newFake(t)
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "hi"})
	intro := "I will write that file now, exactly as you asked me to."
	p := &streamer{steps: []step{
		{chunks: []string{intro}, tools: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}},
		{chunks: []string{"The file is written, and everything went fine."}},
	}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "write it")}
	var prompt sentMsg
	f.waitFor("the approval prompt", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, m := range f.sent {
			if m.HasKB {
				prompt = m
				return true
			}
		}
		return false
	})

	// By the time the prompt appears, the introduction is complete and has no cursor.
	var promptAt, introFinalAt = -1, -1
	for i, e := range f.eventLog() {
		if e.KB {
			promptAt = i
		}
		if e.Text == intro {
			introFinalAt = i
		}
	}
	if introFinalAt < 0 || promptAt < introFinalAt {
		t.Fatalf("the introduction must be finished before the prompt is shown: %+v", f.eventLog())
	}

	f.updates <- []Update{{UpdateID: 2, CallbackQuery: &CallbackQuery{
		ID: "y", From: User{ID: 1}, Data: prompt.Callback[0][:strings.LastIndex(prompt.Callback[0], ":")+1] + "y",
		Message: &Message{MessageID: 99, Chat: Chat{ID: 1, Type: "private"}, Text: prompt.Text},
	}}}
	f.waitFor("the closing message", func() bool { return f.sawText("The file is written") })
	if b, err := os.ReadFile(target); err != nil || string(b) != "hi" {
		t.Fatalf("the approved write should have happened: %q %v", b, err)
	}
}

func TestLongStreamedReplyRollsOverIntoNewMessages(t *testing.T) {
	f := newFake(t)
	var b strings.Builder
	for i := 0; b.Len() < 9500; i++ {
		fmt.Fprintf(&b, "line %03d: the quick brown fox jumps over the lazy dog again and again.\n", i)
	}
	full := strings.TrimSpace(b.String())
	p := &streamer{steps: []step{{chunks: chunked(full, 300), pause: 2 * time.Millisecond}}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "long please")}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	f.waitFor("every part of the reply", func() bool {
		ids, text := f.finals()
		var joined strings.Builder
		for _, id := range ids {
			joined.WriteString(text[id])
		}
		return squash(strings.ReplaceAll(joined.String(), chat.StreamCursor, "")) == squash(full) && !strings.Contains(joined.String(), chat.StreamCursor)
	})

	ids, text := f.finals()
	if len(ids) < 3 {
		t.Fatalf("9.5k characters need at least three messages, got %d", len(ids))
	}
	for _, e := range f.eventLog() {
		if n := len([]rune(e.Text)); n > messageLimit+len([]rune(chat.StreamCursor)) || n == 0 {
			t.Fatalf("message %d holds %d characters, over Telegram's limit", e.ID, n)
		}
	}
	for _, id := range ids {
		if strings.HasSuffix(text[id], chat.StreamCursor) {
			t.Fatalf("message %d never lost its cursor", id)
		}
	}
	if n := len(f.texts()); n != len(ids) {
		t.Fatalf("each message is sent once and then edited, never re-sent: %d sends for %d messages", n, len(ids))
	}
}

func TestPartialTextSurvivesAnErrorAndTheErrorFollows(t *testing.T) {
	f := newFake(t)
	partial := "Here is the start of an answer that was cut short by a failure."
	p := &streamer{steps: []step{{chunks: []string{partial}, err: errors.New("provider fell over")}}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "go")}
	f.waitFor("the error message", func() bool { return f.sawText("Error: provider fell over") })

	ids, text := f.finals()
	if text[ids[0]] != partial {
		t.Fatalf("the partial text should be kept, finished and without its cursor: %q", text[ids[0]])
	}
}

func TestStopFinishesTheStreamedMessage(t *testing.T) {
	f := newFake(t)
	partial := "This is the part of the reply that arrived before you hit stop."
	p := &streamer{steps: []step{{chunks: []string{partial}, hang: true}}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "go")}
	f.waitFor("the partial message", func() bool { return len(f.eventLog()) > 0 })
	f.updates <- []Update{userMsg(2, 1, 1, "private", "/stop")}
	f.waitFor("the stop confirmation", func() bool { return f.sawText("Stopped.") })

	f.waitFor("the cursor to go", func() bool {
		ids, text := f.finals()
		return text[ids[0]] == partial
	})
}

func TestEmptyFinalReplyAfterShownTextAddsNothing(t *testing.T) {
	f := newFake(t)
	intro := "I am looking into that for you right now, one moment."
	p := &streamer{steps: []step{
		{chunks: []string{intro}, tools: []provider.ToolCall{{ID: "1", Name: "current_time", Arguments: "{}"}}},
		{chunks: nil}, // the model ends the turn without saying anything more
	}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "go")}
	f.waitFor("the introduction", func() bool { return f.sawText(intro) })
	time.Sleep(300 * time.Millisecond)
	if f.sawText("(no output)") {
		t.Fatalf("nothing is missing here, so no placeholder is wanted: %q", f.texts())
	}
}

func TestBlankChunksNeverProduceBlankMessages(t *testing.T) {
	f := newFake(t)
	text := "A real sentence follows the blank lines, long enough to be sent."
	p := &streamer{steps: []step{{chunks: []string{"\n", "\n\n", text}, pause: 40 * time.Millisecond}}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming)

	f.updates <- []Update{userMsg(1, 1, 1, "private", "go")}
	f.waitFor("the text", func() bool {
		ev := f.eventLog()
		return len(ev) > 0 && ev[len(ev)-1].Text == text
	})
	for _, e := range f.eventLog() {
		if strings.TrimSpace(strings.TrimSuffix(e.Text, chat.StreamCursor)) == "" {
			t.Fatalf("Telegram rejects empty messages, saw %q", e.Text)
		}
	}
}

func TestRateLimitedEditsAreRetriedAndTheTextArrives(t *testing.T) {
	f := newFake(t)
	f.failEdits.Store(2) // the first two edits are refused with "retry after 1"
	full := words(40)
	p := &streamer{steps: []step{{chunks: chunked(full, 14), pause: 30 * time.Millisecond}}}
	var calls atomic.Int32
	var waits []time.Duration
	var mu sync.Mutex
	start(t, f, []int64{1}, echoAgent(p, &calls), streaming, func(g *Gateway) {
		g.API.RetrySleep = func(_ context.Context, d time.Duration) error {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
			return nil
		}
	})

	f.updates <- []Update{userMsg(1, 1, 1, "private", "go")}
	f.waitFor("the finished text", func() bool {
		ev := f.eventLog()
		return len(ev) > 0 && ev[len(ev)-1].Text == full
	})
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 2 || waits[0] != time.Second {
		t.Fatalf("the two refusals should each have been waited out for the time Telegram asked: %v", waits)
	}
	if f.editCalls.Load() < 4 {
		t.Fatalf("refused edits are retried, saw %d edit requests", f.editCalls.Load())
	}
}
