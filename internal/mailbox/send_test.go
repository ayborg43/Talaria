package mailbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"talaria/internal/tool"
)

func TestBuildMessageThreadsAndAttaches(t *testing.T) {
	msg, err := BuildMessage(Outgoing{
		From: "Me <me@example.com>", To: []string{"a@example.com"}, Subject: "Re: Hello",
		Body: "Thanks!", InReplyTo: "<m1@example.com>", References: "<m0@example.com> <m1@example.com>",
		Attachments: []SendAttachment{{Name: "form.pdf", Type: "application/pdf", Data: []byte("%PDF-fake")}},
	}, time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	for _, want := range []string{"From: Me <me@example.com>", "To: a@example.com", "Subject: Re: Hello", "In-Reply-To: <m1@example.com>", "References: <m0@example.com> <m1@example.com>", "multipart/mixed", "form.pdf", "JVBERi1mYWtl"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if _, err := BuildMessage(Outgoing{From: "me@example.com", Subject: "x", Body: "y"}, time.Now()); err == nil {
		t.Fatal("a message with no recipient was built")
	}
	if _, err := BuildMessage(Outgoing{From: "me@example.com", To: []string{"a@example.com"}, Subject: "bad\nBcc: x@y.z", Body: "y"}, time.Now()); err == nil {
		t.Fatal("a header injection was built")
	}
}

func TestSendToolsAskFirstAndUseTheTransport(t *testing.T) {
	var sentTo []string
	var sentMsg []byte
	s := &Service{Address: "me@example.com", User: "me@example.com", Password: "pw", Server: "imap.example.com:993"}
	s.SendMail = func(_ context.Context, server, user, pass, from string, to []string, msg []byte) error {
		sentTo, sentMsg = to, msg
		if !strings.Contains(server, "smtp.") {
			t.Errorf("server not derived as smtp: %s", server)
		}
		return nil
	}
	r := tool.NewRegistry()
	RegisterSendTools(r, s, nil)
	for _, name := range []string{"email_send", "email_reply"} {
		tl, ok := r.Get(name)
		if !ok || !tl.NeedsApproval {
			t.Fatalf("%s must be registered and ask first: %+v", name, tl)
		}
	}
	tl, _ := r.Get("email_send")
	out, err := tl.Run(context.Background(), json.RawMessage(`{"to":"a@example.com","subject":"Hi","body":"Hello there"}`))
	if err != nil || !strings.Contains(out, "Sent") || len(sentTo) != 1 || !strings.Contains(string(sentMsg), "Hello there") {
		t.Fatalf("%v %s sent=%v", err, out, sentTo)
	}
}

func TestAttachmentDownloadsDecodedAndAppendFiles(t *testing.T) {
	f, s := newFake(t)
	raw := "From: A <a@example.com>\r\nTo: me@example.com\r\nSubject: form\r\nMessage-ID: <m1@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--B\r\nContent-Type: application/pdf; name=\"form.pdf\"\r\nContent-Disposition: attachment; filename=\"form.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi1mYWtl\r\n--B--\r\n"
	f.with(func() {
		f.msgs[7] = raw
		f.search = []uint32{7}
	})
	att, err := s.Attachment(context.Background(), "", 7, "form.pdf")
	if err != nil || string(att.Data) != "%PDF-fake" {
		t.Fatalf("%v %+v", err, att)
	}
	if err := s.Append(context.Background(), "Sent", []byte("the sent copy")); err != nil {
		t.Fatal(err)
	}
	joined := ""
	f.with(func() { joined = strings.Join(f.cmds, "\n") })
	if !strings.Contains(joined, "APPEND") || !strings.Contains(joined, "the sent copy") {
		t.Fatalf("append not recorded:\n%s", joined)
	}
	// Reply threading reads the original's Message-ID.
	msg, err := s.Read(context.Background(), "", 7)
	if err != nil || msg.MessageID != "<m1@example.com>" {
		t.Fatalf("%v %+v", err, msg)
	}
}
