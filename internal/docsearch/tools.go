package docsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"talaria/internal/tool"
)

// RegisterTools gives the agent two read-only tools over the index. What they return is text from files the person
// keeps or that someone else may have written (an upload, a downloaded page), so reading it counts as outside content.
func RegisterTools(r *tool.Registry, ix *Index) {
	r.Register(tool.Tool{
		Name: "docs_search",
		Description: "Search the user's own files and notes: documents they uploaded, ones you made, the notes you keep about them, and the folders they set up (such as a notes or documents folder). " +
			"Use it when they ask about something they wrote, saved or sent you. Give a few plain words; every word must appear. Results name the file and show where the words are; " +
			"use docs_read with the ref to read more of one.",
		Schema:    json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","description":"results, default 6, at most 20"}},"required":["query"]}`),
		Parallel:  true,
		Untrusted: true,
		Describe: func(args json.RawMessage) string {
			var a struct{ Query string }
			json.Unmarshal(args, &a)
			return "Search your files for " + a.Query
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Query string
				Limit int
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			hits, err := ix.Search(ctx, a.Query, a.Limit)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "Nothing in the user's files or notes matches that. Try fewer or different words.", nil
			}
			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "- %s (%s) ref=%s\n  …%s…\n", h.Name, h.Kind, h.Ref, h.Snippet)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
	r.Register(tool.Tool{
		Name:        "docs_read",
		Description: "Read a file or note found with docs_search, by its ref. It returns the next part of the text; pass offset to continue where a long one stopped.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"ref":{"type":"string"},"offset":{"type":"integer","description":"character to start from, default 0"}},"required":["ref"]}`),
		Parallel:    true,
		Untrusted:   true,
		Describe:    func(json.RawMessage) string { return "Read one of your files" },
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Ref    string
				Offset int
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			name, text, more, err := ix.Read(ctx, a.Ref, a.Offset)
			if err != nil {
				return "", err
			}
			out := name + ":\n" + text
			if more {
				out += fmt.Sprintf("\n[more: call again with offset %d]", a.Offset+ReadChars)
			}
			return out, nil
		},
	})
}
