package persona

import (
	"strings"
	"testing"
)

func TestExportParseRoundTrip(t *testing.T) {
	in := Agent{ID: "a1", Name: "Researcher", Icon: "🔎", Profile: "my-secret-profile", ReadOnly: true,
		Instructions: "Look things up.\n\nCite sources. Never guess.", Color: "blue"}
	text := Export(in)
	if strings.Contains(text, "my-secret-profile") || strings.Contains(text, "a1") {
		t.Fatalf("local details leaked:\n%s", text)
	}
	out, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "Researcher" || out.Icon != "🔎" || !out.ReadOnly || out.Instructions != in.Instructions || out.Profile != "" || out.ID != "" {
		t.Fatalf("%+v", out)
	}
}

func TestParseRefusesWhatItDoesNotKnow(t *testing.T) {
	cases := map[string]string{
		"no header":        "just some text",
		"unclosed":         "---\ntalaria-agent: 1\nname: A\n",
		"no version":       "---\nname: A\n---\nDo it.",
		"future version":   "---\ntalaria-agent: 9\nname: A\n---\nDo it.",
		"unknown key":      "---\ntalaria-agent: 1\nname: A\nprofile: expensive\n---\nDo it.",
		"bad bool":         "---\ntalaria-agent: 1\nname: A\nlooks-only: maybe\n---\nDo it.",
		"no instructions":  "---\ntalaria-agent: 1\nname: A\n---\n",
		"no name":          "---\ntalaria-agent: 1\n---\nDo it.",
		"too long":         "---\ntalaria-agent: 1\nname: A\n---\n" + strings.Repeat("x", MaxFile),
		"instructions cap": "---\ntalaria-agent: 1\nname: A\n---\n" + strings.Repeat("x", MaxInstructions+10),
	}
	for name, text := range cases {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseToleratesWindowsLineEndingsAndAnEmptyLooksOnly(t *testing.T) {
	a, err := Parse("\ufeff---\r\ntalaria-agent: 1\r\nname: Scribe\r\n---\r\n\r\nWrite short notes.\r\n")
	if err != nil || a.Name != "Scribe" || a.ReadOnly || a.Instructions != "Write short notes." {
		t.Fatalf("%+v %v", a, err)
	}
}
