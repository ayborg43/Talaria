package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"talaria/internal/config"
	"talaria/internal/mailbox"
)

func TestTheMailboxIsFoundFromTheAddressForCommonProviders(t *testing.T) {
	for addr, server := range map[string]string{
		"me@gmail.com": "imap.gmail.com:993", "me@googlemail.com": "imap.gmail.com:993", "me@icloud.com": "imap.mail.me.com:993",
		"me@fastmail.com": "imap.fastmail.com:993", "me@outlook.com": "outlook.office365.com:993", "ME@GMAIL.COM": "",
	} {
		m, problems, configured := mailConfig(config.Email{}, envOf(map[string]string{"TALARIA_EMAIL": addr, "TALARIA_EMAIL_PASSWORD": "pw"}))
		if addr == "ME@GMAIL.COM" { // an address in capitals is still an address
			if !configured || len(problems) != 0 || m.Server != "imap.gmail.com:993" {
				t.Fatalf("%q: %+v %v", addr, m, problems)
			}
			continue
		}
		if !configured || len(problems) != 0 || m.Server != server || m.Address != addr {
			t.Fatalf("%q: %+v %v", addr, m, problems)
		}
	}
	// Nothing set is not a mistake: there is just no mailbox.
	if _, problems, configured := mailConfig(config.Email{}, envOf(nil)); configured || len(problems) != 0 {
		t.Fatalf("%v %v", configured, problems)
	}
}

func TestMailSettingsThatAreWrongAreNamedAndLeaveMailOff(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TALARIA_EMAIL_PASSWORD": "pw"}, "no address is set"},
		{map[string]string{"TALARIA_EMAIL": "me@gmail.com"}, "no app password is set"},
		{map[string]string{"TALARIA_EMAIL": "Me <me@gmail.com>", "TALARIA_EMAIL_PASSWORD": "pw"}, "not an email address"},
		{map[string]string{"TALARIA_EMAIL": "me@mydomain.example", "TALARIA_EMAIL_PASSWORD": "pw"}, "a server is needed for mydomain.example"},
		{map[string]string{"TALARIA_EMAIL": "me@x.example", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_EMAIL_SERVER": "imap://evil.example"}, "must be a server name"},
		{map[string]string{"TALARIA_EMAIL": "me@x.example", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_EMAIL_SERVER": "imap.x.example/path"}, "must be a server name"},
		{map[string]string{"TALARIA_EMAIL": "me@x.example", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_EMAIL_SERVER": "imap.x.example:0"}, "must be a server name"},
		{map[string]string{"TALARIA_EMAIL": "me@x.example", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_EMAIL_SERVER": "imap.x.example:abc"}, "must be a server name"},
		{map[string]string{"TALARIA_EMAIL": "me@x.example", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_EMAIL_SERVER": "bad host"}, "must be a server name"},
	}
	for _, c := range cases {
		svc, problems := buildMailbox(&config.Config{}, envOf(c.env))
		if svc != nil || len(problems) == 0 || !strings.Contains(strings.Join(problems, "|"), c.want) {
			t.Errorf("%v: want %q, got %v (service %v)", c.env, c.want, problems, svc != nil)
		}
	}
}

func TestAnOwnServerPortAndSignInNameAreUsedAndGooglePasswordSpacesDropped(t *testing.T) {
	m, problems, _ := mailConfig(config.Email{}, envOf(map[string]string{"TALARIA_EMAIL": "me@corp.example", "TALARIA_EMAIL_PASSWORD": "pw with spaces", "TALARIA_EMAIL_SERVER": "mail.corp.example:1993", "TALARIA_EMAIL_USER": "me-login"}))
	if len(problems) != 0 || m.Server != "mail.corp.example:1993" || m.User != "me-login" || m.Password != "pw with spaces" {
		t.Fatalf("%+v %v", m, problems)
	}
	g, _, _ := mailConfig(config.Email{}, envOf(map[string]string{"TALARIA_EMAIL": "me@gmail.com", "TALARIA_EMAIL_PASSWORD": "abcd efgh ijkl mnop"}))
	if g.Password != "abcdefghijklmnop" {
		t.Fatalf("%q", g.Password)
	}
}

func TestWhatSettingsSavedWinsOverTheEnvironment(t *testing.T) {
	m, problems, configured := mailConfig(
		config.Email{Address: "saved@example.com", Password: "saved-pw"},
		envOf(map[string]string{"TALARIA_EMAIL": "env@example.com", "TALARIA_EMAIL_PASSWORD": "env-pw", "TALARIA_EMAIL_SERVER": "mail.env.example:993"}),
	)
	if !configured || len(problems) != 0 || m.Address != "saved@example.com" || m.Password != "saved-pw" {
		t.Fatalf("%+v %v", m, problems)
	}
	m, _, _ = mailConfig(
		config.Email{Password: "saved-pw"},
		envOf(map[string]string{"TALARIA_EMAIL": "env@example.com"}),
	)
	if m.Address != "env@example.com" || m.Password != "saved-pw" {
		t.Fatalf("what is left empty falls back to the environment: %+v", m)
	}
}

func TestTheServiceGetsTheSettingsAndTheZone(t *testing.T) {
	svc, problems := buildMailbox(&config.Config{}, envOf(map[string]string{"TALARIA_EMAIL": "me@gmail.com", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_TIMEZONE": "Africa/Lagos"}))
	if svc == nil || len(problems) != 0 || svc.Address != "me@gmail.com" || svc.Server != "imap.gmail.com:993" || svc.Display.String() != "Africa/Lagos" {
		t.Fatalf("%+v %v", svc, problems)
	}
}

func TestTheAppPasswordIsKeptFromShellsAndScrubbedFromOutput(t *testing.T) {
	cfg := &config.Config{}
	get := envOf(map[string]string{"TALARIA_EMAIL": "me@gmail.com", "TALARIA_EMAIL_PASSWORD": "abcd efgh ijkl mnop"})
	f := buildFence(cfg, "/h/config.json", "/h")
	deny := map[string]bool{}
	for _, n := range f.EnvDeny {
		deny[n] = true
	}
	for _, n := range []string{"TALARIA_EMAIL", "TALARIA_EMAIL_PASSWORD", "TALARIA_EMAIL_SERVER", "TALARIA_EMAIL_USER"} {
		if !deny[n] {
			t.Fatalf("%s reaches the agent's shell: %v", n, f.EnvDeny)
		}
	}
	have := map[string]bool{}
	for _, s := range collectSecrets(cfg, get, nil) {
		have[s] = true
	}
	// Both the way Google shows it and the way it is sent.
	if !have["abcd efgh ijkl mnop"] || !have["abcdefghijklmnop"] {
		t.Fatalf("the app password is not scrubbed: %v", have)
	}
}

func TestDoctorSaysWhatItFindsAboutEmail(t *testing.T) {
	prov := `{"profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`
	out, n := doctorOn(t, prov, map[string]string{"TALARIA_EMAIL": "me@gmail.com", "TALARIA_EMAIL_PASSWORD": "supersecretpw"}, nil)
	if !strings.Contains(out, "mailbox me@gmail.com, read-only, over an encrypted connection to imap.gmail.com:993") || strings.Contains(out, "supersecretpw") {
		t.Fatalf("%d:\n%s", n, out)
	}
	out, _ = doctorOn(t, prov, map[string]string{"TALARIA_EMAIL": "me@outlook.com", "TALARIA_EMAIL_PASSWORD": "pw"}, nil)
	if !strings.Contains(out, "OAuth") {
		t.Fatalf("a Microsoft mailbox is not warned about:\n%s", out)
	}
	out, _ = doctorOn(t, prov, map[string]string{"TALARIA_EMAIL": "me@gmail.com"}, nil)
	if !strings.Contains(out, "no app password is set") {
		t.Fatalf("%s", out)
	}
	out, _ = doctorOn(t, prov, nil, nil)
	if strings.Contains(out, "\nEmail\n") {
		t.Fatal("nothing about email when none is set up")
	}
}

// ---- tal email, against a small mail server ----

type tinyIMAP struct {
	mu   sync.Mutex
	cmds []string
	ln   net.Listener
}

func startTinyIMAP(t *testing.T) (*tinyIMAP, func(*mailbox.Service)) {
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
	s := &tinyIMAP{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	return s, func(svc *mailbox.Service) { svc.Server = ln.Addr().String(); svc.TLS = &tls.Config{RootCAs: pool} }
}

const tinyMsg = "From: Amina <amina@acme.example>\r\nSubject: Hello there\r\n\r\nThe full text.\r\n"

func (s *tinyIMAP) handle(conn net.Conn) {
	defer conn.Close()
	r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
	w.WriteString("* OK ready\r\n")
	w.Flush()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.mu.Lock()
		s.cmds = append(s.cmds, line)
		s.mu.Unlock()
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		tag, verb := f[0], strings.ToUpper(f[1])
		switch {
		case verb == "CAPABILITY":
			fmt.Fprintf(w, "* CAPABILITY IMAP4rev1\r\n%s OK ok\r\n", tag)
		case verb == "LOGIN":
			fmt.Fprintf(w, "%s OK ok\r\n", tag)
		case verb == "LIST":
			fmt.Fprintf(w, "* LIST (\\HasNoChildren) \"/\" INBOX\r\n* LIST (\\HasNoChildren) \"/\" Sent\r\n%s OK ok\r\n", tag)
		case verb == "EXAMINE":
			fmt.Fprintf(w, "* 1 EXISTS\r\n%s OK [READ-ONLY] ok\r\n", tag)
		case verb == "UID" && strings.EqualFold(f[2], "SEARCH"):
			fmt.Fprintf(w, "* SEARCH 7\r\n%s OK ok\r\n", tag)
		case verb == "UID" && strings.Contains(line, "HEADER.FIELDS"):
			h := "From: Amina <amina@acme.example>\r\nSubject: Hello there\r\n\r\n"
			fmt.Fprintf(w, "* 1 FETCH (UID 7 FLAGS () INTERNALDATE \"22-Oct-2026 08:30:00 +0000\" RFC822.SIZE %d BODY[HEADER.FIELDS (FROM SUBJECT)] {%d}\r\n%s)\r\n%s OK ok\r\n", len(tinyMsg), len(h), h, tag)
		case verb == "UID":
			fmt.Fprintf(w, "* 1 FETCH (UID 7 FLAGS () INTERNALDATE \"22-Oct-2026 08:30:00 +0000\" RFC822.SIZE %d BODY[]<0> {%d}\r\n%s)\r\n%s OK ok\r\n", len(tinyMsg), len(tinyMsg), tinyMsg, tag)
		case verb == "LOGOUT":
			fmt.Fprintf(w, "* BYE\r\n%s OK ok\r\n", tag)
			w.Flush()
			return
		default:
			fmt.Fprintf(w, "%s BAD no\r\n", tag)
		}
		w.Flush()
	}
}

func TestTalEmailListsReadsAndShowsFolders(t *testing.T) {
	srv, adjust := startTinyIMAP(t)
	env := envOf(map[string]string{"TALARIA_EMAIL": "me@gmail.com", "TALARIA_EMAIL_PASSWORD": "pw", "TALARIA_TIMEZONE": "Africa/Lagos"})
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runEmailWith(context.Background(), &config.Config{}, env, args, &out, adjust)
		return out.String(), err
	}
	out, err := run()
	if err != nil || !strings.Contains(out, "id 7 *") || !strings.Contains(out, "Amina <amina@acme.example>") || !strings.Contains(out, "Hello there") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err = run("30", "unread"); err != nil || !strings.Contains(out, "the last 30 day(s), unread only") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err = run("read", "7"); err != nil || !strings.Contains(out, "The full text.") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err = run("folders"); err != nil || !strings.Contains(out, "INBOX") || !strings.Contains(out, "Sent") {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, bad := range [][]string{{"read"}, {"read", "x"}, {"delete", "7"}, {"send"}} {
		if _, err := run(bad...); err == nil || !strings.Contains(err.Error(), "usage: tal email") {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	// Whatever was asked, only reading commands reached the server.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, c := range srv.cmds {
		v := strings.ToUpper(strings.Fields(c)[1])
		if v == "STORE" || v == "SELECT" || v == "DELETE" || v == "EXPUNGE" || v == "APPEND" || v == "COPY" {
			t.Fatalf("a command that changes mail: %s", c)
		}
	}
}

func TestTalEmailSaysWhatIsWrongWhenThereIsNoMailbox(t *testing.T) {
	var out bytes.Buffer
	err := runEmail(context.Background(), &config.Config{}, envOf(nil), nil, &out)
	if err == nil || !strings.Contains(err.Error(), "no mailbox is set up") {
		t.Fatalf("%v", err)
	}
	out.Reset()
	err = runEmail(context.Background(), &config.Config{}, envOf(map[string]string{"TALARIA_EMAIL": "me@x.example", "TALARIA_EMAIL_PASSWORD": "pw"}), nil, &out)
	if err == nil || !strings.Contains(out.String(), "problem: a server is needed for x.example") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
