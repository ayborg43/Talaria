package web

import (
	"net/http"
	"strings"
	"time"
)

// SuggestionInfo is something the agent proposes to do: a title, why, where it came from, and when. Starting it
// runs a background task; the prompt it would run stays on the server.
type SuggestionInfo struct {
	ID      string             `json:"id"`
	Title   string             `json:"title"`
	Body    string             `json:"body,omitempty"`
	Sources []SuggestionSource `json:"sources,omitempty"`
	At      time.Time          `json:"at"`
	// Startable is false for an idea with nothing to run (a research note): it can only be hidden.
	Startable bool `json:"startable"`
}

// SuggestionSource is one piece of evidence: a page, or mail:<id> or calendar:<name> for what was read.
type SuggestionSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (s *Server) suggestions() []SuggestionInfo {
	if s.Sources.Suggestions == nil {
		return []SuggestionInfo{}
	}
	if l := s.Sources.Suggestions(); l != nil {
		return l
	}
	return []SuggestionInfo{}
}

func (s *Server) suggestionsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": s.suggestions(), "available": s.Sources.Suggestions != nil})
}

// suggestionsPost starts or hides one suggestion. Start makes a background task that cannot use a tool that needs
// approval, exactly like a task the agent starts in chat, and moves the suggestion out of the list.
func (s *Server) suggestionsPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start string `json:"start"`
		Hide  string `json:"hide"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Start, in.Hide = strings.TrimSpace(in.Start), strings.TrimSpace(in.Hide)
	if s.Sources.Suggestions == nil {
		fail(w, http.StatusNotImplemented, "suggestions are not set up here")
		return
	}
	if len(in.Start) > 64 || len(in.Hide) > 64 || (in.Start == "") == (in.Hide == "") {
		fail(w, http.StatusBadRequest, "say which suggestion to start, or which to hide")
		return
	}
	out := map[string]any{"ok": true}
	switch {
	case in.Start != "":
		if s.Sources.StartSuggestion == nil {
			fail(w, http.StatusNotImplemented, "suggestions cannot be started here")
			return
		}
		name, err := s.Sources.StartSuggestion(in.Start)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		out["started"] = name
	default:
		if s.Sources.HideSuggestion == nil {
			fail(w, http.StatusNotImplemented, "suggestions cannot be hidden here")
			return
		}
		if err := s.Sources.HideSuggestion(in.Hide); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	out["suggestions"] = s.suggestions()
	writeJSON(w, http.StatusOK, out)
}
