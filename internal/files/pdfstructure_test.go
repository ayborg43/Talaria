package files

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Every page must name, as its /Contents, an object that really is its content stream. A bug once pointed the
// pages at the font objects instead, which strict readers (Android's, poppler's) draw as a blank page.
func TestGeneratedPDFPagesPointAtTheirOwnContent(t *testing.T) {
	long := strings.Repeat("A line of text that fills a page well enough.\n", 120) // several pages
	for _, body := range []string{"hello", long} {
		raw := string(GeneratePDF("Report", body))
		contents := regexp.MustCompile(`/Contents (\d+) 0 R`).FindAllStringSubmatch(raw, -1)
		if len(contents) == 0 {
			t.Fatal("no pages")
		}
		for _, m := range contents {
			obj := regexp.MustCompile(`(?s)\n` + m[1] + ` 0 obj\n(.*?)endobj`).FindStringSubmatch(raw)
			if obj == nil {
				t.Fatalf("object %s is missing", m[1])
			}
			if !strings.Contains(obj[1], "stream\n") || !strings.Contains(obj[1], " Tf") {
				t.Errorf("object %s is not a content stream: %.80q", m[1], obj[1])
			}
		}
		for _, m := range regexp.MustCompile(`(?s)\n(\d+) 0 obj\n(.*?)endobj`).FindAllStringSubmatch(raw, -1) {
			if strings.Contains(m[2], "/Type /Font") && strings.Contains(m[2], "stream") {
				t.Errorf("font object %s is wrapped as a stream: %.80q", m[1], m[2])
			}
		}
		if got := fmt.Sprint(strings.Count(raw, "/Type /Page ")); got != fmt.Sprint(len(contents)) {
			t.Errorf("pages %s, contents %d", got, len(contents))
		}
	}
}
