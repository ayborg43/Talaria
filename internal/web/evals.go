package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"talaria/internal/evals"
	"talaria/internal/provider"
)

// evalState is the one run that may be going at a time, and the last thing it said.
type evalState struct {
	mu      sync.Mutex
	running string // the profile being tested, "" when idle
	done    int
	total   int
	failed  string // why the last run could not start or finish
}

type evalsView struct {
	Cases    []evals.Case `json:"cases"`
	Runs     []evals.Run  `json:"runs"`
	Profiles []string     `json:"profiles"`
	Running  string       `json:"running"`
	Done     int          `json:"done"`
	Total    int          `json:"total"`
	Failed   string       `json:"failed,omitempty"`
}

func (s *Server) evalsView() evalsView {
	v := evalsView{Cases: []evals.Case{}, Runs: []evals.Run{}, Profiles: []string{}}
	if s.Evals == nil {
		return v
	}
	if c := s.Evals.Cases(); c != nil {
		v.Cases = c
	}
	if r := s.Evals.Runs(); r != nil {
		v.Runs = r
	}
	if s.Cfg != nil {
		for n := range s.Cfg.Profiles {
			v.Profiles = append(v.Profiles, n)
		}
		slices.Sort(v.Profiles)
	}
	s.evalRun.mu.Lock()
	v.Running, v.Done, v.Total, v.Failed = s.evalRun.running, s.evalRun.done, s.evalRun.total, s.evalRun.failed
	s.evalRun.mu.Unlock()
	return v
}

func (s *Server) evalsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.evalsView())
}

// evalsPost adds or removes a test question, or starts a run of them on a model. A run goes on in the background, one at a
// time; the page asks again to see how far it got.
func (s *Server) evalsPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Add    *evals.Case `json:"add"`
		Delete string      `json:"delete"`
		Run    string      `json:"run"`
		Judge  string      `json:"judge"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Evals == nil {
		fail(w, http.StatusNotFound, "test questions are not set up here")
		return
	}
	switch {
	case in.Add != nil:
		if _, err := s.Evals.Add(*in.Add); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	case in.Delete != "":
		if !s.Evals.Remove(in.Delete) {
			fail(w, http.StatusNotFound, "that question is gone")
			return
		}
	case in.Run != "":
		if status, msg := s.startEvalRun(strings.TrimSpace(in.Run), strings.TrimSpace(in.Judge)); status != 0 {
			fail(w, status, msg)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.evalsView())
}

func (s *Server) startEvalRun(profile, judge string) (int, string) {
	if s.Engine == nil || s.Engine.Backends == nil || s.Engine.Backends.OpenRaw == nil {
		return http.StatusBadRequest, "there are no model profiles to test"
	}
	cases := s.Evals.Cases()
	if len(cases) == 0 {
		return http.StatusBadRequest, "write at least one test question first"
	}
	be, err := s.Engine.Backends.OpenRaw(profile)
	if err != nil {
		return http.StatusBadRequest, "that model cannot be used: " + err.Error()
	}
	var jp provider.Provider
	var jmodel string
	if judge != "" {
		jb, err := s.Engine.Backends.OpenRaw(judge)
		if err != nil {
			return http.StatusBadRequest, "the judging model cannot be used: " + err.Error()
		}
		jp, jmodel = jb.Provider, jb.Model
	}
	s.evalRun.mu.Lock()
	if s.evalRun.running != "" {
		s.evalRun.mu.Unlock()
		return http.StatusConflict, "a test of " + s.evalRun.running + " is already running"
	}
	s.evalRun.running, s.evalRun.done, s.evalRun.total, s.evalRun.failed = profile, 0, len(cases), ""
	s.evalRun.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		var results evals.Run2
		for i, c := range cases {
			results = append(results, evals.Execute(ctx, []evals.Case{c}, be.Provider, be.Model, jp, jmodel)...)
			s.evalRun.mu.Lock()
			s.evalRun.done = i + 1
			s.evalRun.mu.Unlock()
		}
		if len(results) > 0 {
			s.Evals.Keep(results.Summary(profile, be.Model, judge))
		}
		s.evalRun.mu.Lock()
		s.evalRun.running = ""
		if ctx.Err() != nil {
			s.evalRun.failed = "the test took too long and was stopped"
		}
		s.evalRun.mu.Unlock()
	}()
	return 0, ""
}
