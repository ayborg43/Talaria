package web

import (
	"encoding/json"
	"strings"
	"testing"

	"talaria/internal/files"
	"talaria/internal/memory"
)

func TestTheMorningBriefIsSwitchedFromTheApp(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("POST", "/api/brief", map[string]any{"enabled": true, "at": "07:30"}); resp.StatusCode != 501 {
		t.Fatalf("with no scheduler behind it the brief answered %d, want 501", resp.StatusCode)
	}
	var on bool
	var at string
	r.srv.Sources.Brief = func() BriefInfo { return BriefInfo{Enabled: on, At: at} }
	r.srv.Sources.SetBrief = func(enabled bool, when string) error { on, at = enabled, when; return nil }

	resp, body := r.do("POST", "/api/brief", map[string]any{"enabled": true, "at": "06:45"})
	var got BriefInfo
	if err := json.Unmarshal(body, &got); err != nil || resp.StatusCode != 200 || !got.Available || !got.Enabled || got.At != "06:45" {
		t.Fatalf("turning it on: %d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/brief", map[string]any{"enabled": true, "at": "7:30pm"}); resp.StatusCode != 400 {
		t.Fatalf("a time that is not a clock time answered %d", resp.StatusCode)
	}
	if at != "06:45" {
		t.Fatalf("a refused time changed the brief to %q", at)
	}
}

func TestOnboardingIsDoneOnceAndSurvivesSavingTheProfile(t *testing.T) {
	r := newRig(t)
	r.srv.Notes = &memory.Notes{Dir: t.TempDir()}
	r.login()
	if _, body := r.do("GET", "/api/state", nil); !strings.Contains(string(body), `"onboarded":false`) {
		t.Fatalf("a new profile is not onboarded: %s", body)
	}
	if resp, body := r.do("POST", "/api/profile", map[string]any{"onboarded": true}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// The page's own forms send the profile without the flag; that must not bring the welcome back.
	if resp, body := r.do("POST", "/api/profile", map[string]any{"profile": map[string]string{"name": "Ayo"}}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, body := r.do("GET", "/api/state", nil); !strings.Contains(string(body), `"onboarded":true`) || !strings.Contains(string(body), `"name":"Ayo"`) {
		t.Fatalf("saving the profile undid the introduction: %s", body)
	}
}

func TestSetupCheckSaysWhatItCouldNotReach(t *testing.T) {
	r := newRig(t)
	r.login()
	resp, body := r.do("POST", "/api/setup/check", map[string]any{})
	if resp.StatusCode != 200 || strings.TrimSpace(string(body)) != "{}" {
		t.Fatalf("with nothing connected the check should report nothing: %d %s", resp.StatusCode, body)
	}
}

func TestAMadePageIsOnlyEverServedSandboxed(t *testing.T) {
	r := newRig(t)
	r.srv.Files = &files.Store{Dir: t.TempDir()}
	r.login()
	page, _ := files.RenderDoc("Memo", "hello")
	f, err := r.srv.Files.SaveMade("Memo.html", page, files.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := r.do("GET", "/api/files/"+f.ID+"?inline=1", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"sandbox", "default-src 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("a made page is served without %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "script-src") || strings.Contains(csp, "connect-src") {
		t.Errorf("a made page may run script or reach out: %s", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("headers: %v", resp.Header)
	}
	resp, _ = r.do("GET", "/api/files/"+f.ID, nil)
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("a plain request for a made page should download it: %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestAFinishedTaskCanBeCheckedOff(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("POST", "/api/tasks", map[string]any{"review": "t1"}); resp.StatusCode != 501 {
		t.Fatalf("with nothing behind it review answered %d, want 501", resp.StatusCode)
	}
	var seen []string
	r.srv.Sources.ReviewTask = func(id string) error { seen = append(seen, id); return nil }
	if resp, body := r.do("POST", "/api/tasks", map[string]any{"review": "t1"}); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/tasks", map[string]any{"review": strings.Repeat("x", 65)}); resp.StatusCode != 400 {
		t.Fatalf("an absurd id answered %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/tasks", map[string]any{}); resp.StatusCode == 200 {
		t.Fatal("an empty request did something")
	}
	if len(seen) != 1 || seen[0] != "t1" {
		t.Fatalf("reviewed %v", seen)
	}
}

func TestSuggestionsCanBeStartedOrHiddenFromTheApp(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("POST", "/api/suggestions", map[string]any{"hide": "a"}); resp.StatusCode != 501 {
		t.Fatalf("with nothing behind it hide answered %d, want 501", resp.StatusCode)
	}
	list := []SuggestionInfo{{ID: "a", Title: "Draft a reply", Startable: true}, {ID: "b", Title: "A note"}}
	var started, hidden []string
	r.srv.Sources.Suggestions = func() []SuggestionInfo { return list }
	r.srv.Sources.StartSuggestion = func(id string) (string, error) {
		started = append(started, id)
		list = list[1:]
		return "Draft a reply", nil
	}
	r.srv.Sources.HideSuggestion = func(id string) error { hidden = append(hidden, id); return nil }

	if _, body := r.do("GET", "/api/state", nil); !strings.Contains(string(body), `"suggestion":{"id":"a"`) {
		t.Fatalf("the newest suggestion is not in the state: %s", body)
	}
	resp, body := r.do("POST", "/api/suggestions", map[string]any{"start": "a"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"started":"Draft a reply"`) || strings.Contains(string(body), `"id":"a"`) {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/suggestions", map[string]any{"hide": "b"}); resp.StatusCode != 200 {
		t.Fatalf("hide answered %d", resp.StatusCode)
	}
	for name, in := range map[string]map[string]any{"both": {"start": "a", "hide": "b"}, "neither": {}, "huge": {"start": strings.Repeat("x", 65)}} {
		if resp, _ := r.do("POST", "/api/suggestions", in); resp.StatusCode != 400 {
			t.Errorf("%s answered %d", name, resp.StatusCode)
		}
	}
	if len(started) != 1 || len(hidden) != 1 || hidden[0] != "b" {
		t.Fatalf("started %v hidden %v", started, hidden)
	}
}
