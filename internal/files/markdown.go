package files

import (
	"html"
	"regexp"
	"strings"
)

// Markdown is rendered here by a small converter of its own rather than a library: the output is a page the
// agent wrote, so every character of text is escaped first and only the few shapes below are turned back into
// tags. There is no way to write a raw tag, a script or an event handler through it.

var (
	inlineCode = regexp.MustCompile("`([^`\n]+)`")
	inlineBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	inlineEm   = regexp.MustCompile(`(^|[^*\w])\*([^*\n]+)\*`)
	inlineLink = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+|mailto:[^\s)]+)\)`)
	orderedRe  = regexp.MustCompile(`^\s*\d{1,3}[.)]\s+(.*)$`)
	bulletRe   = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	ruleRe     = regexp.MustCompile(`^\s*(-{3,}|\*{3,}|_{3,})\s*$`)
	tableSepRe = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
)

// inline turns one line's text into HTML: escaped, then code, bold, italic and http(s) or mailto links.
func inline(s string) string {
	s = html.EscapeString(s)
	s = inlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = inlineBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = inlineEm.ReplaceAllString(s, "$1<em>$2</em>")
	s = inlineLink.ReplaceAllString(s, `<a href="$2" rel="noopener noreferrer">$1</a>`)
	return s
}

// Escape is inline without any formatting: plain text made safe for a page.
func escapeText(s string) string { return html.EscapeString(s) }

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// markdownToHTML renders headings, paragraphs, bullet and numbered lists, quotes, fenced code, rules and pipe
// tables. Anything else is a paragraph of text.
func markdownToHTML(src string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n"), "\n")
	var out strings.Builder
	var para []string
	flush := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			flush()
		case strings.HasPrefix(trim, "```"):
			flush()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case headingRe.MatchString(trim):
			flush()
			m := headingRe.FindStringSubmatch(trim)
			n := len(m[1])
			out.WriteString("<h" + string(rune('0'+n)) + ">" + inline(m[2]) + "</h" + string(rune('0'+n)) + ">\n")
		case ruleRe.MatchString(trim):
			flush()
			out.WriteString("<hr>\n")
		case strings.HasPrefix(trim, ">"):
			flush()
			var quote []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
			}
			i--
			out.WriteString("<blockquote><p>" + inline(strings.Join(quote, " ")) + "</p></blockquote>\n")
		case bulletRe.MatchString(line) || orderedRe.MatchString(line):
			flush()
			tag, re := "ul", bulletRe
			if orderedRe.MatchString(line) {
				tag, re = "ol", orderedRe
			}
			out.WriteString("<" + tag + ">\n")
			for ; i < len(lines) && re.MatchString(lines[i]); i++ {
				out.WriteString("<li>" + inline(re.FindStringSubmatch(lines[i])[1]) + "</li>\n")
			}
			i--
			out.WriteString("</" + tag + ">\n")
		case strings.Contains(trim, "|") && i+1 < len(lines) && tableSepRe.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			flush()
			head := splitRow(trim)
			out.WriteString("<table>\n<thead><tr>")
			for _, c := range head {
				out.WriteString("<th>" + inline(c) + "</th>")
			}
			out.WriteString("</tr></thead>\n<tbody>\n")
			for i += 2; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				out.WriteString("<tr>")
				cells := splitRow(lines[i])
				for j := range head {
					c := ""
					if j < len(cells) {
						c = cells[j]
					}
					out.WriteString("<td>" + inline(c) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
			i--
			out.WriteString("</tbody></table>\n")
		default:
			para = append(para, trim)
		}
	}
	flush()
	return out.String()
}
