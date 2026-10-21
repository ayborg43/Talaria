package browser

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var sensitiveParam = regexp.MustCompile(`(?i)pass|pwd|^pw$|secret|token|key|auth|code|session|sid$|^sid|signature|^sig$|credential|otp|ticket`)

// SafeURL is a web address with the values of query and fragment parameters
// that look like secrets (a password a form sent by GET, an OAuth code, a
// magic-link token) replaced, since the address is shown to the model and kept
// in logs. It is only for display: the browser still uses the real address.
func SafeURL(raw string) string {
	if _, err := url.Parse(raw); err != nil {
		return raw
	}
	clean := func(q string) string {
		parts := strings.Split(q, "&")
		for i, p := range parts {
			name, _, hasValue := strings.Cut(p, "=")
			if !hasValue {
				continue
			}
			if dec, err := url.QueryUnescape(name); err == nil {
				name = dec
			}
			if sensitiveParam.MatchString(name) {
				parts[i] = p[:strings.Index(p, "=")+1] + "[redacted]"
			}
		}
		return strings.Join(parts, "&")
	}
	head, frag, hasFrag := strings.Cut(raw, "#")
	path, query, hasQuery := strings.Cut(head, "?")
	out := path
	if hasQuery {
		out += "?" + clean(query)
	}
	if hasFrag {
		if strings.Contains(frag, "=") { // a fragment of name=value pairs, as an OAuth redirect has
			frag = clean(frag)
		}
		out += "#" + frag
	}
	return out
}

// Brief names an element in a few words: `button "Place order"`.
func Brief(e Element) string {
	kind := e.Role
	switch {
	case e.Tag == "a":
		kind = "link"
	case e.Tag == "button" || e.Type == "button" || e.Type == "submit" || e.Type == "reset" || e.Role == "button":
		kind = "button"
	case e.Tag == "select":
		kind = "drop-down list"
	case e.Tag == "textarea":
		kind = "text area"
	case e.Type == "checkbox" || e.Type == "radio":
		kind = e.Type
	case e.Tag == "input":
		kind = "field"
		if e.Type != "" && e.Type != "text" {
			kind = e.Type + " field"
		}
	case kind == "":
		kind = e.Tag
	}
	if e.Label == "" {
		return kind + fmt.Sprintf(" #%d", e.Ref)
	}
	return fmt.Sprintf("%s %q", kind, e.Label)
}

// line renders one element for the page listing.
func line(e Element) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] %s", e.Ref, Brief(e))
	switch {
	case e.Tag == "a" && e.Href != "":
		fmt.Fprintf(&b, " → %s", clip(SafeURL(e.Href), 110))
	case e.Tag == "select":
		fmt.Fprintf(&b, " = %q", e.Value)
		if len(e.Options) > 0 {
			fmt.Fprintf(&b, " (options: %s)", strings.Join(e.Options, " | "))
		}
	case e.Tag == "input" || e.Tag == "textarea":
		if e.Checked != nil {
			b.WriteString(map[bool]string{true: " (checked)", false: " (not checked)"}[*e.Checked])
			break
		}
		if e.Placeholder != "" && e.Placeholder != e.Label {
			fmt.Fprintf(&b, " placeholder %q", e.Placeholder)
		}
		if e.Value == "(hidden)" { // a password or card number: whether it is filled in, never what it is
			b.WriteString(" (filled in; the value is hidden)")
		} else if e.Value != "" {
			fmt.Fprintf(&b, " value %q", e.Value)
		} else {
			b.WriteString(" (empty)")
		}
	}
	if e.Disabled {
		b.WriteString(" (disabled)")
	}
	return b.String()
}

// Render writes a snapshot as the text the model reads.
func Render(s *Snapshot, notes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Page: %s\nURL: %s\n", clip(strings.TrimSpace(s.Title), 150), SafeURL(s.URL))
	for _, n := range notes {
		fmt.Fprintf(&b, "Note: %s\n", n)
	}
	if s.ScrollMax > 0 {
		fmt.Fprintf(&b, "Scrolled %d of %d pixels.\n", s.ScrollY, s.ScrollMax)
	}
	b.WriteString("\n")
	if len(s.Elements) == 0 {
		b.WriteString("Nothing on this page can be clicked or filled in.\n")
	} else {
		b.WriteString("Elements you can use (numbers are good until the next listing):\n")
		for _, e := range s.Elements {
			b.WriteString(line(e) + "\n")
		}
		if s.More > 0 {
			fmt.Fprintf(&b, "(%d more elements are not listed; scroll or open a narrower page)\n", s.More)
		}
	}
	if text := strings.TrimSpace(s.Text); text != "" {
		b.WriteString("\n" + untrusted + "\n" + text + "\n")
		if s.TextLength > len(s.Text) {
			b.WriteString("[the text was cut here]\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
