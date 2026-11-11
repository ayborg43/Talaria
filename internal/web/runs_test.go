package web

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"talaria/internal/runlog"
)

func TestActivityNeedsASignInAndIsEmptyWithoutARecord(t *testing.T) {
	r := newRig(t)
	if resp, _ := r.do("GET", "/api/runs", nil); resp.StatusCode != 401 {
		t.Fatalf("%d", resp.StatusCode)
	}
	r.login()
	resp, body := r.do("GET", "/api/runs", nil)
	var out struct {
		Runs      []runlog.Summary
		Available bool
	}
	if resp.StatusCode != 200 || json.Unmarshal(body, &out) != nil || out.Available || len(out.Runs) != 0 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("GET", "/api/runs/x", nil); resp.StatusCode != 404 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestActivityShowsARunAndItsFilesAndScrubsWhatItSays(t *testing.T) {
	r := newRig(t)
	r.login()
	r.srv.Runs = &runlog.Log{}
	r.srv.Scrub = func(s string) string { return strings.ReplaceAll(s, "hunter2", "[hidden]") }
	id := r.srv.Runs.Begin("job1", "Check prices", "task", "web", ChatID, "look at the page, password hunter2", "m", "")
	r.srv.Runs.Step(id, "web_fetch", "https://example.com")
	r.srv.Runs.AddFile(id, runlog.File{ID: "f1", Name: "Prices.md", MIME: "text/markdown"})

	var list struct{ Runs []runlog.Summary }
	_, body := r.do("GET", "/api/runs", nil)
	if json.Unmarshal(body, &list) != nil || len(list.Runs) != 1 || list.Runs[0].Status != runlog.Running || list.Runs[0].Doing == "" || list.Runs[0].Files != 1 {
		t.Fatalf("%s", body)
	}
	// By the run's id, and by the job's id.
	for _, key := range []string{id, "job1"} {
		resp, body := r.do("GET", "/api/runs/"+key, nil)
		var run runlog.Run
		if resp.StatusCode != 200 || json.Unmarshal(body, &run) != nil || run.Name != "Check prices" || len(run.Steps) != 1 || len(run.Files) != 1 {
			t.Fatalf("%s: %d %s", key, resp.StatusCode, body)
		}
		if strings.Contains(run.Prompt, "hunter2") || !strings.Contains(run.Prompt, "[hidden]") {
			t.Fatalf("the prompt must be scrubbed: %q", run.Prompt)
		}
	}
}

func TestSteeringARunningTaskQueuesWhatYouSayAndTheTaskReadsItOnce(t *testing.T) {
	r := newRig(t)
	r.login()
	r.srv.Runs = &runlog.Log{}
	id := r.srv.Runs.Begin("job1", "Research", "task", "web", ChatID, "p", "m", "")

	if resp, _ := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "  "}); resp.StatusCode != 400 {
		t.Fatalf("empty: %d", resp.StatusCode)
	}
	resp, body := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "skip the second site"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"steer"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := r.srv.Runs.Take(id); len(got) != 1 || got[0] != "skip the second site" {
		t.Fatalf("%v", got)
	}
	if got := r.srv.Runs.Take(id); len(got) != 0 {
		t.Fatalf("read twice: %v", got)
	}
	// Too many unread notes at once are refused rather than queued without end.
	for i := 0; i < runlog.MaxSteersLive; i++ {
		if resp, _ := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "note"}); resp.StatusCode != 200 {
			t.Fatalf("note %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "one more"}); resp.StatusCode != 409 {
		t.Fatalf("%d", resp.StatusCode)
	}
	// A made-up run is not found.
	if resp, _ := r.do("POST", "/api/runs/nope/steer", map[string]string{"text": "hi"}); resp.StatusCode != 404 {
		t.Fatalf("%d", resp.StatusCode)
	}
	// The same-origin header is required.
	if resp, _ := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "hi"}, "X-Talaria", ""); resp.StatusCode == 200 {
		t.Fatal("a request from another page was accepted")
	}
}

func TestWritingToAFinishedTaskStartsAFollowUp(t *testing.T) {
	r := newRig(t)
	r.login()
	r.srv.Runs = &runlog.Log{}
	id := r.srv.Runs.Begin("job1", "Research", "task", "web", ChatID, "p", "m", "")
	r.srv.Runs.End(id, runlog.Done, "found three")

	// Nothing to start it with: an honest refusal.
	if resp, _ := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "now compare them"}); resp.StatusCode != 501 {
		t.Fatalf("%d", resp.StatusCode)
	}
	var gotID, gotText string
	r.srv.Sources.Runs = &RunHooks{FollowUp: func(runID, text string) (string, error) {
		gotID, gotText = runID, text
		return "Follow-up: Research", nil
	}}
	resp, body := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "now compare them"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"follow_up"`) || !strings.Contains(string(body), "Follow-up: Research") || gotID != id || gotText != "now compare them" {
		t.Fatalf("%d %s %q %q", resp.StatusCode, body, gotID, gotText)
	}
	r.srv.Sources.Runs.FollowUp = func(string, string) (string, error) { return "", errors.New("your rules say no") }
	if resp, body := r.do("POST", "/api/runs/"+id+"/steer", map[string]string{"text": "again"}); resp.StatusCode != 400 || !strings.Contains(string(body), "your rules say no") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestStoppingOnlyAppliesToARunningTask(t *testing.T) {
	r := newRig(t)
	r.login()
	r.srv.Runs = &runlog.Log{}
	var stopped string
	r.srv.Sources.Runs = &RunHooks{Stop: func(id string) error { stopped = id; return nil }}
	done := r.srv.Runs.Begin("j0", "Old", "task", "web", ChatID, "p", "m", "")
	r.srv.Runs.End(done, runlog.Done, "ok")
	if resp, _ := r.do("POST", "/api/runs/"+done+"/stop", nil); resp.StatusCode != 400 || stopped != "" {
		t.Fatalf("%d %q", resp.StatusCode, stopped)
	}
	live := r.srv.Runs.Begin("j1", "Now", "task", "web", ChatID, "p", "m", "")
	if resp, _ := r.do("POST", "/api/runs/"+live+"/stop", nil); resp.StatusCode != 200 || stopped != live {
		t.Fatalf("%d %q", resp.StatusCode, stopped)
	}
}
