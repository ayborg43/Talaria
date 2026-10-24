package idea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"talaria/internal/tool"
)

// Binding says whose ideas the tools work on.
type Binding struct {
	Store   *Store
	Gateway string
	ChatID  int64
	// Done returns titles of finished work suggestions must not repeat.
	Done func() []string
	// Sent returns the chat's recent replies, so suggestions already said
	// are left out. Nil skips that half of the exclusion.
	Sent func() []string
	// Create offers idea_add. A run that is looking at a change does not
	// start more suggestions.
	Create bool
}

// RegisterTools adds the idea tools for one chat.
func RegisterTools(r *tool.Registry, b Binding) {
	if b.Create {
		r.Register(tool.Tool{
			Name: "idea_add",
			Description: "Keep a suggestion with its source evidence: what the user might want, and where the idea came from. " +
				"Every idea needs at least one HTTP(S) source. Suggestions already sent or matching finished work are left out when listing.",
			Schema: json.RawMessage(`{"type":"object","properties":{
				"title":{"type":"string","description":"a short name"},
				"body":{"type":"string","description":"a sentence or two"},
				"sources":{"type":"array","items":{"type":"object","properties":{
					"title":{"type":"string"},"url":{"type":"string"}},"required":["title","url"]}}},
				"required":["title","sources"]}`),
			Run: b.add,
		})
	}
	r.Register(tool.Tool{
		Name:        "idea_list",
		Description: "List this chat's open suggestions with their sources. Sent replies, finished work and dismissed ideas are left out.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Run:         b.list,
	})
	r.Register(tool.Tool{
		Name:        "idea_accept",
		Description: "Accept an open suggestion: the user wants it.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run:         b.accept,
	})
	r.Register(tool.Tool{
		Name:        "idea_edit",
		Description: "Change an open suggestion's title and body; its sources stay.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"}},"required":["id","title"]}`),
		Run:         b.edit,
	})
	r.Register(tool.Tool{
		Name:        "idea_dismiss",
		Description: "Dismiss a suggestion; it is never suggested again.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run:         b.dismiss,
	})
}

func (b Binding) done() []string {
	if b.Done == nil {
		return nil
	}
	return b.Done()
}

func (b Binding) add(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Title   string   `json:"title"`
		Body    string   `json:"body"`
		Sources []Source `json:"sources"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	id, err := b.Store.Add(b.Gateway, b.ChatID, a.Title, a.Body, a.Sources)
	if err != nil {
		return "", err
	}
	return "kept " + id, nil
}

func (b Binding) list(_ context.Context, _ json.RawMessage) (string, error) {
	var sent []string
	if b.Sent != nil {
		sent = b.Sent()
	}
	ideas := b.Store.Suggest(b.Gateway, b.ChatID, sent, b.done())
	if len(ideas) == 0 {
		return "no open suggestions", nil
	}
	var out strings.Builder
	for _, it := range ideas {
		fmt.Fprintf(&out, "%s: %s", it.ID, it.Title)
		if it.Body != "" {
			fmt.Fprintf(&out, " — %s", clip(it.Body, 200))
		}
		for _, src := range it.Sources {
			fmt.Fprintf(&out, " [%s: %s]", clip(src.Title, 80), src.URL)
		}
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String()), nil
}

func idOf(raw json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("an idea id is needed")
	}
	return a.ID, nil
}

func (b Binding) accept(_ context.Context, raw json.RawMessage) (string, error) {
	id, err := idOf(raw)
	if err != nil {
		return "", err
	}
	return "accepted", b.Store.Accept(b.Gateway, b.ChatID, id)
}

func (b Binding) edit(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("an idea id is needed")
	}
	return "edited", b.Store.Edit(b.Gateway, b.ChatID, a.ID, a.Title, a.Body)
}

func (b Binding) dismiss(_ context.Context, raw json.RawMessage) (string, error) {
	id, err := idOf(raw)
	if err != nil {
		return "", err
	}
	return "dismissed", b.Store.Dismiss(b.Gateway, b.ChatID, id)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
