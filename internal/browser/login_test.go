package browser

import (
	"context"
	"strings"
	"testing"
)

// FillLogin is how the user signs in by hand from the web app: their username
// and password go straight into the page's own fields, in order, and the
// password is never read back.
func TestFillLoginTypesUsernameThenPasswordIntoThePage(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://login.test/"}`)
	p := r.m.Peek(r.owner)
	ctx := context.Background()
	if !p.HasPassword(ctx) {
		t.Fatal("a page with a password field was not reported as one")
	}
	before := len(r.sent("Input.insertText"))
	if err := p.FillLogin(ctx, "alice@example.com", "s3cret-pass"); err != nil {
		t.Fatal(err)
	}
	ins := r.sent("Input.insertText")[before:]
	if len(ins) != 2 || ins[0].Params["text"] != "alice@example.com" || ins[1].Params["text"] != "s3cret-pass" {
		t.Fatalf("typed = %v", ins)
	}
	out := r.ok("browser_snapshot", `{}`)
	if !strings.Contains(out, "alice@example.com") {
		t.Fatalf("the username did not land in its field:\n%s", out)
	}
	if strings.Contains(out, "s3cret-pass") {
		t.Fatalf("the password came back in a snapshot:\n%s", out)
	}
	if !strings.Contains(out, "The user took control of this tab") {
		t.Fatalf("the agent was not told the user took control:\n%s", out)
	}
}

func TestFillLoginRefusesWhenThereIsNothingToFill(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://shop.test/results"}`)
	p := r.m.Peek(r.owner)
	ctx := context.Background()
	if p.HasPassword(ctx) {
		t.Fatal("a page with no password field was reported as having one")
	}
	if err := p.FillLogin(ctx, "alice", "s3cret"); err == nil {
		t.Fatal("filling a page with no password field was accepted")
	}
	if err := p.FillLogin(ctx, "", ""); err == nil {
		t.Fatal("filling nothing was accepted")
	}
	if err := p.FillLogin(ctx, strings.Repeat("x", 4001), "s3cret"); err == nil {
		t.Fatal("an enormous username was accepted")
	}
}
