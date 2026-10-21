package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// A person approving a tool call decides on what they are shown, so what they
// are shown must be what will happen: the target first, nothing cut off, and no
// characters that can make one line look like another.

// approvalFirst are the arguments that say what a call acts on, shown before the
// rest. A long value sorted ahead of them would otherwise push them out of sight.
var approvalFirst = []string{"path", "command", "url", "to", "name", "tool", "id", "title", "recipient", "host"}

// ApprovalText renders a call's arguments for a person to read. It never
// shortens anything: a caller that cannot show it all must say so and show the
// rest another way, or refuse. Control and direction-changing characters are
// written out visibly, and a value that runs over several lines is indented, so
// that no text inside a value can pass for a "path: ..." line of its own.
func ApprovalText(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil || obj == nil {
		return visible(strings.TrimSpace(raw))
	}
	if len(obj) == 0 {
		return "(no arguments)"
	}
	seen := map[string]bool{}
	var keys []string
	for _, k := range approvalFirst {
		if _, ok := obj[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range obj {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var b strings.Builder
	for _, k := range keys {
		var val string
		if s, ok := obj[k].(string); ok {
			val = s
		} else {
			v, _ := json.Marshal(obj[k])
			val = string(v)
		}
		lines := strings.Split(visible(val), "\n")
		fmt.Fprintf(&b, "%s: %s\n", visible(k), lines[0])
		for _, l := range lines[1:] {
			b.WriteString("  │ " + l + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// visible writes characters that act on a display, or hide, as \u{...}. Newlines
// and tabs stay as they are.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == ' ', r == ' ':
			fmt.Fprintf(&b, "\\u{%04x}", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
