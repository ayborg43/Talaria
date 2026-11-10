package web

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// CheckResult is what one live check of a connection found.
type CheckResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// setupCheck asks the services Talaria is connected to whether they answer, for the setup checklist's
// "Check again": the mailbox is signed in to and the calendars are read, so "Ready" means it worked just now
// and not only that a setting exists. Nothing is changed and nothing is read beyond a folder list and the
// next week's event count. The model is not called: a key's presence is shown from Settings, and a test
// message would cost money on every press.
func (s *Server) setupCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out := map[string]CheckResult{}
	if s.Mailbox != nil {
		if folders, err := s.Mailbox.Folders(ctx); err != nil {
			out["email"] = CheckResult{Detail: oneLineErr(err)}
		} else {
			out["email"] = CheckResult{OK: true, Detail: fmt.Sprintf("Signed in to %s: %d folders.", s.Mailbox.Address, len(folders))}
		}
	}
	if s.Calendars != nil {
		now := time.Now()
		if events, _, err := s.Calendars.Events(ctx, now, now.AddDate(0, 0, 7), "", ""); err != nil {
			out["calendar"] = CheckResult{Detail: oneLineErr(err)}
		} else {
			out["calendar"] = CheckResult{OK: true, Detail: fmt.Sprintf("Read the calendars: %d events in the next 7 days.", len(events))}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func oneLineErr(err error) string {
	msg := err.Error()
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	return msg
}
