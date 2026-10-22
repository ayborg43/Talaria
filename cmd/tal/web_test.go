package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/cron"
	"talaria/internal/goal"
	"talaria/internal/watch"
	"talaria/internal/web"
)

const webPw = "a long enough password"

func TestTheWebAppIsOffUntilAPasswordIsGiven(t *testing.T) {
	if _, problems, on := webConfig(envOf(nil)); on || len(problems) != 0 {
		t.Fatalf("%v %v", on, problems)
	}
	w, problems, on := webConfig(envOf(map[string]string{"TALARIA_WEB_PASSWORD": webPw}))
	if !on || len(problems) != 0 || w.Listen != ":8080" || w.Password != webPw || w.Secure || w.PublicURL != "" {
		t.Fatalf("%+v %v", w, problems)
	}
	w, problems, _ = webConfig(envOf(map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_LISTEN": "127.0.0.1:9000", "TALARIA_WEB_PUBLIC_URL": "https://talaria.example.com/"}))
	if len(problems) != 0 || w.Listen != "127.0.0.1:9000" || w.PublicURL != "https://talaria.example.com" || !w.Secure {
		t.Fatalf("%+v %v", w, problems)
	}
	if w, problems, _ = webConfig(envOf(map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_PUBLIC_URL": "http://localhost:8080"})); len(problems) != 0 || w.Secure {
		t.Fatalf("a local address for trying it out: %+v %v", w, problems)
	}
}

func TestWebSettingsThatAreWrongAreNamed(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TALARIA_WEB_LISTEN": ":8080"}, "TALARIA_WEB_PASSWORD is not set"},
		{map[string]string{"TALARIA_WEB_PUBLIC_URL": "https://x.example.com"}, "TALARIA_WEB_PASSWORD is not set"},
		{map[string]string{"TALARIA_WEB_PASSWORD": "short"}, "at least 12 characters"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_LISTEN": "8080"}, "TALARIA_WEB_LISTEN must be an address"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_LISTEN": ":99999"}, "TALARIA_WEB_LISTEN must be an address"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_LISTEN": ":abc"}, "TALARIA_WEB_LISTEN must be an address"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_PUBLIC_URL": "http://talaria.example.com"}, "must start with https://"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_PUBLIC_URL": "talaria.example.com"}, "TALARIA_WEB_PUBLIC_URL must be an address"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_PUBLIC_URL": "https://talaria.example.com/app"}, "TALARIA_WEB_PUBLIC_URL must be an address"},
		{map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_PUBLIC_URL": "https://x.example.com?a=1"}, "TALARIA_WEB_PUBLIC_URL must be an address"},
	}
	for _, c := range cases {
		_, problems, on := webConfig(envOf(c.env))
		if !on || !strings.Contains(strings.Join(problems, "|"), c.want) {
			t.Errorf("%v: want %q, got %v", c.env, c.want, problems)
		}
	}
}

func TestTheWebPasswordIsASecretKeptFromShellsAndScrubbedFromOutput(t *testing.T) {
	cfg := &config.Config{}
	f := buildFence(cfg, "/h/config.json", "/h")
	deny := map[string]bool{}
	for _, n := range f.EnvDeny {
		deny[n] = true
	}
	for _, n := range []string{"TALARIA_WEB_PASSWORD", "TALARIA_WEB_LISTEN", "TALARIA_WEB_PUBLIC_URL"} {
		if !deny[n] {
			t.Errorf("%s reaches the agent's shell", n)
		}
	}
	found := false
	for _, s := range collectSecrets(cfg, envOf(map[string]string{"TALARIA_WEB_PASSWORD": webPw}), nil) {
		if s == webPw {
			found = true
		}
	}
	if !found {
		t.Fatal("the web password is not scrubbed from what the model sees")
	}
}

func TestPausingEverythingSurvivesARestart(t *testing.T) {
	home := t.TempDir()
	p := newPauseState(home)
	if p.Paused() {
		t.Fatal("paused from the start")
	}
	if err := p.SetPaused(true); err != nil || !p.Paused() {
		t.Fatalf("%v", err)
	}
	if st, err := os.Stat(filepath.Join(home, "paused")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	if !newPauseState(home).Paused() {
		t.Fatal("a restart quietly undid the pause")
	}
	if err := p.SetPaused(false); err != nil || p.Paused() || newPauseState(home).Paused() {
		t.Fatalf("not resumed: %v", err)
	}
	if err := p.SetPaused(false); err != nil { // resuming what is not paused is fine
		t.Fatal(err)
	}
}

func TestDoctorSaysWhatItFindsAboutTheWebApp(t *testing.T) {
	prov := `{"profiles":{"m":{"base_url":"http://localhost:1/v1"}}}`
	out, n := doctorOn(t, prov, map[string]string{"TALARIA_WEB_PASSWORD": webPw, "TALARIA_WEB_PUBLIC_URL": "https://talaria.example.com"}, nil)
	if !strings.Contains(out, "served on :8080") || !strings.Contains(out, "public address https://talaria.example.com") || strings.Contains(out, webPw) {
		t.Fatalf("%d:\n%s", n, out)
	}
	out, _ = doctorOn(t, prov, map[string]string{"TALARIA_WEB_PASSWORD": webPw}, nil)
	if !strings.Contains(out, "TALARIA_WEB_PUBLIC_URL is not set") {
		t.Fatalf("%s", out)
	}
	out, _ = doctorOn(t, prov, map[string]string{"TALARIA_WEB_PASSWORD": "short"}, nil)
	if !strings.Contains(out, "at least 12 characters") {
		t.Fatalf("%s", out)
	}
	out, _ = doctorOn(t, prov, nil, nil)
	if strings.Contains(out, "\nWeb app\n") {
		t.Fatal("nothing about the web app when it is not set up")
	}
}

func TestTheControlScreenListsGoalsWatchesAndRemindersOfTheWebChatOnly(t *testing.T) {
	home := t.TempDir()
	gs := &goal.Store{Path: filepath.Join(home, "goals.json")}
	ws := &watch.Store{Path: filepath.Join(home, "watches.json")}
	rt := app.Open(app.Config{Home: home, Goals: gs, Watches: ws}, func(string, ...any) {})
	defer rt.Close()

	gs.Add(goal.Goal{Gateway: "web", ChatID: web.ChatID, Title: "Prepare the Q4 report", Objective: "a report", Plan: []goal.Step{{Text: "gather", Done: true}, {Text: "draft"}, {Text: "send"}}})
	gs.Add(goal.Goal{Gateway: "telegram", ChatID: 77, Title: "Someone else's goal", Objective: "x"})
	ws.Add(watch.Watch{Gateway: "web", ChatID: web.ChatID, Title: "Mail from Amina", Tool: "email_list", Every: 15 * time.Minute})
	ws.Add(watch.Watch{Gateway: "telegram", ChatID: 77, Title: "Someone else's watch", Tool: "email_list", Every: 15 * time.Minute})
	rt.Cron.Add(cron.Job{Name: "Leave for the dentist", Schedule: "@every 24h", Prompt: "remind", Gateway: "web", ChatID: web.ChatID})
	rt.Cron.Add(cron.Job{Name: "Someone else's reminder", Schedule: "@every 24h", Prompt: "remind", Gateway: "telegram", ChatID: 77})

	jobs := webJobs(rt, time.UTC)
	var got []string
	for _, j := range jobs {
		got = append(got, j.Kind+"|"+j.Title+"|"+j.State)
		if strings.Contains(j.Title, "else") {
			t.Fatalf("another chat's work is shown: %+v", j)
		}
	}
	want := []string{"GOAL|Prepare the Q4 report|Active", "WATCH|Mail from Amina|Quiet", "TIME|Leave for the dentist|Waiting"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%v", got)
	}
	if !strings.HasPrefix(jobs[0].Sub, "Step 2 of 3") || jobs[1].Sub != "Checks every 15m0s" {
		t.Fatalf("%+v", jobs)
	}
	// A paused goal says so, and a finished one is not listed.
	all, _ := gs.List("web", web.ChatID)
	gs.Update("web", web.ChatID, all[0].ID, func(g *goal.Goal) error { g.Status = goal.Paused; return nil })
	if j := webJobs(rt, time.UTC)[0]; j.State != "Paused" {
		t.Fatalf("%+v", j)
	}
	gs.Update("web", web.ChatID, all[0].ID, func(g *goal.Goal) error { g.Status = goal.Done; return nil })
	for _, j := range webJobs(rt, time.UTC) {
		if j.Kind == "GOAL" {
			t.Fatalf("a finished goal is listed: %+v", j)
		}
	}
}

func TestTheControlScreenListsWhatIsConnected(t *testing.T) {
	rt := app.Open(app.Config{Home: t.TempDir()}, func(string, ...any) {})
	defer rt.Close()
	links := webLinks(&config.Config{}, rt, "deepseek-chat", envOf(nil), "")()
	if len(links) != 1 || links[0].Name != "Model" || links[0].Detail != "deepseek-chat" {
		t.Fatalf("%+v", links)
	}
}

func TestPausingThatCannotBeKeptIsNotClaimedToHaveWorked(t *testing.T) {
	home := t.TempDir()
	p := newPauseState(filepath.Join(home, "no", "such", "folder"))
	if err := p.SetPaused(true); err == nil || p.Paused() {
		t.Fatalf("it says paused though nothing was kept: err=%v paused=%v", err, p.Paused())
	}
	// And resuming when the file cannot be removed is an error, and leaves it paused.
	q := newPauseState(home)
	q.SetPaused(true)
	os.Chmod(home, 0o500)
	defer os.Chmod(home, 0o700)
	if os.Geteuid() != 0 { // root can remove it anyway
		if err := q.SetPaused(false); err == nil || !q.Paused() {
			t.Fatalf("it says resumed though the file is still there: err=%v paused=%v", err, q.Paused())
		}
	}
}
