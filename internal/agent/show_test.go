package agent

import (
	"strings"
	"testing"
)

func TestApprovalTextShowsTheTargetFirstWhateverTheKeysAreCalled(t *testing.T) {
	got := ApprovalText(`{"content":"` + strings.Repeat("x", 5000) + `","path":"/home/me/.zshrc"}`)
	if !strings.HasPrefix(got, "path: /home/me/.zshrc\ncontent: x") {
		t.Fatalf("the path comes before a long body: %.80q", got)
	}
	if len(got) < 5000 {
		t.Fatal("nothing is cut off")
	}
	got = ApprovalText(`{"zzz":"1","command":"rm -rf x","aaa":"2"}`)
	if got != "command: rm -rf x\naaa: 2\nzzz: 1" {
		t.Fatalf("%q", got)
	}
}

func TestApprovalTextCannotBeMadeToLookLikeAnotherLine(t *testing.T) {
	got := ApprovalText(`{"content":"harmless\npath: /tmp/innocent","path":"/etc/cron.d/evil"}`)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "path: /tmp/innocent") {
			t.Fatalf("a forged line at the start of a line:\n%s", got)
		}
	}
	if !strings.Contains(got, "  │ path: /tmp/innocent") || !strings.HasPrefix(got, "path: /etc/cron.d/evil\n") {
		t.Fatalf("%s", got)
	}
}

func TestApprovalTextWritesOutCharactersThatActOnADisplay(t *testing.T) {
	got := ApprovalText("{\"command\":\"echo hi\\r\\u001b[2K\\u001b[1Aecho harmless\\u202eevil\\u200bx\"}")
	for _, bad := range []string{"\r", "\x1b", "‮", "​"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q is in the output", bad)
		}
	}
	for _, want := range []string{`\u{000d}`, `\u{001b}`, `\u{202e}`, `\u{200b}`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %q", want, got)
		}
	}
	// Raw text that is not JSON gets the same care, and ordinary text is untouched.
	if got := ApprovalText("plain\ttext\nline two é 日本"); got != "plain\ttext\nline two é 日本" {
		t.Fatalf("%q", got)
	}
	if got := ApprovalText("evil\x1b[31mred"); strings.Contains(got, "\x1b") {
		t.Fatalf("%q", got)
	}
}

func TestApprovalTextHandlesOtherValues(t *testing.T) {
	got := ApprovalText(`{"n":3,"list":["a","b"],"flag":true,"nothing":null}`)
	for _, want := range []string{"n: 3", `list: ["a","b"]`, "flag: true", "nothing: null"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if ApprovalText("") != "" || ApprovalText("{}") != "(no arguments)" {
		t.Fatalf("%q %q", ApprovalText(""), ApprovalText("{}"))
	}
}
