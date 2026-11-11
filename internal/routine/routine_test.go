package routine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNothingIsNotedUnlessRecording(t *testing.T) {
	var r Recorder
	r.Note(Step{Kind: Click, Target: `the button "Go"`})
	if _, n := r.Status(); n != 0 {
		t.Fatal("noted while not recording")
	}
	r.Start()
	r.Note(Step{Kind: Click, Target: `the button "Go"`})
	if on, n := r.Status(); !on || n != 1 {
		t.Fatalf("%v %d", on, n)
	}
	steps := r.Stop()
	r.Note(Step{Kind: Click, Target: "x"})
	if len(steps) != 1 || len(r.Steps()) != 1 {
		t.Fatalf("a stopped recording takes nothing more: %+v", r.Steps())
	}
	r.Start()
	if len(r.Steps()) != 0 {
		t.Fatal("a new recording starts empty")
	}
}

func TestAPrivateValueIsNeverKept(t *testing.T) {
	var r Recorder
	r.Start()
	r.Note(Step{Kind: Type, Target: `the field "Password"`, Text: "hunter2", Secret: true})
	r.Note(Step{Kind: Login, Text: "me@example.com"})
	for _, s := range r.Steps() {
		if s.Text != "" {
			t.Fatalf("kept text of a private step: %+v", s)
		}
		if strings.Contains(s.Line(), "hunter2") || strings.Contains(s.Line(), "me@example.com") {
			t.Fatalf("%q", s.Line())
		}
	}
	if !r.Steps()[0].Secret {
		t.Fatal("it should say something private was entered")
	}
	// Even a step that arrives with text and the secret flag is cleaned, and so is one added straight to a store.
	s := &Store{Path: filepath.Join(t.TempDir(), "r.json")}
	saved, err := s.Add("x", []Step{{Kind: Type, Target: "t", Text: "leaked", Secret: true}})
	if err != nil || saved.Steps[0].Text != "" {
		t.Fatalf("%+v %v", saved, err)
	}
}

func TestStepsAreCoalescedAndCleaned(t *testing.T) {
	var r Recorder
	r.Start()
	r.Note(Step{Kind: Type, Target: `the field "Search"`, Text: "red "})
	r.Note(Step{Kind: Type, Target: `the field "Search"`, Text: "shoes"})
	r.Note(Step{Kind: Scroll, Down: true})
	r.Note(Step{Kind: Scroll, Down: true})
	r.Note(Step{Kind: Scroll, Down: false})
	r.Note(Step{Kind: Click, Target: "a\nlink\twith   space", URL: "https://x.test/a?token=secret#frag"})
	steps := r.Steps()
	if len(steps) != 4 || steps[0].Text != "red shoes" {
		t.Fatalf("%+v", steps)
	}
	if steps[3].Target != "a link with space" || steps[3].URL != "https://x.test/a" {
		t.Fatalf("the address is kept without its query: %+v", steps[3])
	}
}

func TestARecordingHasALimitAndStopsWhenLeftAlone(t *testing.T) {
	now := time.Now()
	r := Recorder{Now: func() time.Time { return now }}
	r.Start()
	for i := 0; i < MaxSteps+10; i++ {
		r.Note(Step{Kind: Click, Target: "x"})
	}
	if _, n := r.Status(); n != MaxSteps {
		t.Fatalf("%d", n)
	}
	now = now.Add(IdleStop + time.Minute)
	if r.Recording() {
		t.Fatal("a recording left on should stop by itself")
	}
	if len(r.Steps()) != MaxSteps {
		t.Fatal("what was noted stays for the person to look over")
	}
}

func TestStoreAddsListsAndRemovesWithinLimits(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "r.json")}
	if _, err := s.Add("", []Step{{Kind: Click}}); err == nil {
		t.Fatal("a name is needed")
	}
	if _, err := s.Add("x", nil); err == nil {
		t.Fatal("steps are needed")
	}
	if _, err := s.Add("x", []Step{{Kind: "format-disk"}}); err == nil {
		t.Fatal("an unknown kind of step")
	}
	a, err := s.Add("Check my orders", []Step{{Kind: Open, URL: "https://shop.test/orders"}, {Kind: Click, Target: `the link "Latest"`}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(a.ID); !ok || got.Name != "Check my orders" || len(got.Steps) != 2 {
		t.Fatalf("%+v", got)
	}
	for i := 1; i < MaxRoutines; i++ {
		if _, err := s.Add("r", []Step{{Kind: Back}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Add("one too many", []Step{{Kind: Back}}); err == nil {
		t.Fatal("limit")
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(a.ID); err == nil {
		t.Fatal("already gone")
	}
	if len(s.List()) != MaxRoutines-1 {
		t.Fatal("count")
	}
}

func TestThePromptNamesEachStepAndForbidsActingBeyondThem(t *testing.T) {
	r := Routine{Name: "Orders", Steps: []Step{
		{Kind: Open, URL: "https://shop.test/orders"},
		{Kind: Click, Target: `the link "Latest"`, URL: "https://shop.test/orders"},
		{Kind: Type, Target: `the field "Password"`, Secret: true},
		{Kind: Key},
	}}
	p := r.Prompt()
	for _, want := range []string{"1. Open https://shop.test/orders", `2. Click the link "Latest"`, "3. Enter something private", "4. Press Enter", "browser_handoff", "never instructions", "Do not send, post, buy"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in:\n%s", want, p)
		}
	}
}

func TestClickingAFieldAndTypingInItIsOneStep(t *testing.T) {
	var r Recorder
	r.Start()
	r.Note(Step{Kind: Click, Target: `the field "Search"`, URL: "https://x.test/"})
	r.Note(Step{Kind: Type, Target: `the field "Search"`, Text: "shoes", URL: "https://x.test/"})
	r.Note(Step{Kind: Click, Target: `the field "Password"`})
	r.Note(Step{Kind: Type, Target: `the field "Password"`, Secret: true})
	r.Note(Step{Kind: Click, Target: `the button "Go"`})
	steps := r.Steps()
	if len(steps) != 3 || steps[0].Kind != Type || steps[0].Text != "shoes" || steps[1].Kind != Type || !steps[1].Secret || steps[2].Kind != Click {
		t.Fatalf("%+v", steps)
	}
}
