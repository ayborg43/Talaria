package web

import (
	"net/http"
	"slices"
	"strings"

	"talaria/internal/persona"
)

// PersonaInfo is a named agent as the page shows it.
type PersonaInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Icon         string `json:"icon,omitempty"`
	Color        string `json:"color"`
	Instructions string `json:"instructions"`
	Profile      string `json:"profile,omitempty"`
	ReadOnly     bool   `json:"read_only,omitempty"`
	Active       bool   `json:"active,omitempty"` // the open conversation is with it
}

func (s *Server) personaInfos() []PersonaInfo {
	active := ""
	if s.Convos != nil {
		active = s.Convos.ActiveAgent()
	}
	out := []PersonaInfo{}
	for _, a := range s.Agents.List() {
		out = append(out, PersonaInfo{ID: a.ID, Name: a.Name, Icon: a.Icon, Color: a.Color, Instructions: a.Instructions, Profile: a.Profile, ReadOnly: a.ReadOnly, Active: a.ID == active})
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
	return map[string]any{"available": true, "agents": s.personaInfos(), "profiles": names, "default_profile": def, "colors": ChannelColors, "max": persona.MaxAgents}
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
		Action       string `json:"action"` // save, remove or chat
		ID           string `json:"id"`
		Name         string `json:"name"`
		Icon         string `json:"icon"`
		Color        string `json:"color"`
		Instructions string `json:"instructions"`
		Profile      string `json:"profile"`
		ReadOnly     bool   `json:"read_only"`
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
		saved, err := s.Agents.Save(persona.Agent{ID: in.ID, Name: in.Name, Icon: in.Icon, Color: in.Color, Instructions: in.Instructions, Profile: in.Profile, ReadOnly: in.ReadOnly})
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.applyPersona() // if it is the one talking now, the change takes effect at once
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
		fail(w, http.StatusBadRequest, "say what to do: save, remove or chat")
		return
	}
	for k, v := range s.personasState() {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}
