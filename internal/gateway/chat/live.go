package chat

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// StreamCursor marks a message that is still being written. Every edit
	// carries it; the final edit drops it.
	StreamCursor = " ▌"
	// defaultEditInterval is used when neither the engine nor the transport says
	// how often a message may be edited.
	defaultEditInterval = 1200 * time.Millisecond
)

// firstSendRunes is how much text must have arrived before a reply's first
// message is sent, so the push notification shows more than a word or two. A
// variable so tests can shrink it.
var firstSendRunes = 40

// liveReply shows a model's reply in a chat as it is written: the first piece
// of text becomes a message, which is then edited as more arrives.
//
// A reply is a series of segments, one per stretch of text the model writes
// before a tool call; each segment gets its own message, so the chat reads in
// order (text, approval prompt, more text). Text longer than a message holds
// rolls over into a new message.
//
// onText is called from the agent's goroutine and never waits on the network:
// a separate goroutine does the sending and editing.
type liveReply struct {
	t        Transport
	chatID   int64
	ctx      context.Context // outlives the run, so a stopped run can still be tidied up
	interval time.Duration
	logf     func(format string, args ...any)

	mu   sync.Mutex
	text []rune // everything streamed in this segment
	done int    // runes of text already placed in earlier, finished messages

	wake chan struct{}
	quit chan struct{}
	wg   sync.WaitGroup

	flushMu  sync.Mutex // one network flush at a time; guards what follows
	msgID    int64      // the message being edited; 0 until one is sent
	carry    string     // opens the message being written, to reopen a code block the last one closed
	shown    string     // what that message currently displays
	last     time.Time  // when the last send or edit succeeded
	segShown bool       // this segment has put text in front of the user
	anyShown bool       // some segment has
	complete bool       // the last flush delivered everything streamed so far
}

func newLiveReply(ctx context.Context, t Transport, chatID int64, interval time.Duration, logf func(string, ...any)) *liveReply {
	if interval <= 0 {
		interval = defaultEditInterval
	}
	l := &liveReply{t: t, chatID: chatID, ctx: ctx, interval: interval, logf: logf,
		wake: make(chan struct{}, 1), quit: make(chan struct{})}
	l.wg.Add(1)
	go l.loop()
	return l
}

// onText receives the next piece of the model's reply.
func (l *liveReply) onText(s string) {
	l.mu.Lock()
	l.text = append(l.text, []rune(s)...)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *liveReply) loop() {
	defer l.wg.Done()
	for {
		select {
		case <-l.quit:
			return
		case <-l.wake:
		}

		l.flushMu.Lock()
		wait := l.interval - time.Since(l.last)
		l.flushMu.Unlock()
		if wait > 0 { // text keeps accumulating while we wait, so one edit carries it all
			select {
			case <-time.After(wait):
			case <-l.quit:
				return
			}
		}

		l.flushMu.Lock()
		l.flush(false)
		l.flushMu.Unlock()
	}
}

// endSegment finishes the message being written, so the next text starts a new
// one. The agent calls it when the model moves on to a tool.
func (l *liveReply) endSegment() {
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.flushFinal()
	l.mu.Lock()
	l.text, l.done = nil, 0
	l.mu.Unlock()
	l.msgID, l.shown, l.segShown, l.carry = 0, "", false, ""
}

// finish stops the live updates and makes sure the last text is fully shown. It
// reports whether the reply is already in the chat, so the caller need not send
// it again.
func (l *liveReply) finish(reply string) bool {
	close(l.quit)
	l.wg.Wait()

	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.flushFinal()
	if l.segShown && l.complete {
		return true
	}
	// A reply with no text of its own, after text that was already shown, needs no "(no output)".
	return strings.TrimSpace(reply) == "" && l.anyShown
}

// flushFinal is a final flush with one retry, for a send that failed in passing.
func (l *liveReply) flushFinal() {
	for i := 0; i < 2; i++ {
		if l.flush(true); l.complete {
			return
		}
	}
}

// flush shows the pending text. final drops the cursor and sends even a short
// first message. The caller holds flushMu.
func (l *liveReply) flush(final bool) {
	for {
		l.mu.Lock()
		view := append([]rune(nil), l.text[l.done:]...)
		l.mu.Unlock()

		text := strings.TrimSpace(string(view))
		if text == "" {
			l.complete = true
			return
		}

		// Room for the cursor, and for what a split code block adds to each piece.
		room := l.t.MaxMessage() - len([]rune(StreamCursor)) - len([]rune(l.carry))
		if l.t.Markdown() {
			room -= fenceHeadroom
		}
		if len(view) > room { // this message is full: finish it and carry on in the next
			cut := cutAt(view, room)
			piece := l.carry + strings.TrimSpace(string(view[:cut]))
			next := ""
			if l.t.Markdown() {
				if open, lang := fenceState(piece, false, ""); open {
					piece += "\n```"
					next = "```" + lang + "\n"
				}
			}
			if !l.put(piece) {
				l.complete = false
				return
			}
			n := cut
			for n < len(view) && unicode.IsSpace(view[n]) {
				n++
			}
			l.mu.Lock()
			l.done += n
			l.mu.Unlock()
			l.msgID, l.shown, l.carry = 0, "", next
			continue
		}

		if !final {
			if l.msgID == 0 && len(view) < firstSendRunes {
				return
			}
			text += StreamCursor
		}
		l.complete = l.put(l.carry + text)
		return
	}
}

// put sends text as the segment's message, or edits the message already there.
func (l *liveReply) put(text string) bool {
	if text == l.shown {
		return true
	}
	if l.msgID == 0 {
		id, err := l.t.Send(l.ctx, l.chatID, text)
		if err != nil {
			l.logf("live reply: send failed: %v", err)
			return false
		}
		l.msgID = id
	} else if err := l.t.Edit(l.ctx, l.chatID, l.msgID, text); err != nil {
		switch l.t.Classify(err) {
		case EditUnchanged: // already showing it
		case EditGone:
			l.msgID, l.shown = 0, "" // the user deleted it: start a fresh message next time
			return false
		default:
			l.logf("live reply: edit failed: %v", err)
			return false
		}
	}
	l.shown, l.last = text, time.Now()
	l.segShown, l.anyShown = true, true
	return true
}
