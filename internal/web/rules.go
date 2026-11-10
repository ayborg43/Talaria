package web

import (
	"net/http"
	"slices"
	"strings"

	"talaria/internal/guard"
)

// ruleCategory is one kind of action as the Rules page shows it: what it covers and the mode it is set to.
type ruleCategory struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Tools  []string `json:"tools"`
	Mode   string   `json:"mode"`
	Mixed  bool     `json:"mixed,omitempty"` // its tools do not all share a rule (one was written by hand)
	Risky  bool     `json:"risky,omitempty"`
}

type rulesView struct {
	Modes      []string          `json:"modes"`
	Categories []ruleCategory    `json:"categories"`
	Other      map[string]string `json:"other"`          // rules written by hand that no category owns
	Live       bool              `json:"live,omitempty"` // a change applies at once, with no restart
	Ask        string            `json:"ask"`            // which calls ask by default: deletes_and_posts or strict
}

func (s *Server) rulesView() rulesView {
	var rules map[string]string
	if s.Cfg != nil {
		rules = s.Cfg.Policy.Rules
	}
	v := rulesView{Modes: guard.Modes, Other: guard.Other(rules), Live: s.ApplyRules != nil, Ask: permissionsName(s.Permissions)}
	for _, c := range guard.Categories {
		m, mixed := guard.ModeOf(rules, c)
		v.Categories = append(v.Categories, ruleCategory{ID: c.ID, Title: c.Title, Detail: c.Detail, Tools: c.Tools, Mode: m, Mixed: mixed, Risky: c.Risky})
	}
	if v.Other == nil {
		v.Other = map[string]string{}
	}
	return v
}

func (s *Server) rulesGet(w http.ResponseWriter, r *http.Request) {
	if s.Cfg == nil {
		fail(w, http.StatusNotImplemented, "settings are not available here")
		return
	}
	writeJSON(w, http.StatusOK, s.rulesView())
}

// rulesPost sets one kind of action to a mode, saves it in the config file, and applies it to the agents at once.
// Only the rules for that category's tools change; rules written by hand for anything else are left alone.
func (s *Server) rulesPost(w http.ResponseWriter, r *http.Request) {
	if s.Cfg == nil {
		fail(w, http.StatusNotImplemented, "settings are not available here")
		return
	}
	var in struct {
		Category string `json:"category"`
		Mode     string `json:"mode"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	c, ok := guard.CategoryByID(strings.TrimSpace(in.Category))
	if !ok {
		fail(w, http.StatusBadRequest, "that is not a kind of action")
		return
	}
	mode := strings.TrimSpace(in.Mode)
	if !slices.Contains(guard.Modes, mode) {
		fail(w, http.StatusBadRequest, "the mode is one of: "+strings.Join(guard.Modes, ", "))
		return
	}
	next, err := guard.SetMode(s.Cfg.Policy.Rules, c, mode)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.ApplyRules != nil { // check the whole set before anything is saved
		if err := s.ApplyRules(next); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(next) == 0 {
		next = nil
	}
	was := s.Cfg.Policy.Rules
	s.Cfg.Policy.Rules = next
	if err := s.saveConfigQuiet(); err != nil {
		s.Cfg.Policy.Rules = was
		if s.ApplyRules != nil {
			_ = s.ApplyRules(was) // put the running rules back as they were
		}
		fail(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	out := map[string]any{"ok": true, "rules": s.rulesView()}
	if s.ApplyRules == nil {
		out["restart_needed"] = true
	}
	writeJSON(w, http.StatusOK, out)
}
