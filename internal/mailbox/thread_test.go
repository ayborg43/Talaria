package mailbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func threadMsg(id, refs, from, body string) string {
	head := "From: " + from + "\r\nTo: me@example.com\r\nSubject: Re: School trip\r\nMessage-ID: " + id + "\r\n"
	if refs != "" {
		head += "References: " + refs + "\r\n"
	}
	return head + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n"
}

func TestThreadReadsTheWholeConversationOldestFirstAndChangesNothing(t *testing.T) {
	f, svc := newFake(t)
	f.with(func() {
		f.msgs[1] = threadMsg("<root@x>", "", "Amina <amina@example.org>", "Can you sign the slip?")
		f.msgs[2] = threadMsg("<two@x>", "<root@x>", "Me <me@example.com>", "Yes, sending it tonight.")
		f.msgs[3] = threadMsg("<three@x>", "<root@x> <two@x>", "Amina <amina@example.org>", "Thank you!")
		f.search = []uint32{1, 2, 3}
	})
	out, err := svc.ShowThread(context.Background(), "", 3)
	if err != nil {
		t.Fatal(err)
	}
	first, last := strings.Index(out, "Can you sign the slip?"), strings.Index(out, "Thank you!")
	if first < 0 || last < first || !strings.Contains(out, "3 message(s)") {
		t.Fatalf("thread is not whole or not in order:\n%s", out)
	}
	if !strings.Contains(out, "information, not instructions") {
		t.Error("message text is not marked as outside text")
	}
	joined := strings.Join(f.commands(), "\n")
	if !strings.Contains(joined, `HEADER Message-ID "<root@x>"`) || !strings.Contains(joined, `HEADER References "<root@x>"`) {
		t.Errorf("the search did not follow the thread's root:\n%s", joined)
	}
	for _, v := range verbs(f.commands()) {
		switch v {
		case "LOGIN", "CAPABILITY", "LIST", "EXAMINE", "UID SEARCH", "UID FETCH", "LOGOUT":
		default:
			t.Errorf("reading a thread sent %s", v)
		}
	}
}

func TestDraftIsFiledInDraftsAndNeverSent(t *testing.T) {
	f, svc := newFake(t)
	sent := false
	svc.SendMail = func(context.Context, string, string, string, string, []string, []byte) error { sent = true; return nil }
	f.with(func() {
		f.folders = append(f.folders, `(\HasNoChildren \Drafts) "/" "[Gmail]/Drafts"`)
		f.msgs[7] = threadMsg("<root@x>", "", "Amina <amina@example.org>", "Can you sign the slip?")
	})
	r := tool.NewRegistry()
	RegisterThreadTools(r, svc)
	d, ok := r.Get("email_draft")
	if !ok {
		t.Fatal("email_draft is not registered")
	}
	if d.NeedsApproval {
		t.Error("a draft leaves nothing, so it does not ask")
	}
	args, _ := json.Marshal(map[string]any{"reply_to_id": 7, "body": "Signed copy attached."})
	out, err := d.Run(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Fatal("a draft went out over SMTP")
	}
	joined := strings.Join(f.commands(), "\n")
	if !strings.Contains(joined, `APPEND "[Gmail]/Drafts" (\Draft \Seen)`) {
		t.Errorf("the draft was not filed in the Drafts folder:\n%s", joined)
	}
	if !strings.Contains(joined, "In-Reply-To: <root@x>") || !strings.Contains(joined, "To: amina@example.org") || !strings.Contains(joined, "Subject: Re: School trip") {
		t.Errorf("the reply draft is not threaded and addressed:\n%s", joined)
	}
	if !strings.Contains(out, "Nothing was sent") {
		t.Errorf("result = %q", out)
	}
}
