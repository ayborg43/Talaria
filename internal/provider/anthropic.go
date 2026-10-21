package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"talaria/internal/sse"
)

const (
	anthropicVersion = "2023-06-01"
	defaultMaxTokens = 8192
)

// Anthropic talks to the native Messages API.
type Anthropic struct {
	BaseURL   string // https://api.anthropic.com (a trailing /v1 is fine)
	APIKey    string
	MaxTokens int // per-reply limit, required by the API; default 8192
	HTTP      *http.Client
	NoVision  bool // leave images out: the model cannot see them
}

func NewAnthropic(baseURL, apiKey string, maxTokens int) *Anthropic {
	return &Anthropic{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, MaxTokens: maxTokens, HTTP: newHTTPClient()}
}

type aCache struct {
	Type string `json:"type"`
}

// aBlock is a content block in both requests and responses.
type aBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`          // tool_use
	Name         string          `json:"name,omitempty"`        // tool_use
	Input        json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID    string          `json:"tool_use_id,omitempty"` // tool_result
	Content      any             `json:"content,omitempty"`     // tool_result: text, or blocks
	Source       *aSource        `json:"source,omitempty"`      // image
	CacheControl *aCache         `json:"cache_control,omitempty"`
}

type aSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type aMsg struct {
	Role    string   `json:"role"`
	Content []aBlock `json:"content"`
}

type aTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type aRequest struct {
	Model     string   `json:"model"`
	MaxTokens int      `json:"max_tokens"`
	System    []aBlock `json:"system,omitempty"`
	Messages  []aMsg   `json:"messages"`
	Tools     []aTool  `json:"tools,omitempty"`
	Stream    bool     `json:"stream,omitempty"`
}

type aResponse struct {
	Type       string   `json:"type"`
	Content    []aBlock `json:"content"`
	StopReason string   `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type aEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (a *Anthropic) url() string {
	if strings.HasSuffix(a.BaseURL, "/v1") {
		return a.BaseURL + "/messages"
	}
	return a.BaseURL + "/v1/messages"
}

func (a *Anthropic) Chat(ctx context.Context, req Request) (Message, error) {
	system, msgs := toAnthropic(req.Messages, a.NoVision)
	body := aRequest{Model: req.Model, MaxTokens: a.MaxTokens, System: system, Messages: msgs, Stream: req.OnText != nil}
	if body.MaxTokens <= 0 {
		body.MaxTokens = defaultMaxTokens
	}
	for _, t := range req.Tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		body.Tools = append(body.Tools, aTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}

	headers := map[string]string{"x-api-key": a.APIKey, "anthropic-version": anthropicVersion}
	return roundTrip(ctx, a.HTTP, a.url(), headers, payload, req.OnText,
		parseAnthropic, func(r io.Reader) (Message, error) { return readAnthropicStream(r, req.OnText) })
}

// toAnthropic converts the conversation. System text moves to the top-level
// system field, marked cacheable so the stable prefix (tools + system) is
// reused across turns. Tool results become tool_result blocks in a user
// message, and consecutive messages with the same role are merged, so all the
// results for one assistant turn arrive together.
func toAnthropic(msgs []Message, noVision bool) (system []aBlock, out []aMsg) {
	var sys []string
	add := func(role string, blocks ...aBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, aMsg{Role: role, Content: blocks})
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			sys = append(sys, m.Content)
		case RoleUser:
			var blocks []aBlock
			if m.Content != "" {
				blocks = append(blocks, aBlock{Type: "text", Text: m.Content})
			}
			blocks = append(blocks, imageBlocks(m.Images, noVision)...)
			add("user", blocks...)
		case RoleAssistant:
			var blocks []aBlock
			if m.Content != "" {
				blocks = append(blocks, aBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Arguments)
				if !json.Valid(input) {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, aBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			add("assistant", blocks...)
		case RoleTool:
			content := m.Content
			if content == "" {
				content = "(no output)"
			}
			result := aBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: content}
			if imgs := imageBlocks(m.Images, noVision); len(imgs) > 0 {
				result.Content = append([]aBlock{{Type: "text", Text: content}}, imgs...)
			}
			add("user", result)
		}
	}
	if len(sys) > 0 {
		system = []aBlock{{Type: "text", Text: strings.Join(sys, "\n\n"), CacheControl: &aCache{Type: "ephemeral"}}}
	}
	return system, out
}

// imageBlocks makes the content blocks for pictures, or, for a model that
// cannot see, a text block saying a picture was there.
func imageBlocks(imgs []Image, noVision bool) []aBlock {
	var out []aBlock
	for _, img := range imgs {
		if noVision {
			out = append(out, aBlock{Type: "text", Text: NoImageNote})
			continue
		}
		out = append(out, aBlock{Type: "image", Source: &aSource{Type: "base64", MediaType: img.MIME, Data: base64.StdEncoding.EncodeToString(img.Data)}})
	}
	return out
}

func parseAnthropic(raw []byte) (Message, error) {
	var r aResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return Message{}, fmt.Errorf("decode response: %w", err)
	}
	if r.Type == "error" || r.Error != nil {
		msg := "unknown error"
		if r.Error != nil {
			msg = r.Error.Message
		}
		return Message{}, fmt.Errorf("provider error: %s", msg)
	}

	out := Message{Role: RoleAssistant}
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			out.Content += b.Text
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
		}
	}
	return finalize(out, r.StopReason == "max_tokens", nil)
}

// readAnthropicStream assembles a reply from Messages API events. Text arrives
// as text_delta; a tool call's arguments arrive as input_json_delta fragments
// of one JSON document, keyed by content-block index.
func readAnthropicStream(body io.Reader, onText func(string)) (Message, error) {
	type call struct {
		id, name string
		args     strings.Builder
	}
	var (
		content  strings.Builder
		calls    = map[int]*call{}
		order    []int
		stop     string
		finished bool
	)
	err := sse.Read(body, func(data string) error {
		var ev aEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fmt.Errorf("decode stream: %w", err)
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				if len(order) >= maxToolCalls {
					return fmt.Errorf("the reply announces more than %d tool calls: refusing it", maxToolCalls)
				}
				calls[ev.Index] = &call{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				order = append(order, ev.Index)
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				content.WriteString(ev.Delta.Text)
				onText(ev.Delta.Text)
			case "input_json_delta":
				if c := calls[ev.Index]; c != nil {
					c.args.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stop = ev.Delta.StopReason
			}
		case "message_stop":
			finished = true
			return sse.Stop
		case "error":
			msg := "unknown error"
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			return fmt.Errorf("provider error: %s", msg)
		}
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	if !finished {
		return Message{}, errors.New("stream ended before the reply was complete")
	}

	out := Message{Role: RoleAssistant, Content: content.String()}
	for _, i := range order {
		c := calls[i]
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: c.id, Name: c.name, Arguments: c.args.String()})
	}
	return finalize(out, stop == "max_tokens", onText)
}
