package web

import (
	"net/http"
	"strconv"
	"time"
)

// DigestInfo is the scheduled "while you were away" digest as the app shows it: a plain account of what Talaria did on
// its own, written without asking a model and posted into the chat, daily or each Monday.
type DigestInfo struct {
	Available bool      `json:"available"`
	Enabled   bool      `json:"enabled"`
	At        string    `json:"at,omitempty"`
	Weekly    bool      `json:"weekly,omitempty"`
	Next      time.Time `json:"next,omitzero"`
	Last      time.Time `json:"last,omitzero"`
}

func (s *Server) digestInfo() DigestInfo {
	if s.Sources.Digest == nil || s.Sources.SetDigest == nil {
		return DigestInfo{}
	}
	d := s.Sources.Digest()
	d.Available = true
	return d
}

// digestGet answers with the schedule, and with ?hours=N also with the digest for the last N hours, so the page can
// show it at once.
func (s *Server) digestGet(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"schedule": s.digestInfo()}
	if h := r.URL.Query().Get("hours"); h != "" && s.Sources.DigestNow != nil {
		n, _ := strconv.Atoi(h)
		if n < 1 || n > 24*30 {
			n = 24
		}
		out["text"] = s.Sources.DigestNow(time.Duration(n) * time.Hour)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) digestPost(w http.ResponseWriter, r *http.Request) {
	if !s.digestInfo().Available {
		fail(w, http.StatusNotImplemented, "the digest is not set up here")
		return
	}
	var in struct {
		Enabled bool   `json:"enabled"`
		At      string `json:"at"`
		Weekly  bool   `json:"weekly"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.At == "" {
		in.At = "08:00"
	}
	if !ValidClock(in.At) {
		fail(w, http.StatusBadRequest, "give the time as hours and minutes, such as 08:00")
		return
	}
	if err := s.Sources.SetDigest(in.Enabled, in.At, in.Weekly); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule": s.digestInfo()})
}
