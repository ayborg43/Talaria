package web

import (
	"net/http"
	"slices"
	"strings"

	"talaria/internal/gateway/chat"
	"talaria/internal/routing"
)

// feedback is the person's thumbs on a reply. It is counted against the model that wrote it, for the kind of message it
// answered, so the router can learn which model does well at what.
func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID   int64
		Vote string // up, down, or "" to take the vote back
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Vote != "" && in.Vote != routing.Up && in.Vote != routing.Down {
		fail(w, http.StatusBadRequest, "the vote is up or down")
		return
	}
	m := s.Hub.Vote(in.ID, in.Vote)
	if m == nil {
		fail(w, http.StatusNotFound, "that reply is no longer here")
		return
	}
	if in.Vote != "" && m.Profile != "" {
		s.Learn.Record(routing.Kind(m.Kind), m.Profile, in.Vote)
	}
	w.WriteHeader(http.StatusNoContent)
}

// retry runs the latest message again on a stronger model, or on the one the person names, and drops the answer it
// replaces. Only the latest answer can be tried again: the conversation after it would no longer make sense.
func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      int64
		Profile string
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Engine == nil || s.Engine.Backends == nil {
		fail(w, http.StatusBadRequest, "there is only one model, so there is nothing stronger to try")
		return
	}
	msgs, _, _, running := s.Hub.Snapshot()
	if running {
		fail(w, http.StatusConflict, "wait for it to finish, or stop it, before trying again")
		return
	}
	last := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			last = i
			break
		}
	}
	var reply *Message
	for _, m := range msgs[max(last, 0):] {
		if m.ID == in.ID && m.Role == "assistant" {
			reply = m
		}
	}
	if last < 0 || reply == nil {
		fail(w, http.StatusBadRequest, "only the latest answer can be tried again")
		return
	}
	profile := strings.TrimSpace(in.Profile)
	if profile == "" {
		profile = s.Engine.Escalate(reply.Profile)
	}
	if profile == "" || !slices.Contains(s.Engine.Backends.Names, profile) && !slices.Contains(profile2names(s.Engine), profile) {
		fail(w, http.StatusBadRequest, "there is no other model to try")
		return
	}
	text := msgs[last].Text
	if reply.Profile != "" {
		s.Learn.Record(routing.Kind(reply.Kind), reply.Profile, routing.Retry)
	}
	s.Hub.Replace(msgs[:last+1])
	s.Hub.Seen()
	if err := s.Engine.Retry(ChatID, UserID, text, profile, routing.Kind(reply.Kind)); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"profile": profile})
}

func profile2names(e *chat.Engine) []string {
	if e.Backends.List != nil {
		return e.Backends.List()
	}
	return e.Backends.Names
}

// noteReception counts how the last reply was received, when the person's next message shows it: a message that opens
// by saying it was wrong is a correction, anything else is carrying on, which is a weak good sign. A reply the person
// voted on is not counted again.
func (s *Server) noteReception(text string) {
	if s.Learn == nil {
		return
	}
	last := s.Hub.LastReply()
	if last == nil || last.Vote != "" || !s.Hub.Score(last.ID) {
		return
	}
	signal := routing.Continue
	if routing.IsCorrection(text) {
		signal = routing.Correction
	}
	s.Learn.Record(routing.Kind(last.Kind), last.Profile, signal)
}

type routingView struct {
	Smart    bool          `json:"smart"`
	Profile  string        `json:"profile"`
	Learn    bool          `json:"learn"`
	Profiles []string      `json:"profiles"`
	Rows     []routing.Row `json:"rows"`
}

func (s *Server) routingView() routingView {
	v := routingView{Profiles: []string{}, Rows: []routing.Row{}}
	if s.Cfg != nil {
		v.Smart, v.Profile, v.Learn = s.Cfg.Routing.Smart, s.Cfg.Routing.Profile, s.Cfg.Routing.Learning()
		for n := range s.Cfg.Profiles {
			v.Profiles = append(v.Profiles, n)
		}
		slices.Sort(v.Profiles)
	}
	if rows := s.Learn.Rows(); rows != nil {
		v.Rows = rows
	}
	return v
}

func (s *Server) routingGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.routingView())
}

// routingPost changes how models are chosen, or forgets what has been learned. It applies at once.
func (s *Server) routingPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Smart   *bool   `json:"smart"`
		Profile *string `json:"profile"`
		Learn   *bool   `json:"learn"`
		Reset   bool    `json:"reset"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Cfg == nil {
		fail(w, http.StatusBadRequest, "routing cannot be changed here")
		return
	}
	if in.Profile != nil {
		p := strings.TrimSpace(*in.Profile)
		if _, ok := s.Cfg.Profiles[p]; p != "" && !ok {
			fail(w, http.StatusBadRequest, "that is not one of your profiles")
			return
		}
		s.Cfg.Routing.Profile = p
	}
	if in.Smart != nil {
		s.Cfg.Routing.Smart = *in.Smart
	}
	if in.Learn != nil {
		l := *in.Learn
		s.Cfg.Routing.Learn = &l
	}
	if in.Reset {
		if err := s.Learn.Reset(); err != nil {
			fail(w, http.StatusInternalServerError, "could not forget what was learned")
			return
		}
	}
	if !s.writeConfig(w) {
		return
	}
	if s.Router != nil {
		s.Router.Set(chat.RouterSettings{Smart: s.Cfg.Routing.Smart, Profile: s.Cfg.Routing.Profile, Learn: s.Cfg.Routing.Learning()})
	}
	writeJSON(w, http.StatusOK, s.routingView())
}
