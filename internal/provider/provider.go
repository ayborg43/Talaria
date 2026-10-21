// Package provider defines the LLM abstraction Talaria's agent loop talks to.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model request to run a tool. Arguments is raw JSON.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Image is a picture sent to the model: a screenshot, say.
type Image struct {
	MIME string // "image/png", "image/jpeg", ...
	Data []byte
}

// NoImageNote stands in for a picture the model cannot be shown, so it knows
// something was there.
const NoImageNote = "[an image was here, but this model profile cannot see images]"

// Message is one turn in the conversation.
type Message struct {
	Role       Role
	Content    string
	Images     []Image    // on user and tool messages; sent only if the profile can see
	ToolCalls  []ToolCall // set on assistant messages
	ToolCallID string     // set on tool messages
	// Reasoning is the model's own thinking, for the APIs that return it apart
	// from the answer. It is never shown to the user or kept in history; it is
	// only given back to the API that produced it, which some require while a
	// tool-using conversation goes on.
	Reasoning string
}

// ToolSpec describes a tool to the model. Parameters is a JSON Schema object.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolSpec
	// OnText, if set, asks the provider to stream: it is called with each piece
	// of reply text as it arrives. Chat still returns the complete message.
	OnText func(delta string)
}

// Provider turns a conversation into the model's next assistant message.
type Provider interface {
	Chat(ctx context.Context, req Request) (Message, error)
}

// cutOffNote is appended to a reply the model stopped because of its token limit.
const cutOffNote = "\n[response cut off: token limit reached]"

// finalize tidies a finished reply the same way for every provider: it fills
// in missing tool-call ids and empty arguments (some OpenAI-compatible servers
// omit them) and flags replies that hit the token limit. onText, if non-nil,
// is told about text added here so a streamed view stays complete.
func finalize(m Message, truncated bool, onText func(string)) (Message, error) {
	for i := range m.ToolCalls {
		tc := &m.ToolCalls[i]
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d", i)
		}
		if strings.TrimSpace(tc.Arguments) == "" {
			tc.Arguments = "{}"
		}
		if truncated && !json.Valid([]byte(tc.Arguments)) {
			return Message{}, errors.New("the model's reply was cut off inside a tool call: raise max_tokens or ask for less")
		}
	}
	if truncated {
		m.Content += cutOffNote
		if onText != nil {
			onText(cutOffNote)
		}
	}
	return m, nil
}
