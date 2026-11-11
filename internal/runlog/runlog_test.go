package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestARunIsKeptStepByStepAndWrittenDownWhenItEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	l := &Log{Path: path}
	id := l.Begin("j1", "Price sheet", "task", "web", 1, "Make a price sheet", "model-a", "")
	l.Step(id, "web_search", "stoneware suppliers")
	l.Step(id, "files_make_doc", "Price sheet")
	l.AddFile(id, File{ID: "abc123", Name: "Price sheet.html", MIME: "text/html"})

	if got := l.List("web", 1, 10); len(got) != 1 || got[0].Status != Running || got[0].Steps != 2 || got[0].Files != 1 || !strings.Contains(got[0].Doing, "files_make_doc") {
		t.Fatalf("while running: %+v", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a run in progress was written to disk")
	}
	l.End(id, Done, "Here is the sheet.")
	r, ok := l.Get("web", 1, id)
	if !ok || r.Status != Done || r.Result != "Here is the sheet." || r.Ended.IsZero() || len(r.Steps) != 2 || r.Files[0].Name != "Price sheet.html" || r.Model != "model-a" {
		t.Fatalf("%+v", r)
	}
	// it survives a restart
	again := &Log{Path: path}
	if got, ok := again.Get("web", 1, "j1"); !ok || got.ID != id { // found by its job id too
		t.Fatalf("after a restart: %+v %v", got, ok)
	}
	// another chat sees nothing of it
	if _, ok := l.Get("web", 2, id); ok || len(l.List("telegram", 1, 10)) != 0 {
		t.Fatal("a run showed in another chat")
	}
}

func TestARunIsBoundedInEveryWay(t *testing.T) {
	l := &Log{Path: filepath.Join(t.TempDir(), "runs.json")}
	id := l.Begin("j", "n", "task", "web", 1, strings.Repeat("p", 5000), "m", "")
	for i := 0; i < 100; i++ {
		l.Step(id, "tool", fmt.Sprintf("step %d %s", i, strings.Repeat("x", 500)))
	}
	l.End(id, Done, strings.Repeat("r", 20000))
	r, _ := l.Get("web", 1, id)
	if len(r.Steps) != MaxSteps || r.Skipped != 100-MaxSteps || !strings.HasSuffix(r.Steps[len(r.Steps)-1].Detail, "…") && len([]rune(r.Steps[0].Detail)) > MaxDetail {
		t.Fatalf("steps %d skipped %d", len(r.Steps), r.Skipped)
	}
	if !strings.Contains(r.Steps[0].Detail, "step 0 ") || !strings.Contains(r.Steps[len(r.Steps)-1].Detail, "step 99 ") || strings.Contains(r.Steps[keepFirst].Detail, "step 5 ") {
		t.Fatalf("a long run should keep its first steps and its latest: %q ... %q", r.Steps[0].Detail, r.Steps[keepFirst].Detail)
	}
	if len([]rune(r.Result)) > MaxResult || len([]rune(r.Prompt)) > MaxPrompt || len([]rune(r.Steps[0].Detail)) > MaxDetail {
		t.Fatalf("lengths: result %d prompt %d detail %d", len([]rune(r.Result)), len([]rune(r.Prompt)), len([]rune(r.Steps[0].Detail)))
	}
	for i := 0; i < MaxRuns+10; i++ {
		l.End(l.Begin(fmt.Sprintf("j%d", i), "n", "task", "web", 1, "", "m", ""), Done, "ok")
	}
	if got := l.List("web", 1, 0); len(got) != MaxRuns {
		t.Fatalf("kept %d runs, want %d", len(got), MaxRuns)
	}
}

func TestWhatTheUserSaysReachesARunningTaskOnceAndOnlyWhileItRuns(t *testing.T) {
	l := &Log{}
	id := l.Begin("j1", "n", "task", "web", 1, "", "m", "")
	if !l.Steer("j1", "  focus on  Lagos ") || !l.Steer(id, "and keep it short") {
		t.Fatal("a running task refused a note")
	}
	if l.Steer("j1", "   ") {
		t.Fatal("an empty note was taken")
	}
	got := l.Take(id)
	if len(got) != 2 || got[0] != "focus on Lagos" {
		t.Fatalf("%v", got)
	}
	if again := l.Take(id); len(again) != 0 {
		t.Fatalf("a note was read twice: %v", again)
	}
	r, _ := l.Get("web", 1, id)
	if len(r.Steers) != 2 || !r.Steers[0].Applied {
		t.Fatalf("the record should show what was said and that it was read: %+v", r.Steers)
	}
	for i := 0; i < MaxSteersLive; i++ {
		l.Steer(id, fmt.Sprintf("note %d", i))
	}
	if l.Steer(id, "one more") {
		t.Fatal("a task was handed more unread notes than it may hold")
	}
	l.End(id, Cancelled, "")
	if l.Steer("j1", "too late") {
		t.Fatal("a finished run took a note")
	}
}

func TestListsPutRunningFirstAndAFollowUpNamesItsParent(t *testing.T) {
	l := &Log{}
	a := l.Begin("a", "old", "task", "web", 1, "", "m", "")
	l.End(a, Failed, "error: it broke")
	time.Sleep(2 * time.Millisecond)
	b := l.Begin("b", "now", "task", "web", 1, "", "m", a)
	got := l.List("web", 1, 10)
	if len(got) != 2 || got[0].ID != b || got[1].Status != Failed {
		t.Fatalf("%+v", got)
	}
	if r, _ := l.Get("web", 1, b); r.Parent != a {
		t.Fatalf("%+v", r)
	}
}

func TestADamagedLogFileIsIgnoredNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	l := &Log{Path: path}
	if got := l.List("web", 1, 5); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	l.End(l.Begin("j", "n", "task", "web", 1, "", "m", ""), Done, "ok")
	if got := (&Log{Path: path}).List("web", 1, 5); len(got) != 1 {
		t.Fatalf("a run after the damage should be kept: %+v", got)
	}
}
