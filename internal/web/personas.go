package web

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"talaria/internal/kanban"
	"talaria/internal/persona"
)

// PersonaInfo is a named agent as the page shows it.
type PersonaInfo struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Icon         string        `json:"icon,omitempty"`
	Color        string        `json:"color"`
	Description  string        `json:"description,omitempty"`
	Instructions string        `json:"instructions"`
	Profile      string        `json:"profile,omitempty"`
	ReadOnly     bool          `json:"read_only,omitempty"`
	Scopes       []string      `json:"scopes,omitempty"`
	CanRevert    bool          `json:"can_revert,omitempty"`
	Active       bool          `json:"active,omitempty"` // the open conversation is with it
	Track        *PersonaTrack `json:"track,omitempty"`
}

// PersonaTrack is how a named agent's work has gone lately: its board cards, and what its model cost.
type PersonaTrack struct {
	Done     int       `json:"done"`
	Failed   int       `json:"failed"`
	Running  int       `json:"running"`
	Waiting  int       `json:"waiting"` // in Review, waiting for the person to look
	LastAt   time.Time `json:"last_at,omitempty"`
	LastWhat string    `json:"last_what,omitempty"`
	Tokens   int64     `json:"tokens,omitempty"` // model tokens in the last 30 days, from the usage ledger
	Eval     string    `json:"eval,omitempty"`   // how its model's latest run on the person's test questions went
}

func (s *Server) personaInfos() []PersonaInfo {
	active := ""
	if s.Convos != nil {
		active = s.Convos.ActiveAgent()
	}
	tracks := s.personaTracks()
	out := []PersonaInfo{}
	for _, a := range s.Agents.List() {
		out = append(out, PersonaInfo{ID: a.ID, Name: a.Name, Icon: a.Icon, Color: a.Color, Description: a.Description, Instructions: a.Instructions, Profile: a.Profile, ReadOnly: a.ReadOnly, Scopes: a.Scopes, CanRevert: a.PrevInstructions != "", Active: a.ID == active, Track: tracks[a.Name]})
	}
	return out
}

// personaTracks builds each saved agent's record from the board, the usage ledger and the test questions.
func (s *Server) personaTracks() map[string]*PersonaTrack {
	out := map[string]*PersonaTrack{}
	get := func(name string) *PersonaTrack {
		if out[name] == nil {
			out[name] = &PersonaTrack{}
		}
		return out[name]
	}
	if s.Cards != nil {
		if cards, err := s.Cards.All(); err == nil {
			for _, c := range cards {
				if c.Agent == "" || c.Agent == kanban.Main {
					continue
				}
				t := get(c.Agent)
				switch {
				case c.Column == kanban.Running:
					t.Running++
				case c.Status == kanban.StatusFailed || c.Status == kanban.StatusCancelled || c.Status == kanban.StatusLost:
					t.Failed++
				case c.Column == kanban.Done:
					t.Done++
				case c.Column == kanban.Review:
					t.Waiting++
				}
				if !c.Finished.IsZero() && c.Finished.After(t.LastAt) {
					t.LastAt, t.LastWhat = c.Finished, c.Title
				}
			}
		}
	}
	if s.Usage != nil {
		for _, l := range s.Usage.Report(30, nil).ByAgent {
			if l.Name != "" && l.Name != "main agent" {
				get(l.Name).Tokens = l.Totals.In + l.Totals.Out
			}
		}
	}
	if s.Evals != nil {
		_, def := s.profileNames()
		for _, a := range s.Agents.List() {
			p := a.Profile
			if p == "" {
				p = def
			}
			if run, ok := s.Evals.Latest(p); ok {
				get(a.Name).Eval = run.Line()
			}
		}
	}
	// An agent with no record at all gets none shown.
	for name, t := range out {
		if t.Done+t.Failed+t.Running+t.Waiting == 0 && t.Tokens == 0 && t.Eval == "" {
			delete(out, name)
		}
	}
	return out
}

func (s *Server) profileNames() (names []string, def string) {
	if s.Engine != nil && s.Engine.Backends != nil {
		return s.Engine.Backends.Names, s.Engine.Backends.Default
	}
	return nil, ""
}

func (s *Server) personasState() map[string]any {
	names, def := s.profileNames()
	if names == nil {
		names = []string{}
	}
	return map[string]any{"available": true, "agents": s.personaInfos(), "profiles": names, "default_profile": def, "colors": ChannelColors, "scope_groups": persona.ScopeGroups}
}

func (s *Server) personasGet(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil || s.Convos == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "agents": []PersonaInfo{}})
		return
	}
	writeJSON(w, http.StatusOK, s.personasState())
}

// personasPost makes, changes and removes named agents, and starts a conversation with one.
func (s *Server) personasPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action       string   `json:"action"` // save, remove, chat, duplicate or revert
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Icon         string   `json:"icon"`
		Color        string   `json:"color"`
		Description  string   `json:"description"`
		Instructions string   `json:"instructions"`
		Profile      string   `json:"profile"`
		ReadOnly     bool     `json:"read_only"`
		Scopes       []string `json:"scopes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if s.Agents == nil || s.Convos == nil {
		fail(w, http.StatusNotImplemented, "named agents are not set up here")
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	if len(in.ID) > 32 {
		fail(w, http.StatusBadRequest, "that is not an agent")
		return
	}
	out := map[string]any{"ok": true}
	switch in.Action {
	case "save":
		if in.Color == "" {
			in.Color = ChannelColors[0]
		}
		if !slices.Contains(ChannelColors, in.Color) {
			fail(w, http.StatusBadRequest, "the colour is one of: "+strings.Join(ChannelColors, ", "))
			return
		}
		if names, _ := s.profileNames(); in.Profile != "" && !slices.Contains(names, in.Profile) {
			fail(w, http.StatusBadRequest, "there is no model profile called "+in.Profile)
			return
		}
		saved, err := s.Agents.Save(persona.Agent{ID: in.ID, Name: in.Name, Icon: in.Icon, Color: in.Color, Description: in.Description, Instructions: in.Instructions, Profile: in.Profile, ReadOnly: in.ReadOnly, Scopes: in.Scopes})
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.applyPersona() // if it is the one talking now, the change takes effect at once
		out["saved"] = saved.Name
	case "duplicate":
		saved, err := s.Agents.Duplicate(in.ID)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		out["saved"] = saved.Name
	case "revert":
		saved, err := s.Agents.Revert(in.ID)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.applyPersona()
		out["saved"] = saved.Name
	case "remove":
		if err := s.Agents.Remove(in.ID); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Convos.ForgetAgent(in.ID) // its conversations stay, as ordinary ones
		s.applyPersona()
	case "chat":
		a, ok := s.Agents.Get(in.ID)
		if !ok {
			fail(w, http.StatusNotFound, "there is no such agent")
			return
		}
		s.SaveActive()
		purpose := strings.Join(strings.Fields(a.Instructions), " ")
		if r := []rune(purpose); len(r) > 240 {
			purpose = string(r[:239]) + "…"
		}
		id, err := s.Convos.NewAgentChat(a.ID, a.Name, a.Icon, a.Color, purpose)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Hub.Replace(nil)
		s.setHistory(nil)
		out["id"] = id
	default:
		fail(w, http.StatusBadRequest, "say what to do: save, remove, chat, duplicate or revert")
		return
	}
	for k, v := range s.personasState() {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// agentFileGet is one saved agent as a file to send to someone: its name, icon, whether it only looks, and the
// instructions. Its model profile and everything else stays here.
func (s *Server) agentFileGet(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		fail(w, http.StatusNotImplemented, "named agents are not set up here")
		return
	}
	a, ok := s.Agents.Get(strings.TrimSpace(r.URL.Query().Get("id")))
	if !ok {
		fail(w, http.StatusNotFound, "there is no such agent")
		return
	}
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, a.Name)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.agent.md"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(persona.Export(a)))
}

// agentFilePost reads an agent file someone sent. Without import it only shows what is in it, so the person reads the
// instructions before anything is made. With import it saves the agent: no model profile (it is the person's to
// choose afterwards), and looking only unless the person turned that off in the page, whatever the file asked for.
func (s *Server) agentFilePost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text     string `json:"text"`
		Import   bool   `json:"import"`
		ReadOnly *bool  `json:"read_only"`
		Color    string `json:"color"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, persona.MaxFile*2)
	if !readJSON(w, r, &in) {
		return
	}
	if s.Agents == nil || s.Convos == nil {
		fail(w, http.StatusNotImplemented, "named agents are not set up here")
		return
	}
	a, err := persona.Parse(in.Text)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	exists := false
	for _, o := range s.Agents.List() {
		if strings.EqualFold(o.Name, a.Name) {
			exists = true
		}
	}
	if !in.Import {
		writeJSON(w, http.StatusOK, map[string]any{"agent": PersonaInfo{Name: a.Name, Icon: a.Icon, Description: a.Description, Instructions: a.Instructions, ReadOnly: a.ReadOnly, Scopes: a.Scopes}, "exists": exists})
		return
	}
	a.ReadOnly = in.ReadOnly == nil || *in.ReadOnly // an imported agent only looks until the person says otherwise
	a.Color = in.Color
	if a.Color == "" {
		a.Color = ChannelColors[0]
	}
	if !slices.Contains(ChannelColors, a.Color) {
		fail(w, http.StatusBadRequest, "the colour is one of: "+strings.Join(ChannelColors, ", "))
		return
	}
	saved, err := s.Agents.Save(a)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"ok": true, "saved": saved.Name}
	for k, v := range s.personasState() {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}
