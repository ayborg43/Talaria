package mailbox

import (
	"strings"
	"testing"
)

func TestClassifySortsWhatNeedsTheUser(t *testing.T) {
	me := "me@example.com"
	cases := []struct {
		name  string
		m     Summary
		class string
		said  string // a word the reason must contain
	}{
		{"flagged", Summary{Subject: "Lunch", From: "a@x.com", Flagged: true}, NeedsYou, "flagged"},
		{"direct question", Summary{Subject: "Can you review the draft?", From: "Sam <sam@x.com>", To: "me@example.com"}, NeedsYou, "directly"},
		{"direct unread", Summary{Subject: "Hello", From: "Sam <sam@x.com>", To: "Me <me@example.com>", Unread: true}, NeedsYou, "not read"},
		{"direct read", Summary{Subject: "Hello", From: "Sam <sam@x.com>", To: "me@example.com"}, CanWait, "already read"},
		{"only copied", Summary{Subject: "Please approve", From: "Sam <sam@x.com>", To: "a@x.com", Cc: "me@example.com", Unread: true}, CanWait, "copied"},
		{"newsletter", Summary{Subject: "This week in Go", From: "news@go.dev", Bulk: true, Unread: true}, CanWait, "newsletter"},
		{"program", Summary{Subject: "Build passed", From: "ci@x.com", Auto: true, Unread: true}, CanWait, "program"},
		{"receipt", Summary{Subject: "Your order has shipped", From: "shop@x.com", Auto: true}, CanWait, "receipt"},
		{"urgent from a program", Summary{Subject: "Security alert: new sign-in", From: "no-reply@accounts.example", Auto: true, Unread: true}, NeedsYou, "security alert"},
		{"invitation", Summary{Subject: "Invitation: Standup @ Tue 9am", From: "cal@x.com", Auto: true}, NeedsYou, "invitation"},
		{"already replied", Summary{Subject: "Re: plan", From: "Sam <sam@x.com>", To: "me@example.com", Answered: true, Unread: true}, CanWait, "replied"},
		{"unsure and unread errs toward you", Summary{Subject: "Hmm", From: "Sam <sam@x.com>", To: "list@x.com", Unread: true}, NeedsYou, "not read"},
	}
	for _, c := range cases {
		class, why := Classify(c.m, me)
		if class != c.class || !strings.Contains(strings.ToLower(why), c.said) {
			t.Errorf("%s: got %s %q, want %s mentioning %q", c.name, class, why, c.class, c.said)
		}
	}
}

func TestNoReplyAddressesAreRecognised(t *testing.T) {
	for _, from := range []string{"no-reply@x.com", "Acme <noreply@acme.com>", "Bank <do-not-reply@bank.com>", "alerts@x.com"} {
		if !noReply(from) {
			t.Errorf("%q was not seen as a program", from)
		}
	}
	for _, from := range []string{"Sam <sam@x.com>", "replying@x.com", "newsom@x.com"} {
		if noReply(from) {
			t.Errorf("%q was taken for a program", from)
		}
	}
}
