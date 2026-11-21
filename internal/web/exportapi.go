package web

import (
	"net/http"
	"strconv"
	"time"

	"talaria/internal/export"
)

// trajectoriesGet is the kept conversations as a JSON Lines file, one conversation per line in the chat-completions
// message format, for training or testing a model elsewhere. ?since=2026-11-01, ?archived=1 and ?min_tools=3 narrow it.
// Known secrets are scrubbed, pictures and the system prompt are left out.
func (s *Server) trajectoriesGet(w http.ResponseWriter, r *http.Request) {
	if s.Convos == nil {
		fail(w, http.StatusNotFound, "there are no kept conversations here")
		return
	}
	q := r.URL.Query()
	o := export.Options{IncludeArchived: q.Get("archived") != "", Scrub: s.Scrub}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			fail(w, http.StatusBadRequest, "since is a date such as 2026-11-01")
			return
		}
		o.Since = t
	}
	if v := q.Get("min_tools"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1000 {
			fail(w, http.StatusBadRequest, "min_tools is a number")
			return
		}
		o.MinToolCalls = n
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="talaria-trajectories-`+time.Now().Format("20060102")+`.jsonl"`)
	w.Header().Set("Cache-Control", "no-store")
	if _, err := export.Write(w, s.Convos.Sources(), o); err != nil {
		s.logf("web: export: %v", err)
	}
}
