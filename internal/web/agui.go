package web

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"talaria/internal/gateway/chat"
)

// AG-UI is an open protocol for talking to an agent from a user interface: the client posts a run (the thread,
// the messages so far), and the agent answers with a stream of events: a run starts, text arrives piece by piece,
// tools are called and return, the run ends. Speaking it lets a client that was not written for Talaria, such as
// a native phone app built on an AG-UI library, hold the same conversation as the web page.
//
// This is a second door into the one conversation, not a second agent: a run posted here is a message to the web
// chat, goes through the same engine, permissions and history, and shows on the page as it happens. It is off
// unless a key is set (TALARIA_AGUI_KEY), because a client that is not this page cannot use the page's sign-in.
//
// It keeps nothing: each request translates the events of its own run and ends with it.

const (
	aguiMaxBody = 256 << 10
	// aguiQuiet is how long to wait for a run to begin before taking the message for one the engine answered
	// without the model (a command such as /new), and ending the stream.
	aguiQuiet = 8 * time.Second
	// aguiMaxRun bounds one stream. The run itself goes on if the client leaves or this passes.
	aguiMaxRun = 30 * time.Minute
)

// aguiAllowed says whether a request may use the AG-UI door: it carries the key, or it comes from the signed-in
// page itself. A wrong key waits a moment, so guesses are slow; the key is at least 24 characters, so they are hopeless.
func (s *Server) aguiAllowed(r *http.Request) bool {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.AGUIKey != "" {
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(bearer)), []byte(s.AGUIKey)) == 1 {
			return true
		}
		time.Sleep(300 * time.Millisecond)
		return false
	}
	return SameOrigin(r, s.origin()) && s.Auth.Valid(r)
}

// aguiText is the text of a message's content, which the protocol allows as a string or as a list of parts.
func aguiText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// accept takes in what the user said, from the page or from an AG-UI client: it is shown in the conversation
// (unless it is a command) and handed to the engine.
func (s *Server) accept(text string, alsoTelegram bool) {
	name := ""
	if strings.HasPrefix(text, "/") {
		name = strings.ToLower(strings.TrimPrefix(strings.Fields(text)[0], "/"))
	}
	if name == "" || !s.Engine.HasCommand(name) { // a command is not part of the conversation
		s.Hub.AddUser(text)
	}
	if name == "new" { // the open conversation is kept, and a blank one starts
		s.newConversation()
	}
	toAgent := text
	if alsoTelegram && s.Telegram && name == "" {
		toAgent += telegramAsk
	}
	s.Engine.Incoming(ChatID, UserID, toAgent)
}

func (s *Server) agui(w http.ResponseWriter, r *http.Request) {
	if !s.aguiAllowed(r) {
		fail(w, http.StatusUnauthorized, "this needs the AG-UI key, as a bearer token")
		return
	}
	var in struct {
		ThreadID string `json:"threadId"`
		RunID    string `json:"runId"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, aguiMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "that run could not be read")
		return
	}
	// The conversation lives here, not in the client: of what it sends, only the newest thing the user said is new.
	text := ""
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == "user" {
			text = strings.TrimSpace(aguiText(in.Messages[i].Content))
			break
		}
	}
	switch {
	case text == "":
		fail(w, http.StatusBadRequest, "the run has no message from the user")
		return
	case len([]rune(text)) > maxMessage:
		fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("that is longer than %d characters", maxMessage))
		return
	}
	if in.ThreadID == "" {
		in.ThreadID = "talaria"
	}
	if in.RunID == "" {
		in.RunID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}

	// Listen before speaking, so nothing the run does is missed.
	_, seq, epoch, _ := s.Hub.Snapshot()
	ch, cancel := s.Hub.Subscribe(epoch, seq)
	defer cancel()
	s.accept(text, false)

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	emit := func(typ string, fields map[string]any) bool {
		ev := map[string]any{"type": typ, "timestamp": time.Now().UnixMilli()}
		for k, v := range fields {
			ev[k] = v
		}
		raw, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		rc.Flush()
		return true
	}
	if !emit("RUN_STARTED", map[string]any{"threadId": in.ThreadID, "runId": in.RunID}) {
		return
	}

	var (
		first      = true               // the first run event only says how things stood when we began listening
		skipEnds   = 0                  // ends of runs that were already going, which are not ours
		started    = false              // our run has begun
		openText   = ""                 // the text message being written, by its AG-UI id
		texts      = map[int64]string{} // what each assistant message said last time
		parts      = map[int64]int{}    // how many AG-UI messages one of ours has become, when it was rewritten
		toolsBegun = map[int64]bool{}
		toolsDone  = map[int64]bool{}
		asked      = map[int64]string{}
	)
	closeText := func() bool {
		if openText == "" {
			return true
		}
		id := openText
		openText = ""
		return emit("TEXT_MESSAGE_END", map[string]any{"messageId": id})
	}
	finish := func() {
		if closeText() {
			emit("RUN_FINISHED", map[string]any{"threadId": in.ThreadID, "runId": in.RunID})
		}
	}
	quiet := time.NewTimer(aguiQuiet)
	defer quiet.Stop()
	limit := time.NewTimer(aguiMaxRun)
	defer limit.Stop()
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return // the client left; the run carries on, and is on the page
		case <-limit.C:
			finish()
			return
		case <-quiet.C:
			if !started { // nothing began: the message was answered without a run
				finish()
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			rc.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Type {
			case "reset":
				closeText()
				emit("RUN_ERROR", map[string]any{"message": "the conversation was replaced while this run was going", "code": "reset"})
				return
			case "run":
				switch {
				case first:
					first = false
					if ev.On {
						skipEnds = 1 // something was already running: its end is not ours, and ours begins after it
					}
				case ev.On:
					started = true
				case skipEnds > 0:
					skipEnds--
				case started:
					finish()
					return
				}
			case "msg":
				m := ev.Msg
				if m == nil {
					continue
				}
				alive := true
				switch m.Role {
				case "assistant":
					prev, seen := texts[m.ID]
					id := fmt.Sprintf("m%d.%d", m.ID, parts[m.ID])
					if seen && !strings.HasPrefix(m.Text, prev) {
						// It was rewritten rather than added to, which a stream of pieces cannot say: end that
						// message and say this one whole, as its next part.
						if openText == id {
							alive = closeText()
						}
						parts[m.ID]++
						id, prev = fmt.Sprintf("m%d.%d", m.ID, parts[m.ID]), ""
					}
					if openText != id {
						alive = alive && closeText() && emit("TEXT_MESSAGE_START", map[string]any{"messageId": id, "role": "assistant"})
						openText = id
					}
					if delta := m.Text[len(prev):]; delta != "" {
						alive = alive && emit("TEXT_MESSAGE_CONTENT", map[string]any{"messageId": id, "delta": delta})
					}
					texts[m.ID] = m.Text
				case "tool":
					call := fmt.Sprintf("t%d", m.ID)
					if !toolsBegun[m.ID] {
						toolsBegun[m.ID] = true
						// The engine reports a call by its name; its arguments are in the activity log, not on this stream.
						alive = closeText() &&
							emit("TOOL_CALL_START", map[string]any{"toolCallId": call, "toolCallName": m.Tool}) &&
							emit("TOOL_CALL_ARGS", map[string]any{"toolCallId": call, "delta": "{}"}) &&
							emit("TOOL_CALL_END", map[string]any{"toolCallId": call})
					}
					if m.State != "running" && !toolsDone[m.ID] {
						toolsDone[m.ID] = true
						alive = alive && emit("TOOL_CALL_RESULT", map[string]any{"messageId": fmt.Sprintf("r%d", m.ID), "toolCallId": call, "role": "tool", "content": m.Result})
						if len(m.Artifact) > 0 {
							alive = alive && emit("CUSTOM", map[string]any{"name": "talaria.artifact", "value": map[string]any{"toolCallId": call, "tool": m.Tool, "artifact": m.Artifact}})
						}
					}
				case "approval":
					if asked[m.ID] != m.State {
						asked[m.ID] = m.State
						value := map[string]any{"id": m.ID, "state": m.State, "text": m.Text}
						if m.State == "pending" {
							value["token"], value["always"] = m.Token, m.Always
						}
						alive = closeText() && emit("CUSTOM", map[string]any{"name": "talaria.approval", "value": value})
					}
				}
				if !alive {
					return
				}
			}
		}
	}
}

// aguiApprove answers a question a run raised, for a client that came in by the AG-UI door.
func (s *Server) aguiApprove(w http.ResponseWriter, r *http.Request) {
	if !s.aguiAllowed(r) {
		fail(w, http.StatusUnauthorized, "this needs the AG-UI key, as a bearer token")
		return
	}
	s.decide(w, r)
}

// decide reads an answer to a question and gives it to the engine.
func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Choice string }
	if !readJSON(w, r, &in) {
		return
	}
	choice, ok := map[string]chat.Choice{"allow": chat.ChoiceAllow, "always": chat.ChoiceAlways, "deny": chat.ChoiceDeny}[in.Choice]
	if !ok || in.Token == "" || len(in.Token) > 64 {
		fail(w, http.StatusBadRequest, "that answer is not one of allow, always or deny")
		return
	}
	decision, got := s.Engine.Decide(in.Token, UserID, ChatID, choice)
	switch decision {
	case chat.DecisionExpired:
		fail(w, http.StatusGone, chat.MsgExpired)
	case chat.DecisionForbidden:
		fail(w, http.StatusForbidden, chat.MsgNotAllowed)
	case chat.DecisionAnswered:
		fail(w, http.StatusConflict, chat.MsgAnswered)
	default:
		s.Hub.Resolve(in.Token, got)
		w.WriteHeader(http.StatusNoContent)
	}
}
