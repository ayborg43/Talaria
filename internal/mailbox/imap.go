// Package mailbox lets the agent read mail over IMAP, and only read it.
//
// The client below is small on purpose. It can send seven commands (see allowed) and none of them
// changes anything: the mailbox is opened with EXAMINE, which the server itself treats as read-only, and
// messages are fetched with BODY.PEEK so they are not even marked as read. What the server sends back is
// treated as hostile: every line, literal and response has a size limit, and nothing from it is trusted
// to be well formed.
package mailbox

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxLine     = 64 << 10 // the longest line, atom or quoted string
	maxLiteral  = 2 << 20  // the largest single block of data
	maxResponse = 8 << 20  // everything one command may bring back
	maxItems    = 200000   // search results
	maxFetches  = 500
	maxEntries  = 1000
	maxDepth    = 6
)

var errTooBig = errors.New("the mail server sent more than Talaria will read")

// allowed is every command this client will send. Anything that could change a mailbox (STORE, COPY,
// MOVE, APPEND, EXPUNGE, DELETE, CREATE, RENAME, SUBSCRIBE, SELECT) is not in it, and send refuses it.
var allowed = map[string]bool{
	"LOGIN": true, "CAPABILITY": true, "LIST": true, "EXAMINE": true,
	"UID SEARCH": true, "UID FETCH": true, "LOGOUT": true,
}

type atomV string // an unquoted word, a number or NIL
type strV string  // a quoted string or a block of data

// arg is one argument of a command: either raw (written by this package, from a fixed alphabet) or a
// string that came from the user or the model, which is quoted or sent as a literal.
type arg struct {
	s   string
	raw bool
}

func raw(s string) arg { return arg{s: s, raw: true} }
func str(s string) arg { return arg{s: s} }

var rawOK = regexp.MustCompile(`^[A-Za-z0-9 .,:*()\[\]<>_-]+$`)

type response struct {
	cont, tagged bool
	tag          string
	status, word string
	text         string
	nums         []uint32
	caps         []string
	fetch        map[string]any
	list         *listEntry
}

type listEntry struct {
	attrs []string
	name  string
}

// result is what one command brought back.
type result struct {
	text    string // the tagged reply's text
	caps    []string
	nums    []uint32
	fetches []map[string]any
	list    []listEntry
}

type counter struct {
	r    io.Reader
	used int
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.used += n
	if c.used > maxResponse {
		return n, errTooBig
	}
	return n, err
}

type client struct {
	conn net.Conn
	cnt  *counter
	r    *bufio.Reader
	w    *bufio.Writer
	tag  int
}

func newClient(conn net.Conn) *client {
	cnt := &counter{r: conn}
	return &client{conn: conn, cnt: cnt, r: bufio.NewReaderSize(cnt, 32<<10), w: bufio.NewWriter(conn)}
}

// ---- reading ----

func (c *client) readByte() (byte, error) { return c.r.ReadByte() }

func (c *client) peek() (byte, error) {
	b, err := c.r.Peek(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// readLine reads up to the end of the line, without the line ending.
func (c *client) readLine() (string, error) {
	var buf []byte
	for {
		b, err := c.readByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			return strings.TrimSuffix(string(buf), "\r"), nil
		}
		if len(buf) >= maxLine {
			return "", errTooBig
		}
		buf = append(buf, b)
	}
}

var literalMark = regexp.MustCompile(`\{(\d{1,9})\+?\}$`)

// skipLine reads and drops the rest of a line, including any blocks of data it announces.
func (c *client) skipLine() (string, error) {
	var all strings.Builder
	for {
		line, err := c.readLine()
		if err != nil {
			return "", err
		}
		all.WriteString(line)
		m := literalMark.FindStringSubmatch(line)
		if m == nil {
			return all.String(), nil
		}
		n, _ := strconv.Atoi(m[1])
		if n > maxLiteral {
			return "", errTooBig
		}
		if _, err := io.CopyN(io.Discard, c.r, int64(n)); err != nil {
			return "", err
		}
	}
}

func (c *client) skipSpaces() error {
	for {
		b, err := c.peek()
		if err != nil {
			return err
		}
		if b != ' ' {
			return nil
		}
		c.readByte()
	}
}

// word reads up to the next space or line ending.
func (c *client) word() (string, error) {
	var buf []byte
	for {
		b, err := c.peek()
		if err != nil {
			return "", err
		}
		if b == ' ' || b == '\r' || b == '\n' {
			return string(buf), nil
		}
		if len(buf) >= maxLine {
			return "", errTooBig
		}
		c.readByte()
		buf = append(buf, b)
	}
}

func (c *client) parseValue(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("the mail server's reply is nested too deeply")
	}
	if err := c.skipSpaces(); err != nil {
		return nil, err
	}
	b, err := c.peek()
	if err != nil {
		return nil, err
	}
	switch b {
	case '(':
		c.readByte()
		var list []any
		for {
			if err := c.skipSpaces(); err != nil {
				return nil, err
			}
			if b, _ := c.peek(); b == ')' {
				c.readByte()
				return list, nil
			}
			if len(list) >= maxEntries {
				return nil, errTooBig
			}
			v, err := c.parseValue(depth + 1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case '"':
		c.readByte()
		var buf []byte
		for {
			b, err := c.readByte()
			if err != nil {
				return nil, err
			}
			if b == '"' {
				return strV(buf), nil
			}
			if b == '\\' {
				if b, err = c.readByte(); err != nil {
					return nil, err
				}
			}
			if len(buf) >= maxLine {
				return nil, errTooBig
			}
			buf = append(buf, b)
		}
	case '{':
		c.readByte()
		digits, err := c.readLine()
		if err != nil {
			return nil, err
		}
		digits = strings.TrimSuffix(strings.TrimSuffix(digits, "+"), "}")
		n, err := strconv.Atoi(digits)
		if err != nil || n < 0 {
			return nil, errors.New("the mail server sent a block of data with no size")
		}
		if n > maxLiteral {
			return nil, errTooBig
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		return strV(buf), nil
	default:
		var buf []byte
		for {
			b, err := c.peek()
			if err != nil {
				return nil, err
			}
			switch {
			case b == '[': // BODY[HEADER.FIELDS (FROM SUBJECT)]: the brackets may hold spaces and parentheses
				for {
					b, err := c.readByte()
					if err != nil {
						return nil, err
					}
					if len(buf) >= maxLine {
						return nil, errTooBig
					}
					buf = append(buf, b)
					if b == ']' {
						break
					}
				}
				continue
			case b == ' ' || b == ')' || b == '(' || b == '\r' || b == '\n':
				return atomV(buf), nil
			}
			if len(buf) >= maxLine {
				return nil, errTooBig
			}
			c.readByte()
			buf = append(buf, b)
		}
	}
}

// readOne reads one response from the server.
func (c *client) readOne() (response, error) {
	var r response
	first, err := c.word()
	if err != nil {
		return r, err
	}
	switch first {
	case "+":
		_, err := c.skipLine()
		r.cont = true
		return r, err
	case "*":
		if err := c.skipSpaces(); err != nil {
			return r, err
		}
		w, err := c.word()
		if err != nil {
			return r, err
		}
		r.word = strings.ToUpper(w)
		if _, numeric := strconv.Atoi(w); numeric == nil {
			if err := c.skipSpaces(); err != nil {
				return r, err
			}
			kind, err := c.word()
			if err != nil {
				return r, err
			}
			r.word = strings.ToUpper(kind)
			if r.word == "FETCH" {
				v, err := c.parseValue(0)
				if err != nil {
					return r, err
				}
				list, _ := v.([]any)
				r.fetch = map[string]any{}
				for i := 0; i+1 < len(list); i += 2 {
					key, _ := list[i].(atomV)
					k := strings.ToUpper(string(key))
					if strings.HasPrefix(k, "BODY[") {
						k = "BODY"
					}
					r.fetch[k] = list[i+1]
				}
			}
			_, err = c.skipLine()
			return r, err
		}
		switch r.word {
		case "SEARCH":
			line, err := c.skipLine()
			for _, f := range strings.Fields(line) {
				if n, e := strconv.ParseUint(f, 10, 32); e == nil && len(r.nums) < maxItems {
					r.nums = append(r.nums, uint32(n))
				}
			}
			return r, err
		case "CAPABILITY":
			line, err := c.skipLine()
			r.caps = strings.Fields(strings.ToUpper(line))
			return r, err
		case "LIST":
			attrs, err := c.parseValue(0)
			if err != nil {
				return r, err
			}
			if _, err := c.parseValue(0); err != nil { // the separator
				return r, err
			}
			name, err := c.parseValue(0)
			if err != nil {
				return r, err
			}
			e := &listEntry{}
			if l, ok := attrs.([]any); ok {
				for _, a := range l {
					if s, ok := a.(atomV); ok {
						e.attrs = append(e.attrs, strings.ToLower(string(s)))
					}
				}
			}
			switch n := name.(type) {
			case strV:
				e.name = string(n)
			case atomV:
				e.name = string(n)
			}
			r.list = e
			_, err = c.skipLine()
			return r, err
		case "OK", "NO", "BAD", "BYE", "PREAUTH":
			r.status = r.word
			line, err := c.skipLine()
			r.text = strings.TrimSpace(line)
			return r, err
		}
		_, err = c.skipLine()
		return r, err
	default:
		r.tagged, r.tag = true, first
		if err := c.skipSpaces(); err != nil {
			return r, err
		}
		st, err := c.word()
		if err != nil {
			return r, err
		}
		r.status = strings.ToUpper(st)
		line, err := c.skipLine()
		r.text = strings.TrimSpace(line)
		return r, err
	}
}

// ---- writing ----

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func quotable(s string) bool {
	if len(s) > 1000 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// send writes one command. A string argument can never become a second command or another argument: a
// control character (which would end the line) is refused, a quote or backslash is escaped, and text
// that is not plain ASCII goes as a literal, whose length the server is told first.
func (c *client) send(verb string, args ...arg) (string, error) {
	if !allowed[verb] {
		return "", fmt.Errorf("Talaria's mail reader does not send %s: it only reads", verb)
	}
	c.tag++
	tag := fmt.Sprintf("T%03d", c.tag)
	c.cnt.used = 0
	var line bytes.Buffer
	line.WriteString(tag + " " + verb)
	for _, a := range args {
		line.WriteByte(' ')
		if a.raw {
			if !rawOK.MatchString(a.s) {
				return "", errors.New("internal error: an unsafe mail command was refused")
			}
			line.WriteString(a.s)
			continue
		}
		if hasControl(a.s) {
			return "", errors.New("a line break or control character cannot be used in a mail search or folder name")
		}
		if quotable(a.s) {
			line.WriteString(quote(a.s))
			continue
		}
		fmt.Fprintf(&line, "{%d}\r\n", len(a.s))
		c.w.Write(line.Bytes())
		line.Reset()
		if err := c.w.Flush(); err != nil {
			return "", err
		}
		for {
			r, err := c.readOne()
			if err != nil {
				return "", err
			}
			if r.cont {
				break
			}
			if r.tagged || r.status == "BYE" {
				return "", errors.New("the mail server would not accept that request")
			}
		}
		c.w.WriteString(a.s)
	}
	line.WriteString("\r\n")
	c.w.Write(line.Bytes())
	return tag, c.w.Flush()
}

// run sends a command and gathers everything the server says until it finishes.
func (c *client) run(verb string, args ...arg) (*result, error) {
	tag, err := c.send(verb, args...)
	if err != nil {
		return nil, err
	}
	res := &result{}
	for {
		r, err := c.readOne()
		if err != nil {
			return nil, err
		}
		switch {
		case r.cont:
			return nil, errors.New("the mail server asked for more than Talaria sent")
		case r.tagged:
			if r.tag != tag {
				continue
			}
			res.text = r.text
			if r.status != "OK" {
				return res, &refused{verb: verb, status: r.status, text: r.text}
			}
			return res, nil
		case r.word == "BYE" && verb != "LOGOUT":
			return nil, errors.New("the mail server closed the connection")
		}
		res.nums = append(res.nums, r.nums...)
		res.caps = append(res.caps, r.caps...)
		if r.fetch != nil {
			if len(res.fetches) >= maxFetches {
				return nil, errTooBig
			}
			res.fetches = append(res.fetches, r.fetch)
		}
		if r.list != nil {
			if len(res.list) >= maxEntries {
				return nil, errTooBig
			}
			res.list = append(res.list, *r.list)
		}
	}
}

// refused is the server saying no to a command.
type refused struct{ verb, status, text string }

func (e *refused) Error() string {
	t := strings.Join(strings.Fields(e.text), " ")
	if len(t) > 120 {
		t = t[:120]
	}
	return fmt.Sprintf("the mail server answered %s to %s: %s", e.status, strings.ToLower(e.verb), t)
}
