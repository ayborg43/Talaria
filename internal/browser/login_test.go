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

// A sign-in is not always one form: the boxes offered are the ones the page asks for.
func TestTheBoxesOfferedAreWhatThePageIsAskingFor(t *testing.T) {
	r := newRig(t, Options{})
	p := r.m.Peek(r.owner)
	ctx := context.Background()
	kinds := func(url string) string {
		t.Helper()
		r.ok("browser_open", `{"url":"`+url+`"}`)
		p = r.m.Peek(r.owner)
		var out []string
		for _, f := range p.SignInFields(ctx) {
			out = append(out, f.Kind+":"+f.Label)
		}
		return strings.Join(out, ",")
	}
	for url, want := range map[string]string{
		"http://login.test/":       "username:Username,password:Password",
		"http://login.test/email":  "username:Email address",
		"http://login.test/pass":   "password:Password",
		"http://shop.test/results": "",
	} {
		if got := kinds(url); got != want {
			t.Errorf("%s: %q, want %q", url, got, want)
		}
	}
}

func TestOnlyTheBoxesTheUserFilledAreTypedEachIntoItsOwnField(t *testing.T) {
	r := newRig(t, Options{})
	r.ok("browser_open", `{"url":"http://login.test/email"}`)
	p := r.m.Peek(r.owner)
	ctx := context.Background()
	if err := p.FillSignIn(ctx, []SignInValue{{Index: 0, Kind: "username", Value: "alice@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if out := r.ok("browser_snapshot", `{}`); !strings.Contains(out, "alice@example.com") || !strings.Contains(out, "took control") {
		t.Fatalf("%s", out)
	}
	// The second page asks only for a password; the first page's username cannot be typed into it.
	r.ok("browser_open", `{"url":"http://login.test/pass"}`)
	p = r.m.Peek(r.owner)
	before := len(r.sent("Input.insertText"))
	if err := p.FillSignIn(ctx, []SignInValue{{Index: 0, Kind: "username", Value: "alice@example.com"}}); err == nil {
		t.Fatal("a username was typed into a page that asked only for a password")
	}
	if err := p.FillSignIn(ctx, []SignInValue{{Index: 3, Kind: "password", Value: "x"}}); err == nil {
		t.Fatal("a field that is not there")
	}
	if len(r.sent("Input.insertText")) != before {
		t.Fatal("something was typed although every request was refused")
	}
	if err := p.FillSignIn(ctx, []SignInValue{{Index: 0, Kind: "password", Value: "s3cret-pass"}, {Index: 1, Kind: "username", Value: ""}}); err != nil {
		t.Fatal(err)
	}
	if out := r.ok("browser_snapshot", `{}`); strings.Contains(out, "s3cret-pass") {
		t.Fatalf("the password came back in a snapshot:\n%s", out)
	}
	if err := p.FillSignIn(ctx, nil); err == nil {
		t.Fatal("nothing to type was accepted")
	}
	if err := p.FillSignIn(ctx, []SignInValue{{Index: 0, Kind: "password", Value: strings.Repeat("x", 4001)}}); err == nil {
		t.Fatal("an enormous value was accepted")
	}
	// A password never goes into a field that is not one, whatever it was listed as.
	r.ok("browser_open", `{"url":"http://login.test/shifty"}`)
	p = r.m.Peek(r.owner)
	if err := p.FillSignIn(ctx, []SignInValue{{Index: 0, Kind: "password", Value: "s3cret-pass"}}); err == nil {
		t.Fatal("a password was typed into a field that is not a password field")
	}
}
