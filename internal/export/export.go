// Package export writes conversations as trajectories: one JSON object per line, each holding a whole conversation as
// the chat-completions message list (user, assistant with tool_calls, tool results). That is the shape the common
// fine-tuning and evaluation tools read, so a person who wants to train or test a model on their own agent's work can
// do it elsewhere. Talaria trains nothing itself.
//
// What it writes is private: conversations hold what the person said and what their tools returned. So known secrets
// are scrubbed from every line, pictures are left out (a note says one was there), the system prompt (which carries the
// person's notes) is never included, and the model's own hidden reasoning is dropped.
package export

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"talaria/internal/provider"
)

// Source is one conversation to write.
type Source struct {
	ID         string
	Title      string
	Agent      string
	Archived   bool
	Updated    time.Time
	Transcript []provider.Message
}

// Options says which conversations and how.
type Options struct {
	Since           time.Time // only conversations touched at or after this; zero is all
	IncludeArchived bool
	Agent           string // only conversations with this named agent's id; "" is all
	// MinToolCalls leaves out conversations with fewer tool calls: a trajectory with no tool use teaches little about acting.
	MinToolCalls int
	// Scrub removes known secrets from every text. Nil leaves text as it is (tests only).
	Scrub func(string) string
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Line is one trajectory.
type Line struct {
	ID        string        `json:"id"`
	Title     string        `json:"title,omitempty"`
	Agent     string        `json:"agent,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
	Messages  []wireMessage `json:"messages"`
	Stats     Stats         `json:"stats"`
}

// Stats summarises a trajectory, so a trainer can filter without reading it.
type Stats struct {
	Turns     int `json:"user_turns"`
	ToolCalls int `json:"tool_calls"`
	// Unanswered counts tool calls that have no result in the conversation: it was cut off there.
	Unanswered int `json:"unanswered_tool_calls,omitempty"`
}

// Convert turns one conversation into a Line; ok is false when there is nothing worth writing.
func Convert(src Source, o Options) (Line, bool) {
	scrub := o.Scrub
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	line := Line{ID: src.ID, Title: scrub(src.Title), Agent: src.Agent, UpdatedAt: src.Updated, Messages: []wireMessage{}}
	answered := map[string]bool{}
	called := map[string]bool{}
	for _, m := range src.Transcript {
		if m.Role == provider.RoleSystem {
			continue
		}
		content := m.Content
		if n := len(m.Images); n > 0 {
			note := "[a picture was here]"
			if n > 1 {
				note = "[pictures were here]"
			}
			content = strings.TrimSpace(content + "\n" + note)
		}
		wm := wireMessage{Role: string(m.Role), Content: scrub(content), ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			var c wireCall
			c.ID, c.Type = tc.ID, "function"
			c.Function.Name, c.Function.Arguments = tc.Name, scrub(tc.Arguments)
			if !json.Valid([]byte(c.Function.Arguments)) { // scrubbing can break a JSON string: keep the line parseable
				b, _ := json.Marshal(map[string]string{"redacted": "[arguments could not be kept]"})
				c.Function.Arguments = string(b)
			}
			wm.ToolCalls = append(wm.ToolCalls, c)
			called[tc.ID] = true
			line.Stats.ToolCalls++
		}
		switch m.Role {
		case provider.RoleUser:
			line.Stats.Turns++
		case provider.RoleTool:
			answered[m.ToolCallID] = true
		}
		line.Messages = append(line.Messages, wm)
	}
	for id := range called {
		if !answered[id] {
			line.Stats.Unanswered++
		}
	}
	if line.Stats.Turns == 0 || len(line.Messages) < 2 || line.Stats.ToolCalls < o.MinToolCalls {
		return Line{}, false
	}
	return line, true
}

// Write writes every conversation that passes the options, newest first as given, one per line. It returns how many it wrote.
func Write(w io.Writer, srcs []Source, o Options) (int, error) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	n := 0
	for _, s := range srcs {
		switch {
		case s.Archived && !o.IncludeArchived,
			!o.Since.IsZero() && s.Updated.Before(o.Since),
			o.Agent != "" && s.Agent != o.Agent:
			continue
		}
		line, ok := Convert(s, o)
		if !ok {
			continue
		}
		if err := enc.Encode(line); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
