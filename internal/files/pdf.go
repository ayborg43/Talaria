package files

import (
	"fmt"
	"strings"
)

// GeneratePDF builds a small PDF 1.4 document from a title and plain text,
// using only the standard library and the built-in Helvetica family: no
// embedded fonts, no dependencies. Text outside Latin-1 becomes "?".
func GeneratePDF(title, body string) []byte {
	lines := paginate(title, body)
	var objs [][]byte
	// 1: catalog, 2: pages, then per page: page + content, then 2 fonts.
	nPages := len(lines)
	pageObj := 3
	var kids []string
	for i := 0; i < nPages; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj))
		pageObj += 2
	}
	fontTitle, fontBody := pageObj, pageObj+1
	objs = append(objs, []byte("<< /Type /Catalog /Pages 2 0 R >>"))
	objs = append(objs, []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), nPages)))
	contentIDs := []int{}
	for i := 0; i < nPages; i++ {
		contentIDs = append(contentIDs, pageObj+1)
		objs = append(objs, []byte(fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R /F2 %d 0 R >> >> /Contents %d 0 R >>",
			fontTitle, fontBody, pageObj+1)))
		objs = append(objs, contentStream(lines[i], i == 0, title))
		pageObj += 2
	}
	objs = append(objs, []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>"))
	objs = append(objs, []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"))

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objs)+1)
	for i, o := range objs {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n", i+1)
		if i >= 2 && (i-2)%2 == 1 {
			// Content streams need length headers.
			fmt.Fprintf(&b, "<< /Length %d >>\nstream\n", len(o))
			b.Write(o)
			b.WriteString("\nendstream\n")
		} else {
			b.Write(o)
			b.WriteString("\n")
		}
		b.WriteString("endobj\n")
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", len(objs)+1, xref)
	return []byte(b.String())
}

// paginate wraps the title and body into pages of printable lines.
func paginate(title, body string) [][]string {
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if l == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrap(l, 88)...)
	}
	const perPage = 46
	var pages [][]string
	for len(lines) > 0 {
		n := perPage
		if len(lines) < n {
			n = len(lines)
		}
		pages = append(pages, lines[:n])
		lines = lines[n:]
	}
	if len(pages) == 0 {
		pages = [][]string{{}}
	}
	_ = title
	return pages
}

// contentStream renders one page: the title on top of the first page, then
// the lines in Helvetica 12pt with 14.4pt leading.
func contentStream(lines []string, first bool, title string) []byte {
	var b strings.Builder
	y := 770.0
	if first {
		fmt.Fprintf(&b, "BT /F1 18 Tf 72 %.1f Td (%s) Tj ET\n", y, pdfText(title))
		y -= 30
	}
	b.WriteString("BT /F2 12 Tf 14.4 TL\n")
	firstLine := true
	for _, l := range lines {
		if firstLine {
			fmt.Fprintf(&b, "72 %.1f Td (%s) Tj\n", y, pdfText(l))
			firstLine = false
			continue
		}
		fmt.Fprintf(&b, "T* (%s) Tj\n", pdfText(l))
	}
	b.WriteString("ET")
	return []byte(b.String())
}

// wrap folds one line to width columns on spaces.
func wrap(line string, width int) []string {
	var out []string
	for len([]rune(line)) > width {
		r := []rune(line)
		cut := width
		for cut > 0 && r[cut] != ' ' {
			cut--
		}
		if cut == 0 {
			cut = width
		}
		out = append(out, string(r[:cut]))
		line = strings.TrimLeft(string(r[cut:]), " ")
	}
	out = append(out, line)
	return out
}

// pdfText escapes a string for a PDF literal and folds non-Latin-1 to "?".
func pdfText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			if r < 32 || r > 255 {
				if r == '\t' {
					b.WriteString("    ")
					continue
				}
				b.WriteByte('?')
				continue
			}
			// One Latin-1 byte: what the built-in fonts read under their
			// default encoding. UTF-8 bytes would show as two wrong glyphs.
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}
