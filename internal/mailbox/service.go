package mailbox

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	opTimeout   = 30 * time.Second
	maxFetchLen = 1 << 20 // how much of one message is read
)

// Service reads one mailbox. It connects for each request and signs out when done.
type Service struct {
	Address  string // shown to the user; also the sign-in name unless User is set
	User     string
	Password string // an app password: a secret, never written into an error, a log or what the model sees
	Server   string // host:port, over TLS
	Display  *time.Location
	Now      func() time.Time
	// TLS replaces the connection's TLS settings, for tests.
	TLS *tls.Config

	mu sync.Mutex
}

func (s *Service) zone() *time.Location {
	if s.Display != nil {
		return s.Display
	}
	return time.UTC
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) login() string {
	if s.User != "" {
		return s.User
	}
	return s.Address
}

// scrubErr removes the password from anything that is about to be reported.
func (s *Service) scrubErr(err error) error {
	if err == nil || s.Password == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), s.Password, "<password>"))
}

// session connects, signs in, runs fn, and signs out.
func (s *Service) session(ctx context.Context, fn func(c *client) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cfg := s.TLS
	if cfg == nil {
		cfg = &tls.Config{}
	}
	cfg = cfg.Clone()
	cfg.MinVersion = tls.VersionTLS12
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", s.Server)
	if err != nil {
		return s.scrubErr(fmt.Errorf("could not connect to the mail server %s: %v", s.Server, err))
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(opTimeout))
	go func() { // a cancelled request must not wait for a slow server
		<-ctx.Done()
		conn.Close()
	}()
	c := newClient(conn)

	greet, err := c.readOne()
	if err != nil {
		return s.scrubErr(fmt.Errorf("the mail server did not greet us: %v", err))
	}
	if greet.status != "OK" && greet.status != "PREAUTH" {
		return errors.New("the mail server did not accept the connection")
	}
	defer func() {
		if _, e := c.send("LOGOUT"); e == nil {
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			c.readOne()
		}
	}()
	if greet.status != "PREAUTH" {
		caps, cerr := c.run("CAPABILITY")
		if cerr != nil {
			return s.scrubErr(cerr)
		}
		for _, k := range caps.caps {
			if k == "LOGINDISABLED" {
				return errors.New("the mail server does not allow password sign-in on this connection")
			}
		}
		if _, lerr := c.run("LOGIN", str(s.login()), str(s.Password)); lerr != nil {
			var r *refused
			if errors.As(lerr, &r) {
				return errors.New("the mail server refused the sign-in: check the address and the app password (a normal account password usually does not work), and that IMAP is turned on for the account")
			}
			return s.scrubErr(lerr)
		}
	}
	return s.scrubErr(fn(c))
}

// Folders lists the folders that can be opened.
func (s *Service) Folders(ctx context.Context) ([]string, error) {
	var out []string
	err := s.session(ctx, func(c *client) error {
		res, err := c.run("LIST", str(""), str("*"))
		if err != nil {
			return err
		}
	next:
		for _, e := range res.list {
			for _, a := range e.attrs {
				if a == `\noselect` || a == `\nonexistent` {
					continue next
				}
			}
			name, derr := decodeFolder(e.name)
			if derr != nil {
				name = e.name
			}
			out = append(out, plainLine(name, 100))
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.EqualFold(out[i], "INBOX"), strings.EqualFold(out[j], "INBOX")
		if a != b {
			return a
		}
		return out[i] < out[j]
	})
	return out, err
}

// Query says which messages to list.
type Query struct {
	Folder string
	Days   int // how far back to look
	From   string
	Text   string
	Unread bool
	Limit  int
}

// Summary is one line of a listing.
type Summary struct {
	UID     uint32
	Date    time.Time
	From    string
	Subject string
	Unread  bool
	Size    int
}

func ascii(s string) bool { return quotable(s) }

func (s *Service) examine(c *client, folder string) error {
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.run("EXAMINE", str(encodeFolder(folder))); err != nil {
		var r *refused
		if errors.As(err, &r) {
			return fmt.Errorf("there is no folder called %q (use email_folders to see them)", plainLine(folder, 60))
		}
		return err
	}
	return nil
}

func imapDate(t time.Time) string { return t.Format("02-Jan-2006") }

func uidList(uids []uint32) string {
	parts := make([]string, len(uids))
	for i, u := range uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return strings.Join(parts, ",")
}

func str1(v any) string {
	switch x := v.(type) {
	case strV:
		return string(x)
	case atomV:
		if strings.EqualFold(string(x), "NIL") {
			return ""
		}
		return string(x)
	}
	return ""
}

func num(v any) uint32 {
	n, _ := strconv.ParseUint(str1(v), 10, 32)
	return uint32(n)
}

func hasFlag(v any, flag string) bool {
	l, _ := v.([]any)
	for _, f := range l {
		if strings.EqualFold(str1(f), flag) {
			return true
		}
	}
	return false
}

func parseInternalDate(s string, loc *time.Location) time.Time {
	t, err := time.Parse("2-Jan-2006 15:04:05 -0700", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.In(loc)
}

// Search lists messages, newest first. It returns how many matched in all, which may be more than it lists.
func (s *Service) Search(ctx context.Context, q Query) (list []Summary, total int, err error) {
	if q.Limit <= 0 {
		q.Limit = 15
	}
	err = s.session(ctx, func(c *client) error {
		if err := s.examine(c, q.Folder); err != nil {
			return err
		}
		var args []arg
		if !ascii(q.From) || !ascii(q.Text) {
			args = append(args, raw("CHARSET UTF-8"))
		}
		criteria := 0
		if q.Days > 0 {
			now := s.now().In(s.zone())
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.zone()).AddDate(0, 0, -(q.Days - 1))
			args = append(args, raw("SINCE "+imapDate(start)))
			criteria++
		}
		if q.Unread {
			args = append(args, raw("UNSEEN"))
			criteria++
		}
		if q.From != "" {
			args = append(args, raw("FROM"), str(q.From))
			criteria++
		}
		if q.Text != "" {
			args = append(args, raw("TEXT"), str(q.Text))
			criteria++
		}
		if criteria == 0 {
			args = append(args, raw("ALL"))
		}
		res, err := c.run("UID SEARCH", args...)
		if err != nil {
			return err
		}
		uids := res.nums
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		total = len(uids)
		if len(uids) > q.Limit {
			uids = uids[len(uids)-q.Limit:]
		}
		if len(uids) == 0 {
			return nil
		}
		fr, err := c.run("UID FETCH", raw(uidList(uids)), raw("(UID FLAGS INTERNALDATE RFC822.SIZE BODY.PEEK[HEADER.FIELDS (FROM SUBJECT)])"))
		if err != nil {
			return err
		}
		for _, f := range fr.fetches {
			sum := Summary{UID: num(f["UID"]), Unread: !hasFlag(f["FLAGS"], `\Seen`), Size: int(num(f["RFC822.SIZE"]))}
			sum.Date = parseInternalDate(str1(f["INTERNALDATE"]), s.zone())
			head := str1(f["BODY"])
			if m, merr := mail.ReadMessage(strings.NewReader(strings.TrimRight(head, "\r\n") + "\r\n\r\n")); merr == nil {
				sum.From = plainLine(Address(m.Header.Get("From")), 120)
				sum.Subject = plainLine(decodeHeader(m.Header.Get("Subject")), 140)
			}
			if sum.UID != 0 {
				list = append(list, sum)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].UID > list[j].UID })
		return nil
	})
	return list, total, err
}

// Read fetches one message without marking it as read.
func (s *Service) Read(ctx context.Context, folder string, uid uint32) (*Message, error) {
	var msg *Message
	err := s.session(ctx, func(c *client) error {
		if err := s.examine(c, folder); err != nil {
			return err
		}
		res, err := c.run("UID FETCH", raw(strconv.FormatUint(uint64(uid), 10)),
			raw(fmt.Sprintf("(UID FLAGS INTERNALDATE RFC822.SIZE BODY.PEEK[]<0.%d>)", maxFetchLen)))
		if err != nil {
			return err
		}
		for _, f := range res.fetches {
			if num(f["UID"]) != uid {
				continue
			}
			m, perr := parseMessage([]byte(str1(f["BODY"])), int(num(f["RFC822.SIZE"])) > maxFetchLen)
			if perr != nil {
				return perr
			}
			m.UID = uid
			if t := parseInternalDate(str1(f["INTERNALDATE"]), s.zone()); !t.IsZero() {
				m.Date = t.Format("Mon 2 Jan 2006 15:04 MST")
			}
			msg = m
			return nil
		}
		return fmt.Errorf("there is no message with id %d in that folder", uid)
	})
	return msg, err
}
