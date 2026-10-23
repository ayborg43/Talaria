package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"talaria/internal/calendar"
	"talaria/internal/config"
)

var calendarEnvName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// calendarSources works out the calendars to read: those in the config, whose addresses are in the
// environment variables they name, and any given as name=address pairs in TALARIA_CALENDARS (separated
// by spaces, commas or new lines). A calendar whose address is missing or unusable is left out, with a
// problem to report, rather than stopping Talaria.
func calendarSources(cfg *config.Config, getenv func(string) string) (sources []calendar.Source, problems []string) {
	seen := map[string]bool{}
	add := func(name, raw string, private bool, from string) {
		if seen[name] {
			problems = append(problems, fmt.Sprintf("calendar %q is set twice: the first is used", name))
			return
		}
		if _, err := calendar.NormalizeURL(raw); err != nil {
			problems = append(problems, fmt.Sprintf("calendar %q (%s): %v", name, from, err))
			return
		}
		seen[name] = true
		sources = append(sources, calendar.Source{Name: name, URL: raw, AllowPrivate: private})
	}
	names := make([]string, 0, len(cfg.Calendars))
	for n := range cfg.Calendars {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		c := cfg.Calendars[n]
		if addr := strings.TrimSpace(c.URL); addr != "" {
			add(n, addr, c.AllowPrivate, "the config file")
			continue
		}
		raw := strings.TrimSpace(getenv(c.URLEnv))
		if raw == "" {
			problems = append(problems, fmt.Sprintf("calendar %q: $%s is not set, so it is left out", n, c.URLEnv))
			continue
		}
		add(n, raw, c.AllowPrivate, "$"+c.URLEnv)
	}
	for _, pair := range regexp.MustCompile(`[\s,]+`).Split(strings.TrimSpace(getenv("TALARIA_CALENDARS")), -1) {
		if pair == "" {
			continue
		}
		name, raw, ok := strings.Cut(pair, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || !calendarEnvName.MatchString(name) {
			problems = append(problems, "TALARIA_CALENDARS: each entry must be name=address, with a name of lower-case letters, digits, - or _")
			continue
		}
		add(name, strings.TrimSpace(raw), false, "TALARIA_CALENDARS")
	}
	return sources, problems
}

// calendarZone is where calendar times are shown: the config's timezone, else TALARIA_TIMEZONE, else UTC.
func calendarZone(cfg *config.Config, getenv func(string) string) (*time.Location, []string) {
	name := cfg.Timezone
	if name == "" {
		name = strings.TrimSpace(getenv("TALARIA_TIMEZONE"))
	}
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, []string{fmt.Sprintf("timezone %q is not a time zone name (such as Africa/Lagos): calendar times are shown in UTC", name)}
	}
	return loc, nil
}

// buildCalendars makes the calendar service: the calendars read from elsewhere,
// plus the built-in one, which is always there.
func buildCalendars(cfg *config.Config, getenv func(string) string, home string) (*calendar.Service, []string) {
	sources, problems := calendarSources(cfg, getenv)
	loc, zp := calendarZone(cfg, getenv)
	problems = append(problems, zp...)
	return &calendar.Service{
		Sources: sources,
		Local:   &calendar.Store{Path: filepath.Join(home, "calendar.json")},
		Display: loc,
	}, problems
}

// calendarSecrets are the feed addresses, and the part of each that makes it private, so they can be kept
// out of anything the model sees or that is stored.
func calendarSecrets(cfg *config.Config, getenv func(string) string) []string {
	sources, _ := calendarSources(cfg, getenv)
	var out []string
	for _, s := range sources {
		out = append(out, s.URL)
		if u, err := url.Parse(s.URL); err == nil {
			// The path as written and as decoded (%40 and @): whichever form turns up is scrubbed.
			for _, p := range []string{strings.Trim(u.EscapedPath(), "/"), strings.Trim(u.Path, "/")} {
				if len(p) >= 8 {
					out = append(out, p)
				}
			}
			if len(u.RawQuery) >= 8 {
				out = append(out, u.RawQuery)
			}
		}
	}
	return out
}

// calendarEnvDeny names the variables that hold feed addresses, so a shell command is not given them.
func calendarEnvDeny(cfg *config.Config) []string {
	out := []string{"TALARIA_CALENDARS"}
	for _, c := range cfg.Calendars {
		if c.URLEnv != "" {
			out = append(out, c.URLEnv)
		}
	}
	return out
}

// runCalendar is `tal calendar [days] [from]`: print the agenda, to check that a calendar is read correctly.
// It needs no model and no key.
func runCalendar(ctx context.Context, cfg *config.Config, getenv func(string) string, home string, args []string, out io.Writer) error {
	return runCalendarWith(ctx, cfg, getenv, home, args, out, nil)
}

// runCalendarWith is runCalendar with the HTTP client for feeds replaceable, for tests.
func runCalendarWith(ctx context.Context, cfg *config.Config, getenv func(string) string, home string, args []string, out io.Writer, client func(calendar.Source) *http.Client) error {
	svc, problems := buildCalendars(cfg, getenv, home)
	if client != nil {
		svc.Client = client
	}
	for _, p := range problems {
		fmt.Fprintln(out, "problem:", p)
	}
	days, from := 7, "today"
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil {
			days = n
		} else {
			from = a
		}
	}
	text, err := svc.Agenda(ctx, from, days, "", "")
	if err != nil {
		return err
	}
	fmt.Fprint(out, text)
	return nil
}
