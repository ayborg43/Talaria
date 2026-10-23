package mailbox

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxBodyRead = 256 << 10 // how much of one text part is read
	maxParts    = 50
	maxMIMEDeep = 5
	maxBody     = 8000 // characters of a message the model is shown
)

// Attachment is a file in a message. Its content is never fetched or shown.
type Attachment struct {
	Name string
	Type string
	Size int
}

// Message is one message, read.
type Message struct {
	UID         uint32
	From, To    string
	Cc, Subject string
	Date        string
	Body        string
	Attachments []Attachment
	Notes       []string
}

var cp1252 = [32]rune{0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178}

// toUTF8 converts text in a charset the standard library does not know about. The common ones are
// done by hand; for others it reports false and the text is shown as well as it can be.
func toUTF8(label string, b []byte) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return strings.ToValidUTF8(string(b), "�"), true
	case "iso-8859-1", "latin1", "latin-1", "iso_8859-1":
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		return string(rs), true
	case "windows-1252", "cp1252", "iso-8859-15":
		rs := make([]rune, len(b))
		for i, c := range b {
			if c >= 0x80 && c < 0xA0 {
				rs[i] = cp1252[c-0x80]
			} else {
				rs[i] = rune(c)
			}
		}
		return string(rs), true
	}
	return strings.ToValidUTF8(string(b), "�"), false
}

type charsetReader struct{ unknown *string }

func (cr charsetReader) read(label string, r io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return nil, err
	}
	s, ok := toUTF8(label, b)
	if !ok && *cr.unknown == "" {
		*cr.unknown = label
	}
	return strings.NewReader(s), nil
}

func decodeHeader(s string) string {
	var unknown string
	dec := mime.WordDecoder{CharsetReader: charsetReader{&unknown}.read}
	out, err := dec.DecodeHeader(s)
	if err != nil {
		out = s
	}
	return strings.ToValidUTF8(out, "�")
}

// Address shows a sender or recipient as "Name <address>". The address is always shown: a display name
// is whatever the sender chose to write, and can claim to be someone else.
func Address(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if list, err := mail.ParseAddressList(s); err == nil {
		out := make([]string, 0, len(list))
		for i, a := range list {
			if i == 5 {
				out = append(out, fmt.Sprintf("and %d more", len(list)-5))
				break
			}
			name := strings.TrimSpace(decodeHeader(a.Name))
			if name == "" || name == a.Address {
				out = append(out, a.Address)
			} else {
				out = append(out, name+" <"+a.Address+">")
			}
		}
		return strings.Join(out, ", ")
	}
	return decodeHeader(s)
}

type walker struct {
	plain, html bytes.Buffer
	attach      []Attachment
	notes       []string
	parts       int
	unknown     string
}

type headerGetter func(string) string

func (w *walker) walk(get headerGetter, body io.Reader, depth int) {
	w.parts++
	mt, params, err := mime.ParseMediaType(get("Content-Type"))
	if err != nil || mt == "" {
		mt, params = "text/plain", map[string]string{}
	}
	disp, dparams, _ := mime.ParseMediaType(get("Content-Disposition"))
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	if strings.HasPrefix(mt, "multipart/") {
		if depth >= maxMIMEDeep || params["boundary"] == "" {
			return
		}
		mr := multipart.NewReader(body, params["boundary"])
		for w.parts < maxParts {
			p, err := mr.NextPart()
			if err != nil {
				return
			}
			w.walk(func(k string) string { return p.Header.Get(k) }, p, depth+1)
		}
		w.notes = append(w.notes, "this message has more parts than are read")
		return
	}
	attachment := disp == "attachment" || !strings.HasPrefix(mt, "text/")
	if attachment {
		n, _ := io.Copy(io.Discard, io.LimitReader(body, 32<<20))
		if len(w.attach) < 30 {
			w.attach = append(w.attach, Attachment{Name: strings.TrimSpace(decodeHeader(name)), Type: mt, Size: int(n)})
		}
		return
	}
	var r io.Reader = body
	switch strings.ToLower(strings.TrimSpace(get("Content-Transfer-Encoding"))) {
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	}
	raw, _ := io.ReadAll(io.LimitReader(r, maxBodyRead))
	text, ok := toUTF8(params["charset"], raw)
	if !ok && w.unknown == "" {
		w.unknown = params["charset"]
	}
	dst := &w.plain
	if mt == "text/html" {
		dst = &w.html
		text = htmlToText(text)
	} else if mt != "text/plain" {
		return
	}
	if dst.Len() > 0 {
		dst.WriteString("\n\n")
	}
	dst.WriteString(text)
}

var (
	htmlDrop  = regexp.MustCompile(`(?is)<(script|style|head|title)\b.*?</(script|style|head|title)\s*>|<!--.*?-->`)
	htmlBreak = regexp.MustCompile(`(?i)</?(br|p|div|li|tr|h[1-6]|table|blockquote)\b[^>]*>`)
	htmlTag   = regexp.MustCompile(`(?s)<[^>]*>`)
)

// htmlToText is a rough way to read an HTML message as text. Anything that is not shown to a person
// reading the mail (scripts, styles, comments) is dropped.
func htmlToText(s string) string {
	s = htmlDrop.ReplaceAllString(s, " ")
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

// cleanBody makes message text safe to show: no control or direction-changing characters, no runs of
// blank lines, and cut to n characters.
func cleanBody(s string, n int) (string, int) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t' || r == 0xA0:
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 || r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	s = strings.TrimSpace(strings.Join(out, "\n"))
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])), len(r) - n
	}
	return s, 0
}

// plainLine makes a header into one safe line.
func plainLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// parseMessage reads a whole message. truncated says the server was asked for only the start of it.
func parseMessage(rawMsg []byte, truncated bool) (*Message, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(rawMsg))
	if err != nil {
		return nil, fmt.Errorf("the message could not be read: %w", err)
	}
	h := msg.Header
	m := &Message{
		From:    plainLine(Address(h.Get("From")), 200),
		To:      plainLine(Address(h.Get("To")), 300),
		Cc:      plainLine(Address(h.Get("Cc")), 300),
		Subject: plainLine(decodeHeader(h.Get("Subject")), 200),
	}
	w := &walker{}
	w.walk(func(k string) string { return h.Get(k) }, msg.Body, 0)
	text := w.plain.String()
	if strings.TrimSpace(text) == "" {
		text = w.html.String()
	}
	body, more := cleanBody(text, maxBody)
	m.Body = body
	if more > 0 {
		m.Notes = append(m.Notes, fmt.Sprintf("%d more characters of this message are not shown", more))
	}
	if truncated {
		m.Notes = append(m.Notes, "this message is large, and only its start was read")
	}
	if w.unknown != "" {
		m.Notes = append(m.Notes, fmt.Sprintf("some text is in the %q character set, which is shown approximately", plainLine(w.unknown, 30)))
	}
	m.Notes = append(m.Notes, w.notes...)
	m.Attachments = w.attach
	if strings.TrimSpace(m.Body) == "" && len(m.Attachments) == 0 {
		m.Body = "(no text)"
	}
	return m, nil
}
