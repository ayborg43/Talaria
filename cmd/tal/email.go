package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"talaria/internal/config"
	"talaria/internal/mailbox"
)

type mailSettings struct {
	Address, User, Password, Server string
}

// microsoft reports whether the server is Microsoft's, which has mostly turned off password sign-in.
func (m mailSettings) microsoft() bool { return strings.HasSuffix(m.Server, "office365.com:993") }

// mailConfig reads the mailbox from TALARIA_EMAIL (the address), TALARIA_EMAIL_PASSWORD (an app password),
// and optionally TALARIA_EMAIL_SERVER (host or host:port, found from the address for common providers) and
// TALARIA_EMAIL_USER (if the sign-in name is not the address). configured is false when none is set.
// mailConfig reads the mailbox from what Settings saved and the environment:
// what Settings saved wins, so it takes effect, and the environment fills in
// whatever is left empty. configured is false when no mailbox is set up.
func mailConfig(saved config.Email, getenv func(string) string) (m mailSettings, problems []string, configured bool) {
	addr, user, pw, server := mailbox.Merge(saved.Address, saved.User, saved.Password, saved.PasswordEnv, saved.Server, getenv)
	raddr, rpw, rserver, problems, configured := mailbox.Resolve(addr, pw, server)
	if !configured {
		return m, nil, false
	}
	m.Address, m.Password, m.Server = raddr, rpw, rserver
	m.User = user
	return m, problems, true
}

// buildMailbox makes the mail service, or nil when no mailbox is set up or the settings are wrong.
func buildMailbox(cfg *config.Config, getenv func(string) string) (*mailbox.Service, []string) {
	m, problems, configured := mailConfig(cfg.Email, getenv)
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
