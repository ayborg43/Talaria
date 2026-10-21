package cron

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func TestCronTools(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	reg := tool.NewRegistry()
	RegisterTools(reg, s, "", 7, Targets{})
	mine := func(name, args string) (string, error) {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		return tl.Run(context.Background(), json.RawMessage(args))
	}

	if tl, _ := reg.Get("cron_add"); !tl.NeedsApproval {
		t.Fatal("cron_add must require approval: it schedules unattended runs")
	}

	// Recurring schedule.
	out, err := mine("cron_add", `{"name":"standup","schedule":"0 9 * * mon-fri","prompt":"post standup notes"}`)
	if err != nil || !strings.Contains(out, "standup") {
		t.Fatalf("add: %q %v", out, err)
	}
	// One-off reminder via delay.
	if _, err := mine("cron_add", `{"delay":"90m","prompt":"stretch","allow_risky":true}`); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	if len(jobs) != 2 || jobs[0].ChatID != 7 || jobs[0].AllowRisky || !jobs[1].AllowRisky {
		t.Fatalf("stored jobs: %+v", jobs)
	}
	if jobs[1].Schedule != "@at 2026-10-20T11:30:00Z" || !jobs[1].NextRun.Equal(utc("2026-10-20 11:30")) {
		t.Fatalf("delay not converted to an absolute time: %+v", jobs[1])
	}

	// Argument errors.
	for name, args := range map[string]string{
		"neither":      `{"prompt":"p"}`,
		"both":         `{"schedule":"@daily","delay":"1h","prompt":"p"}`,
		"short delay":  `{"delay":"10s","prompt":"p"}`,
		"bad delay":    `{"delay":"soon","prompt":"p"}`,
		"bad schedule": `{"schedule":"every day","prompt":"p"}`,
		"no prompt":    `{"schedule":"@daily"}`,
	} {
		if _, err := mine("cron_add", args); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}

	// Listing and removal are scoped to the chat.
	other := tool.NewRegistry()
	RegisterTools(other, s, "", 8, Targets{})
	otherRun := func(name, args string) (string, error) {
		tl, _ := other.Get(name)
		return tl.Run(context.Background(), json.RawMessage(args))
	}
	if out, _ := otherRun("cron_list", `{}`); !strings.Contains(out, "no scheduled jobs") {
		t.Fatalf("chat 8 can see chat 7's jobs: %q", out)
	}
	if _, err := otherRun("cron_remove", `{"id":"`+jobs[0].ID+`"}`); err == nil {
		t.Fatal("chat 8 removed chat 7's job")
	}

	list, _ := mine("cron_list", `{}`)
	if !strings.Contains(list, jobs[0].ID) || !strings.Contains(list, "[risky tools allowed]") {
		t.Fatalf("list output: %q", list)
	}
	if _, err := mine("cron_remove", `{"id":"`+jobs[0].ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.List(); len(jobs) != 1 {
		t.Fatalf("remove failed: %+v", jobs)
	}
}

// A chat on the "claude" profile and the "sandbox" environment, with a third of each on offer.
func chatTargets(profile, env *string) Targets {
	return Targets{
		Profiles:           []string{"claude", "local", "openai"},
		Environments:       []string{"local", "sandbox"},
		DefaultProfile:     "openai",
		DefaultEnvironment: "local",
		Current:            func() (string, string) { return *profile, *env },
		CheckProfile: func(name string) error {
			if name == "local" {
				return errors.New("the model server is not reachable")
			}
			return nil
		},
	}
}

func addJob(t *testing.T, r *tool.Registry, args string) (string, error) {
	t.Helper()
	tl, _ := r.Get("cron_add")
	return tl.Run(context.Background(), json.RawMessage(args))
}

func TestJobsDefaultToTheChatsCurrentChoices(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	profile, env := "claude", "sandbox"
	r := tool.NewRegistry()
	RegisterTools(r, s, "", 7, chatTargets(&profile, &env))

	out, err := addJob(t, r, `{"schedule":"@daily","prompt":"report"}`)
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	if jobs[0].Profile != "claude" || jobs[0].Environment != "sandbox" {
		t.Fatalf("a job takes what its chat uses now: %+v", jobs[0])
	}
	if !strings.HasSuffix(out, "\nit runs with the claude model profile and the sandbox environment") {
		t.Fatalf("the model should be told where the job will run: %q", out)
	}

	// The job keeps what it was given. The chat moving on later does not change it.
	profile, env = "local", "local"
	if jobs, _ := s.List(); jobs[0].Profile != "claude" || jobs[0].Environment != "sandbox" {
		t.Fatalf("a created job is not tied to the chat's later choices: %+v", jobs[0])
	}

	// A chat on the defaults gives a job that follows the defaults, stored as "".
	profile, env = "", ""
	out, _ = addJob(t, r, `{"schedule":"@daily","prompt":"on the defaults"}`)
	jobs, _ = s.List()
	if jobs[1].Profile != "" || jobs[1].Environment != "" {
		t.Fatalf("%+v", jobs[1])
	}
	if !strings.HasSuffix(out, "the openai (default) model profile and the local (default) environment") {
		t.Fatalf("a job following the default says so: %q", out)
	}
	// ...and naming the default explicitly is the same as following it.
	profile, env = "claude", "sandbox"
	addJob(t, r, `{"schedule":"@daily","prompt":"x","profile":"openai","environment":"local"}`)
	if jobs, _ := s.List(); jobs[2].Profile != "" || jobs[2].Environment != "" {
		t.Fatalf("naming the default stores 'follow the default': %+v", jobs[2])
	}
}

func TestJobsCanChooseSomewhereElse(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	profile, env := "claude", "sandbox"
	r := tool.NewRegistry()
	RegisterTools(r, s, "", 7, chatTargets(&profile, &env))

	// One setting overridden, the other still taken from the chat.
	if _, err := addJob(t, r, `{"schedule":"@daily","prompt":"p","profile":"openai"}`); err != nil {
		t.Fatal(err)
	}
	// "default" is how to say "the gateway's own, whatever this chat is using".
	if _, err := addJob(t, r, `{"schedule":"@daily","prompt":"p","environment":"default"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := addJob(t, r, `{"schedule":"@daily","prompt":"p","profile":"default","environment":"default"}`); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	got := func(j Job) string { return j.Profile + "/" + j.Environment }
	if got(jobs[0]) != "/sandbox" || got(jobs[1]) != "claude/" || got(jobs[2]) != "/" {
		t.Fatalf("%q %q %q", got(jobs[0]), got(jobs[1]), got(jobs[2]))
	}
}

func TestJobsCannotNameWhatAChatCouldNotUse(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	profile, env := "", ""
	r := tool.NewRegistry()
	RegisterTools(r, s, "", 7, chatTargets(&profile, &env))

	for name, tc := range map[string]struct{ args, want string }{
		"unknown profile":     {`{"schedule":"@daily","prompt":"p","profile":"gpt9"}`, `no model profile named "gpt9" (choose from: claude, local, openai, or default)`},
		"unknown environment": {`{"schedule":"@daily","prompt":"p","environment":"prod"}`, `no environment named "prod" (choose from: local, sandbox, or default)`},
		"unusable profile":    {`{"schedule":"@daily","prompt":"p","profile":"local"}`, `the model profile "local" cannot be used right now: the model server is not reachable`},
	} {
		if _, err := addJob(t, r, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	if jobs, _ := s.List(); len(jobs) != 0 {
		t.Fatalf("a rejected job is not stored: %+v", jobs)
	}

	// A chat whose own choice is no longer on offer gets the default, not a job that will fail.
	profile, env = "retired", "gone"
	addJob(t, r, `{"schedule":"@daily","prompt":"p"}`)
	if jobs, _ := s.List(); jobs[0].Profile != "" || jobs[0].Environment != "" {
		t.Fatalf("%+v", jobs[0])
	}
}

func TestWithNoChoiceJobsRunOnTheDefaultsAsBefore(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	r := tool.NewRegistry()
	RegisterTools(r, s, "", 7, Targets{})

	out, err := addJob(t, r, `{"schedule":"@daily","prompt":"p"}`)
	if err != nil || strings.Contains(out, "it runs with") {
		t.Fatalf("with nothing to choose, nothing is said: %q %v", out, err)
	}
	for _, args := range []string{`{"schedule":"@daily","prompt":"p","profile":"x"}`, `{"schedule":"@daily","prompt":"p","environment":"x"}`} {
		if _, err := addJob(t, r, args); err == nil || !strings.Contains(err.Error(), "there is no choice") {
			t.Errorf("%s: %v", args, err)
		}
	}
	tl, _ := r.Get("cron_add")
	if strings.Contains(string(tl.Schema), "profile") || strings.Contains(string(tl.Schema), "environment") || strings.Contains(tl.Description, "this chat is using") {
		t.Fatalf("the tool should not mention choices that do not exist: %s / %s", tl.Schema, tl.Description)
	}
}

func TestTheModelSeesWhatItMayChoose(t *testing.T) {
	profile, env := "", ""
	r := tool.NewRegistry()
	RegisterTools(r, newStore(t, utc("2026-10-20 10:00")), "", 7, chatTargets(&profile, &env))
	tl, _ := r.Get("cron_add")

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tl.Schema, &schema); err != nil {
		t.Fatalf("the schema must be valid JSON: %v\n%s", err, tl.Schema)
	}
	if strings.Join(schema.Properties["profile"].Enum, ",") != "default,claude,local,openai" ||
		strings.Join(schema.Properties["environment"].Enum, ",") != "default,local,sandbox" {
		t.Fatalf("the allowed values are listed, so the model cannot guess: %+v", schema.Properties)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "prompt" || schema.Properties["schedule"].Enum != nil {
		t.Fatalf("the existing arguments are unchanged: %+v", schema)
	}
	if !strings.Contains(tl.Description, "runs on the model and in the environment this chat is using when it is created") {
		t.Fatalf("%q", tl.Description)
	}
}

func TestListingAndStoringJobTargets(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	j, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1, Profile: "claude", Environment: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	if d := Describe(j); !strings.HasSuffix(d, "[model: claude] [env: sandbox]") {
		t.Fatalf("a job's own choices show in listings: %q", d)
	}
	plain, _ := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1})
	if d := Describe(plain); strings.Contains(d, "model:") || strings.Contains(d, "env:") {
		t.Fatalf("a job on the defaults shows nothing extra: %q", d)
	}

	// They persist, and a job file from before these fields existed still loads as "the defaults".
	again, _ := (&Store{Path: s.Path}).List()
	if again[0].Profile != "claude" || again[0].Environment != "sandbox" {
		t.Fatalf("%+v", again[0])
	}
	old := `{"jobs":[{"id":"abc","name":"old","schedule":"@daily","prompt":"p","chat_id":1,"allow_risky":false,"created_at":"2026-01-01T00:00:00Z","next_run":"2026-01-02T00:00:00Z"}]}`
	os.WriteFile(s.Path, []byte(old), 0o600)
	if jobs, err := (&Store{Path: s.Path}).List(); err != nil || jobs[0].Profile != "" || jobs[0].Environment != "" {
		t.Fatalf("%+v %v", jobs, err)
	}

	for _, bad := range []Job{{Profile: "a\nb"}, {Environment: strings.Repeat("x", 101)}} {
		bad.Schedule, bad.Prompt, bad.ChatID = "@daily", "p", 1
		if _, err := s.Add(bad); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}
