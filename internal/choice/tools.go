package choice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"talaria/internal/tool"
)

// Binding says whose questions the tools work on.
type Binding struct {
	Store   *Store
	Gateway string
	ChatID  int64
}

// RegisterTools adds the choice tools for one chat.
func RegisterTools(r *tool.Registry, b Binding) {
	r.Register(tool.Tool{
		Name: "choice_present",
		Description: "Ask the user a structured question they answer by picking an option: a clarification (which one, what did you mean) " +
			"or a comparison of up to three sourced candidates. The question is shown as numbered options with details and sources; " +
			"the user replies in words, and you record their pick with choice_select. A new question on the same thread replaces the old one.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"thread":{"type":"string","description":"what the question is about; a new question on the same thread replaces the old one"},
			"type":{"type":"string","enum":["clarification","comparison"]},
			"title":{"type":"string","description":"the question in one line"},
			"options":{"type":"array","maxItems":12,"items":{"type":"object","properties":{
				"id":{"type":"string","description":"short id; made from the label when missing"},
				"label":{"type":"string"},"details":{"type":"array","items":{"type":"string"}},
				"sources":{"type":"array","items":{"type":"object","properties":{
					"title":{"type":"string"},"url":{"type":"string"}},"required":["title","url"]}}},
				"required":["label"]}},
			"preferred":{"type":"string","description":"the id of the option you lean toward"}},
			"required":["type","title","options"]}`),
		Run: b.present,
	})
	r.Register(tool.Tool{
		Name:        "choice_select",
		Description: "Record the user's pick from a presented question: the option id, its label, or its number as shown. Only the newest open question on its thread answers.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"panel_id":{"type":"string"},"option":{"type":"string"}},"required":["panel_id","option"]}`),
		Run:         b.selectOpt,
	})
}

func slug(label string) string {
	label = strings.ToLower(squash(label))
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "option"
	}
	return s
}

func (b Binding) present(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Thread  string `json:"thread"`
		Type    string `json:"type"`
		Title   string `json:"title"`
		Options []struct {
			ID      string   `json:"id"`
			Label   string   `json:"label"`
			Details []string `json:"details"`
			Sources []Source `json:"sources"`
		} `json:"options"`
		Preferred string `json:"preferred"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	options := make([]Option, 0, len(a.Options))
	for _, o := range a.Options {
		id := strings.TrimSpace(o.ID)
		if id == "" {
			id = slug(o.Label)
		}
		options = append(options, Option{ID: id, Label: o.Label, Details: o.Details, Sources: o.Sources})
	}
	p, err := b.Store.Present(b.Gateway, b.ChatID, a.Thread, a.Type, a.Title, options, strings.TrimSpace(a.Preferred))
	if err != nil {
		return "", err
	}
	return "question " + p.ID + ":\n" + Render(p), nil
}

func (b Binding) selectOpt(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		PanelID string `json:"panel_id"`
		Option  string `json:"option"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.PanelID) == "" || strings.TrimSpace(a.Option) == "" {
		return "", errors.New("a question and an option are needed")
	}
	p, err := b.Store.Get(b.Gateway, b.ChatID, strings.TrimSpace(a.PanelID))
	if err != nil {
		return "", err
	}
	want := squash(a.Option)
	for _, o := range p.Options {
		if o.ID == want {
			return b.finish(a.PanelID, o)
		}
	}
	for _, o := range p.Options {
		if strings.EqualFold(squash(o.Label), want) {
			return b.finish(a.PanelID, o)
		}
	}
	if n, err := strconv.Atoi(want); err == nil && n >= 1 && n <= len(p.Options) {
		return b.finish(a.PanelID, p.Options[n-1])
	}
	return "", fmt.Errorf("no option %q on that question", a.Option)
}

func (b Binding) finish(panelID string, o Option) (string, error) {
	p, err := b.Store.Select(b.Gateway, b.ChatID, panelID, o.ID)
	if err != nil {
		return "", err
	}
	_ = p
	return "picked " + o.Label, nil
}
