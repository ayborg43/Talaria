package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"talaria/internal/agent"
	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/cron"
	"talaria/internal/goal"
	"talaria/internal/mailbox"
	"talaria/internal/watch"
	"talaria/internal/web"
)

type webSettings struct {
	Listen, Password, PublicURL string
	Secure                      bool // the public address is https, so cookies are for https only and a front proxy is in the way
}

// webConfig reads the web app's settings: TALARIA_WEB_PASSWORD (what signs you in), TALARIA_WEB_LISTEN (where
// to listen, default :8080 once a password is set) and TALARIA_WEB_PUBLIC_URL (the https address people use,
// which phones need to install the app and to get notifications). configured is false when none is set.
func webConfig(getenv func(string) string) (w webSettings, problems []string, configured bool) {
	w.Listen = strings.TrimSpace(getenv("TALARIA_WEB_LISTEN"))
	w.Password = getenv("TALARIA_WEB_PASSWORD")
	w.PublicURL = strings.TrimRight(strings.TrimSpace(getenv("TALARIA_WEB_PUBLIC_URL")), "/")
	if w.Listen == "" && w.Password == "" && w.PublicURL == "" {
		return w, nil, false
	}
	if w.Password == "" {
		problems = append(problems, "TALARIA_WEB_PASSWORD is not set: it is the password you sign in to the web app with")
	} else if err := web.ValidatePassword(w.Password); err != nil {
		problems = append(problems, "TALARIA_WEB_PASSWORD: "+err.Error())
	}
	if w.Listen == "" {
		w.Listen = ":8080"
	}
	if host, port, err := net.SplitHostPort(w.Listen); err != nil {
		problems = append(problems, "TALARIA_WEB_LISTEN must be an address such as :8080")
	} else if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 || strings.ContainsAny(host, " /") {
		problems = append(problems, "TALARIA_WEB_LISTEN must be an address such as :8080")
	}
	if w.PublicURL != "" {
		u, err := url.Parse(w.PublicURL)
		local := err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
		switch {
		case err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "":
			problems = append(problems, "TALARIA_WEB_PUBLIC_URL must be an address such as https://talaria.example.com")
		case u.Scheme != "https" && !local:
			problems = append(problems, "TALARIA_WEB_PUBLIC_URL must start with https:// (phones will not install the app or send notifications over plain http)")
		default:
			w.Secure = u.Scheme == "https"
		}
	}
	return w, problems, true
}

func webSecrets(getenv func(string) string) []string {
	if p := getenv("TALARIA_WEB_PASSWORD"); p != "" {
		return []string{p}
	}
	return nil
}

func webEnvDeny() []string {
	return []string{"TALARIA_WEB_PASSWORD", "TALARIA_WEB_LISTEN", "TALARIA_WEB_PUBLIC_URL"}
}

// pauseState is the switch behind "Pause everything": while it is on, the scheduler claims no job, so goals,
// watches and reminders wait and run when it is turned off. It is kept in a file, so a restart does not
// quietly undo it.
type pauseState struct {
	path string
	mu   sync.Mutex
	on   bool
}

func newPauseState(home string) *pauseState {
	p := &pauseState{path: filepath.Join(home, "paused")}
	_, err := os.Stat(p.path)
	p.on = err == nil
	return p
}

func (p *pauseState) Paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.on
}

func (p *pauseState) SetPaused(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var err error
	if on {
		err = os.WriteFile(p.path, []byte("paused\n"), 0o600)
	} else if err = os.Remove(p.path); errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err == nil {
		p.on = on
	}
	return err
}

// webSources connects the Today and Control screens to what Talaria knows. Nothing here goes through the
// model: it reads the calendar and mailbox directly, and the stores that hold goals, watches and jobs.
func webSources(rt *app.Runtime, zone *time.Location, links func() []web.LinkInfo) web.Sources {
	src := web.Sources{Zone: zone, Links: links}
	if cal := rt.Calendars(); cal != nil {
		src.Agenda = func(ctx context.Context) ([]web.AgendaItem, string, error) {
			loc := cal.Display
			if loc == nil {
				loc = time.UTC
			}
			now := time.Now().In(loc)
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
			events, notes, err := cal.Events(ctx, start, start.AddDate(0, 0, 1), "", "")
			if err != nil {
				return nil, "", err
			}
			out := make([]web.AgendaItem, 0, len(events))
			for _, e := range events {
				out = append(out, web.AgendaItem{Title: tidy(e.Summary, 120, "(no title)"), Start: e.Start, End: e.End, AllDay: e.AllDay, Where: tidy(e.Location, 100, ""), Calendar: e.Calendar})
			}
			return out, strings.Join(notes, " "), nil
		}
	}
	if mb := rt.Mail(); mb != nil {
		src.Mail = func(ctx context.Context) (int, []web.MailItem, error) {
			list, _, err := mb.Search(ctx, mailbox.Query{Days: 3, Limit: 8})
			if err != nil {
				return 0, nil, err
			}
			_, unread, err := mb.Search(ctx, mailbox.Query{Days: 3, Unread: true, Limit: 1})
			if err != nil {
				return 0, nil, err
			}
			out := make([]web.MailItem, 0, len(list))
			for _, m := range list {
				out = append(out, web.MailItem{ID: m.UID, From: m.From, Subject: m.Subject, At: m.Date, Unread: m.Unread})
			}
			return unread, out, nil
		}
	}
	src.Jobs = func() []web.JobInfo { return webJobs(rt, zone) }
	return src
}

func tidy(s string, n int, none string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return none
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// webJobs lists what Talaria does on its own for the web chat: goals, watches and reminders.
func webJobs(rt *app.Runtime, zone *time.Location) []web.JobInfo {
	var out []web.JobInfo
	if gs := rt.Goals(); gs != nil {
		goals, _ := gs.List("web", web.ChatID)
		for _, g := range goals {
			if g.Status != goal.Open && g.Status != goal.Paused {
				continue
			}
			done := 0
			for _, s := range g.Plan {
				if s.Done {
					done++
				}
			}
			sub := "Checks in every " + g.Every.String()
			if len(g.Plan) > 0 {
				sub = fmt.Sprintf("Step %d of %d", done+1, len(g.Plan))
			}
			if next, ok := rt.Cron.NextCheckin(g.ID); ok && g.Status == goal.Open {
				sub += " · next check-in " + next.In(zone).Format("15:04")
			}
			state := "Active"
			if g.Status == goal.Paused {
				state = "Paused"
			}
			out = append(out, web.JobInfo{Kind: "GOAL", Title: tidy(g.Title, 80, "(goal)"), Sub: sub, State: state})
		}
	}
	if ws := rt.Watches(); ws != nil {
		watches, _ := ws.List("web", web.ChatID)
		for _, w := range watches {
			state := "Quiet"
			switch {
			case w.Status == watch.Paused:
				state = "Paused"
			case !w.LastChange.IsZero() && time.Since(w.LastChange) < 24*time.Hour && w.Polls > 1:
				state = "Fired"
			}
			out = append(out, web.JobInfo{Kind: "WATCH", Title: tidy(w.Title, 80, "(watch)"), Sub: "Checks every " + w.Every.String(), State: state})
		}
	}
	if jobs, err := rt.Cron.List(); err == nil {
		slices.SortFunc(jobs, func(a, b cron.Job) int { return a.NextRun.Compare(b.NextRun) })
		for _, j := range jobs {
			if j.Gateway != "web" || j.ChatID != web.ChatID || j.Kind != "" {
				continue
			}
			out = append(out, web.JobInfo{Kind: "TIME", Title: tidy(j.Name, 80, "(reminder)"), Sub: "Next " + j.NextRun.In(zone).Format("Mon 2 Jan 15:04"), State: "Waiting"})
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// webLinks says what Talaria is connected to, for the Control screen.
func webLinks(cfg *config.Config, rt *app.Runtime, model string, getenv func(string) string, publicURL string) func() []web.LinkInfo {
	return func() []web.LinkInfo {
		var out []web.LinkInfo
		if cfg.TelegramToken(getenv) != "" {
			out = append(out, web.LinkInfo{Name: "Telegram", Detail: "bot connected", Tag: "CHAT"})
		}
		if cal := rt.Calendars(); cal != nil {
			names := make([]string, 0, len(cal.Sources))
			for _, s := range cal.Sources {
				names = append(names, s.Name)
			}
			out = append(out, web.LinkInfo{Name: "Calendar", Detail: strings.Join(names, ", "), Tag: "READ-ONLY"})
		}
		if mb := rt.Mail(); mb != nil {
			out = append(out, web.LinkInfo{Name: "Email", Detail: mb.Server, Tag: "READ-ONLY"})
		}
		out = append(out, web.LinkInfo{Name: "Model", Detail: model, Tag: "MODEL"})
		return out
	}
}

// askMode is which calls ask the user first: TALARIA_PERMISSIONS (deletes_and_posts or strict) if set, else the config's.
func askMode(cfg *config.Config, getenv func(string) string) agent.AskMode {
	switch strings.TrimSpace(strings.ToLower(getenv("TALARIA_PERMISSIONS"))) {
	case config.PermissionsStrict:
		return agent.AskRisky
	case config.PermissionsDeletesAndPosts:
		return agent.AskDestructive
	}
	return cfg.Policy.AskMode()
}
