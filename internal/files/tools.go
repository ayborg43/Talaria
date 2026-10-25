package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"talaria/internal/tool"
)

// Binding says whose files the tools work on.
type Binding struct {
	Store *Store
	// Retention is the download lifetime the admin sets, such as 12h.
	Retention func() int
}

// RegisterTools adds the file tools for one chat.
func RegisterTools(r *tool.Registry, b Binding) {
	r.Register(tool.Tool{
		Name: "files_list",
		Description: "List the files kept for this chat: uploads from the web app and PDFs made here, with their download links. " +
			"Files expire: anything older than the retention is deleted from the server.",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run:    b.list,
	})
	r.Register(tool.Tool{
		Name: "files_make_pdf",
		Description: "Make a PDF document from a title and text and share its download link: the file lives on the server until the retention runs out, " +
			"at most a few days. Say the link exactly as returned so the user can tap it.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"title":{"type":"string","description":"the document title"},
			"text":{"type":"string","description":"the full text, paragraphs separated by blank lines"}},
			"required":["title","text"]}`),
		Run: b.makePDF,
	})
	r.Register(tool.Tool{
		Name:        "files_delete",
		Description: "Delete a kept file before it expires.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run:         b.delete,
	})
}

// retention is the download lifetime in hours, when the admin's setting is
// wired in. Without it the messages say files expire without naming when:
// the web app still deletes them on time.
func (b Binding) retention() (int, bool) {
	if b.Retention == nil {
		return 0, false
	}
	return ClampRetention(b.Retention()), true
}

func expiryNote(b Binding) string {
	if h, ok := b.retention(); ok {
		return fmt.Sprintf("Files live %d hours from when they arrive.", h)
	}
	return "Files expire: downloads are deleted from the server automatically."
}

func link(id string) string { return "/api/files/" + id }

func (b Binding) list(_ context.Context, _ json.RawMessage) (string, error) {
	files, err := b.Store.List()
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "no files are kept right now", nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n", expiryNote(b))
	for _, f := range files {
		fmt.Fprintf(&out, "%s: %s (%s, %d bytes) — download at %s\n",
			f.ID, f.Name, f.MIME, f.Size, link(f.ID))
	}
	return strings.TrimSpace(out.String()), nil
}

func (b Binding) makePDF(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("a PDF needs some text")
	}
	f, err := b.Store.MakePDF(a.Title, a.Text)
	if err != nil {
		return "", err
	}
	if h, ok := b.retention(); ok {
		return fmt.Sprintf("%s is ready (%d bytes) — download it at %s before it expires in %d hours.",
			f.Name, f.Size, link(f.ID), h), nil
	}
	return fmt.Sprintf("%s is ready (%d bytes) — download it at %s before it expires.",
		f.Name, f.Size, link(f.ID)), nil
}

func (b Binding) delete(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("a file id is needed")
	}
	return "deleted", b.Store.Delete(strings.TrimSpace(a.ID))
}
