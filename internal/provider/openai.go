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

// OpenAI talks to any OpenAI-compatible /chat/completions endpoint
// (OpenAI, OpenRouter, Ollama, vLLM, LM Studio, ...).
type OpenAI struct {
	BaseURL  string // e.g. https://api.openai.com/v1
	APIKey   string
	HTTP     *http.Client
	NoVision bool // leave images out: the model cannot see them
	// PassReasoning gives the model's earlier thinking back with each assistant
	// message (as reasoning_content), which DeepSeek requires of a conversation that uses tools.
	// Other servers may refuse a field they do not know, so it is off by default.
	PassReasoning bool
	// Thinking ("enabled" or "disabled") and ReasoningEffort are DeepSeek's request settings;
	// empty leaves them to the service.
	Thinking        string
	ReasoningEffort string
}

func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: newHTTPClient()}
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []wireCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

// wireOutMessage is a message as sent. Its content is a string, or a list of
// parts when it carries pictures.
type wireOutMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []wireCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type wirePart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

type wireImageURL struct {
	URL string `json:"url"`
}

type wireThinking struct {
	Type string `json:"type"`
}

type wireRequest struct {
	Model           string           `json:"model"`
	Messages        []wireOutMessage `json:"messages"`
	Tools           []wireTool       `json:"tools,omitempty"`
	Stream          bool             `json:"stream,omitempty"`
	Thinking        *wireThinking    `json:"thinking,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAI) Chat(ctx context.Context, req Request) (Message, error) {
	body := wireRequest{Model: req.Model, Stream: req.OnText != nil}
	body.Messages = o.toWire(req.Messages)
	if o.Thinking != "" {
		body.Thinking = &wireThinking{Type: o.Thinking}
	}
	if o.Thinking != "disabled" { // an effort means nothing without thinking
		body.ReasoningEffort = o.ReasoningEffort
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, wireTool{
			Type:     "function",
			Function: wireFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}

	headers := map[string]string{}
	if o.APIKey != "" {
		headers["Authorization"] = "Bearer " + o.APIKey
	}
	return roundTrip(ctx, o.HTTP, o.BaseURL+"/chat/completions", headers, payload, req.OnText,
		parseOpenAI, func(r io.Reader) (Message, error) { return readOpenAIStream(r, req.OnText) })
}

// toWire converts the conversation. A tool message can only hold text in this
// API, so pictures a tool returned follow the run of tool messages they belong
// to as one user message.
func (o *OpenAI) toWire(msgs []Message) []wireOutMessage {
	var out []wireOutMessage
	var pending []wirePart // pictures from tool results, to be sent after the last of them
	flush := func() {
		if len(pending) > 0 {
			out = append(out, wireOutMessage{Role: "user", Content: append([]wirePart{{Type: "text", Text: "Images returned by the tool call(s) above:"}}, pending...)})
			pending = nil
		}
	}
	for _, m := range msgs {
		if m.Role != RoleTool {
			flush()
		}
		wm := wireOutMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		if o.PassReasoning && m.Role == RoleAssistant {
			wm.ReasoningContent = m.Reasoning
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireCall{
				ID:       tc.ID,
				Type:     "function",
				Function: wireFunction{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		switch {
		case len(m.Images) == 0:
		case o.NoVision:
			wm.Content = strings.TrimSpace(m.Content + "\n" + NoImageNote)
		case m.Role == RoleTool:
			for _, img := range m.Images {
				pending = append(pending, imagePart(img))
			}
		default:
			parts := []wirePart{}
			if m.Content != "" {
				parts = append(parts, wirePart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, imagePart(img))
			}
			wm.Content = parts
		}
		out = append(out, wm)
	}
	flush()
	return out
}

func imagePart(img Image) wirePart {
	return wirePart{Type: "image_url", ImageURL: &wireImageURL{URL: "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)}}
}

func parseOpenAI(raw []byte) (Message, error) {
	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return Message{}, fmt.Errorf("decode response: %w", err)
	}
	if wr.Error != nil {
		return Message{}, fmt.Errorf("provider error: %s", wr.Error.Message)
	}
	if len(wr.Choices) == 0 {
		return Message{}, errors.New("provider returned no choices")
	}

	c := wr.Choices[0]
	out := Message{Role: RoleAssistant, Content: c.Message.Content, Reasoning: c.Message.ReasoningContent}
	for _, tc := range c.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return finalize(out, c.FinishReason == "length", nil)
}

// readOpenAIStream assembles a reply from chat-completion chunks. Tool calls
// arrive in fragments keyed by index: id and name first, then pieces of the
// JSON arguments.
func readOpenAIStream(body io.Reader, onText func(string)) (Message, error) {
	var (
		content   strings.Builder
		reasoning strings.Builder
		calls     []*ToolCall
		finished  bool
		truncated bool
	)
	err := sse.Read(body, func(data string) error {
		if data == "[DONE]" {
			finished = true
			return sse.Stop
		}
		var ch streamChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return fmt.Errorf("decode stream: %w", err)
		}
		if ch.Error != nil {
			return fmt.Errorf("provider error: %s", ch.Error.Message)
		}
		if len(ch.Choices) == 0 { // e.g. a usage-only chunk
			return nil
		}

		c := ch.Choices[0]
		reasoning.WriteString(c.Delta.ReasoningContent) // kept for the next request, never shown
		if c.Delta.Content != "" {
			content.WriteString(c.Delta.Content)
			onText(c.Delta.Content)
		}
		for _, tc := range c.Delta.ToolCalls {
			if tc.Index < 0 || tc.Index >= maxToolCalls {
				return fmt.Errorf("the reply numbers a tool call %d: refusing it", tc.Index)
			}
			for len(calls) <= tc.Index {
				calls = append(calls, &ToolCall{})
			}
			cur := calls[tc.Index]
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" && cur.Name == "" {
				cur.Name = tc.Function.Name
			}
			cur.Arguments += tc.Function.Arguments
		}
		if c.FinishReason != "" {
			finished = true
			truncated = c.FinishReason == "length"
		}
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	if !finished {
		return Message{}, errors.New("stream ended before the reply was complete")
	}

	out := Message{Role: RoleAssistant, Content: content.String(), Reasoning: reasoning.String()}
	for _, c := range calls {
		out.ToolCalls = append(out.ToolCalls, *c)
	}
	return finalize(out, truncated, onText)
}
