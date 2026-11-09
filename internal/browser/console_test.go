package browser

import (
	"context"
	"strings"
	"testing"
)

// The user's console works the same tab the agent has open, and the agent is
// told afterwards that the page may not be what it remembers.
func TestTheConsoleDrivesTheAgentsTabAndTellsTheAgent(t *testing.T) {
	r := newRig(t, Options{})
	if _, err := r.call("browser_open", `{"url":"http://shop.test/"}`); err != nil {
		t.Fatal(err)
	}
	if got := r.m.Owners(); len(got) != 1 || got[0] != r.owner {
		t.Fatalf("owners = %v", got)
	}
	p := r.m.Peek(r.owner)
	ctx := context.Background()
	img, err := p.Shot(ctx, 50)
	if err != nil || !strings.HasPrefix(string(img), "\xff\xd8") {
		t.Fatalf("shot = %q, %v", img, err)
	}
	if got := p.Where(ctx); got != "http://shop.test/" {
		t.Fatalf("where = %q", got)
	}
	if err := p.ClickAt(ctx, 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := p.ClickAt(ctx, -1, 5000); err == nil {
		t.Fatal("a click outside the window was sent")
	}
	if err := p.InsertText(ctx, "typed by hand"); err != nil {
		t.Fatal(err)
	}
	out, err := r.call("browser_snapshot", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "The user took control of this tab") {
		t.Fatalf("the agent was not told the user took control:\n%s", out)
	}
	if strings.Contains(out, "typed by hand") && !strings.Contains(out, "value") {
		t.Fatalf("what the user typed came back outside a field value:\n%s", out)
	}
}

func TestTheConsoleCannotBeSentToAPrivateAddress(t *testing.T) {
	r := newRig(t, Options{})
	if _, err := r.call("browser_open", `{"url":"http://shop.test/"}`); err != nil {
		t.Fatal(err)
	}
	p := r.m.Peek(r.owner)
	if err := r.m.Go(context.Background(), p, "http://sneaky.test/"); err == nil {
		t.Fatal("the console opened an address on a private network")
	}
	if err := r.m.Go(context.Background(), p, "file:///etc/passwd"); err == nil {
		t.Fatal("the console opened a file address")
	}
	if err := r.m.Go(context.Background(), p, "example.com"); err != nil {
		t.Fatalf("a bare host was not opened over https: %v", err)
	}
}
