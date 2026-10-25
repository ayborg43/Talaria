package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

// FileSender delivers a kept file to the user's chat as a native document.
type FileSender func(ctx context.Context, chatID int64, name string, data []byte) error

// Binding says whose files the tools work on.
type Binding struct {
	Store *Store
	// Retention is the download lifetime the admin sets, such as 12h.
	Retention func() int
	// Send delivers a kept file to this chat. Nil means the service has no
	// document delivery, and files_send says so.
	Send FileSender
	// ChatID is the chat the file goes to.
	ChatID int64
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
		Name: "files_view",
		Description: "Show a kept image to the model: the picture is attached to the result, so a profile that can see images describes what is in it. " +
			"Only images can be shown; for a PDF's words use files_read.",
		Schema:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		RunRich: b.view,
	})
	r.Register(tool.Tool{
		Name: "files_read",
		Description: "Read the words of a kept PDF: the text layer, capped, for quoting or summarizing. " +
			"Scanned PDFs have no readable text and say so. Images cannot be read this way: show them with files_view instead.",
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run:    b.read,
	})
	r.Register(tool.Tool{
		Name: "files_send",
		Description: "Send a kept file to this chat as a native document (Telegram, Discord): the user gets the file itself, not a link. " +
			"Only where the service delivers documents; otherwise share the download link from files_list.",
		Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run:    b.send,
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
	now := time.Now()
	var ret time.Duration
	if h, ok := b.retention(); ok {
		ret = time.Duration(h) * time.Hour
	} else {
		ret = time.Duration(DefaultRetentionHours) * time.Hour
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n", expiryNote(b))
	for _, f := range files {
		fmt.Fprintf(&out, "%s: %s (%s, %d bytes, expires in %s) — download at %s\n",
			f.ID, f.Name, f.MIME, f.Size, ExpiresIn(f.Created, now, ret), link(f.ID))
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

func (b Binding) view(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(a.ID) == "" {
		return tool.Result{}, errors.New("a file id is needed")
	}
	f, data, err := b.Store.Open(strings.TrimSpace(a.ID))
	if err != nil {
		return tool.Result{}, err
	}
	if !strings.HasPrefix(f.MIME, "image/") {
		return tool.Result{Text: f.Name + " is a " + f.MIME + " document, not a picture: use files_read for its words."}, nil
	}
	return tool.Result{
		Text:   f.Name + " (" + f.MIME + "): what do you see?",
		Images: []provider.Image{{MIME: f.MIME, Data: data}},
	}, nil
}

func (b Binding) read(_ context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("a file id is needed")
	}
	f, data, err := b.Store.Open(strings.TrimSpace(a.ID))
	if err != nil {
		return "", err
	}
	if f.MIME != "application/pdf" {
		return "", errors.New(f.Name + " is a picture: show it with files_view instead")
	}
	text, err := ExtractText(data)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return f.Name + " has no readable text: it is probably a scan.", nil
	}
	return f.Name + ":\n" + text, nil
}

func (b Binding) send(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.ID) == "" {
		return "", errors.New("a file id is needed")
	}
	if b.Send == nil {
		return "", errors.New("files cannot be sent as documents on this service: share the download link instead")
	}
	f, data, err := b.Store.Open(strings.TrimSpace(a.ID))
	if err != nil {
		return "", err
	}
	if err := b.Send(ctx, b.ChatID, f.Name, data); err != nil {
		return "", fmt.Errorf("the file could not be sent: %w", err)
	}
	return "sent " + f.Name + " as a document.", nil
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
