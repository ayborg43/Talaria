package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"talaria/internal/calendar"
	"talaria/internal/config"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const feedAddr = "https://calendar.google.com/calendar/ical/me%40example.com/private-0a1b2c3d4e5f6a7b/basic.ics"

func TestCalendarsComeFromTheConfigAndFromOneEnvironmentVariable(t *testing.T) {
	cfg := &config.Config{Calendars: map[string]config.Calendar{
		"family": {URLEnv: "FAMILY_ICS"},
		"work":   {URLEnv: "WORK_ICS", AllowPrivate: true},
		"gone":   {URLEnv: "NOT_SET"},
	}}
	src, problems := calendarSources(cfg, envOf(map[string]string{
		"FAMILY_ICS":        "webcal://example.com/family.ics",
		"WORK_ICS":          "https://nc.lan/work.ics",
		"TALARIA_CALENDARS": "personal=" + feedAddr + ", church=https://example.org/c.ics\nbad-entry  Upper=https://x.example/a.ics  http=http://plain.example/a.ics",
	}))
	got := map[string]calendar.Source{}
	for _, s := range src {
		got[s.Name] = s
	}
	if len(got) != 5 || got["upper"].URL != "https://x.example/a.ics" || got["family"].URL != "webcal://example.com/family.ics" || !got["work"].AllowPrivate || got["personal"].URL != feedAddr || got["church"].URL != "https://example.org/c.ics" {
		t.Fatalf("%+v", got)
	}
	if got["personal"].AllowPrivate {
		t.Fatal("the variable cannot allow private addresses: only the config can")
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{`"gone": $NOT_SET is not set`, "name=address", `calendar "http"`, "https://"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "private-0a1b2c3d4e5f6a7b") || strings.Contains(joined, "plain.example") {
		t.Fatalf("a problem message shows an address:\n%s", joined)
	}
	// The same name twice: the first wins, and it is said.
	_, problems = calendarSources(&config.Config{Calendars: map[string]config.Calendar{"a": {URLEnv: "A"}}}, envOf(map[string]string{"A": "https://x.example/1.ics", "TALARIA_CALENDARS": "a=https://x.example/2.ics"}))
	if len(problems) != 1 || !strings.Contains(problems[0], "set twice") {
		t.Fatalf("%v", problems)
	}
}

func TestTheServiceIsOnlyBuiltWhenThereIsACalendarAndShowsTheRightZone(t *testing.T) {
	if svc, _ := buildCalendars(&config.Config{}, envOf(nil)); svc != nil {
		t.Fatal("no calendar, no tool")
	}
	svc, problems := buildCalendars(&config.Config{}, envOf(map[string]string{"TALARIA_CALENDARS": "p=" + feedAddr, "TALARIA_TIMEZONE": "Africa/Lagos"}))
	if svc == nil || len(problems) != 0 || svc.Display.String() != "Africa/Lagos" || len(svc.Sources) != 1 {
		t.Fatalf("%+v %v", svc, problems)
	}
	svc, _ = buildCalendars(&config.Config{Timezone: "Asia/Tokyo"}, envOf(map[string]string{"TALARIA_CALENDARS": "p=" + feedAddr, "TALARIA_TIMEZONE": "Africa/Lagos"}))
	if svc.Display.String() != "Asia/Tokyo" {
		t.Fatalf("the config wins: %v", svc.Display)
	}
	svc, problems = buildCalendars(&config.Config{}, envOf(map[string]string{"TALARIA_CALENDARS": "p=" + feedAddr, "TALARIA_TIMEZONE": "Mars/Olympus"}))
	if svc.Display != time.UTC || len(problems) != 1 || !strings.Contains(problems[0], "Mars/Olympus") {
		t.Fatalf("a bad zone falls back to UTC and says so: %v %v", svc.Display, problems)
	}
	if svc, _ = buildCalendars(&config.Config{}, envOf(map[string]string{"TALARIA_CALENDARS": "p=" + feedAddr})); svc.Display != time.UTC {
		t.Fatal("UTC by default")
	}
}

func TestACalendarAddressIsASecretKeptFromShellsAndScrubbedFromOutput(t *testing.T) {
	cfg := &config.Config{Calendars: map[string]config.Calendar{"family": {URLEnv: "FAMILY_ICS"}}}
	get := envOf(map[string]string{"FAMILY_ICS": "https://example.com/ical/private-9f8e7d6c5b4a/family.ics", "TALARIA_CALENDARS": "p=" + feedAddr})
	f := buildFence(cfg, "/h/config.json", "/h")
	deny := map[string]bool{}
	for _, n := range f.EnvDeny {
		deny[n] = true
	}
	if !deny["TALARIA_CALENDARS"] || !deny["FAMILY_ICS"] {
		t.Fatalf("the variables holding calendar addresses reach the agent's shell: %v", f.EnvDeny)
	}
	secrets := collectSecrets(cfg, get, nil)
	has := func(want string) bool {
		for _, s := range secrets {
			if s == want {
				return true
			}
		}
		return false
	}
	// Each is scrubbed on its own, so the private part is caught even where the rest of the address is not shown.
	for _, want := range []string{
		feedAddr, "calendar/ical/me%40example.com/private-0a1b2c3d4e5f6a7b/basic.ics", "calendar/ical/me@example.com/private-0a1b2c3d4e5f6a7b/basic.ics",
		"https://example.com/ical/private-9f8e7d6c5b4a/family.ics", "ical/private-9f8e7d6c5b4a/family.ics",
	} {
		if !has(want) {
			t.Errorf("%q is not among the secrets that are scrubbed: %q", want, secrets)
		}
	}
}

func TestTalCalendarPrintsTheAgendaAndSaysWhatIsWrong(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:1\r\nDTSTART:20261022T083000Z\r\nDTEND:20261022T090000Z\r\nSUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	defer srv.Close()
	client := func(calendar.Source) *http.Client { return srv.Client() }

	var out bytes.Buffer
	get := envOf(map[string]string{"TALARIA_CALENDARS": "p=" + srv.URL + "/secret-feed-aaaaaaaa.ics", "TALARIA_TIMEZONE": "Africa/Lagos"})
	// "2026-10-22" as the start, 1 day.
	if err := runCalendarWith(context.Background(), &config.Config{}, get, []string{"2026-10-22", "1"}, &out, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "09:30-10:00  Standup  [p]") || strings.Contains(out.String(), "secret-feed") {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	if err := runCalendar(context.Background(), &config.Config{}, envOf(nil), nil, &out); err == nil || !strings.Contains(err.Error(), "TALARIA_CALENDARS") {
		t.Fatalf("with no calendar it says how to set one up: %v", err)
	}
	out.Reset()
	runCalendarWith(context.Background(), &config.Config{}, envOf(map[string]string{"TALARIA_CALENDARS": "p=http://plain.example/a.ics"}), nil, &out, client)
	if !strings.Contains(out.String(), "problem:") {
		t.Fatalf("%s", out.String())
	}
}

func TestDoctorSaysWhatItFindsAboutCalendars(t *testing.T) {
	out, n := doctorOn(t, `{"profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`, map[string]string{"TALARIA_CALENDARS": "p=" + feedAddr}, nil)
	if !strings.Contains(out, "1 calendar(s), read-only: p") || !strings.Contains(out, "calendar times are shown in UTC") || strings.Contains(out, "private-0a1b2c3d4e5f6a7b") {
		t.Fatalf("%d:\n%s", n, out)
	}
	out, _ = doctorOn(t, `{"calendars":{"x":{"url_env":"MISSING"}},"timezone":"Africa/Lagos","profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`, nil, nil)
	if !strings.Contains(out, "$MISSING is not set") {
		t.Fatalf("%s", out)
	}
	out, _ = doctorOn(t, `{"profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`, nil, nil)
	if strings.Contains(out, "\nCalendars\n") {
		t.Fatal("nothing about calendars when none are set up")
	}
}
