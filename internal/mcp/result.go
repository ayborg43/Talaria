package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// renderResult turns a tools/call result into text for the model. A result
// flagged isError becomes an error carrying that text, so the model sees the
// tool's own explanation and can adjust.
func renderResult(raw json.RawMessage) (string, error) {
	var r struct {
		ResultType string `json:"resultType"`
		Content    []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Data        string `json:"data"`
			MimeType    string `json:"mimeType"`
			URI         string `json:"uri"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Resource    *struct {
				URI      string `json:"uri"`
				MimeType string `json:"mimeType"`
				Text     string `json:"text"`
				Blob     string `json:"blob"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("unreadable tool result: %w", err)
	}
	switch r.ResultType {
	case "", "complete": // older servers omit resultType
	case "input_required":
		return "", errors.New("the server needs interactive input (a prompt or a model call) to finish this tool, which Talaria does not support")
	default:
		return "", fmt.Errorf("the server returned an unsupported result type %q", r.ResultType)
	}

	var parts []string
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s %s, %d bytes, not shown]", c.Type, c.MimeType, decodedSize(c.Data)))
		case "resource_link":
			link := "[resource link: " + c.URI
			if c.Name != "" {
				link += " (" + c.Name + ")"
			}
			if c.Description != "" {
				link += " " + c.Description
			}
			parts = append(parts, link+"]")
		case "resource":
			switch {
			case c.Resource == nil:
				parts = append(parts, "[empty resource]")
			case c.Resource.Text != "":
				parts = append(parts, "[resource "+c.Resource.URI+"]\n"+c.Resource.Text)
			default:
				parts = append(parts, fmt.Sprintf("[binary resource %s %s, not shown]", c.Resource.URI, c.Resource.MimeType))
			}
		default:
			parts = append(parts, fmt.Sprintf("[unsupported content type %q]", c.Type))
		}
	}
	text := strings.Join(parts, "\n")

	if text == "" && len(r.StructuredContent) > 0 && string(bytes.TrimSpace(r.StructuredContent)) != "null" {
		var buf bytes.Buffer
		if json.Compact(&buf, r.StructuredContent) == nil {
			text = buf.String()
		}
	}
	if r.IsError {
		if text == "" {
			text = "the tool reported an error without details"
		}
		return "", errors.New(text)
	}
	return text, nil
}

// decodedSize is the byte length of base64 data, without decoding it.
func decodedSize(b64 string) int {
	n := len(b64) / 4 * 3
	for i := len(b64) - 1; i >= 0 && b64[i] == '='; i-- {
		n--
	}
	return n
}
