package web

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/browser"
	"talaria/internal/routine"
)

type fakePage struct {
	url    string
	under  browser.Described
	focus  browser.Described
	kind   string
	blind  bool
	fields []browser.SignInField
}

func (f *fakePage) Where(context.Context) string { return f.url }
func (f *fakePage) DescribeAt(context.Context, float64, float64) (browser.Described, bool) {
	return f.under, !f.blind
}
func (f *fakePage) DescribeFocus(context.Context) (browser.Described, bool) { return f.focus, !f.blind }
func (f *fakePage) FocusKind(context.Context) string                        { return f.kind }
func (f *fakePage) SignInFields(context.Context) []browser.SignInField      { return f.fields }

func routineRig(t *testing.T) *rig {
	r := newRig(t)
	r.login()
	r.srv.Rec = &routine.Recorder{}
	r.srv.Routines = &routine.Store{Path: filepath.Join(t.TempDir(), "routines.json")}
	return r
}

func TestRoutinesAreOffUntilSetUp(t *testing.T) {
	r := newRig(t)
	if resp, _ := r.do("GET", "/api/routines", nil); resp.StatusCode != 401 {
		t.Fatalf("%d", resp.StatusCode)
	}
	r.login()
	resp, body := r.do("GET", "/api/routines", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/api/routines", map[string]any{"action": "start"}); resp.StatusCode != 501 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestWhatYouDoByHandBecomesStepsInWordsAndAPrivateValueIsNeverKept(t *testing.T) {
	r := routineRig(t)
	ctx := context.Background()
	page := &fakePage{url: "https://shop.test/account?session=abc", under: browser.Described{Tag: "a", Label: "Orders"}}
	r.srv.Scrub = func(s string) string { return strings.ReplaceAll(s, "sk-123", "[hidden]") }
	r.srv.Rec.Start()
	note := func(kind string, x, y, dy float64, text, key, url string) {
		if st := r.srv.recordStep(ctx, page, kind, x, y, dy, text, key, url); st != nil {
			r.srv.Rec.Note(*st)
		}
	}
	note("open", 0, 0, 0, "", "", "shop.test/orders")
	note("click", 10, 10, 0, "", "", "")
	page.focus = browser.Described{Tag: "input", Type: "text", Label: "Search"}
	page.kind = "text"
	note("type", 0, 0, 0, "red shoes sk-123", "", "")
	page.focus = browser.Described{Tag: "input", Type: "password", Label: "Password"}
	page.kind = "password"
	note("type", 0, 0, 0, "hunter2", "", "")
	note("replace", 0, 0, 0, "hunter3", "", "") // a password manager filling the same field
	note("key", 0, 0, 0, "", "Enter", "")
	note("key", 0, 0, 0, "", "Tab", "") // not something a routine repeats
	note("wheel", 5, 5, 10, "", "", "") // a twitch of the wheel is not a scroll
	note("wheel", 5, 5, 300, "", "", "")
	note("login", 0, 0, 0, "", "", "")
	steps := r.srv.Rec.Steps()
	var lines []string
	for _, s := range steps {
		lines = append(lines, s.Line())
		if strings.Contains(s.Line(), "hunter") || strings.Contains(s.Line(), "sk-123") {
			t.Fatalf("a private value or a known secret was kept: %q", s.Line())
		}
	}
	want := []string{"Open https://shop.test/orders", `Click the link "Orders"`, `Type "red shoes [hidden]" into the field "Search"`,
		`Enter something private into the field "Password" (you did this yourself and it was not kept)`, "Press Enter", "Scroll down",
		"Sign in (you did this yourself and it was not kept)"}
	if len(lines) != len(want) {
		t.Fatalf("%q", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("step %d: %q, want %q", i+1, lines[i], want[i])
		}
	}
	if steps[1].URL != "https://shop.test/account" {
		t.Fatalf("the page address is kept without its query: %q", steps[1].URL)
	}
	// A page that cannot be described yields no step rather than a useless one.
	page.blind = true
	if st := r.srv.recordStep(ctx, page, "click", 1, 1, 0, "", "", ""); st != nil {
		t.Fatalf("%+v", st)
	}
}

func TestRecordingSavingRunningAndSchedulingARoutine(t *testing.T) {
	r := routineRig(t)
	var ran, scheduled, unscheduled string
	r.srv.Sources.Routines = &RoutineHooks{
		Run:        func(id string) (string, error) { ran = id; return "Started it.", nil },
		Schedule:   func(id, days, at string) (string, error) { scheduled = id + " " + days + " " + at; return "Set.", nil },
		Unschedule: func(id string) error { unscheduled = id; return nil },
		Scheduled:  func() map[string]RoutineSchedule { return map[string]RoutineSchedule{} },
	}
	post := func(body map[string]any) (int, map[string]any) {
		resp, raw := r.do("POST", "/api/routines", body)
		var out map[string]any
		json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}
	if code, out := post(map[string]any{"action": "start"}); code != 200 || out["recording"] != true {
		t.Fatalf("%d %v", code, out)
	}
	for _, s := range []routine.Step{{Kind: routine.Open, URL: "https://shop.test"}, {Kind: routine.Click, Target: `the link "Orders"`}, {Kind: routine.Back}} {
		r.srv.Rec.Note(s)
	}
	if code, out := post(map[string]any{"action": "stop"}); code != 200 || out["recording"] != false || len(out["steps"].([]any)) != 3 {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := post(map[string]any{"action": "save", "name": ""}); code != 400 {
		t.Fatalf("a name is needed: %d", code)
	}
	code, out := post(map[string]any{"action": "save", "name": "Orders", "keep": []int{0, 1}})
	if code != 200 || out["saved"] != "Orders" || len(out["steps"].([]any)) != 0 {
		t.Fatalf("%d %v", code, out)
	}
	list := out["routines"].([]any)
	if len(list) != 1 || len(list[0].(map[string]any)["steps"].([]any)) != 2 {
		t.Fatalf("only the steps kept are saved: %v", list)
	}
	id := list[0].(map[string]any)["id"].(string)

	if code, out := post(map[string]any{"action": "run", "id": id}); code != 200 || ran != id || out["message"] != "Started it." {
		t.Fatalf("%d %v %q", code, out, ran)
	}
	if code, _ := post(map[string]any{"action": "run", "id": "nope"}); code != 404 {
		t.Fatalf("%d", code)
	}
	if code, _ := post(map[string]any{"action": "schedule", "id": id, "days": "weekdays", "at": "07:30"}); code != 200 || scheduled != id+" weekdays 07:30" {
		t.Fatalf("%d %q", code, scheduled)
	}
	r.srv.Sources.Routines.Run = func(string) (string, error) { return "", errors.New("your rules say no") }
	if code, _ := post(map[string]any{"action": "run", "id": id}); code != 400 {
		t.Fatalf("a refusal is passed on: %d", code)
	}
	if code, _ := post(map[string]any{"action": "remove", "id": id}); code != 200 || unscheduled != id {
		t.Fatalf("%d %q", code, unscheduled)
	}
	if code, _ := post(map[string]any{"action": "bogus"}); code != 400 {
		t.Fatalf("%d", code)
	}
	// Another page cannot do any of this.
	if resp, _ := r.do("POST", "/api/routines", map[string]any{"action": "start"}, "X-Talaria", ""); resp.StatusCode == 200 {
		t.Fatal("a request from another page was accepted")
	}
}

// The page builds everything from text, and a bar or card that may be missing is filtered out before it is added.
func TestTheRoutineAndAgentScreensBuildFromTextOnly(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("static", "more.js"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(src, bad) {
			t.Errorf("more.js uses %s", bad)
		}
	}
	app, err := os.ReadFile(filepath.Join("static", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"routinesCard()", "agentsCard()", "loadRoutines()", "loadPersonas()"} {
		if !strings.Contains(string(app), want) {
			t.Errorf("app.js does not use %s", want)
		}
	}
	for _, want := range []string{"saveRoutineForm", "recordBar()", "openAgentForm", "/api/personas", "/api/routines"} {
		if !strings.Contains(src, want) {
			t.Errorf("more.js lacks %s", want)
		}
	}
}
