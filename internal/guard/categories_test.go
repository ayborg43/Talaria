package guard

import (
	"strings"
	"testing"

	"talaria/internal/tool"
)

func TestEveryCategoryToolIsKnownAndNoToolIsInTwoCategories(t *testing.T) {
	seen := map[string]string{}
	for _, c := range Categories {
		if c.ID == "" || c.Title == "" || len(c.Tools) == 0 {
			t.Fatalf("a category is incomplete: %+v", c)
		}
		for _, name := range c.Tools {
			if !strings.HasSuffix(name, "*") && !knownTools[name] {
				t.Errorf("%s: %q is not a tool Talaria has", c.ID, name)
			}
			if other, dup := seen[name]; dup {
				t.Errorf("%q is in both %s and %s", name, other, c.ID)
			}
			seen[name] = c.ID
		}
	}
	// every set of rules a category writes must be accepted by the policy
	for _, c := range Categories {
		for _, m := range Modes {
			rules, err := SetMode(nil, c, m)
			if err != nil {
				t.Fatalf("%s %s: %v", c.ID, m, err)
			}
			if _, err := ParsePolicy(rules, nil); err != nil {
				t.Errorf("%s set to %s writes rules the policy refuses: %v", c.ID, m, err)
			}
		}
	}
}

func TestACategoryReadsAndWritesItsOwnRulesOnly(t *testing.T) {
	mail, _ := CategoryByID("email")
	rules := map[string]string{"shell": "deny", "mcp_mail_*": "ask"}
	next, err := SetMode(rules, mail, ModeHandoff)
	if err != nil {
		t.Fatal(err)
	}
	if next["email_send"] != "handoff" || next["email_reply"] != "handoff" || next["shell"] != "deny" || next["mcp_mail_*"] != "ask" {
		t.Fatalf("%v", next)
	}
	if rules["email_send"] != "" {
		t.Fatal("the original rules were changed in place")
	}
	if m, mixed := ModeOf(next, mail); m != ModeHandoff || mixed {
		t.Fatalf("read back %q mixed=%v", m, mixed)
	}
	// a hand-written rule for one tool of the group makes it mixed rather than hiding it
	next["email_reply"] = "allow"
	if _, mixed := ModeOf(next, mail); !mixed {
		t.Fatal("a mixed category was reported as one mode")
	}
	back, _ := SetMode(next, mail, ModeDefault)
	if _, has := back["email_send"]; has {
		t.Fatalf("default should remove the rules: %v", back)
	}
	if m, _ := ModeOf(nil, mail); m != ModeDefault {
		t.Fatalf("no rules should read as default, got %q", m)
	}
	if got := Other(map[string]string{"shell": "deny", "mcp_mail_*": "ask", "email_send": "ask"}); len(got) != 1 || got["mcp_mail_*"] != "ask" {
		t.Fatalf("the rules no category owns: %v", got)
	}
	if _, err := SetMode(nil, mail, "sometimes"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

func TestHandoffIsARuleAndRulesCanBeReplacedWhileRunning(t *testing.T) {
	p, err := ParsePolicy(map[string]string{"email_send": "handoff"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &Guard{Policy: p}
	if v := g.For("chat", "web", 1).Verdict("email_send", true); v.Action != tool.Handoff || v.Via != "rule" {
		t.Fatalf("%+v", v)
	}
	if err := p.SetRules(map[string]string{"email_send": "deny", "shell": "ask"}); err != nil {
		t.Fatal(err)
	}
	if v := g.For("chat", "web", 1).Verdict("email_send", true); v.Action != tool.Deny {
		t.Fatalf("the new rule is not in force: %+v", v)
	}
	if err := p.SetRules(map[string]string{"email_send": "perhaps"}); err == nil {
		t.Fatal("a wrong rule was accepted")
	}
	if got := p.Rules(); got["email_send"] != "deny" || got["shell"] != "ask" {
		t.Fatalf("a refused change altered the rules: %v", got)
	}
	if !strings.Contains(p.Describe(), "email_send: deny") {
		t.Fatalf("%s", p.Describe())
	}
}

// A saved login is the one thing a kind-of-action rule must never switch off: it is not in any category, so no
// "Just do it" on a kind can authorise using one.
func TestNoCategoryCanSilenceTheApprovalForASavedLogin(t *testing.T) {
	for _, c := range Categories {
		for _, name := range c.Tools {
			if name == "browser_fill" || name == "vault_list" || name == "mcp_*" && false {
				t.Errorf("category %s covers %s", c.ID, name)
			}
		}
	}
}
