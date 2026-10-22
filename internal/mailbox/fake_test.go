package mailbox

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIMAP is a mail server that records every command it is sent, so the tests can show what Talaria
// did and, more to the point, what it never did.
type fakeIMAP struct {
	mu       sync.Mutex
	cmds     []string
	user     string
	pass     string
	folders  []string
	msgs     map[uint32]string
	unread   map[uint32]bool
	search   []uint32
	greeting string
	// custom, if set, answers a command instead of the normal handling; it returns true when it has.
	custom func(w *bufio.Writer, tag, verb, rest string) bool
	ln     net.Listener
}

var fakeStamp = "22-Oct-2026 08:30:00 +0000"

func newFake(t *testing.T) (*fakeIMAP, *Service) {
	t.Helper()
	ts := httptest.NewTLSServer(nil)
	t.Cleanup(ts.Close)
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: ts.TLS.Certificates})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeIMAP{
		user: "me@example.com", pass: "app-pass-1234",
		folders:  []string{`(\HasNoChildren) "/" INBOX`, `(\Noselect \HasChildren) "/" "[Gmail]"`, `(\HasNoChildren) "/" "Archive &AOk-"`, `(\HasNoChildren) "/" Sent`},
		msgs:     map[uint32]string{},
		unread:   map[uint32]bool{},
		greeting: "* OK IMAP ready\r\n",
		ln:       ln,
	}
	go f.serve()
	lagos, _ := time.LoadLocation("Africa/Lagos")
	svc := &Service{
		Address: f.user, Password: f.pass, Server: ln.Addr().String(), Display: lagos,
		Now: func() time.Time { return time.Date(2026, 10, 22, 10, 0, 0, 0, lagos) },
		TLS: &tls.Config{RootCAs: pool},
	}
	return f, svc
}

func (f *fakeIMAP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeIMAP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

var quotedArg = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

func unquote(s string) string { return regexp.MustCompile(`\\(.)`).ReplaceAllString(s, "$1") }

func (f *fakeIMAP) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	f.mu.Lock()
	greeting := f.greeting
	f.mu.Unlock()
	w.WriteString(greeting)
	w.Flush()
	if greeting == "" {
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		// Fold in any literals the client announces.
		for {
			m := regexp.MustCompile(`\{(\d+)\}$`).FindStringSubmatch(line)
			if m == nil {
				break
			}
			n, _ := strconv.Atoi(m[1])
			w.WriteString("+ go ahead\r\n")
			w.Flush()
			buf := make([]byte, n)
			if _, err := r.Read(buf); err != nil {
				return
			}
			for len(buf) < n {
				return
			}
			rest, _ := r.ReadString('\n')
			line = strings.TrimSuffix(line, m[0]) + `"` + strings.ReplaceAll(string(buf), `"`, `\"`) + `"` + strings.TrimRight(rest, "\r\n")
		}
		f.mu.Lock()
		stop := f.process(w, line)
		f.mu.Unlock()
		w.Flush()
		if stop {
			return
		}
	}
}

// process answers one command, with the lock held. It reports whether the connection is finished.
func (f *fakeIMAP) process(w *bufio.Writer, line string) bool {
	f.cmds = append(f.cmds, line)
	for once := true; once; once = false {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			return false
		}
		tag, verb := parts[0], strings.ToUpper(parts[1])
		rest := ""
		if len(parts) == 3 {
			rest = parts[2]
		}
		if verb == "UID" {
			sub := strings.SplitN(rest, " ", 2)
			verb = "UID " + strings.ToUpper(sub[0])
			rest = ""
			if len(sub) == 2 {
				rest = sub[1]
			}
		}
		if f.custom != nil && f.custom(w, tag, verb, rest) {
			return false
		}
		switch verb {
		case "CAPABILITY":
			fmt.Fprintf(w, "* CAPABILITY IMAP4rev1 UIDPLUS\r\n%s OK done\r\n", tag)
		case "LOGIN":
			args := quotedArg.FindAllStringSubmatch(rest, -1)
			if len(args) == 2 && unquote(args[0][1]) == f.user && unquote(args[1][1]) == f.pass {
				fmt.Fprintf(w, "%s OK logged in\r\n", tag)
			} else {
				fmt.Fprintf(w, "%s NO [AUTHENTICATIONFAILED] Invalid credentials for %s\r\n", tag, rest)
			}
		case "LIST":
			for _, l := range f.folders {
				fmt.Fprintf(w, "* LIST %s\r\n", l)
			}
			fmt.Fprintf(w, "%s OK done\r\n", tag)
		case "EXAMINE":
			name := unquote(strings.Trim(rest, `"`))
			if name == "INBOX" || name == "Sent" || name == "Archive &AOk-" {
				fmt.Fprintf(w, "* %d EXISTS\r\n%s OK [READ-ONLY] examined\r\n", len(f.msgs), tag)
			} else {
				fmt.Fprintf(w, "%s NO no such mailbox\r\n", tag)
			}
		case "UID SEARCH":
			fmt.Fprintf(w, "* SEARCH")
			for _, u := range f.search {
				fmt.Fprintf(w, " %d", u)
			}
			fmt.Fprintf(w, "\r\n%s OK done\r\n", tag)
		case "UID FETCH":
			set := strings.SplitN(rest, " ", 2)[0]
			for _, us := range strings.Split(set, ",") {
				u, _ := strconv.Atoi(us)
				raw, ok := f.msgs[uint32(u)]
				if !ok {
					continue
				}
				flags := `\Seen`
				if f.unread[uint32(u)] {
					flags = ""
				}
				if strings.Contains(rest, "HEADER.FIELDS") {
					head := raw[:strings.Index(raw, "\r\n\r\n")+4]
					var keep []string
					for _, l := range strings.Split(head, "\r\n") {
						if strings.HasPrefix(l, "From:") || strings.HasPrefix(l, "Subject:") {
							keep = append(keep, l)
						}
					}
					h := strings.Join(keep, "\r\n") + "\r\n\r\n"
					fmt.Fprintf(w, "* %d FETCH (UID %d FLAGS (%s) INTERNALDATE \"%s\" RFC822.SIZE %d BODY[HEADER.FIELDS (FROM SUBJECT)] {%d}\r\n%s)\r\n", u, u, flags, fakeStamp, len(raw), len(h), h)
				} else {
					fmt.Fprintf(w, "* %d FETCH (UID %d FLAGS (%s) INTERNALDATE \"%s\" RFC822.SIZE %d BODY[]<0> {%d}\r\n%s)\r\n", u, u, flags, fakeStamp, len(raw), len(raw), raw)
				}
			}
			fmt.Fprintf(w, "%s OK done\r\n", tag)
		case "LOGOUT":
			fmt.Fprintf(w, "* BYE bye\r\n%s OK logged out\r\n", tag)
			return true
		default:
			fmt.Fprintf(w, "%s BAD command not understood\r\n", tag)
		}
	}
	return false
}

// with changes the fake's settings while it may be serving.
func (f *fakeIMAP) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// verbs lists the command verbs the server was sent, in order.
func verbs(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		p := strings.Fields(c)
		if len(p) < 2 {
			continue
		}
		v := strings.ToUpper(p[1])
		if v == "UID" && len(p) > 2 {
			v += " " + strings.ToUpper(p[2])
		}
		out = append(out, v)
	}
	return out
}
