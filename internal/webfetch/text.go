package webfetch

import (
	"regexp"
	"strings"
)

var (
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	scriptRe  = regexp.MustCompile(`(?is)<script.*?</script>`)
	styleRe   = regexp.MustCompile(`(?is)<style.*?</style>`)
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	spaceRe   = regexp.MustCompile(`[ \t\f\v]+`)
)

// textify strips a page to the words a reader sees: no markup, scripts,
// styles or comments, one line each.
func textify(body []byte) string {
	s := string(body)
	s = commentRe.ReplaceAllString(s, "")
	s = scriptRe.ReplaceAllString(s, "")
	s = styleRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllString(s, "\n")
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = spaceRe.ReplaceAllString(strings.TrimSpace(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > MaxText {
		out = string(r[:MaxText])
	}
	return out
}
