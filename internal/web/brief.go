package web

import (
	"net/http"
	"regexp"
	"time"
)

// BriefInfo is the morning brief as the app shows it: a daily message the agent writes by itself, from the
// calendar, the mail and what it is tracking, and posts into the chat.
type BriefInfo struct {
	Available bool      `json:"available"`     // the server can schedule it
	Enabled   bool      `json:"enabled"`       // it is on
	At        string    `json:"at,omitempty"`  // the time of day it arrives, "07:30", in the person's zone
	Next      time.Time `json:"next,omitzero"` // when it arrives next
	Last      time.Time `json:"last,omitzero"` // when it last ran
	Status    string    `json:"status,omitempty"`
}

var clock = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// ValidClock says whether s is a time of day such as 07:30.
func ValidClock(s string) bool { return clock.MatchString(s) }

func (s *Server) briefInfo() BriefInfo {
	if s.Sources.Brief == nil || s.Sources.SetBrief == nil {
		return BriefInfo{}
	}
	b := s.Sources.Brief()
	b.Available = true
	return b
}

func (s *Server) briefGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.briefInfo())
}

func (s *Server) briefPost(w http.ResponseWriter, r *http.Request) {
	if s.briefInfo().Available == false {
		fail(w, http.StatusNotImplemented, "the morning brief is not set up here")
		return
	}
	var in struct {
		Enabled bool   `json:"enabled"`
		At      string `json:"at"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.At == "" {
		in.At = "07:30"
	}
	if !ValidClock(in.At) {
		fail(w, http.StatusBadRequest, "give the time as hours and minutes, such as 07:30")
		return
	}
	if err := s.Sources.SetBrief(in.Enabled, in.At); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.briefInfo())
}
