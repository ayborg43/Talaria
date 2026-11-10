package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"talaria/internal/browser"
)

// handoffWait is how long the agent waits for the user before it carries on without them.
const handoffWait = 20 * time.Minute

// Handoff is how the agent asks the user to take over its browser tab for a sign-in, a code or a check. It puts a
// card in the chat with a button that opens the tab and one to say they are done, notifies the user if the page is
// not open, and waits until they answer, write in the chat, stop the agent, or the time runs out. It returns what
// they did (see browser.HandoffDone and the others).
func (h *Hub) Handoff(ctx context.Context, reason, url string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	ch := make(chan string, 1)

	h.mu.Lock()
	h.init()
	if h.handoffs == nil {
		h.handoffs = map[string]chan string{}
	}
	h.handoffs[token] = ch
	m := h.add(&Message{Role: "handoff", Text: clip(reason), State: "pending", Token: token, URL: clip(url)})
	id := m.ID
	if h.Notifier != nil && h.awayLocked() {
		title := "Needs you in the browser"
		if url == "" { // not a page to take over: an action the user's rules say they do themselves
			title = "Needs you"
		}
		go h.Notifier.Notify(title, firstLine(reason), "approval", "/#chat")
	}
	h.mu.Unlock()

	outcome, state := browser.HandoffTimeout, "expired"
	timer := time.NewTimer(handoffWait)
	defer timer.Stop()
	select {
	case got := <-ch:
		outcome = got
		state = map[string]string{browser.HandoffDone: "done", browser.HandoffSkip: "skipped", browser.HandoffReplied: "replied"}[got]
	case <-ctx.Done():
		state = "expired" // the agent was stopped
		outcome = browser.HandoffTimeout
	case <-timer.C:
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.handoffs, token)
	if m := h.byID[id]; m != nil && m.State == "pending" {
		m.State, m.Token = state, ""
		h.publish(m)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return outcome, nil
}

// ResolveHandoff records the user's answer to a handoff and lets the waiting agent go on. It says whether the
// token named one that was still waiting.
func (h *Hub) ResolveHandoff(token, action string) bool {
	outcome := map[string]string{"done": browser.HandoffDone, "skip": browser.HandoffSkip}[action]
	if outcome == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.handoffs[token]
	if !ok {
		return false
	}
	select {
	case ch <- outcome:
	default:
	}
	delete(h.handoffs, token)
	return true
}

func (s *Server) handoffPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token  string `json:"token"`
		Action string `json:"action"` // done or skip
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Token, in.Action = strings.TrimSpace(in.Token), strings.TrimSpace(in.Action)
	if len(in.Token) > 64 || (in.Action != "done" && in.Action != "skip") {
		fail(w, http.StatusBadRequest, "say done or skip, and which request")
		return
	}
	if !s.Hub.ResolveHandoff(in.Token, in.Action) {
		fail(w, http.StatusNotFound, "that request is no longer waiting")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
