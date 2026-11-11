package browser

import "testing"

func TestElementsAreNamedInWordsAndPrivateOnesAreSpotted(t *testing.T) {
	for _, c := range []struct {
		e       Described
		words   string
		private bool
	}{
		{Described{Tag: "button", Label: "Sign in"}, `the button "Sign in"`, false},
		{Described{Tag: "a", Label: "Pricing"}, `the link "Pricing"`, false},
		{Described{Tag: "input", Type: "text", Label: "Search"}, `the field "Search"`, false},
		{Described{Tag: "input", Type: "password", Label: "Password"}, `the field "Password"`, true},
		{Described{Tag: "input", Type: "text", Label: "Card number"}, `the field "Card number"`, true},
		{Described{Tag: "input", Type: "text", AC: "one-time-code", Label: "Enter"}, `the field "Enter"`, true},
		{Described{Tag: "input", Type: "text", Name: "cvv"}, `a field with no label`, true},
		{Described{Tag: "input", Type: "email", Label: "Email"}, `the field "Email"`, false},
		{Described{Tag: "div", Label: "Hello"}, `the text "Hello"`, false},
	} {
		if got := c.e.Words(); got != c.words {
			t.Errorf("%+v: %q, want %q", c.e, got, c.words)
		}
		if got := c.e.Private(); got != c.private {
			t.Errorf("%+v: private %v, want %v", c.e, got, c.private)
		}
	}
}
