package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strconv"
	"strings"

	"talaria/internal/config"
	"talaria/internal/mailbox"
)

// imapHosts are the mail servers of the usual providers, found from the domain of the address, so that
// most people set only an address and an app password.
var imapHosts = map[string]string{
	"gmail.com": "imap.gmail.com", "googlemail.com": "imap.gmail.com",
	"icloud.com": "imap.mail.me.com", "me.com": "imap.mail.me.com", "mac.com": "imap.mail.me.com",
	"fastmail.com": "imap.fastmail.com", "fastmail.fm": "imap.fastmail.com",
	"yahoo.com": "imap.mail.yahoo.com", "aol.com": "imap.aol.com", "zoho.com": "imap.zoho.com",
	"outlook.com": "outlook.office365.com", "hotmail.com": "outlook.office365.com", "live.com": "outlook.office365.com",
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

type mailSettings struct {
	Address, User, Password, Server string
}

// microsoft reports whether the server is Microsoft's, which has mostly turned off password sign-in.
func (m mailSettings) microsoft() bool { return strings.HasSuffix(m.Server, "office365.com:993") }

// mailConfig reads the mailbox from TALARIA_EMAIL (the address), TALARIA_EMAIL_PASSWORD (an app password),
// and optionally TALARIA_EMAIL_SERVER (host or host:port, found from the address for common providers) and
// TALARIA_EMAIL_USER (if the sign-in name is not the address). configured is false when none is set.
func mailConfig(getenv func(string) string) (m mailSettings, problems []string, configured bool) {
	addr := strings.TrimSpace(getenv("TALARIA_EMAIL"))
	m.Password = strings.TrimSpace(getenv("TALARIA_EMAIL_PASSWORD"))
	server := strings.TrimSpace(getenv("TALARIA_EMAIL_SERVER"))
	m.User = strings.TrimSpace(getenv("TALARIA_EMAIL_USER"))
	if addr == "" && m.Password == "" && server == "" && m.User == "" {
		return m, nil, false
	}
	if addr == "" {
		problems = append(problems, "TALARIA_EMAIL is not set: it is the address of the mailbox to read")
	} else if a, err := mail.ParseAddress(addr); err != nil || a.Address != addr {
		problems = append(problems, "TALARIA_EMAIL is not an email address: write it as you@example.com")
	} else {
		m.Address = addr
	}
	if m.Password == "" {
		problems = append(problems, "TALARIA_EMAIL_PASSWORD is not set: it is an app password for the mailbox (not your normal password)")
	}
	if server == "" && m.Address != "" {
		domain := strings.ToLower(m.Address[strings.LastIndex(m.Address, "@")+1:])
		server = imapHosts[domain]
		if server == "" {
			problems = append(problems, fmt.Sprintf("TALARIA_EMAIL_SERVER is needed for %s: it is the IMAP server of your provider (such as imap.example.com)", domain))
		}
	}
	if server != "" {
		host, port := server, "993"
		if h, p, ok := strings.Cut(server, ":"); ok {
			host, port = h, p
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || !hostName.MatchString(host) {
			problems = append(problems, "TALARIA_EMAIL_SERVER must be a server name such as imap.example.com, with an optional :port (the connection is always encrypted)")
		} else {
			m.Server = host + ":" + port
		}
	}
	// Google shows app passwords in groups of four with spaces, and accepts them without.
	if strings.HasPrefix(m.Server, "imap.gmail.com:") {
		m.Password = strings.Join(strings.Fields(m.Password), "")
	}
	return m, problems, true
}

// buildMailbox makes the mail service, or nil when no mailbox is set up or the settings are wrong.
func buildMailbox(cfg *config.Config, getenv func(string) string) (*mailbox.Service, []string) {
	m, problems, configured := mailConfig(getenv)
	if !configured {
		return nil, nil
	}
	loc, zp := calendarZone(cfg, getenv)
	problems = append(problems, zp...)
	if len(problems) > len(zp) {
		return nil, problems
	}
	return &mailbox.Service{Address: m.Address, User: m.User, Password: m.Password, Server: m.Server, Display: loc}, problems
}

// mailSecrets is the app password, so it can be kept out of anything the model sees or that is stored.
func mailSecrets(getenv func(string) string) []string {
	out := []string{}
	if p := strings.TrimSpace(getenv("TALARIA_EMAIL_PASSWORD")); p != "" {
		out = append(out, p, strings.Join(strings.Fields(p), ""))
	}
	return out
}

// mailEnvDeny names the variables that hold the mailbox's sign-in, so a shell command is not given them.
func mailEnvDeny() []string {
	return []string{"TALARIA_EMAIL", "TALARIA_EMAIL_PASSWORD", "TALARIA_EMAIL_SERVER", "TALARIA_EMAIL_USER"}
}

const emailUsage = "usage: tal email [days] [unread] | tal email folders | tal email read <id>"

// runEmail is `tal email`: list recent mail, or read one message, to check that the mailbox signs in and
// reads correctly. It needs no model and no key, and like the tools it only reads.
func runEmail(ctx context.Context, cfg *config.Config, getenv func(string) string, args []string, out io.Writer) error {
	return runEmailWith(ctx, cfg, getenv, args, out, nil)
}

// runEmailWith is runEmail with the service adjustable, for tests.
func runEmailWith(ctx context.Context, cfg *config.Config, getenv func(string) string, args []string, out io.Writer, adjust func(*mailbox.Service)) error {
	svc, problems := buildMailbox(cfg, getenv)
	for _, p := range problems {
		fmt.Fprintln(out, "problem:", p)
	}
	if svc == nil {
		return errors.New("no mailbox is set up: set TALARIA_EMAIL to its address and TALARIA_EMAIL_PASSWORD to an app password for it")
	}
	if adjust != nil {
		adjust(svc)
	}
	var text string
	var err error
	switch {
	case len(args) > 0 && args[0] == "folders":
		text, err = svc.FolderList(ctx)
	case len(args) > 0 && args[0] == "read":
		if len(args) != 2 {
			return errors.New(emailUsage)
		}
		id, perr := strconv.ParseInt(args[1], 10, 64)
		if perr != nil {
			return errors.New(emailUsage)
		}
		text, err = svc.Show(ctx, "", id)
	default:
		q := mailbox.Query{}
		for _, a := range args {
			if a == "unread" {
				q.Unread = true
			} else if n, perr := strconv.Atoi(a); perr == nil {
				q.Days = n
			} else {
				return errors.New(emailUsage)
			}
		}
		text, err = svc.List(ctx, q)
	}
	if err != nil {
		return err
	}
	fmt.Fprint(out, text)
	return nil
}
