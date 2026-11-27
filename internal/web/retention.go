package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"talaria/internal/config"
)

// retentionState is what the last sweep did, kept in memory for the page.
type retentionState struct {
	mu     sync.Mutex
	At     time.Time
	Result SweepResult
	Pruned int
}

func (s *Server) keepDays() (archive, history int) {
	if s.Cfg == nil {
		return 0, 0
	}
	return s.Cfg.Conversations.KeepDays, s.Cfg.Conversations.HistoryKeepDays
}

func cutoff(now time.Time, days int) time.Time {
	if days <= 0 {
		return time.Time{}
	}
	return now.AddDate(0, 0, -days)
}

// sweepChats applies the keep-for setting once: it archives conversations nobody has touched for that long, deletes those
// the sweep archived a month ago, and forgets old searchable history if that is set. With no setting it does nothing.
func (s *Server) sweepChats(now time.Time, dry bool) (SweepResult, int) {
	archive, history := s.keepDays()
	var res SweepResult
	if s.Convos != nil && archive > 0 {
		res = s.Convos.Sweep(cutoff(now, archive), now.AddDate(0, 0, -config.GraceDays), dry)
	}
	pruned := 0
	if !dry && history > 0 && s.Sources.PruneHistory != nil {
		n, err := s.Sources.PruneHistory(cutoff(now, history))
		if err != nil {
			s.logf("web: pruning the history: %v", err)
		}
		pruned = n
	}
	if !dry && (res.Archived+res.Deleted+pruned > 0) {
		s.logf("web: old chats: %d archived, %d deleted, %d messages forgotten", res.Archived, res.Deleted, pruned)
		s.retention.mu.Lock()
		s.retention.At, s.retention.Result, s.retention.Pruned = now, res, pruned
		s.retention.mu.Unlock()
	}
	return res, pruned
}

// sweepChatsLoop runs the sweep a minute after start and then every few hours until the server stops. The memory
// review runs on the same clock: it keeps its own stamp, so a pass that already read the history is not repeated.
func (s *Server) sweepChatsLoop(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepChats(time.Now(), false)
			s.reviewMemory(ctx)
			t.Reset(6 * time.Hour)
		}
	}
}

// reviewMemory runs the consolidation pass, if one is set up. It is given its own bounded time: a slow model
// must not hold up the sweep, and a failure is only logged — the next sweep tries again.
func (s *Server) reviewMemory(ctx context.Context) {
	if s.Sources.ConsolidateMemory == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	res, err := s.Sources.ConsolidateMemory(ctx, false)
	if err != nil {
		s.logf("web: reviewing conversations for the notes: %v", err)
		return
	}
	if res.Sessions > 0 {
		s.logf("web: memory review: %s", res.Summary())
	}
}

type retentionView struct {
	Available       bool      `json:"available"`
	KeepDays        int       `json:"keep_days"`
	HistoryKeepDays int       `json:"history_keep_days"`
	GraceDays       int       `json:"grace_days"`
	WouldArchive    int       `json:"would_archive"`
	WouldDelete     int       `json:"would_delete"`
	LastAt          time.Time `json:"last_at,omitzero"`
	LastArchived    int       `json:"last_archived,omitempty"`
	LastDeleted     int       `json:"last_deleted,omitempty"`
	LastPruned      int       `json:"last_pruned,omitempty"`
	CanPruneHistory bool      `json:"can_prune_history"`
}

func (s *Server) retentionView() retentionView {
	v := retentionView{Available: s.Convos != nil && s.Cfg != nil, GraceDays: config.GraceDays}
	if !v.Available {
		return v
	}
	v.KeepDays, v.HistoryKeepDays = s.keepDays()
	v.CanPruneHistory = s.Sources.PruneHistory != nil
	dry, _ := s.sweepChats(time.Now(), true)
	v.WouldArchive, v.WouldDelete = dry.Archived, dry.Deleted
	s.retention.mu.Lock()
	v.LastAt, v.LastArchived, v.LastDeleted, v.LastPruned = s.retention.At, s.retention.Result.Archived, s.retention.Result.Deleted, s.retention.Pruned
	s.retention.mu.Unlock()
	return v
}

func (s *Server) retentionGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.retentionView())
}

// retentionPost sets how long old chats are kept. Both numbers are in days and 0 means forever, the default: nothing is
// ever removed unless the person chooses it here. {"sweep": true} runs the sweep now.
func (s *Server) retentionPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		KeepDays        *int `json:"keep_days"`
		HistoryKeepDays *int `json:"history_keep_days"`
		Sweep           bool `json:"sweep"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Convos == nil || s.Cfg == nil {
		fail(w, http.StatusNotImplemented, "this server does not keep conversations")
		return
	}
	next := s.Cfg.Conversations
	if in.KeepDays != nil {
		next.KeepDays = *in.KeepDays
	}
	if in.HistoryKeepDays != nil {
		next.HistoryKeepDays = *in.HistoryKeepDays
	}
	if k := next.KeepDays; k != 0 && (k < 7 || k > 3650) {
		fail(w, http.StatusBadRequest, "keep conversations for 7 to 3650 days, or 0 to keep them forever")
		return
	}
	if k := next.HistoryKeepDays; k != 0 && (k < 30 || k > 3650) {
		fail(w, http.StatusBadRequest, "keep the searchable history for 30 to 3650 days, or 0 to keep it forever")
		return
	}
	if next != s.Cfg.Conversations {
		s.Cfg.Conversations = next
		if !s.writeConfig(w) {
			return
		}
	}
	if in.Sweep {
		s.sweepChats(time.Now(), false)
	}
	writeJSON(w, http.StatusOK, s.retentionView())
}
