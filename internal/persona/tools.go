package persona

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"talaria/internal/tool"
)

// defaultColor is what an agent the assistant proposes is drawn in until its owner picks another.
const defaultColor = "brass"

// RegisterTools gives the main agent one tool, agent_create, for suggesting a standing agent when it sees the user
// keep needing the same kind of help. Nothing is created until the user approves the exact agent shown in the prompt,
// and the approval is never remembered, so every new agent is its own yes. The agent cannot change or remove agents:
// those stay with the user. profiles lists the model profiles that exist, for the choice of model.
func RegisterTools(r *tool.Registry, s *Store, profiles func() []string) {
	var names []string
	if profiles != nil {
		names = profiles()
	}
	props := `"name":{"type":"string","description":"a short name for the agent, like Researcher"},` +
		`"description":{"type":"string","description":"one line saying when to choose this agent, like 'looks things up and cites sources'"},` +
		`"instructions":{"type":"string","description":"what it is for and how it should behave: written to it, in the second person, standing alone"},` +
		`"scopes":{"type":"array","items":{"type":"string","enum":["web","files","shell","browser","mail","calendar","phone","docs","social","apps","finance"]},"description":"tool areas it may use; leave out for all of them"},` +
		`"read_only":{"type":"boolean","description":"true if its work is only to look things up and read (the safe choice, and the default); false if it needs to act, such as write files or run commands, still with the user's usual approvals"},` +
		`"icon":{"type":"string","description":"an emoji or a couple of letters (optional)"}`
	if len(names) > 0 {
		enum, _ := json.Marshal(names)
		props += `,"profile":{"type":"string","enum":` + string(enum) + `,"description":"the model it should use; leave out for the model in use"}`
	}
	r.Register(tool.Tool{
		Name: "agent_create",
		Description: "Suggest a standing agent: a named helper with its own instructions, and perhaps its own model, that you can hand tasks to later with delegate (agent=name), and that the user can chat with. " +
			"Use it only when the user keeps needing the same kind of help that a ready-made job description would serve (weekly research, reviewing code, drafting in a house style), not for a single task: for that use delegate. " +
			"The user is shown exactly what you propose and must approve it; if they refuse, do not ask again. " +
			"You cannot change or remove agents. Choose read_only=true when the job only needs looking things up and reading, and false when it has to act; an agent that acts still asks the user for the same approvals as you do.",
		Schema:        json.RawMessage(`{"type":"object","properties":{` + props + `},"required":["name","instructions"]}`),
		NeedsApproval: true,
		Risk:          tool.RiskSaves, // asks even where only deleting and posting do
		Egress:        tool.Always,    // it leaves something behind that is used later, so it asks even after reading outside content
		Describe: func(raw json.RawMessage) string {
			a, err := parseCreate(raw, names)
			if err != nil {
				return "Create a saved agent"
			}
			return "Create the agent " + a.Name + " (" + access(a) + model(a) + "): " + clip(a.Instructions, 400)
		},
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			a, err := parseCreate(raw, names)
			if err != nil {
				return "", err
			}
			saved, err := s.Save(a)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("created the agent %s (%s%s). It is in the user's Agents list, and you can hand it tasks with delegate agent=%q.", saved.Name, access(saved), model(saved), saved.Name), nil
		},
	})
}

func access(a Agent) string {
	if a.ReadOnly {
		return "looks only"
	}
	return "can act, with the usual approvals"
}

func model(a Agent) string {
	if a.Profile == "" {
		return ""
	}
	return ", model " + a.Profile
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// parseCreate reads a proposal. An agent that does not say whether it is look-only is look-only: the wider choice has to be made out loud.
func parseCreate(raw json.RawMessage, profiles []string) (Agent, error) {
	var in struct {
		Name, Description, Instructions, Icon, Profile string
		ReadOnly                                       *bool `json:"read_only"`
		Scopes                                         []string
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Agent{}, err
	}
	profile := strings.TrimSpace(in.Profile)
	if profile != "" && profiles != nil && !slices.Contains(profiles, profile) {
		return Agent{}, fmt.Errorf("there is no model profile called %q: choose one of %s, or leave it out", profile, strings.Join(profiles, ", "))
	}
	a := Agent{Name: in.Name, Description: in.Description, Icon: in.Icon, Color: defaultColor, Instructions: in.Instructions, Profile: profile, ReadOnly: in.ReadOnly == nil || *in.ReadOnly, Scopes: in.Scopes}
	return Check(a)
}
