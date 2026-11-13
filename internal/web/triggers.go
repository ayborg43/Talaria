package web

import (
	"net/http"
	"slices"

	phonelink "talaria/internal/phone"
)

type triggerRow struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Describe string `json:"describe"`
	Prompt   string `json:"prompt"`
	Once     bool   `json:"once,omitempty"`
	Fired    string `json:"fired,omitempty"`
}

type triggersView struct {
	Triggers []triggerRow `json:"triggers"`
	Allowed  bool         `json:"allowed"` // the linked phone has allowed events
	Linked   bool         `json:"linked"`
}

func (s *Server) triggersView() triggersView {
	v := triggersView{Triggers: []triggerRow{}}
	if s.Triggers == nil {
		return v
	}
	for _, t := range s.Triggers.List() {
		row := triggerRow{ID: t.ID, Kind: t.Kind, Describe: "When " + t.Describe(), Prompt: t.Prompt, Once: t.Once}
		if !t.Fired.IsZero() {
			row.Fired = t.Fired.Format("2006-01-02 15:04")
		}
		v.Triggers = append(v.Triggers, row)
	}
	if s.Phone != nil {
		st := s.Phone.Status()
		v.Linked, v.Allowed = st.Connected, slices.Contains(st.Caps, phonelink.OpTriggers)
	}
	return v
}

func (s *Server) triggersGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.triggersView())
}

// triggersPost removes a trigger. They are only ever set up by the agent, with the person's approval, so this is the
// person's way to stop one from the phone or the page.
func (s *Server) triggersPost(w http.ResponseWriter, r *http.Request) {
	var in struct{ Delete string }
	if !readJSON(w, r, &in) {
		return
	}
	if s.Triggers == nil {
		fail(w, http.StatusNotFound, "this server has no phone events")
		return
	}
	if !s.Triggers.Remove(in.Delete) {
		fail(w, http.StatusNotFound, "that one is already gone")
		return
	}
	if s.Phone != nil {
		s.Phone.PushTriggers()
	}
	writeJSON(w, http.StatusOK, s.triggersView())
}
