package web

import (
	"net/http"
	"strings"

	"talaria/internal/provider"
)

// ---- conversations ----
//
// The web chat keeps every conversation, not just the one showing: opening a
// new one or going back to an old one never loses anything, because the open
// one is written down whenever it changes. The hub shows the active
// conversation; the engine's agent carries its history.

// saveActive writes down what the open conversation looks like and what its
// agent knows, so it can be taken up again later. A chat that never started
// one adopts what is on the page as its first.
func (s *Server) SaveActive() {
	if s.Convos == nil {
		return
	}
	id := s.Convos.ActiveID()
	msgs, _, _, _ := s.Hub.Snapshot()
	var hist []provider.Message
	if s.Engine != nil {
		hist = s.Engine.History(ChatID)
	}
	if id == "" {
		if len(msgs) == 0 && len(hist) == 0 {
			id = s.Convos.New()
		} else {
			id = s.Convos.New()
			s.Convos.Save(id, msgs, hist)
		}
		return
	}
	s.Convos.Save(id, msgs, hist)
}

// adoptActiveConversation puts back the conversation that was open when
// Talaria stopped: the page shows what it showed, and the agent knows what
// it knew. When nothing was ever kept, what is on the page becomes the
// first conversation.
func (s *Server) AdoptActiveConversation() {
	if s.Convos == nil {
		return
	}
	id := s.Convos.ActiveID()
	msgs, _, _, _ := s.Hub.Snapshot()
	var hist []provider.Message
	if s.Engine != nil {
		hist = s.Engine.History(ChatID)
	}
	if id == "" {
		if len(msgs) == 0 && len(hist) == 0 {
			s.Convos.New()
		} else {
			s.Convos.Save(s.Convos.New(), msgs, hist)
		}
		return
	}
	cv, ok := s.Convos.Get(id)
	if !ok || (len(cv.Display) == 0 && len(cv.Transcript) == 0) {
		s.Convos.Save(id, msgs, hist)
		return
	}
	s.Hub.Replace(cv.Display)
	if s.Engine != nil {
		s.Engine.SetHistory(ChatID, cv.Transcript)
	}
}

// showConversation opens a kept conversation: the running task stops, the
// page shows what it held, and the agent carries on from its history.
func (s *Server) showConversation(id string) error {
	cv, ok := s.Convos.Open(id)
	if !ok {
		return errUnknownConversation
	}
	s.Hub.Replace(cv.Display)
	if s.Engine != nil {
		s.Engine.SetHistory(ChatID, cv.Transcript)
	}
	return nil
}

var errUnknownConversation = errorString("no conversation with that id is kept")

type errorString string

func (e errorString) Error() string { return string(e) }

// chats lists every kept conversation, newest first.
func (s *Server) chats(w http.ResponseWriter, r *http.Request) {
	if s.Convos == nil {
		writeJSON(w, http.StatusOK, map[string]any{"chats": []ConvoMeta{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": s.Convos.List()})
}

// switchChat opens a new blank conversation or a kept one. Whatever was open
// is already kept, so nothing is lost either way.
func (s *Server) switchChat(w http.ResponseWriter, r *http.Request) {
	if s.Convos == nil {
		fail(w, http.StatusServiceUnavailable, "conversations are not kept here")
		return
	}
	var in struct {
		New  bool   `json:"new"`
		Open string `json:"open"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.SaveActive()
	switch {
	case in.New:
		id := s.Convos.New()
		s.Hub.Replace(nil)
		if s.Engine != nil {
			s.Engine.SetHistory(ChatID, nil)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
	case strings.TrimSpace(in.Open) != "":
		if err := s.showConversation(strings.TrimSpace(in.Open)); err != nil {
			fail(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": strings.TrimSpace(in.Open)})
	default:
		fail(w, http.StatusBadRequest, "say which conversation: {\"new\":true} or {\"open\":id}")
	}
}

// newConversation starts a blank conversation the way the new-chat button
// does: whatever was open is kept, and the page starts empty.
func (s *Server) newConversation() string {
	if s.Convos == nil {
		s.Hub.Reset()
		return ""
	}
	s.SaveActive()
	id := s.Convos.New()
	s.Hub.Replace(nil)
	if s.Engine != nil {
		s.Engine.SetHistory(ChatID, nil)
	}
	return id
}
