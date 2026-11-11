package web

import (
	"net/http"
	"strings"

	"talaria/internal/runlog"
)

// RunHooks connect the Activity view to what can be done about a run.
type RunHooks struct {
	// FollowUp starts a new background task that carries on from a finished run, with what the user wrote, and
	// returns its name.
	FollowUp func(runID, text string) (string, error)
	// Stop stops a run that is in progress.
	Stop func(runID string) error
}

func (s *Server) runsOn() bool { return s.Runs != nil }

// runsGet lists what is running and what has run lately.
func (s *Server) runsGet(w http.ResponseWriter, r *http.Request) {
	if !s.runsOn() {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []runlog.Summary{}, "available": false})
		return
	}
	list := s.Runs.List("web", ChatID, 30)
	if list == nil {
		list = []runlog.Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": list, "available": true})
}

// runGet is one run: its steps, files, result and what the user told it. The id may be a run's or its job's.
func (s *Server) runGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.runsOn() || len(id) > 96 {
		fail(w, http.StatusNotFound, "there is no record of that")
		return
	}
	run, ok := s.Runs.Get("web", ChatID, id)
	if !ok {
		fail(w, http.StatusNotFound, "there is no record of that run: it may be older than the last thirty, or ran before records were kept")
		return
	}
	if s.Scrub != nil {
		run.Prompt, run.Result = s.Scrub(run.Prompt), s.Scrub(run.Result)
	}
	writeJSON(w, http.StatusOK, run)
}

// runSteer says something to a run. While it works the words are read at its next step; once it has finished they
// become a follow-up task that starts from what it did.
func (s *Server) runSteer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case !s.runsOn():
		fail(w, http.StatusNotImplemented, "activity is not set up here")
		return
	case text == "":
		fail(w, http.StatusBadRequest, "write what you want it to do")
		return
	case len(text) > 2*runlog.MaxSteerText:
		fail(w, http.StatusBadRequest, "that is too long: keep it to a few sentences")
		return
	}
	run, ok := s.Runs.Get("web", ChatID, id)
	if !ok {
		fail(w, http.StatusNotFound, "there is no record of that run")
		return
	}
	if run.Status == runlog.Running {
		if !s.Runs.Steer(run.ID, text) {
			fail(w, http.StatusConflict, "it already has a few notes it has not read yet: wait a moment")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "steer"})
		return
	}
	if s.Sources.Runs == nil || s.Sources.Runs.FollowUp == nil {
		fail(w, http.StatusNotImplemented, "this task has finished, and following up is not set up here")
		return
	}
	name, err := s.Sources.Runs.FollowUp(run.ID, text)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "follow_up", "started": name})
}

// runStop stops a run that is in progress.
func (s *Server) runStop(w http.ResponseWriter, r *http.Request) {
	if !s.runsOn() || s.Sources.Runs == nil || s.Sources.Runs.Stop == nil {
		fail(w, http.StatusNotImplemented, "activity is not set up here")
		return
	}
	run, ok := s.Runs.Get("web", ChatID, r.PathValue("id"))
	if !ok || run.Status != runlog.Running {
		fail(w, http.StatusBadRequest, "that is not running")
		return
	}
	if err := s.Sources.Runs.Stop(run.ID); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
