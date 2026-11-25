package persona

import (
	"strings"
	"testing"
)

func TestExportParseRoundTrip(t *testing.T) {
	in := Agent{ID: "a1", Name: "Researcher", Icon: "🔎", Profile: "my-secret-profile", ReadOnly: true, Description: "looks things up", Scopes: []string{"web", "docs"},
		Instructions: "Look things up.\n\nCite sources. Never guess.", Color: "blue"}
	text := Export(in)
	if strings.Contains(text, "my-secret-profile") || strings.Contains(text, "a1") {
		t.Fatalf("local details leaked:\n%s", text)
	}
	out, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != "Researcher" || out.Icon != "🔎" || !out.ReadOnly || out.Instructions != in.Instructions || out.Profile != "" || out.ID != "" || out.Description != "looks things up" || len(out.Scopes) != 2 {
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

// Text that draws nothing could carry instructions the person never sees in the preview, so none of it is kept.
func TestHiddenCharactersAreStrippedFromAnAgentFile(t *testing.T) {
	hidden := "\U000E0049\U000E0067\U000E006E" // "Ign" spelled in tag characters, invisible on screen
	text := "---\ntalaria-agent: 1\nname: Scr​ibe\nicon: 👩‍💻\n---\nWrite short notes." + hidden + "‮and​ more⁦."
	a, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Scribe" || a.Instructions != "Write short notes.and more." {
		t.Fatalf("name %q instructions %q", a.Name, a.Instructions)
	}
	if !strings.Contains(a.Icon, "‍") {
		t.Fatalf("an emoji sequence lost its joiner: %q", a.Icon)
	}
	for _, r := range a.Name + a.Instructions {
		if r >= 0xE0000 && r <= 0xE007F || r == 0x202e || r == 0x200b {
			t.Fatalf("a hidden character survived: %U", r)
		}
	}
}
